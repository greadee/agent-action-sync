package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/daemon"
	"syncgate/internal/identity"
	"syncgate/internal/pairing"
	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
	"syncgate/internal/transfer"
	transportapi "syncgate/internal/transport"
	tcptls "syncgate/internal/transport/tcp"
)

func TestTwoDaemonTrustedSyncAddModifyDeleteAndRestart(t *testing.T) {
	fixture := newTrustedSyncFixture(t)
	fixture.pairDaemons(t)

	firstSession := fixture.connectSource(t)
	fixture.writeSource(t, "docs/report.txt", "version one")
	addRevision := fixture.scanCurrent(t, "docs/report.txt")
	add := fixture.queueRevision(t, firstSession.target, addRevision, syncengine.OneWayActionAdd)
	if add.Job.Remote {
		t.Fatal("direct mutual-TLS work was persisted as remote")
	}
	firstSession.close()

	// Work accepted before shutdown remains queued in SQLite and is recovered by
	// a newly bootstrapped target daemon using the same identity and data path.
	if err := fixture.target.Close(); err != nil {
		t.Fatalf("close target before restart: %v", err)
	}
	fixture.target = fixture.bootstrapTarget(t)
	fixture.startTarget(t)
	fixture.waitForJob(t, add.Job.ID, core.OneWayJobCompleted)
	fixture.assertTargetFile(t, "docs/report.txt", "version one")

	restartedSession := fixture.connectSource(t)
	defer restartedSession.close()
	fixture.writeSource(t, "docs/report.txt", "version two")
	modifyRevision := fixture.scanCurrent(t, "docs/report.txt")
	modify := fixture.queueRevision(t, restartedSession.target, modifyRevision, syncengine.OneWayActionModify)
	fixture.waitForJob(t, modify.Job.ID, core.OneWayJobCompleted)
	fixture.assertTargetFile(t, "docs/report.txt", "version two")

	if err := os.Remove(filepath.Join(fixture.sourceRoot, "docs", "report.txt")); err != nil {
		t.Fatalf("remove source file: %v", err)
	}
	deleteRevision := fixture.scanCurrent(t, "docs/report.txt")
	deleted := fixture.queueRevision(t, restartedSession.target, deleteRevision, syncengine.OneWayActionDelete)
	fixture.waitForJob(t, deleted.Job.ID, core.OneWayJobCompleted)
	if _, err := os.Stat(filepath.Join(fixture.targetRoot, "docs", "report.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target file after authoritative deletion: %v", err)
	}
	tombstone, err := fixture.target.Store.Tombstones().Get(fixture.ctx, fixture.shareID, "docs/report.txt")
	if err != nil || tombstone.BaseRevisionID != modifyRevision.ID {
		t.Fatalf("target tombstone = %+v, err=%v", tombstone, err)
	}
}

func TestTwoDaemonTrustedSyncRejectsRevocationAndImpersonation(t *testing.T) {
	t.Run("revocation", func(t *testing.T) {
		fixture := newTrustedSyncFixture(t)
		fixture.pairDaemons(t)
		session := fixture.connectSource(t)
		defer session.close()

		fixture.writeSource(t, "revoked.txt", "must not transfer")
		revision := fixture.scanCurrent(t, "revoked.txt")
		if _, err := (pairing.Service{Pairings: fixture.target.Store.Pairings(), Now: fixture.clock}).Revoke(
			fixture.ctx, fixture.target.Identity.DeviceID, fixture.source.Identity.DeviceID,
		); err != nil {
			t.Fatalf("revoke source pairing: %v", err)
		}
		_, err := fixture.queueRevisionResult(session.target, revision, syncengine.OneWayActionAdd)
		if !errors.Is(err, syncengine.ErrChangeAuthorization) {
			t.Fatalf("revoked session work error = %v, want %v", err, syncengine.ErrChangeAuthorization)
		}
		jobs, listErr := fixture.target.Store.OneWayJobs().ListOneWayJobs(fixture.ctx)
		if listErr != nil || len(jobs) != 0 {
			t.Fatalf("revoked session persisted jobs = %+v, err=%v", jobs, listErr)
		}
		if err := fixture.connectIdentityResult(fixture.source.Identity); err == nil {
			t.Fatal("revoked source established a new mutual-TLS session")
		}
	})

	t.Run("impersonation", func(t *testing.T) {
		fixture := newTrustedSyncFixture(t)
		fixture.pairDaemons(t)
		imposter := deterministicIdentity(t, 73)
		if err := fixture.connectIdentityResult(imposter); err == nil {
			t.Fatal("unpaired identity established a mutual-TLS session")
		}
	})
}

func TestTwoDaemonTrustedSyncRetainsUnavailableRootAndDeletionGuards(t *testing.T) {
	fixture := newTrustedSyncFixtureWithDeletionPercent(t, 10)
	fixture.writeSource(t, "guarded.txt", "retain me")
	current := fixture.scanCurrent(t, "guarded.txt")

	if err := os.RemoveAll(fixture.sourceRoot); err != nil {
		t.Fatalf("remove source root: %v", err)
	}
	unavailable, err := fixture.source.ScanOnce(fixture.ctx, fixture.shareID)
	if err != nil || !unavailable.Unavailable || !unavailable.Skipped || unavailable.Committed {
		t.Fatalf("unavailable-root diagnostic = %+v, err=%v", unavailable, err)
	}
	fixture.assertSourceRevision(t, current.ID)

	if err := os.MkdirAll(fixture.sourceRoot, 0o700); err != nil {
		t.Fatalf("recreate empty source root: %v", err)
	}
	fixture.setSourceRootError(errors.New(`open C:\sensitive\root: token=top-secret authorization=Bearer bearer-value`))
	sanitized, err := fixture.source.ScanOnce(fixture.ctx, fixture.shareID)
	if err != nil || !sanitized.Unavailable {
		t.Fatalf("sanitized unavailable-root diagnostic = %+v, err=%v", sanitized, err)
	}
	for _, leaked := range []string{`C:\sensitive\root`, "top-secret", "bearer-value"} {
		if strings.Contains(sanitized.Error, leaked) {
			t.Fatalf("diagnostic leaked %q: %q", leaked, sanitized.Error)
		}
	}
	fixture.setSourceRootError(nil)

	blocked, err := fixture.source.ScanOnce(fixture.ctx, fixture.shareID)
	if err != nil || !blocked.Blocked || blocked.Committed || blocked.Revisions != 0 {
		t.Fatalf("deletion-guard diagnostic = %+v, err=%v", blocked, err)
	}
	fixture.assertSourceRevision(t, current.ID)
	if report := fixture.source.Diagnostics(); len(report.RecentScans) < 3 {
		t.Fatalf("retained diagnostics = %+v", report.RecentScans)
	}
}

type trustedSyncFixture struct {
	t            *testing.T
	ctx          context.Context
	now          time.Time
	shareID      core.ShareID
	sourceRoot   string
	targetRoot   string
	sourceConfig config.Config
	targetConfig config.Config
	source       *daemon.Daemon
	target       *daemon.Daemon

	rootMu      sync.RWMutex
	rootError   error
	workMu      sync.RWMutex
	pendingWork map[string]*trustedSyncWork
	nextWorkID  int
	deletionPct int
	targetStop  context.CancelFunc
	targetDone  chan error
}

type trustedSyncWork struct {
	ready    chan struct{}
	prepared syncengine.PreparedOneWayChange
}

type trustedSyncSession struct {
	target   transportapi.Session
	source   transportapi.Session
	server   *tcptls.Transport
	cancel   context.CancelFunc
	closeOne sync.Once
}

func newTrustedSyncFixture(t *testing.T) *trustedSyncFixture {
	return newTrustedSyncFixtureWithDeletionPercent(t, 100)
}

func newTrustedSyncFixtureWithDeletionPercent(t *testing.T, deletionPercent int) *trustedSyncFixture {
	t.Helper()
	base := t.TempDir()
	fixture := &trustedSyncFixture{
		t: t, ctx: context.Background(), now: time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC),
		shareID: "trusted-sync", sourceRoot: filepath.Join(base, "source-root"), targetRoot: filepath.Join(base, "target-root"),
		pendingWork: make(map[string]*trustedSyncWork), deletionPct: deletionPercent,
	}
	for _, root := range []string{fixture.sourceRoot, fixture.targetRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("create share root: %v", err)
		}
	}
	fixture.sourceConfig = fixture.daemonConfig("source", filepath.Join(base, "source-data"), fixture.sourceRoot, "one_way_source")
	fixture.targetConfig = fixture.daemonConfig("target", filepath.Join(base, "target-data"), fixture.targetRoot, "one_way_target")
	fixture.source = fixture.bootstrapSource(t)
	fixture.target = fixture.bootstrapTarget(t)
	t.Cleanup(func() {
		fixture.stopTarget()
		_ = fixture.source.Close()
		_ = fixture.target.Close()
	})
	return fixture
}

func (fixture *trustedSyncFixture) daemonConfig(name, dataDir, root, mode string) config.Config {
	return config.Config{
		DeviceName: name, DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment,
		Identity: config.IdentityConfig{Store: config.IdentityStoreDevelopment, AllowInsecureDevelopmentFile: true},
		LocalAPI: config.LocalAPIConfig{Host: "127.0.0.1", Port: config.DefaultLocalAPIPort},
		Transfer: config.TransferConfig{ChunkSizeBytes: 1024, MaxParallelTransfers: 1},
		Shares: []config.ShareConfig{{
			ID: string(fixture.shareID), Name: "trusted sync", RootPath: root, Mode: mode,
			ScanIntervalSeconds: 3600, DeletionLimitCount: 100, DeletionLimitPercent: fixture.deletionPct,
			TargetDriftPolicy: config.DefaultTargetDriftPolicy,
		}},
	}
}

func (fixture *trustedSyncFixture) bootstrapSource(t *testing.T) *daemon.Daemon {
	t.Helper()
	localDaemon, err := daemon.Bootstrap(fixture.ctx, fixture.sourceConfig, daemon.Options{
		IdentityStore: identity.DevFileStore{Path: filepath.Join(fixture.sourceConfig.DataDir, daemon.IdentityFileName)},
		NewIdentity:   func() (identity.DeviceIdentity, error) { return deterministicIdentity(t, 11), nil },
		CheckShareRoot: func(path string) error {
			fixture.rootMu.RLock()
			injected := fixture.rootError
			fixture.rootMu.RUnlock()
			if injected != nil {
				return injected
			}
			return syncengine.CheckShareRoot(path)
		},
		Now: fixture.clock, TombstoneRetention: 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("bootstrap source daemon: %v", err)
	}
	return localDaemon
}

func (fixture *trustedSyncFixture) bootstrapTarget(t *testing.T) *daemon.Daemon {
	t.Helper()
	localDaemon, err := daemon.Bootstrap(fixture.ctx, fixture.targetConfig, daemon.Options{
		IdentityStore:   identity.DevFileStore{Path: filepath.Join(fixture.targetConfig.DataDir, daemon.IdentityFileName)},
		NewIdentity:     func() (identity.DeviceIdentity, error) { return deterministicIdentity(t, 29), nil },
		Now:             fixture.clock,
		JobExecutor:     fixture.executeJob,
		JobPollInterval: time.Millisecond,
		JobRetryBase:    time.Millisecond,
		JobMaxBackoff:   5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("bootstrap target daemon: %v", err)
	}
	return localDaemon
}

func (fixture *trustedSyncFixture) pairDaemons(t *testing.T) {
	t.Helper()
	fixture.acceptPairing(t, fixture.source, fixture.target, []pairing.Grant{{
		ShareID: fixture.shareID,
		Capabilities: []core.Capability{
			core.CapabilitySync, core.CapabilityUpload, core.CapabilityModify, core.CapabilityDelete,
		},
		LANOnly: true,
	}})
	fixture.acceptPairing(t, fixture.target, fixture.source, nil)
}

func (fixture *trustedSyncFixture) acceptPairing(t *testing.T, invited, accepting *daemon.Daemon, grants []pairing.Grant) {
	t.Helper()
	created, err := (pairing.Service{
		Audit: invited.Store.Audit(), Now: fixture.clock, Random: bytes.NewReader(bytes.Repeat([]byte{byte(41 + len(grants))}, 512)),
	}).CreateInvitation(fixture.ctx, invited.Identity, invited.Config.DeviceName, 10*time.Minute, nil)
	if err != nil {
		t.Fatalf("create %s pairing invitation: %v", invited.Config.DeviceName, err)
	}
	_, err = (pairing.Service{
		Pairings: accepting.Store.Pairings(), Now: fixture.clock, Random: bytes.NewReader(bytes.Repeat([]byte{61}, 128)),
	}).Accept(fixture.ctx, pairing.AcceptRequest{
		LocalDeviceID: accepting.Identity.DeviceID, EncodedInvite: created.Encoded,
		ExpectedFingerprint: created.Invite.Fingerprint, OneTimeCode: created.Invite.OneTimeCode, Grants: grants,
	})
	if err != nil {
		t.Fatalf("accept %s pairing invitation on %s: %v", invited.Config.DeviceName, accepting.Config.DeviceName, err)
	}
}

func (fixture *trustedSyncFixture) connectSource(t *testing.T) *trustedSyncSession {
	t.Helper()
	serverTLS, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity: fixture.target.Identity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: fixture.target.Store.Devices()}, Server: true,
	})
	if err != nil {
		t.Fatalf("target TLS config: %v", err)
	}
	clientTLS, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity: fixture.source.Identity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: fixture.source.Store.Devices()},
	})
	if err != nil {
		t.Fatalf("source TLS config: %v", err)
	}
	ctx, cancel := context.WithCancel(fixture.ctx)
	server := tcptls.New("127.0.0.1:0", serverTLS)
	sessions, err := server.Listen(ctx)
	if err != nil {
		cancel()
		t.Fatalf("listen for source: %v", err)
	}
	client := tcptls.New(server.Address, clientTLS)
	sourceSession, err := client.Connect(ctx, fixture.target.Identity.DeviceID)
	if err != nil {
		_ = server.Close()
		cancel()
		t.Fatalf("connect paired source: %v", err)
	}
	var targetSession transportapi.Session
	select {
	case targetSession = <-sessions:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out accepting paired source session")
	}
	if targetSession == nil || targetSession.RemoteDeviceID() != fixture.source.Identity.DeviceID {
		t.Fatalf("target authenticated peer = %v", targetSession)
	}
	return &trustedSyncSession{target: targetSession, source: sourceSession, server: server, cancel: cancel}
}

func (fixture *trustedSyncFixture) connectIdentityResult(candidate identity.DeviceIdentity) error {
	serverTLS, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity: fixture.target.Identity, PeerVerifier: pairing.TrustedPeerVerifier{Devices: fixture.target.Store.Devices()}, Server: true,
	})
	if err != nil {
		return err
	}
	clientTLS, err := tcptls.IdentityTLSConfig(tcptls.IdentityTLSConfigOptions{
		Identity: candidate, PeerVerifier: pairing.TrustedPeerVerifier{Devices: fixture.source.Store.Devices()},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancel()
	server := tcptls.New("127.0.0.1:0", serverTLS)
	sessions, err := server.Listen(ctx)
	if err != nil {
		return err
	}
	defer server.Close()
	client := tcptls.New(server.Address, clientTLS)
	clientSession, err := client.Connect(ctx, fixture.target.Identity.DeviceID)
	if err != nil {
		return err
	}
	defer clientSession.Close()
	select {
	case accepted := <-sessions:
		if accepted != nil {
			_ = accepted.Close()
			return nil
		}
		return errors.New("target rejected peer session")
	case <-time.After(250 * time.Millisecond):
		return errors.New("target rejected peer session")
	}
}

func (session *trustedSyncSession) close() {
	if session == nil {
		return
	}
	session.closeOne.Do(func() {
		_ = session.target.Close()
		_ = session.source.Close()
		_ = session.server.Close()
		session.cancel()
	})
}

func (fixture *trustedSyncFixture) queueRevision(t *testing.T, session transportapi.Session, revision core.Revision, action syncengine.OneWayAction) syncengine.AuthenticatedOneWayWorkResult {
	t.Helper()
	result, err := fixture.queueRevisionResult(session, revision, action)
	if err != nil {
		t.Fatalf("queue %s revision %s: %v", action, revision.ID, err)
	}
	return result
}

func (fixture *trustedSyncFixture) queueRevisionResult(session transportapi.Session, revision core.Revision, action syncengine.OneWayAction) (syncengine.AuthenticatedOneWayWorkResult, error) {
	fixture.nextWorkID++
	id := fixture.nextWorkID
	jobID := fmt.Sprintf("trusted-job-%d", id)
	transferID := core.TransferID(fmt.Sprintf("trusted-transfer-%d", id))
	pending := &trustedSyncWork{ready: make(chan struct{})}
	fixture.workMu.Lock()
	fixture.pendingWork[jobID] = pending
	fixture.workMu.Unlock()

	targetRevisionID := core.RevisionID("")
	if current, err := fixture.target.Store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, revision.RelativePath); err == nil {
		targetRevisionID = current.ID
	} else if !errors.Is(err, storage.ErrNotFound) {
		return syncengine.AuthenticatedOneWayWorkResult{}, err
	}
	preparation := syncengine.OneWayChangePreparationRequest{
		Change: syncengine.OneWayChangeRequest{
			ProtocolVersion: syncengine.RevisionManifestProtocolVersion, RequestID: "change-" + jobID,
			SourceDeviceID: fixture.source.Identity.DeviceID, TargetDeviceID: fixture.target.Identity.DeviceID,
			ShareID: fixture.shareID, RevisionID: revision.ID, ExpectedBaseRevisionID: revision.ParentRevisionID, Action: action,
		},
		Source:         syncengine.OneWaySourcePolicy{ShareID: fixture.shareID, DeviceID: fixture.source.Identity.DeviceID, Mode: storage.ShareOneWaySource},
		Target:         syncengine.OneWayTargetPolicy{ShareID: fixture.shareID, Mode: storage.ShareOneWayTarget, DriftPolicy: syncengine.TargetDriftReject},
		SourceRevision: revision, TargetRevisionID: targetRevisionID, Remote: false,
	}
	result, err := (syncengine.AuthenticatedOneWayWorkService{
		Preparation: syncengine.OneWayChangePreparationService{Shares: fixture.target.Store.Shares()},
		Work:        fixture.target.Store.OneWayWork(), Now: fixture.clock,
		NewTransferID: func() (core.TransferID, error) { return transferID, nil },
		NewJobID:      func() (string, error) { return jobID, nil },
	}).PrepareAndQueue(fixture.ctx, session, syncengine.AuthenticatedOneWayWorkRequest{
		Advertisement: trustedSyncAdvertisement(preparation), Preparation: preparation, ChunkSize: 1024,
	})
	if err != nil {
		fixture.workMu.Lock()
		delete(fixture.pendingWork, jobID)
		fixture.workMu.Unlock()
		return syncengine.AuthenticatedOneWayWorkResult{}, err
	}
	pending.prepared = result.Prepared
	close(pending.ready)
	return result, nil
}

func trustedSyncAdvertisement(preparation syncengine.OneWayChangePreparationRequest) syncengine.RevisionAdvertisementRequest {
	revision := preparation.SourceRevision
	return syncengine.RevisionAdvertisementRequest{
		ProtocolVersion: preparation.Change.ProtocolVersion, RequestID: "advertisement-" + preparation.Change.RequestID,
		SourceDeviceID: preparation.Change.SourceDeviceID, TargetDeviceID: preparation.Change.TargetDeviceID,
		ShareID: preparation.Change.ShareID, Limits: syncengine.RevisionManifestLimits{MaxEntries: 1, MaxBytes: 4096},
		Revisions: []syncengine.RevisionManifestEntry{{
			RevisionID: revision.ID, ParentRevisionID: revision.ParentRevisionID, RelativePath: revision.RelativePath,
			EntryType: revision.EntryType, Size: revision.Size, ContentHash: revision.ContentHash,
			HashAlgorithm: revision.HashAlgorithm, Sequence: revision.Sequence, IsDeleted: revision.IsDeleted,
		}},
	}
}

func (fixture *trustedSyncFixture) executeJob(ctx context.Context, job core.OneWayJob) error {
	fixture.workMu.RLock()
	pending := fixture.pendingWork[job.ID]
	fixture.workMu.RUnlock()
	if pending == nil {
		return fmt.Errorf("accepted work %s is unavailable", job.ID)
	}
	select {
	case <-pending.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	prepared := pending.prepared
	if !prepared.SourceRevision.IsDeleted && prepared.SourceRevision.EntryType == core.EntryFile {
		if err := fixture.stagePrepared(prepared); err != nil {
			return err
		}
	}
	request := syncengine.OneWayApplyRequest{
		AuthenticatedPeerID: job.PeerDeviceID, Prepared: prepared, ShareRoot: fixture.targetRoot,
	}
	if prepared.SourceRevision.IsDeleted {
		request.TombstoneID = core.TombstoneID("tombstone-" + job.ID)
		request.TombstoneExpiresAt = fixture.clock().Add(24 * time.Hour)
	}
	_, err := (syncengine.OneWayApplyExecutor{
		Revisions: fixture.target.Store.Revisions(), Applier: fixture.target.Store.FileIndex(),
		Tombstones: fixture.target.Store.Tombstones(), Shares: fixture.target.Store.Shares(), Now: fixture.clock,
	}).Apply(ctx, request)
	return err
}

func (fixture *trustedSyncFixture) stagePrepared(prepared syncengine.PreparedOneWayChange) error {
	spec, err := syncengine.BuildOneWayReceiveSpec(fixture.targetRoot, prepared)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(fixture.sourceRoot, filepath.FromSlash(prepared.RelativePath)))
	if err != nil {
		return err
	}
	writer, err := transfer.NewReceiveWriter(spec)
	if err != nil {
		return err
	}
	defer writer.Close()
	if _, err := writer.Write(raw); err != nil {
		return err
	}
	_, err = writer.Commit()
	return err
}

func (fixture *trustedSyncFixture) startTarget(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(fixture.ctx)
	fixture.targetStop = cancel
	fixture.targetDone = make(chan error, 1)
	go func() { fixture.targetDone <- fixture.target.Run(ctx) }()
}

func (fixture *trustedSyncFixture) stopTarget() {
	if fixture.targetStop == nil {
		return
	}
	fixture.targetStop()
	select {
	case <-fixture.targetDone:
	case <-time.After(5 * time.Second):
	}
	fixture.targetStop = nil
}

func (fixture *trustedSyncFixture) waitForJob(t *testing.T, id string, want core.OneWayJobState) core.OneWayJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := fixture.target.Store.OneWayJobs().GetOneWayJob(fixture.ctx, id)
		if err == nil && job.State == want {
			return job
		}
		if err == nil && job.State == core.OneWayJobFailed {
			t.Fatalf("job %s failed: %s", id, job.LastError)
		}
		time.Sleep(time.Millisecond)
	}
	job, err := fixture.target.Store.OneWayJobs().GetOneWayJob(fixture.ctx, id)
	t.Fatalf("job %s = %+v, err=%v; want %s", id, job, err, want)
	return core.OneWayJob{}
}

func (fixture *trustedSyncFixture) scanCurrent(t *testing.T, relativePath string) core.Revision {
	t.Helper()
	diagnostic, err := fixture.source.ScanOnce(fixture.ctx, fixture.shareID)
	if err != nil || !diagnostic.Committed || diagnostic.Blocked {
		t.Fatalf("source scan = %+v, err=%v", diagnostic, err)
	}
	revision, err := fixture.source.Store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, relativePath)
	if err != nil {
		t.Fatalf("current source revision for %s: %v", relativePath, err)
	}
	return revision
}

func (fixture *trustedSyncFixture) writeSource(t *testing.T, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(fixture.sourceRoot, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create source parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
}

func (fixture *trustedSyncFixture) assertTargetFile(t *testing.T, relativePath, want string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixture.targetRoot, filepath.FromSlash(relativePath)))
	if err != nil || string(raw) != want {
		t.Fatalf("target file = %q, err=%v; want %q", raw, err, want)
	}
}

func (fixture *trustedSyncFixture) assertSourceRevision(t *testing.T, want core.RevisionID) {
	t.Helper()
	current, err := fixture.source.Store.Revisions().GetCurrentRevision(fixture.ctx, fixture.shareID, "guarded.txt")
	if err != nil || current.ID != want || current.IsDeleted {
		t.Fatalf("retained source revision = %+v, err=%v", current, err)
	}
}

func (fixture *trustedSyncFixture) setSourceRootError(err error) {
	fixture.rootMu.Lock()
	fixture.rootError = err
	fixture.rootMu.Unlock()
}

func (fixture *trustedSyncFixture) clock() time.Time { return fixture.now }

func deterministicIdentity(t *testing.T, seed byte) identity.DeviceIdentity {
	t.Helper()
	deviceIdentity, err := identity.GenerateDeviceIdentity(bytes.NewReader(bytes.Repeat([]byte{seed}, 64)))
	if err != nil {
		t.Fatalf("generate deterministic identity: %v", err)
	}
	return deviceIdentity
}
