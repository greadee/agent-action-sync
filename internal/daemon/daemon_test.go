package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/config"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
)

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
