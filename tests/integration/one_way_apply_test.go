package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/filesystem"
	"syncgate/internal/storage"
	sqlitestore "syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
	"syncgate/internal/transfer"
)

func TestOneWayApplyRecoversFileCommitAndIsIdempotent(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	base := fixture.seedFile(t, "docs/report.txt", "old report", "revision-base", "", 1)
	source := fixture.fileRevision("docs/report.txt", "authoritative report", "revision-source", base.ID, 2)
	source.CreatedAt = time.Time{}
	prepared := fixture.prepare(t, syncengine.OneWayActionModify, source, base.ID, syncengine.TargetDriftReject)
	fixture.stageFile(t, prepared, "authoritative report")

	failing := &failOnceOneWayApplyStore{next: fixture.store.FileIndex()}
	executor := fixture.executor(failing)
	if _, err := executor.Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root}); !errors.Is(err, errInjectedApplyCommit) {
		t.Fatalf("first Apply error = %v, want injected commit failure", err)
	}
	if contents := readTextFile(t, filepath.Join(fixture.root, "docs", "report.txt")); contents != "authoritative report" {
		t.Fatalf("destination after interrupted commit = %q", contents)
	}
	current, err := fixture.store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, source.RelativePath)
	if err != nil {
		t.Fatalf("GetCurrentRevision after interrupted commit: %v", err)
	}
	if current.ID != base.ID {
		t.Fatalf("current revision after interrupted commit = %s, want %s", current.ID, base.ID)
	}

	result, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root})
	if err != nil {
		t.Fatalf("retry Apply: %v", err)
	}
	if !result.Recovered || result.AlreadyApplied || result.PreservedPath == "" {
		t.Fatalf("retry result = %+v", result)
	}
	if contents := readTextFile(t, result.PreservedPath); contents != "old report" {
		t.Fatalf("preserved version = %q", contents)
	}
	current, err = fixture.store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, source.RelativePath)
	if err != nil || current.ID != source.ID || !current.CreatedAt.Equal(fixture.now) {
		t.Fatalf("accepted current revision = %+v, err=%v", current, err)
	}

	duplicate, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root})
	if err != nil {
		t.Fatalf("duplicate Apply: %v", err)
	}
	if !duplicate.AlreadyApplied {
		t.Fatalf("duplicate result = %+v", duplicate)
	}
	assertInternalApplyPathsIgnored(t, fixture)
}

func TestAuthenticatedOneWaySessionCreatesAndAppliesOnlyAuthorizedWork(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	contents := "authenticated payload"
	revision := fixture.fileRevision("authenticated.txt", contents, "revision-authenticated", "", 1)
	preparation := fixture.preparationRequest(syncengine.OneWayActionAdd, revision, "", syncengine.TargetDriftReject)
	service := syncengine.AuthenticatedOneWayWorkService{
		Preparation:   syncengine.OneWayChangePreparationService{Shares: fixture.store.Shares()},
		Work:          fixture.store.OneWayWork(),
		Now:           func() time.Time { return fixture.now },
		NewTransferID: func() (core.TransferID, error) { return "transfer-authenticated", nil },
		NewJobID:      func() (string, error) { return "job-authenticated", nil },
	}
	result, err := service.PrepareAndQueue(fixture.ctx, integrationPeerSession(fixture.sourceDeviceID), syncengine.AuthenticatedOneWayWorkRequest{
		Advertisement: fixture.advertisement(preparation),
		Preparation:   preparation,
		ChunkSize:     1024,
	})
	if err != nil {
		t.Fatalf("PrepareAndQueue: %v", err)
	}
	storedTransfer, err := fixture.store.Transfers().GetTransfer(fixture.ctx, result.Transfer.ID)
	if err != nil || storedTransfer.PeerDeviceID != fixture.sourceDeviceID {
		t.Fatalf("stored transfer = %+v, err=%v", storedTransfer, err)
	}
	storedJob, err := fixture.store.OneWayJobs().GetOneWayJob(fixture.ctx, result.Job.ID)
	if err != nil || storedJob.PeerDeviceID != fixture.sourceDeviceID || storedJob.RequiredCapability != core.CapabilityUpload {
		t.Fatalf("stored job = %+v, err=%v", storedJob, err)
	}
	fixture.stageFile(t, result.Prepared, contents)
	if _, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{
		AuthenticatedPeerID: storedJob.PeerDeviceID,
		Prepared:            result.Prepared,
		ShareRoot:           fixture.root,
	}); err != nil {
		t.Fatalf("Apply authenticated work: %v", err)
	}
	if got := readTextFile(t, filepath.Join(fixture.root, revision.RelativePath)); got != contents {
		t.Fatalf("applied contents = %q", got)
	}

	if err := fixture.store.Devices().RevokeDevice(fixture.ctx, fixture.sourceDeviceID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	blockedRevision := fixture.fileRevision("blocked-new-work.txt", "blocked", "revision-blocked-new-work", "", 1)
	blockedPreparation := fixture.preparationRequest(syncengine.OneWayActionAdd, blockedRevision, "", syncengine.TargetDriftReject)
	service.NewTransferID = func() (core.TransferID, error) { return "transfer-blocked", nil }
	service.NewJobID = func() (string, error) { return "job-blocked", nil }
	_, err = service.PrepareAndQueue(fixture.ctx, integrationPeerSession(fixture.sourceDeviceID), syncengine.AuthenticatedOneWayWorkRequest{
		Advertisement: fixture.advertisement(blockedPreparation),
		Preparation:   blockedPreparation,
		ChunkSize:     1024,
	})
	if !errors.Is(err, syncengine.ErrChangeAuthorization) {
		t.Fatalf("revoked PrepareAndQueue error = %v, want %v", err, syncengine.ErrChangeAuthorization)
	}
	if _, err := fixture.store.Transfers().GetTransfer(fixture.ctx, "transfer-blocked"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("revoked peer created transfer: %v", err)
	}
	if _, err := fixture.store.OneWayJobs().GetOneWayJob(fixture.ctx, "job-blocked"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("revoked peer created job: %v", err)
	}
}

func TestOneWayApplyDirectoryAndDeletionPreserveHistory(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		fixture := newOneWayApplyFixture(t)
		revision := core.Revision{
			ID: "revision-directory", ShareID: fixture.shareID, RelativePath: "empty",
			EntryType: core.EntryDirectory, OriginDeviceID: fixture.sourceDeviceID, Sequence: 1, CreatedAt: fixture.now,
		}
		prepared := fixture.prepare(t, syncengine.OneWayActionAdd, revision, "", syncengine.TargetDriftReject)
		result, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root})
		if err != nil {
			t.Fatalf("Apply directory: %v", err)
		}
		info, err := os.Stat(result.DestinationPath)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory info = %+v, err=%v", info, err)
		}
		entry, err := fixture.store.FileIndex().Get(fixture.ctx, fixture.shareID, revision.RelativePath)
		if err != nil || entry.CurrentRevisionID != revision.ID || entry.EntryType != core.EntryDirectory {
			t.Fatalf("directory index = %+v, err=%v", entry, err)
		}

		deletion := core.Revision{
			ID: "revision-directory-delete", ShareID: fixture.shareID, RelativePath: revision.RelativePath,
			EntryType: core.EntryDeleted, ParentRevisionID: revision.ID, OriginDeviceID: fixture.sourceDeviceID,
			Sequence: 2, IsDeleted: true, CreatedAt: fixture.now.Add(time.Minute),
		}
		deletePrepared := fixture.prepare(t, syncengine.OneWayActionDelete, deletion, revision.ID, syncengine.TargetDriftReject)
		deleted, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{
			AuthenticatedPeerID: deletePrepared.AuthenticatedPeerID, Prepared: deletePrepared, ShareRoot: fixture.root, TombstoneID: "tombstone-directory",
		})
		if err != nil {
			t.Fatalf("Apply directory deletion: %v", err)
		}
		historyInfo, err := os.Stat(deleted.PreservedPath)
		if err != nil || !historyInfo.IsDir() {
			t.Fatalf("preserved directory = %+v, err=%v", historyInfo, err)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		fixture := newOneWayApplyFixture(t)
		base := fixture.seedFile(t, "obsolete.txt", "keep in history", "revision-obsolete", "", 1)
		deletion := core.Revision{
			ID: "revision-delete", ShareID: fixture.shareID, RelativePath: base.RelativePath,
			EntryType: core.EntryDeleted, ParentRevisionID: base.ID, OriginDeviceID: fixture.sourceDeviceID,
			Sequence: 2, IsDeleted: true, CreatedAt: fixture.now,
		}
		prepared := fixture.prepare(t, syncengine.OneWayActionDelete, deletion, base.ID, syncengine.TargetDriftReject)
		request := syncengine.OneWayApplyRequest{
			AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root, TombstoneID: "tombstone-delete",
			TombstoneExpiresAt: fixture.now.Add(24 * time.Hour),
		}
		result, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, request)
		if err != nil {
			t.Fatalf("Apply deletion: %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, "obsolete.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleted destination stat error = %v", err)
		}
		if contents := readTextFile(t, result.PreservedPath); contents != "keep in history" {
			t.Fatalf("deleted history = %q", contents)
		}
		entry, err := fixture.store.FileIndex().Get(fixture.ctx, fixture.shareID, deletion.RelativePath)
		if err != nil || !entry.IsDeleted || entry.CurrentRevisionID != deletion.ID {
			t.Fatalf("deleted index = %+v, err=%v", entry, err)
		}
		tombstone, err := fixture.store.Tombstones().Get(fixture.ctx, fixture.shareID, deletion.RelativePath)
		if err != nil || tombstone.ID != request.TombstoneID || tombstone.BaseRevisionID != base.ID {
			t.Fatalf("tombstone = %+v, err=%v", tombstone, err)
		}
		duplicate, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, request)
		if err != nil || !duplicate.AlreadyApplied || duplicate.Tombstone == nil {
			t.Fatalf("duplicate deletion = %+v, err=%v", duplicate, err)
		}
	})
}

func TestOneWayApplyPreservesKnownTargetDrift(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	base := fixture.seedFile(t, "shared.txt", "common base", "revision-common", "", 1)
	target := fixture.seedFile(t, "shared.txt", "target edit", "revision-target", base.ID, 2)
	source := fixture.fileRevision("shared.txt", "source edit", "revision-source", base.ID, 3)
	prepared := fixture.prepare(t, syncengine.OneWayActionModify, source, target.ID, syncengine.TargetDriftPreserveConflictCopy)
	fixture.stageFile(t, prepared, "source edit")

	result, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root})
	if err != nil {
		t.Fatalf("Apply drifted change: %v", err)
	}
	if contents := readTextFile(t, result.PreservedPath); contents != "target edit" {
		t.Fatalf("preserved drift copy = %q", contents)
	}
	current, err := fixture.store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, source.RelativePath)
	if err != nil || current.ID != source.ID || current.ParentRevisionID != base.ID {
		t.Fatalf("drifted current revision = %+v, err=%v", current, err)
	}
}

func TestOneWayApplyRejectsFilesystemDriftBeforeIntent(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	base := fixture.seedFile(t, "report.txt", "indexed", "revision-indexed", "", 1)
	source := fixture.fileRevision("report.txt", "source", "revision-next", base.ID, 2)
	prepared := fixture.prepare(t, syncengine.OneWayActionModify, source, base.ID, syncengine.TargetDriftReject)
	fixture.stageFile(t, prepared, "source")
	if err := os.WriteFile(filepath.Join(fixture.root, "report.txt"), []byte("unindexed local edit"), 0o600); err != nil {
		t.Fatalf("write local drift: %v", err)
	}

	_, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{AuthenticatedPeerID: prepared.AuthenticatedPeerID, Prepared: prepared, ShareRoot: fixture.root})
	if !errors.Is(err, syncengine.ErrOneWayApplyContent) {
		t.Fatalf("Apply drift error = %v", err)
	}
	current, currentErr := fixture.store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, source.RelativePath)
	if currentErr != nil || current.ID != base.ID {
		t.Fatalf("current revision after drift rejection = %+v, err=%v", current, currentErr)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, transfer.DefaultHistoryDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history should not be created, stat error = %v", err)
	}
}

func TestOneWayApplyRejectsRevokedPeerBeforeFilesystemOrStateChange(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	source := fixture.fileRevision("blocked.txt", "blocked payload", "revision-blocked", "", 1)
	prepared := fixture.prepare(t, syncengine.OneWayActionAdd, source, "", syncengine.TargetDriftReject)
	fixture.stageFile(t, prepared, "blocked payload")
	if err := fixture.store.Devices().RevokeDevice(fixture.ctx, fixture.sourceDeviceID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	_, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{
		AuthenticatedPeerID: fixture.sourceDeviceID,
		Prepared:            prepared,
		ShareRoot:           fixture.root,
	})
	if !errors.Is(err, syncengine.ErrOneWayApplyAuthorization) {
		t.Fatalf("Apply error = %v, want %v", err, syncengine.ErrOneWayApplyAuthorization)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, source.RelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination changed after revocation, stat error = %v", err)
	}
	if _, err := fixture.store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, source.RelativePath); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("revision state changed after revocation: %v", err)
	}
	incoming := filepath.Join(fixture.root, syncengine.DefaultOneWayIncomingDir, "one-way")
	entries, err := os.ReadDir(incoming)
	if err != nil {
		t.Fatalf("ReadDir staged artifacts: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".intent") {
			t.Fatalf("apply intent %s was written after revocation", entry.Name())
		}
	}
}

func TestOneWayApplyRejectsAuthenticatedIdentityChange(t *testing.T) {
	fixture := newOneWayApplyFixture(t)
	source := fixture.fileRevision("identity.txt", "payload", "revision-identity", "", 1)
	prepared := fixture.prepare(t, syncengine.OneWayActionAdd, source, "", syncengine.TargetDriftReject)

	_, err := fixture.executor(fixture.store.FileIndex()).Apply(fixture.ctx, syncengine.OneWayApplyRequest{
		AuthenticatedPeerID: "DIFFERENT-PEER",
		Prepared:            prepared,
		ShareRoot:           fixture.root,
	})
	if !errors.Is(err, syncengine.ErrInvalidOneWayApply) {
		t.Fatalf("Apply error = %v, want %v", err, syncengine.ErrInvalidOneWayApply)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, source.RelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination changed after identity mismatch, stat error = %v", err)
	}
}

type oneWayApplyFixture struct {
	t              *testing.T
	ctx            context.Context
	store          *sqlitestore.Store
	root           string
	shareID        core.ShareID
	sourceDeviceID core.DeviceID
	targetDeviceID core.DeviceID
	now            time.Time
}

func newOneWayApplyFixture(t *testing.T) *oneWayApplyFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlitestore.Open(filepath.Join(t.TempDir(), "syncgate.db"))
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	fixture := &oneWayApplyFixture{
		t: t, ctx: ctx, store: store, root: root, shareID: "share-1",
		sourceDeviceID: "SOURCE-1", targetDeviceID: "TARGET-1",
		now: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Devices().TrustDevice(ctx, storage.Device{
		ID: fixture.sourceDeviceID, DisplayName: "source", PublicKey: []byte("source-key"),
		Fingerprint: "SOURCE-FINGERPRINT", TrustState: storage.TrustTrusted,
	}); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{
		ID: fixture.shareID, Name: "target", RootPath: root, Mode: storage.ShareOneWayTarget,
		CasePolicy: "preserve", VersionPolicy: "history", CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}); err != nil {
		t.Fatalf("SaveShare: %v", err)
	}
	if err := store.Shares().SetPermission(ctx, core.SharePermission{
		ShareID: fixture.shareID, DeviceID: fixture.sourceDeviceID,
		Capabilities: map[core.Capability]bool{
			core.CapabilitySync: true, core.CapabilityUpload: true,
			core.CapabilityModify: true, core.CapabilityDelete: true,
		},
	}); err != nil {
		t.Fatalf("SetPermission: %v", err)
	}
	return fixture
}

func (fixture *oneWayApplyFixture) executor(applier storage.OneWayApplyStore) syncengine.OneWayApplyExecutor {
	return syncengine.OneWayApplyExecutor{
		Revisions: fixture.store.Revisions(), Applier: applier, Tombstones: fixture.store.Tombstones(),
		Shares: fixture.store.Shares(),
		Now:    func() time.Time { return fixture.now },
	}
}

func (fixture *oneWayApplyFixture) prepare(t *testing.T, action syncengine.OneWayAction, revision core.Revision, targetRevisionID core.RevisionID, driftPolicy syncengine.TargetDriftPolicy) syncengine.PreparedOneWayChange {
	t.Helper()
	prepared, err := (syncengine.OneWayChangePreparationService{Shares: fixture.store.Shares()}).Prepare(fixture.ctx, fixture.preparationRequest(action, revision, targetRevisionID, driftPolicy))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return prepared
}

func (fixture *oneWayApplyFixture) preparationRequest(action syncengine.OneWayAction, revision core.Revision, targetRevisionID core.RevisionID, driftPolicy syncengine.TargetDriftPolicy) syncengine.OneWayChangePreparationRequest {
	return syncengine.OneWayChangePreparationRequest{
		AuthenticatedPeerID: fixture.sourceDeviceID,
		Change: syncengine.OneWayChangeRequest{
			ProtocolVersion: syncengine.RevisionManifestProtocolVersion, RequestID: "request-" + string(revision.ID),
			SourceDeviceID: fixture.sourceDeviceID, TargetDeviceID: fixture.targetDeviceID, ShareID: fixture.shareID,
			RevisionID: revision.ID, ExpectedBaseRevisionID: revision.ParentRevisionID, Action: action,
		},
		Source:         syncengine.OneWaySourcePolicy{ShareID: fixture.shareID, DeviceID: fixture.sourceDeviceID, Mode: storage.ShareOneWaySource},
		Target:         syncengine.OneWayTargetPolicy{ShareID: fixture.shareID, Mode: storage.ShareOneWayTarget, DriftPolicy: driftPolicy},
		SourceRevision: revision, TargetRevisionID: targetRevisionID,
	}
}

func (fixture *oneWayApplyFixture) advertisement(preparation syncengine.OneWayChangePreparationRequest) syncengine.RevisionAdvertisementRequest {
	revision := preparation.SourceRevision
	return syncengine.RevisionAdvertisementRequest{
		ProtocolVersion: preparation.Change.ProtocolVersion,
		RequestID:       "advertisement-" + preparation.Change.RequestID,
		SourceDeviceID:  preparation.Change.SourceDeviceID,
		TargetDeviceID:  preparation.Change.TargetDeviceID,
		ShareID:         preparation.Change.ShareID,
		Limits:          syncengine.RevisionManifestLimits{MaxEntries: 1, MaxBytes: 4096},
		Revisions: []syncengine.RevisionManifestEntry{{
			RevisionID: revision.ID, ParentRevisionID: revision.ParentRevisionID, RelativePath: revision.RelativePath,
			EntryType: revision.EntryType, Size: revision.Size, ContentHash: revision.ContentHash,
			HashAlgorithm: revision.HashAlgorithm, Sequence: revision.Sequence, IsDeleted: revision.IsDeleted,
		}},
	}
}

func (fixture *oneWayApplyFixture) seedFile(t *testing.T, relativePath, contents string, revisionID, parentID core.RevisionID, sequence int64) core.Revision {
	t.Helper()
	revision := fixture.fileRevision(relativePath, contents, revisionID, parentID, sequence)
	destination, err := filesystem.EnsureParentDirectoriesInsideShare(fixture.root, relativePath, 0o700)
	if err != nil {
		t.Fatalf("EnsureParentDirectoriesInsideShare: %v", err)
	}
	if err := os.WriteFile(destination, []byte(contents), 0o600); err != nil {
		t.Fatalf("write seeded file: %v", err)
	}
	entry := core.FileIndexEntry{
		ShareID: fixture.shareID, RelativePath: relativePath, EntryType: core.EntryFile,
		Size: int64(len(contents)), ContentHash: revision.ContentHash, HashAlgorithm: transfer.HashSHA256,
	}
	if err := fixture.store.FileIndex().CommitSnapshot(fixture.ctx, fixture.shareID, []core.FileIndexEntry{entry}, []core.Revision{revision}, fixture.now.Add(time.Duration(sequence)*time.Minute)); err != nil {
		t.Fatalf("CommitSnapshot seed %s: %v", revisionID, err)
	}
	return revision
}

func (fixture *oneWayApplyFixture) fileRevision(relativePath, contents string, revisionID, parentID core.RevisionID, sequence int64) core.Revision {
	return core.Revision{
		ID: revisionID, ShareID: fixture.shareID, RelativePath: relativePath, EntryType: core.EntryFile,
		Size: int64(len(contents)), ContentHash: hashText(contents), HashAlgorithm: transfer.HashSHA256,
		ParentRevisionID: parentID, OriginDeviceID: fixture.sourceDeviceID, Sequence: sequence,
		CreatedAt: fixture.now.Add(time.Duration(sequence) * time.Minute),
	}
}

func (fixture *oneWayApplyFixture) stageFile(t *testing.T, prepared syncengine.PreparedOneWayChange, contents string) {
	t.Helper()
	spec, err := syncengine.BuildOneWayReceiveSpec(fixture.root, prepared)
	if err != nil {
		t.Fatalf("BuildOneWayReceiveSpec: %v", err)
	}
	writer, err := transfer.NewReceiveWriter(spec)
	if err != nil {
		t.Fatalf("NewReceiveWriter: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatalf("Write staged content: %v", err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatalf("Commit staged content: %v", err)
	}
}

func assertInternalApplyPathsIgnored(t *testing.T, fixture *oneWayApplyFixture) {
	t.Helper()
	result, err := syncengine.ScanShare(syncengine.ScanOptions{ShareID: fixture.shareID, RootPath: fixture.root, Now: func() time.Time { return fixture.now }})
	if err != nil {
		t.Fatalf("ScanShare: %v", err)
	}
	for _, entry := range result.Entries {
		if strings.HasPrefix(entry.RelativePath, ".sync-") {
			t.Fatalf("internal apply path was scanned: %s", entry.RelativePath)
		}
	}
}

func readTextFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(raw)
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type integrationPeerSession core.DeviceID

func (session integrationPeerSession) RemoteDeviceID() core.DeviceID { return core.DeviceID(session) }

var errInjectedApplyCommit = errors.New("injected one-way apply commit failure")

type failOnceOneWayApplyStore struct {
	next   storage.OneWayApplyStore
	failed bool
}

func (store *failOnceOneWayApplyStore) CommitOneWayApply(ctx context.Context, commit storage.OneWayApplyCommit) (storage.OneWayApplyCommitResult, error) {
	if !store.failed {
		store.failed = true
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("%w", errInjectedApplyCommit)
	}
	return store.next.CommitOneWayApply(ctx, commit)
}
