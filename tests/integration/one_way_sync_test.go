package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	sqlitestore "syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
	"syncgate/internal/transfer"
)

func TestTwoAgentOneWaySyncAddModifyDeleteAndOfflineReconciliation(t *testing.T) {
	fixture := newTwoAgentOneWayFixture(t)
	fixture.writeSource(t, "docs/report.txt", "version one")
	add := fixture.scanSource(t, "source-docs-add", "source-add")
	for _, revision := range add {
		fixture.applySourceRevision(t, revision, syncengine.OneWayActionAdd, syncengine.TargetDriftReject)
	}
	fixture.assertTargetFile(t, "docs/report.txt", "version one")

	// The source can make several changes while the receiver is offline. A later
	// scan reconciles directly to the latest authoritative revision.
	fixture.writeSource(t, "docs/report.txt", "version three")
	modify := fixture.scanSource(t, "source-modify")
	fixture.applySourceRevision(t, modify[0], syncengine.OneWayActionModify, syncengine.TargetDriftReject)
	fixture.assertTargetFile(t, "docs/report.txt", "version three")

	if err := os.Remove(filepath.Join(fixture.sourceRoot, "docs", "report.txt")); err != nil {
		t.Fatalf("remove source file: %v", err)
	}
	deleted := fixture.scanSource(t, "source-delete")
	result := fixture.applySourceRevision(t, deleted[0], syncengine.OneWayActionDelete, syncengine.TargetDriftReject)
	if result.Tombstone == nil || result.Tombstone.BaseRevisionID != modify[0].ID {
		t.Fatalf("deletion tombstone = %+v", result.Tombstone)
	}
	if _, err := os.Stat(filepath.Join(fixture.targetRoot, "docs", "report.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target file after deletion stat error = %v", err)
	}
}

func TestTwoAgentOneWaySyncDuplicateAndRestartResume(t *testing.T) {
	fixture := newTwoAgentOneWayFixture(t)
	fixture.writeSource(t, "resume.txt", "resumable payload")
	revision := fixture.scanSource(t, "resume-add")[0]
	prepared := fixture.prepare(t, revision, syncengine.OneWayActionAdd, "", syncengine.TargetDriftReject)
	fixture.stage(t, prepared)

	// Reopen the receiver database before applying the verified staged artifact.
	// This models a receiver restart between transfer completion and apply.
	fixture.reopenTarget(t)
	first, err := fixture.executor().Apply(fixture.ctx, syncengine.OneWayApplyRequest{Prepared: prepared, ShareRoot: fixture.targetRoot})
	if err != nil {
		t.Fatalf("apply after receiver restart: %v", err)
	}
	if first.AlreadyApplied || first.RevisionID != revision.ID {
		t.Fatalf("first result = %+v", first)
	}
	duplicate, err := fixture.executor().Apply(fixture.ctx, syncengine.OneWayApplyRequest{Prepared: prepared, ShareRoot: fixture.targetRoot})
	if err != nil {
		t.Fatalf("duplicate apply: %v", err)
	}
	if !duplicate.AlreadyApplied {
		t.Fatalf("duplicate result = %+v", duplicate)
	}
	fixture.assertTargetFile(t, "resume.txt", "resumable payload")
}

func TestTwoAgentOneWaySyncRejectsDeletionGuardAndUnauthorizedChange(t *testing.T) {
	fixture := newTwoAgentOneWayFixture(t)
	fixture.writeSource(t, "one.txt", "one")
	fixture.writeSource(t, "two.txt", "two")
	fixture.scanSource(t, "guard-one", "guard-two")
	if err := os.Remove(filepath.Join(fixture.sourceRoot, "one.txt")); err != nil {
		t.Fatalf("remove source one: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.sourceRoot, "two.txt")); err != nil {
		t.Fatalf("remove source two: %v", err)
	}
	blocked := fixture.scanSourceWithGuard(t, syncengine.DeletionGuard{MaxCount: 1}, "must-not-exist")
	if !blocked.Blocked || blocked.Committed || len(blocked.Revisions) != 0 {
		t.Fatalf("deletion guard result = %+v", blocked)
	}

	fixture.writeSource(t, "private.txt", "not authorized")
	revisions := fixture.scanSource(t, "unauthorized-add", "del-one", "del-two")
	revision := revisionAtPath(t, revisions, "private.txt")
	if err := fixture.target.Shares().SetPermission(fixture.ctx, core.SharePermission{
		ShareID: fixture.shareID, DeviceID: fixture.sourceDeviceID,
		Capabilities: map[core.Capability]bool{core.CapabilitySync: true},
	}); err != nil {
		t.Fatalf("remove upload permission: %v", err)
	}
	_, err := fixture.prepareResult(revision, syncengine.OneWayActionAdd, "", syncengine.TargetDriftReject)
	if !errors.Is(err, syncengine.ErrChangeAuthorization) {
		t.Fatalf("unauthorized prepare error = %v", err)
	}
}

func TestTwoAgentOneWaySyncDriftPolicyAndIgnoredFiles(t *testing.T) {
	fixture := newTwoAgentOneWayFixture(t)
	fixture.writeSource(t, "shared.txt", "base")
	base := fixture.scanSource(t, "base")[0]
	fixture.applySourceRevision(t, base, syncengine.OneWayActionAdd, syncengine.TargetDriftReject)

	// A target-local indexed edit is preserved only under the explicit policy.
	fixture.writeTarget(t, "shared.txt", "target edit")
	targetRevision := fixture.recordTargetRevision(t, "target-edit", "shared.txt", "target edit", base.ID)
	fixture.writeSource(t, "shared.txt", "source edit")
	sourceRevision := fixture.scanSource(t, "source-edit")[0]
	result := fixture.applySourceRevision(t, sourceRevision, syncengine.OneWayActionModify, syncengine.TargetDriftPreserveConflictCopy)
	if result.PreservedPath == "" || readTextFile(t, result.PreservedPath) != "target edit" {
		t.Fatalf("preserved target drift = %+v", result)
	}
	current, err := fixture.target.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, "shared.txt")
	if err != nil || current.ID != sourceRevision.ID || targetRevision.ID == current.ID {
		t.Fatalf("target revision after drift policy = %+v, err=%v", current, err)
	}

	fixture.writeSource(t, ".sync-history/old.txt", "history")
	fixture.writeSource(t, ".sync-incoming/one-way/untrusted.ready", "incoming")
	fixture.writeSource(t, "partial.sync-part", "partial")
	fixture.writeSource(t, "visible.txt", "visible")
	resultScan := fixture.scanSource(t, "visible")
	if len(resultScan) != 1 || resultScan[0].RelativePath != "visible.txt" {
		t.Fatalf("source revisions after ignored files = %+v", resultScan)
	}
}

type twoAgentOneWayFixture struct {
	t              *testing.T
	ctx            context.Context
	source         *sqlitestore.Store
	target         *sqlitestore.Store
	sourceDB       string
	targetDB       string
	sourceRoot     string
	targetRoot     string
	shareID        core.ShareID
	sourceDeviceID core.DeviceID
	targetDeviceID core.DeviceID
	now            time.Time
}

func newTwoAgentOneWayFixture(t *testing.T) *twoAgentOneWayFixture {
	t.Helper()
	fixture := &twoAgentOneWayFixture{t: t, ctx: context.Background(), sourceRoot: t.TempDir(), targetRoot: t.TempDir(), sourceDB: filepath.Join(t.TempDir(), "source.db"), targetDB: filepath.Join(t.TempDir(), "target.db"), shareID: "two-agent-share", sourceDeviceID: "SOURCE-1", targetDeviceID: "TARGET-1", now: time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)}
	fixture.source = fixture.openStore(t, fixture.sourceDB)
	fixture.target = fixture.openStore(t, fixture.targetDB)
	t.Cleanup(func() { _ = fixture.source.Close(); _ = fixture.target.Close() })
	for _, agent := range []struct {
		store *sqlitestore.Store
		root  string
		mode  storage.ShareMode
		name  string
	}{{fixture.source, fixture.sourceRoot, storage.ShareOneWaySource, "source"}, {fixture.target, fixture.targetRoot, storage.ShareOneWayTarget, "target"}} {
		if err := agent.store.Shares().SaveShare(fixture.ctx, storage.Share{ID: fixture.shareID, Name: agent.name, RootPath: agent.root, Mode: agent.mode, CasePolicy: "preserve", VersionPolicy: "history", CreatedAt: fixture.now, UpdatedAt: fixture.now}); err != nil {
			t.Fatalf("SaveShare %s: %v", agent.name, err)
		}
	}
	if err := fixture.target.Devices().TrustDevice(fixture.ctx, storage.Device{ID: fixture.sourceDeviceID, DisplayName: "source", PublicKey: []byte("source-key"), Fingerprint: "source-fingerprint", TrustState: storage.TrustTrusted}); err != nil {
		t.Fatalf("trust source on target: %v", err)
	}
	fixture.grantAll(t)
	return fixture
}

func (fixture *twoAgentOneWayFixture) openStore(t *testing.T, path string) *sqlitestore.Store {
	t.Helper()
	store, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	if err := store.Migrate(fixture.ctx); err != nil {
		t.Fatalf("Migrate store: %v", err)
	}
	return store
}

func (fixture *twoAgentOneWayFixture) grantAll(t *testing.T) {
	t.Helper()
	if err := fixture.target.Shares().SetPermission(fixture.ctx, core.SharePermission{ShareID: fixture.shareID, DeviceID: fixture.sourceDeviceID, Capabilities: map[core.Capability]bool{core.CapabilitySync: true, core.CapabilityUpload: true, core.CapabilityModify: true, core.CapabilityDelete: true}}); err != nil {
		t.Fatalf("grant target permissions: %v", err)
	}
}

func (fixture *twoAgentOneWayFixture) scanSource(t *testing.T, ids ...core.RevisionID) []core.Revision {
	t.Helper()
	result := fixture.scanSourceWithGuard(t, syncengine.DeletionGuard{MaxCount: 100, MaxPercent: 100}, ids...)
	if !result.Committed {
		t.Fatalf("source scan result = %+v", result)
	}
	return result.Revisions
}

func (fixture *twoAgentOneWayFixture) scanSourceWithGuard(t *testing.T, guard syncengine.DeletionGuard, ids ...core.RevisionID) syncengine.ScanCommitResult {
	t.Helper()
	service := syncengine.ScanCommitService{Planner: syncengine.ScanPlanner{Index: fixture.source.FileIndex()}, Committer: fixture.source.FileIndex(), Now: func() time.Time { return fixture.now }, NewRevisionID: deterministicRevisionIDs(ids...)}
	result, err := service.Run(fixture.ctx, syncengine.ScanCommitOptions{Plan: syncengine.PlanScanOptions{ScanOptions: syncengine.ScanOptions{ShareID: fixture.shareID, RootPath: fixture.sourceRoot, Now: func() time.Time { return fixture.now }}, DeletionGuard: guard}, OriginDeviceID: fixture.sourceDeviceID, SequenceStart: fixture.nextSourceSequence(), TombstoneRetention: 24 * time.Hour})
	if err != nil {
		t.Fatalf("source scan commit: %v", err)
	}
	return result
}

func (fixture *twoAgentOneWayFixture) nextSourceSequence() int64 {
	entries, err := fixture.source.FileIndex().List(fixture.ctx, fixture.shareID)
	if err != nil {
		fixture.t.Fatalf("list source index: %v", err)
	}
	return int64(len(entries))
}

func (fixture *twoAgentOneWayFixture) prepare(t *testing.T, revision core.Revision, action syncengine.OneWayAction, targetRevisionID core.RevisionID, policy syncengine.TargetDriftPolicy) syncengine.PreparedOneWayChange {
	t.Helper()
	prepared, err := fixture.prepareResult(revision, action, targetRevisionID, policy)
	if err != nil {
		t.Fatalf("prepare %s: %v", revision.ID, err)
	}
	return prepared
}

func (fixture *twoAgentOneWayFixture) prepareResult(revision core.Revision, action syncengine.OneWayAction, targetRevisionID core.RevisionID, policy syncengine.TargetDriftPolicy) (syncengine.PreparedOneWayChange, error) {
	return (syncengine.OneWayChangePreparationService{Shares: fixture.target.Shares()}).Prepare(fixture.ctx, syncengine.OneWayChangePreparationRequest{Change: syncengine.OneWayChangeRequest{ProtocolVersion: syncengine.RevisionManifestProtocolVersion, RequestID: "request-" + string(revision.ID), SourceDeviceID: fixture.sourceDeviceID, TargetDeviceID: fixture.targetDeviceID, ShareID: fixture.shareID, RevisionID: revision.ID, ExpectedBaseRevisionID: revision.ParentRevisionID, Action: action}, Source: syncengine.OneWaySourcePolicy{ShareID: fixture.shareID, DeviceID: fixture.sourceDeviceID, Mode: storage.ShareOneWaySource}, Target: syncengine.OneWayTargetPolicy{ShareID: fixture.shareID, Mode: storage.ShareOneWayTarget, DriftPolicy: policy}, SourceRevision: revision, TargetRevisionID: targetRevisionID, Remote: true})
}

func (fixture *twoAgentOneWayFixture) applySourceRevision(t *testing.T, revision core.Revision, action syncengine.OneWayAction, policy syncengine.TargetDriftPolicy) syncengine.OneWayApplyResult {
	t.Helper()
	targetRevisionID := core.RevisionID("")
	if current, err := fixture.target.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, revision.RelativePath); err == nil {
		targetRevisionID = current.ID
	} else if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get target current: %v", err)
	}
	prepared := fixture.prepare(t, revision, action, targetRevisionID, policy)
	if !revision.IsDeleted && revision.EntryType == core.EntryFile {
		fixture.stage(t, prepared)
	}
	request := syncengine.OneWayApplyRequest{Prepared: prepared, ShareRoot: fixture.targetRoot}
	if revision.IsDeleted {
		request.TombstoneID = core.TombstoneID("tombstone-" + string(revision.ID))
		request.TombstoneExpiresAt = fixture.now.Add(24 * time.Hour)
	}
	result, err := fixture.executor().Apply(fixture.ctx, request)
	if err != nil {
		t.Fatalf("apply source revision %s: %v", revision.ID, err)
	}
	return result
}

func (fixture *twoAgentOneWayFixture) stage(t *testing.T, prepared syncengine.PreparedOneWayChange) {
	t.Helper()
	spec, err := syncengine.BuildOneWayReceiveSpec(fixture.targetRoot, prepared)
	if err != nil {
		t.Fatalf("build receive spec: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(fixture.sourceRoot, filepath.FromSlash(prepared.RelativePath)))
	if err != nil {
		t.Fatalf("read source artifact: %v", err)
	}
	writer, err := transfer.NewReceiveWriter(spec)
	if err != nil {
		t.Fatalf("new receive writer: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("stage source artifact: %v", err)
	}
	if _, err := writer.Commit(); err != nil {
		t.Fatalf("commit received artifact: %v", err)
	}
}

func (fixture *twoAgentOneWayFixture) executor() syncengine.OneWayApplyExecutor {
	return syncengine.OneWayApplyExecutor{Revisions: fixture.target.Revisions(), Applier: fixture.target.FileIndex(), Tombstones: fixture.target.Tombstones(), Now: func() time.Time { return fixture.now }}
}

func (fixture *twoAgentOneWayFixture) reopenTarget(t *testing.T) {
	t.Helper()
	if err := fixture.target.Close(); err != nil {
		t.Fatalf("close target: %v", err)
	}
	fixture.target = fixture.openStore(t, fixture.targetDB)
}

func (fixture *twoAgentOneWayFixture) recordTargetRevision(t *testing.T, id core.RevisionID, relativePath, contents string, parent core.RevisionID) core.Revision {
	t.Helper()
	revision := core.Revision{ID: id, ShareID: fixture.shareID, RelativePath: relativePath, EntryType: core.EntryFile, Size: int64(len(contents)), ContentHash: hashText(contents), HashAlgorithm: transfer.HashSHA256, ParentRevisionID: parent, OriginDeviceID: fixture.targetDeviceID, Sequence: 1, CreatedAt: fixture.now}
	entry := core.FileIndexEntry{ShareID: fixture.shareID, RelativePath: relativePath, EntryType: core.EntryFile, Size: revision.Size, ContentHash: revision.ContentHash, HashAlgorithm: revision.HashAlgorithm}
	if err := fixture.target.FileIndex().CommitSnapshot(fixture.ctx, fixture.shareID, []core.FileIndexEntry{entry}, []core.Revision{revision}, fixture.now); err != nil {
		t.Fatalf("record target revision: %v", err)
	}
	return revision
}

func (fixture *twoAgentOneWayFixture) writeSource(t *testing.T, relativePath, contents string) {
	fixture.write(t, fixture.sourceRoot, relativePath, contents)
}
func (fixture *twoAgentOneWayFixture) writeTarget(t *testing.T, relativePath, contents string) {
	fixture.write(t, fixture.targetRoot, relativePath, contents)
}
func (fixture *twoAgentOneWayFixture) write(t *testing.T, root, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create parent %s: %v", relativePath, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}
func (fixture *twoAgentOneWayFixture) assertTargetFile(t *testing.T, relativePath, want string) {
	if got := readTextFile(t, filepath.Join(fixture.targetRoot, filepath.FromSlash(relativePath))); got != want {
		t.Fatalf("target %s = %q, want %q", relativePath, got, want)
	}
}

func deterministicRevisionIDs(ids ...core.RevisionID) func() (core.RevisionID, error) {
	index := 0
	return func() (core.RevisionID, error) {
		if index >= len(ids) {
			return "", errors.New("revision IDs exhausted")
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func revisionAtPath(t *testing.T, revisions []core.Revision, relativePath string) core.Revision {
	t.Helper()
	for _, revision := range revisions {
		if revision.RelativePath == relativePath {
			return revision
		}
	}
	t.Fatalf("revision for %s missing from %+v", relativePath, revisions)
	return core.Revision{}
}
