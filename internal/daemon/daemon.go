package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
)

const (
	DatabaseFileName = "syncgate.db"
	IdentityFileName = "identity.json"
)

var ErrAlreadyRunning = errors.New("daemon is already running")
var ErrClosed = errors.New("daemon is closed")

type Options struct {
	OpenStore      func(string) (storage.Store, error)
	IdentityStore  identity.PrivateKeyStore
	NewIdentity    func() (identity.DeviceIdentity, error)
	CheckShareRoot func(string) error
}

type Daemon struct {
	Config   config.Config
	Store    storage.Store
	Identity identity.DeviceIdentity

	closeOnce sync.Once
	mu        sync.Mutex
	cancel    context.CancelFunc
	closed    bool
	closeErr  error
}

func Bootstrap(ctx context.Context, cfg config.Config, options Options) (*Daemon, error) {
	if ctx == nil {
		return nil, errors.New("bootstrap context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	checkRoot := options.CheckShareRoot
	if checkRoot == nil {
		checkRoot = syncengine.CheckShareRoot
	}
	for _, share := range cfg.Shares {
		if err := checkRoot(share.RootPath); err != nil {
			return nil, fmt.Errorf("validate share %q root: %w", share.ID, err)
		}
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	openStore := options.OpenStore
	if openStore == nil {
		openStore = func(path string) (storage.Store, error) {
			return sqlite.Open(path)
		}
	}
	store, err := openStore(filepath.Join(cfg.DataDir, DatabaseFileName))
	if err != nil {
		return nil, fmt.Errorf("open local storage: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = store.Close()
		}
	}()

	if err := store.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("migrate local storage: %w", err)
	}

	privateKeyStore := options.IdentityStore
	if privateKeyStore == nil {
		privateKeyStore = identity.DevFileStore{Path: filepath.Join(cfg.DataDir, IdentityFileName)}
	}
	deviceIdentity, err := loadOrCreateIdentity(privateKeyStore, options.NewIdentity)
	if err != nil {
		return nil, fmt.Errorf("initialize local identity: %w", err)
	}

	if err := store.Devices().TrustDevice(ctx, storage.Device{
		ID:          deviceIdentity.DeviceID,
		DisplayName: cfg.DeviceName,
		PublicKey:   append([]byte(nil), deviceIdentity.PublicKey...),
		Fingerprint: deviceIdentity.Fingerprint,
		TrustState:  storage.TrustTrusted,
	}); err != nil {
		return nil, fmt.Errorf("persist local device metadata: %w", err)
	}
	for _, share := range cfg.Shares {
		if err := store.Shares().SaveShare(ctx, storage.Share{
			ID:                   core.ShareID(share.ID),
			Name:                 share.Name,
			RootPath:             share.RootPath,
			Mode:                 storage.ShareMode(share.Mode),
			CasePolicy:           "platform",
			VersionPolicy:        "revisioned",
			DeletionLimitCount:   share.DeletionLimitCount,
			DeletionLimitPercent: share.DeletionLimitPercent,
		}); err != nil {
			return nil, fmt.Errorf("persist share %q: %w", share.ID, err)
		}
	}

	closed = true
	return &Daemon{
		Config:   cfg,
		Store:    store,
		Identity: deviceIdentity,
	}, nil
}

func RunConfig(ctx context.Context, configPath string, options Options) error {
	cfg, err := config.LoadFile(ctx, configPath)
	if err != nil {
		return err
	}
	daemon, err := Bootstrap(ctx, cfg, options)
	if err != nil {
		return err
	}
	return daemon.Run(ctx)
}

func (daemon *Daemon) Run(ctx context.Context) error {
	if daemon == nil {
		return errors.New("daemon is required")
	}
	if ctx == nil {
		return errors.New("run context is required")
	}
	if err := ctx.Err(); err != nil {
		return daemon.Close()
	}

	daemon.mu.Lock()
	if daemon.closed {
		daemon.mu.Unlock()
		return ErrClosed
	}
	if daemon.cancel != nil {
		daemon.mu.Unlock()
		return ErrAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(ctx)
	daemon.cancel = cancel
	daemon.mu.Unlock()

	<-runCtx.Done()
	return daemon.Close()
}

func (daemon *Daemon) Close() error {
	if daemon == nil {
		return nil
	}
	daemon.closeOnce.Do(func() {
		daemon.mu.Lock()
		daemon.closed = true
		cancel := daemon.cancel
		daemon.cancel = nil
		daemon.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		if daemon.Store != nil {
			daemon.closeErr = daemon.Store.Close()
		}
	})
	return daemon.closeErr
}

func loadOrCreateIdentity(store identity.PrivateKeyStore, newIdentity func() (identity.DeviceIdentity, error)) (identity.DeviceIdentity, error) {
	deviceIdentity, err := store.Load()
	if err == nil {
		return deviceIdentity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return identity.DeviceIdentity{}, err
	}
	if newIdentity == nil {
		newIdentity = func() (identity.DeviceIdentity, error) {
			return identity.GenerateDeviceIdentity(nil)
		}
	}
	deviceIdentity, err = newIdentity()
	if err != nil {
		return identity.DeviceIdentity{}, fmt.Errorf("generate identity: %w", err)
	}
	if err := store.Save(deviceIdentity); err != nil {
		return identity.DeviceIdentity{}, fmt.Errorf("save identity: %w", err)
	}
	return deviceIdentity, nil
}
