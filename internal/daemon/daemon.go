package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	OpenStore          func(string) (storage.Store, error)
	IdentityStore      identity.PrivateKeyStore
	NewIdentity        func() (identity.DeviceIdentity, error)
	CheckShareRoot     func(string) error
	WatcherFactory     syncengine.WatcherFactory
	Now                func() time.Time
	TombstoneRetention time.Duration
	RecentScanLimit    int
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

	runtimes        map[core.ShareID]shareRuntime
	runtimeWG       sync.WaitGroup
	diagnosticsMu   sync.RWMutex
	recentScans     []syncengine.ScanDiagnostic
	recentScanLimit int
	nowFn           func() time.Time
}

type shareRuntime struct {
	manual  chan struct{}
	runtime syncengine.ShareRuntime
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

	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.RecentScanLimit <= 0 {
		options.RecentScanLimit = 32
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

	runtimes, err := buildShareRuntimes(ctx, cfg, store, deviceIdentity, options)
	if err != nil {
		return nil, err
	}

	closed = true
	return &Daemon{
		Config:          cfg,
		Store:           store,
		Identity:        deviceIdentity,
		runtimes:        runtimes,
		recentScanLimit: options.RecentScanLimit,
		nowFn:           options.Now,
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
	if err := daemon.startRuntimes(runCtx); err != nil {
		_ = daemon.Close()
		return err
	}

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
		daemon.runtimeWG.Wait()
		if daemon.Store != nil {
			daemon.closeErr = daemon.Store.Close()
		}
	})
	return daemon.closeErr
}

func (daemon *Daemon) startRuntimes(ctx context.Context) error {
	for shareID, runtime := range daemon.runtimes {
		outcomes, err := runtime.runtime.Run(ctx)
		if err != nil {
			return fmt.Errorf("start share %s runtime: %w", shareID, err)
		}
		daemon.runtimeWG.Add(1)
		go daemon.retainOutcomes(shareID, outcomes)
	}
	return nil
}

func (daemon *Daemon) retainOutcomes(shareID core.ShareID, outcomes <-chan syncengine.ScanOutcome) {
	defer daemon.runtimeWG.Done()
	for outcome := range outcomes {
		finishedAt := daemon.currentTime()
		diagnostic := syncengine.NewScanDiagnostic(shareID, outcome, finishedAt)
		daemon.diagnosticsMu.Lock()
		daemon.recentScans = append(daemon.recentScans, diagnostic)
		if len(daemon.recentScans) > daemon.recentScanLimit {
			start := len(daemon.recentScans) - daemon.recentScanLimit
			daemon.recentScans = append([]syncengine.ScanDiagnostic(nil), daemon.recentScans[start:]...)
		}
		daemon.diagnosticsMu.Unlock()
	}
}

func (daemon *Daemon) currentTime() time.Time {
	if daemon.nowFn == nil {
		return time.Now().UTC()
	}
	return daemon.nowFn().UTC()
}

func (daemon *Daemon) Diagnostics() syncengine.DiagnosticReport {
	daemon.diagnosticsMu.RLock()
	recent := append([]syncengine.ScanDiagnostic(nil), daemon.recentScans...)
	daemon.diagnosticsMu.RUnlock()
	return syncengine.DiagnosticReport{GeneratedAt: daemon.currentTime(), RecentScans: recent}
}

func (daemon *Daemon) RequestScan(ctx context.Context, shareID core.ShareID) error {
	if daemon == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("scan request context is required")
	}
	daemon.mu.Lock()
	if daemon.closed {
		daemon.mu.Unlock()
		return ErrClosed
	}
	runtime, ok := daemon.runtimes[shareID]
	daemon.mu.Unlock()
	if !ok {
		return fmt.Errorf("share %s does not have an automatic scan runtime", shareID)
	}
	select {
	case runtime.manual <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func buildShareRuntimes(
	ctx context.Context,
	cfg config.Config,
	store storage.Store,
	deviceIdentity identity.DeviceIdentity,
	options Options,
) (map[core.ShareID]shareRuntime, error) {
	runtimes := make(map[core.ShareID]shareRuntime)
	for _, share := range cfg.Shares {
		if !shareNeedsAutomaticScan(share.Mode) {
			continue
		}
		sequenceStart, err := maxRevisionSequence(ctx, store.Revisions(), deviceIdentity.DeviceID)
		if err != nil {
			return nil, fmt.Errorf("load share %q revision sequence: %w", share.ID, err)
		}
		manual := make(chan struct{}, 1)
		sequence := sequenceStart
		var sequenceMu sync.Mutex
		commitService := syncengine.ScanCommitService{
			Planner:   syncengine.ScanPlanner{Index: store.FileIndex()},
			Committer: store.FileIndex(),
			Now:       options.Now,
		}
		scan := func(scanCtx context.Context) (syncengine.ScanCommitResult, error) {
			sequenceMu.Lock()
			start := sequence
			sequenceMu.Unlock()
			result, err := commitService.Run(scanCtx, syncengine.ScanCommitOptions{
				Plan: syncengine.PlanScanOptions{
					ScanOptions: syncengine.ScanOptions{
						ShareID:        core.ShareID(share.ID),
						RootPath:       share.RootPath,
						IgnorePatterns: share.IgnorePatterns,
					},
					DeletionGuard: syncengine.DeletionGuard{
						MaxCount:   share.DeletionLimitCount,
						MaxPercent: share.DeletionLimitPercent,
					},
				},
				OriginDeviceID:     deviceIdentity.DeviceID,
				SequenceStart:      start,
				TombstoneRetention: options.TombstoneRetention,
			})
			if err == nil && result.Committed {
				sequenceMu.Lock()
				sequence += int64(len(result.Revisions))
				sequenceMu.Unlock()
			}
			return result, err
		}
		runtimes[core.ShareID(share.ID)] = shareRuntime{
			manual: manual,
			runtime: syncengine.ShareRuntime{
				Watcher: syncengine.ShareWatcherSupervisor{
					RootPath: share.RootPath,
					Create:   options.WatcherFactory,
				},
				Schedule: syncengine.ScanSchedule{
					RootPath: share.RootPath,
					Interval: time.Duration(share.ScanIntervalSeconds) * time.Second,
					Manual:   manual,
					Scan:     scan,
				},
			},
		}
	}
	return runtimes, nil
}

func shareNeedsAutomaticScan(mode string) bool {
	return mode == "one_way_source" || mode == "upload_only"
}

type revisionSequenceReader interface {
	MaxSequence(context.Context, core.DeviceID) (int64, error)
}

func maxRevisionSequence(ctx context.Context, revisions storage.RevisionStore, deviceID core.DeviceID) (int64, error) {
	reader, ok := revisions.(revisionSequenceReader)
	if !ok {
		return 0, nil
	}
	return reader.MaxSequence(ctx, deviceID)
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
