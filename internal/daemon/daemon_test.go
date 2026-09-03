package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
)

func TestDaemonRunsAuthoritativeStartupAndManualScans(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeTestFile(t, filepath.Join(shareRoot, "notes.txt"), "initial")
	watcher := newDaemonWatcher(4)
	daemon, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory: func(string) (syncengine.Watcher, error) { return watcher, nil },
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	startup := waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup
	})
	if !startup.Committed || startup.Unavailable || startup.Error != "" {
		t.Fatalf("startup diagnostic = %+v", startup)
	}
	entries, err := daemon.Store.FileIndex().List(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("list file index: %v", err)
	}
	if len(entries) != 1 || entries[0].RelativePath != "notes.txt" {
		t.Fatalf("startup file index = %+v", entries)
	}

	if err := daemon.RequestScan(context.Background(), "share-1"); err != nil {
		t.Fatalf("request manual scan: %v", err)
	}
	manual := waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerManual
	})
	if !manual.Committed || manual.Error != "" {
		t.Fatalf("manual diagnostic = %+v", manual)
	}

	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonComposesOrchestrationOnlyWhenExecutionEnabled(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	cfg := testConfig(dataDir, shareRoot)
	runtimePath := filepath.Join(t.TempDir(), "runtime.exe")
	cfg.Node.Execution = config.ExecutionConfig{
		Enabled: true, ProviderID: "codex", ModelID: "gpt-5.6-sol", RuntimeExecutable: runtimePath,
		PreflightReceipt: strings.Repeat("a", 64), MaxConcurrent: 1,
	}
	called := 0
	composer := func(context.Context, config.Config, storage.Store, identity.DeviceIdentity) (OrchestrationComponents, error) {
		called++
		return OrchestrationComponents{Scheduler: &orderedOrchestrationScheduler{events: &orderedEvents{}}, Administration: disabledOrchestrationFacade{}, Setup: disabledSetupFacade{}}, nil
	}
	instance, err := Bootstrap(context.Background(), cfg, Options{ComposeOrchestration: composer})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || instance.orchestrationScheduler == nil || instance.orchestrationAdmin == nil || instance.setupAdmin == nil {
		t.Fatalf("composition called=%d scheduler=%v admin=%v setup=%v", called, instance.orchestrationScheduler, instance.orchestrationAdmin, instance.setupAdmin)
	}
	_ = instance.Close()

	cfg.Node.Execution.Enabled = false
	called = 0
	instance, err = Bootstrap(context.Background(), cfg, Options{ComposeOrchestration: composer})
	if err != nil {
		t.Fatal(err)
	}
	if called != 0 || instance.orchestrationScheduler != nil {
		t.Fatalf("disabled execution composed runtime: called=%d scheduler=%v", called, instance.orchestrationScheduler)
	}
	_ = instance.Close()

	cfg.Node.Execution.Enabled = true
	if _, err := Bootstrap(context.Background(), cfg, Options{}); err == nil || !strings.Contains(err.Error(), "composition is unavailable") {
		t.Fatalf("missing enabled composition error = %v", err)
	}
}

func TestDaemonRequestsProjectIngestionAfterCommittedScan(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeTestFile(t, filepath.Join(shareRoot, "notes.txt"), "initial")
	var requests atomic.Int32
	daemon, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		RequestProjectIngestion: func(_ context.Context, shareID core.ShareID, rootPath string) error {
			if shareID == "share-1" && rootPath == shareRoot {
				requests.Add(1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer daemon.Close()
	diagnostic, err := daemon.ScanOnce(context.Background(), "share-1")
	if err != nil || !diagnostic.Committed {
		t.Fatalf("scan diagnostic=%#v err=%v", diagnostic, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("ingestion requests = %d, want 1", requests.Load())
	}
}

func TestDaemonUnavailableRootDoesNotCommitOrCreateDeletion(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeTestFile(t, filepath.Join(shareRoot, "keep.txt"), "keep")
	watcher := newDaemonWatcher(4)
	daemon, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory: func(string) (syncengine.Watcher, error) { return watcher, nil },
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup
	})

	if err := os.RemoveAll(shareRoot); err != nil {
		t.Fatalf("remove share root: %v", err)
	}
	if err := daemon.RequestScan(context.Background(), "share-1"); err != nil {
		t.Fatalf("request unavailable-root scan: %v", err)
	}
	unavailable := waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerManual && scan.Unavailable
	})
	if unavailable.Committed || unavailable.Started || unavailable.Error == "" {
		t.Fatalf("unavailable diagnostic = %+v", unavailable)
	}
	entries, err := daemon.Store.FileIndex().List(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("list file index after root loss: %v", err)
	}
	if len(entries) != 1 || entries[0].IsDeleted {
		t.Fatalf("file index changed after root loss = %+v", entries)
	}

	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonDeletionGuardBlocksSnapshotAndRetainsDiagnostics(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeTestFile(t, filepath.Join(shareRoot, "one.txt"), "one")
	writeTestFile(t, filepath.Join(shareRoot, "two.txt"), "two")
	watcher := newDaemonWatcher(4)
	cfg := testConfigWithInterval(dataDir, shareRoot, 3600)
	cfg.Shares[0].DeletionLimitCount = 1
	cfg.Shares[0].DeletionLimitPercent = 100
	daemon, err := Bootstrap(context.Background(), cfg, Options{
		WatcherFactory:  func(string) (syncengine.Watcher, error) { return watcher, nil },
		RecentScanLimit: 2,
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup
	})
	if err := os.RemoveAll(shareRoot); err != nil {
		t.Fatalf("remove share root: %v", err)
	}
	if err := os.MkdirAll(shareRoot, 0o700); err != nil {
		t.Fatalf("recreate share root: %v", err)
	}
	if err := daemon.RequestScan(context.Background(), "share-1"); err != nil {
		t.Fatalf("request guard scan: %v", err)
	}
	blocked := waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerManual && scan.Blocked
	})
	if blocked.Committed || len(blocked.Reasons) == 0 {
		t.Fatalf("blocked diagnostic = %+v", blocked)
	}
	entries, err := daemon.Store.FileIndex().List(context.Background(), "share-1")
	if err != nil {
		t.Fatalf("list file index after guard block: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("guard block changed file index = %+v", entries)
	}

	report := daemon.Diagnostics()
	if len(report.RecentScans) > 2 {
		t.Fatalf("diagnostic retention = %d, want at most 2", len(report.RecentScans))
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonRestartRetainsIndexAndIdentity(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	writeTestFile(t, filepath.Join(shareRoot, "restart.txt"), "restart")

	firstWatcher := newDaemonWatcher(2)
	first, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory: func(string) (syncengine.Watcher, error) { return firstWatcher, nil },
	})
	if err != nil {
		t.Fatalf("bootstrap first daemon: %v", err)
	}
	firstID := first.Identity.DeviceID
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstDone := runDaemon(t, first, firstCtx)
	waitForDiagnostic(t, first, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup && scan.Committed
	})
	firstCancel()
	if err := waitForDaemon(t, firstDone); err != nil {
		t.Fatalf("shutdown first daemon: %v", err)
	}

	secondWatcher := newDaemonWatcher(2)
	second, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory: func(string) (syncengine.Watcher, error) { return secondWatcher, nil },
	})
	if err != nil {
		t.Fatalf("bootstrap second daemon: %v", err)
	}
	if second.Identity.DeviceID != firstID {
		t.Fatalf("restart identity = %q, want %q", second.Identity.DeviceID, firstID)
	}
	secondCtx, secondCancel := context.WithCancel(context.Background())
	secondDone := runDaemon(t, second, secondCtx)
	waitForDiagnostic(t, second, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup && scan.Committed
	})
	entries, err := second.Store.FileIndex().List(context.Background(), "share-1")
	if err != nil || len(entries) != 1 {
		t.Fatalf("restart file index = %+v, err=%v", entries, err)
	}
	secondCancel()
	if err := waitForDaemon(t, secondDone); err != nil {
		t.Fatalf("shutdown second daemon: %v", err)
	}
}

func TestDaemonWatcherRecoveryReachesScheduler(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	firstWatcher := newDaemonWatcher(4)
	secondWatcher := newDaemonWatcher(4)
	var mu sync.Mutex
	watchers := []syncengine.Watcher{firstWatcher, secondWatcher}
	daemon, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory: func(string) (syncengine.Watcher, error) {
			mu.Lock()
			defer mu.Unlock()
			if len(watchers) == 0 {
				return nil, errors.New("watcher factory exhausted")
			}
			watcher := watchers[0]
			watchers = watchers[1:]
			return watcher, nil
		},
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerStartup
	})
	firstWatcher.close()
	deadline := time.Now().Add(time.Second)
	for {
		mu.Lock()
		restarted := len(watchers) == 0
		mu.Unlock()
		if restarted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon watcher did not restart")
		}
		time.Sleep(time.Millisecond)
	}
	secondWatcher.events <- syncengine.WatchEvent{Path: filepath.Join(shareRoot, "recovered.txt")}
	recovered := waitForDiagnostic(t, daemon, func(scan syncengine.ScanDiagnostic) bool {
		return scan.Trigger == syncengine.ScanTriggerEvents
	})
	if !recovered.Committed {
		t.Fatalf("recovered diagnostic = %+v", recovered)
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonSupervisesOneWayJobsWithConfiguredConcurrency(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	watcher := newDaemonWatcher(2)
	cfg := testConfigWithInterval(dataDir, shareRoot, 3600)
	cfg.Transfer.MaxParallelTransfers = 1
	var active, maxActive atomic.Int32
	daemon, err := Bootstrap(context.Background(), cfg, Options{
		WatcherFactory:  func(string) (syncengine.Watcher, error) { return watcher, nil },
		JobPollInterval: time.Millisecond,
		JobExecutor: func(context.Context, core.OneWayJob) error {
			current := active.Add(1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	saveDaemonJob(t, daemon, "job-one", core.OneWayJobQueued)
	saveDaemonJob(t, daemon, "job-two", core.OneWayJobQueued)

	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	waitForJobState(t, daemon, "job-one", core.OneWayJobCompleted)
	waitForJobState(t, daemon, "job-two", core.OneWayJobCompleted)
	if maxActive.Load() != 1 {
		t.Fatalf("job queue max concurrency = %d, want 1", maxActive.Load())
	}
	if work := daemon.Diagnostics().Work; len(work) != 0 {
		t.Fatalf("completed jobs remained in diagnostics: %+v", work)
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonRestartRecoversRunningJobOnce(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	first, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{})
	if err != nil {
		t.Fatalf("bootstrap first daemon: %v", err)
	}
	saveDaemonJob(t, first, "recovered", core.OneWayJobRunning)
	if err := first.Close(); err != nil {
		t.Fatalf("close first daemon: %v", err)
	}

	watcher := newDaemonWatcher(2)
	var executions atomic.Int32
	second, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory:  func(string) (syncengine.Watcher, error) { return watcher, nil },
		JobPollInterval: time.Millisecond,
		JobExecutor: func(context.Context, core.OneWayJob) error {
			executions.Add(1)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("bootstrap second daemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, second, ctx)
	waitForJobState(t, second, "recovered", core.OneWayJobCompleted)
	if executions.Load() != 1 {
		t.Fatalf("recovered job executions = %d, want 1", executions.Load())
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestDaemonFailsClosedWithoutPeerJobExecutor(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	watcher := newDaemonWatcher(2)
	daemon, err := Bootstrap(context.Background(), testConfigWithInterval(dataDir, shareRoot, 3600), Options{
		WatcherFactory:  func(string) (syncengine.Watcher, error) { return watcher, nil },
		JobPollInterval: time.Millisecond,
		JobRetryBase:    time.Hour,
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}
	saveDaemonJob(t, daemon, "disabled", core.OneWayJobQueued)
	ctx, cancel := context.WithCancel(context.Background())
	done := runDaemon(t, daemon, ctx)
	job := waitForJobState(t, daemon, "disabled", core.OneWayJobRetryWait)
	if job.LastError != ErrAutomaticPeerWorkDisabled.Error() {
		t.Fatalf("disabled job error = %q", job.LastError)
	}
	work := daemon.Diagnostics().Work
	if len(work) != 1 || work[0].LastError != ErrAutomaticPeerWorkDisabled.Error() {
		t.Fatalf("disabled job diagnostics = %+v", work)
	}
	cancel()
	if err := waitForDaemon(t, done); err != nil {
		t.Fatalf("shutdown daemon: %v", err)
	}
}

func TestBootstrapCreatesAndReloadsDurableRuntime(t *testing.T) {
	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	cfg := testConfig(dataDir, shareRoot)

	first, err := Bootstrap(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("bootstrap first daemon: %v", err)
	}
	firstID := first.Identity.DeviceID
	if err := first.Close(); err != nil {
		t.Fatalf("close first daemon: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, DatabaseFileName)); err != nil {
		t.Fatalf("database was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, IdentityFileName)); err != nil {
		t.Fatalf("identity store was not created: %v", err)
	}

	second, err := Bootstrap(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("bootstrap second daemon: %v", err)
	}
	defer second.Close()
	if second.Identity.DeviceID != firstID {
		t.Fatalf("reloaded device ID = %q, want %q", second.Identity.DeviceID, firstID)
	}
}

func TestBootstrapKeepsPrivateIdentityOutOfSQLiteAndDiagnostics(t *testing.T) {
	dataDir := t.TempDir()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5a}, ed25519.SeedSize))
	deviceIdentity, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("create deterministic identity: %v", err)
	}
	identityStore := &memoryIdentityStore{deviceIdentity: deviceIdentity, exists: true}
	localDaemon, err := Bootstrap(context.Background(), testConfig(dataDir, t.TempDir()), Options{
		IdentityStore: identityStore,
	})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}

	diagnostics, err := json.Marshal(localDaemon.Diagnostics())
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	assertPrivateIdentityAbsent(t, diagnostics, deviceIdentity.PrivateKey, "diagnostics")
	if err := localDaemon.Close(); err != nil {
		t.Fatalf("close daemon: %v", err)
	}

	databaseFiles, err := filepath.Glob(filepath.Join(dataDir, DatabaseFileName+"*"))
	if err != nil {
		t.Fatalf("find SQLite files: %v", err)
	}
	if len(databaseFiles) == 0 {
		t.Fatal("SQLite database was not created")
	}
	for _, databasePath := range databaseFiles {
		database, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatalf("read SQLite file %s: %v", filepath.Base(databasePath), err)
		}
		assertPrivateIdentityAbsent(t, database, deviceIdentity.PrivateKey, filepath.Base(databasePath))
	}
}

func TestBootstrapRejectsUnavailableShareBeforeOpeningStorage(t *testing.T) {
	opened := false
	cfg := testConfig(t.TempDir(), filepath.Join(t.TempDir(), "missing-share"))

	_, err := Bootstrap(context.Background(), cfg, Options{
		OpenStore: func(string) (storage.Store, error) {
			opened = true
			return nil, errors.New("store should not be opened")
		},
	})
	if err == nil {
		t.Fatal("expected unavailable share root to fail bootstrap")
	}
	if !errors.Is(err, syncengine.ErrUnavailableRoot) {
		t.Fatalf("bootstrap error = %v, want unavailable root: %v", err, syncengine.ErrUnavailableRoot)
	}
	if opened {
		t.Fatal("bootstrap opened storage before validating share roots")
	}
}

func TestBootstrapClosesStorageAfterMigrationFailure(t *testing.T) {
	fake := &failingStore{err: errors.New("migration failed")}
	cfg := testConfig(t.TempDir(), t.TempDir())

	_, err := Bootstrap(context.Background(), cfg, Options{
		OpenStore: func(string) (storage.Store, error) { return fake, nil },
	})
	if err == nil || !errors.Is(err, fake.err) {
		t.Fatalf("bootstrap error = %v, want migration failure", err)
	}
	if fake.closeCount != 1 {
		t.Fatalf("storage close count = %d, want 1", fake.closeCount)
	}
}

func TestRunCancelsAndClosesStorage(t *testing.T) {
	daemon, err := Bootstrap(context.Background(), testConfig(t.TempDir(), t.TempDir()), Options{})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run daemon: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not shut down after context cancellation")
	}
	if err := daemon.Close(); err != nil {
		t.Fatalf("close daemon a second time: %v", err)
	}
}

func TestRunRejectsDuplicateStart(t *testing.T) {
	daemon, err := Bootstrap(context.Background(), testConfig(t.TempDir(), t.TempDir()), Options{})
	if err != nil {
		t.Fatalf("bootstrap daemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		daemon.mu.Lock()
		running := daemon.cancel != nil
		daemon.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := daemon.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate run error = %v, want %v", err, ErrAlreadyRunning)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run daemon: %v", err)
	}
}

func testConfig(dataDir, shareRoot string) config.Config {
	return config.Config{
		DeviceName:  "TEST-DEVICE",
		DataDir:     dataDir,
		RuntimeMode: config.RuntimeModeDevelopment,
		Identity: config.IdentityConfig{
			Store:                        config.IdentityStoreDevelopment,
			AllowInsecureDevelopmentFile: true,
		},
		Shares: []config.ShareConfig{{
			ID:       "share-1",
			Name:     "Share 1",
			RootPath: shareRoot,
			Mode:     "one_way_source",
		}},
	}
}

func testConfigWithInterval(dataDir, shareRoot string, seconds int) config.Config {
	cfg := testConfig(dataDir, shareRoot)
	cfg.Shares[0].ScanIntervalSeconds = seconds
	return cfg
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func saveDaemonJob(t *testing.T, daemon *Daemon, id string, state core.OneWayJobState) {
	t.Helper()
	now := time.Now().UTC()
	transferID := core.TransferID("transfer-" + id)
	if err := daemon.Store.Transfers().SaveTransfer(context.Background(), core.Transfer{
		ID:           transferID,
		Direction:    core.TransferSend,
		PeerDeviceID: daemon.Identity.DeviceID,
		ShareID:      "share-1",
		RelativePath: id + ".txt",
		State:        core.TransferQueued,
		ChunkSize:    1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("save transfer: %v", err)
	}
	if err := daemon.Store.OneWayJobs().SaveOneWayJob(context.Background(), core.OneWayJob{
		ID:                 id,
		TransferID:         transferID,
		PeerDeviceID:       daemon.Identity.DeviceID,
		ShareID:            "share-1",
		RevisionID:         core.RevisionID("revision-" + id),
		RelativePath:       id + ".txt",
		RequiredCapability: core.CapabilityModify,
		State:              state,
		CreatedAt:          now,
		UpdatedAt:          now,
	}); err != nil {
		t.Fatalf("save one-way job: %v", err)
	}
}

func waitForJobState(t *testing.T, daemon *Daemon, id string, state core.OneWayJobState) core.OneWayJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		job, err := daemon.Store.OneWayJobs().GetOneWayJob(context.Background(), id)
		if err == nil && job.State == state {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for job %s state %s; last job=%+v err=%v", id, state, job, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func runDaemon(t *testing.T, daemon *Daemon, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	return done
}

func waitForDaemon(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		return errors.New("timed out waiting for daemon shutdown")
	}
}

func TestDaemonDrainsOrchestrationBeforeStorageClose(t *testing.T) {
	events := &orderedEvents{}
	scheduler := &orderedOrchestrationScheduler{events: events}
	store := &orderedCloseStore{events: events}
	daemon := &Daemon{Store: store, orchestrationScheduler: scheduler, orchestrationDrain: time.Second}
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	if got := events.values(); !equalStrings(got, []string{"scheduler_start", "scheduler_shutdown", "store_close"}) {
		t.Fatalf("shutdown order = %v", got)
	}
}

type orderedEvents struct {
	mu     sync.Mutex
	events []string
}

func (events *orderedEvents) add(value string) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.events = append(events.events, value)
}

func (events *orderedEvents) values() []string {
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]string(nil), events.events...)
}

type orderedOrchestrationScheduler struct{ events *orderedEvents }

func (scheduler *orderedOrchestrationScheduler) Start(context.Context) error {
	scheduler.events.add("scheduler_start")
	return nil
}

func (scheduler *orderedOrchestrationScheduler) Shutdown(context.Context) error {
	scheduler.events.add("scheduler_shutdown")
	return nil
}

type orderedCloseStore struct {
	storage.Store
	events *orderedEvents
}

func (store *orderedCloseStore) Close() error {
	store.events.add("store_close")
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func waitForDiagnostic(t *testing.T, daemon *Daemon, match func(syncengine.ScanDiagnostic) bool) syncengine.ScanDiagnostic {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	seen := 0
	for {
		report := daemon.Diagnostics()
		for _, scan := range report.RecentScans[seen:] {
			seen++
			if match(scan) {
				return scan
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for diagnostic, report=%+v", report)
		}
		time.Sleep(time.Millisecond)
	}
}

type failingStore struct {
	storage.Store
	err        error
	closeCount int
}

func (store *failingStore) Migrate(context.Context) error { return store.err }

func (store *failingStore) Close() error {
	store.closeCount++
	return nil
}

var _ storage.Store = (*sqlite.Store)(nil)
var _ identity.PrivateKeyStore = identity.DevFileStore{}

type memoryIdentityStore struct {
	deviceIdentity identity.DeviceIdentity
	exists         bool
}

func (store *memoryIdentityStore) Save(deviceIdentity identity.DeviceIdentity) error {
	store.deviceIdentity = deviceIdentity
	store.exists = true
	return nil
}

func (store *memoryIdentityStore) Load() (identity.DeviceIdentity, error) {
	if !store.exists {
		return identity.DeviceIdentity{}, identity.ErrIdentityNotFound
	}
	return store.deviceIdentity, nil
}

func assertPrivateIdentityAbsent(t *testing.T, content []byte, privateKey ed25519.PrivateKey, location string) {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString(privateKey)
	if bytes.Contains(content, privateKey) ||
		bytes.Contains(content, []byte(encoded)) ||
		bytes.Contains(bytes.ToLower(content), []byte("private_key")) {
		t.Fatalf("%s contains private identity material", location)
	}
}

type daemonWatcher struct {
	events chan syncengine.WatchEvent
	errs   chan error
	once   sync.Once
}

func newDaemonWatcher(size int) *daemonWatcher {
	return &daemonWatcher{events: make(chan syncengine.WatchEvent, size), errs: make(chan error, size)}
}

func (watcher *daemonWatcher) Events() <-chan syncengine.WatchEvent { return watcher.events }
func (watcher *daemonWatcher) Errors() <-chan error                 { return watcher.errs }
func (watcher *daemonWatcher) Close() error {
	watcher.close()
	return nil
}
func (watcher *daemonWatcher) close() {
	watcher.once.Do(func() {
		close(watcher.events)
		close(watcher.errs)
	})
}
