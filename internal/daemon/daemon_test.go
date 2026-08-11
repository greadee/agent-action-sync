package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"syncgate/internal/config"
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
		DeviceName: "TEST-DEVICE",
		DataDir:    dataDir,
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
