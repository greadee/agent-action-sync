package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/config"
	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/insights"
	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	syncengine "syncgate/internal/sync"
)

const (
	DatabaseFileName         = "syncgate.db"
	IdentityFileName         = "identity.json"
	IdentityMetadataFileName = "identity-public.json"
)

var ErrAlreadyRunning = errors.New("daemon is already running")
var ErrClosed = errors.New("daemon is closed")
var ErrAutomaticPeerWorkDisabled = errors.New("automatic peer job execution is disabled until trusted transport is configured")

type Options struct {
	OpenStore               func(string) (storage.Store, error)
	IdentityStore           identity.PrivateKeyStore
	NewIdentity             func() (identity.DeviceIdentity, error)
	CheckShareRoot          func(string) error
	WatcherFactory          syncengine.WatcherFactory
	Now                     func() time.Time
	TombstoneRetention      time.Duration
	RecentScanLimit         int
	JobExecutor             syncengine.OneWayJobExecutor
	JobPollInterval         time.Duration
	JobRetryBase            time.Duration
	JobMaxBackoff           time.Duration
	AdminCredentialStore    api.AdminCredentialStore
	AdminCredentialRandom   io.Reader
	ListenLocalAPI          func(network, address string) (net.Listener, error)
	RequestProjectIngestion func(context.Context, core.ShareID, string) error
	OrchestrationScheduler  OrchestrationScheduler
	OrchestrationDrain      time.Duration
}

type OrchestrationScheduler interface {
	Start(context.Context) error
	Shutdown(context.Context) error
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

	runtimes                map[core.ShareID]shareRuntime
	runtimeWG               sync.WaitGroup
	diagnosticsMu           sync.RWMutex
	recentScans             []syncengine.ScanDiagnostic
	recentScanLimit         int
	nowFn                   func() time.Time
	checkShareRoot          func(string) error
	jobExecutor             syncengine.OneWayJobExecutor
	jobPollInterval         time.Duration
	jobRetryBase            time.Duration
	jobMaxBackoff           time.Duration
	startedAt               time.Time
	localAPI                *localAPIState
	requestProjectIngestion func(context.Context, core.ShareID, string) error
	orchestrationScheduler  OrchestrationScheduler
	orchestrationDrain      time.Duration
}

type shareRuntime struct {
	rootPath string
	manual   chan struct{}
	scan     syncengine.ScheduledScan
	runtime  syncengine.ShareRuntime
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
	if options.OrchestrationDrain <= 0 {
		options.OrchestrationDrain = 30 * time.Second
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
		privateKeyStore, err = identityStoreForConfig(cfg)
		if err != nil {
			return nil, err
		}
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
	if options.RequestProjectIngestion == nil {
		projection := &projector.Projector{Store: store, Now: options.Now}
		hook := projector.LifecycleHook{Projector: projection}
		calculator := &insights.Calculator{Store: store, Now: options.Now}
		options.RequestProjectIngestion = func(ingestCtx context.Context, shareID core.ShareID, rootPath string) error {
			report, err := hook.AfterShareUpdate(ingestCtx, shareID, rootPath)
			if err != nil || report.ProjectID == "" {
				return err
			}
			_, err = calculator.Rebuild(ingestCtx, report.ProjectID)
			return err
		}
	}

	runtimes, err := buildShareRuntimes(ctx, cfg, store, deviceIdentity, options)
	if err != nil {
		return nil, err
	}

	closed = true
	return &Daemon{
		Config:                  cfg,
		Store:                   store,
		Identity:                deviceIdentity,
		runtimes:                runtimes,
		recentScanLimit:         options.RecentScanLimit,
		nowFn:                   options.Now,
		checkShareRoot:          checkRoot,
		jobExecutor:             options.JobExecutor,
		jobPollInterval:         options.JobPollInterval,
		jobRetryBase:            options.JobRetryBase,
		jobMaxBackoff:           options.JobMaxBackoff,
		requestProjectIngestion: options.RequestProjectIngestion,
		orchestrationScheduler:  options.OrchestrationScheduler,
		orchestrationDrain:      options.OrchestrationDrain,
		startedAt:               options.Now().UTC(),
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
	if err := daemon.ConfigureLocalAPI(options); err != nil {
		_ = daemon.Close()
		return err
	}
	return daemon.Run(ctx)
}

func MigrateDevelopmentIdentity(cfg config.Config) (identity.DeviceIdentity, error) {
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return identity.DeviceIdentity{}, fmt.Errorf("validate config: %w", err)
	}
	if cfg.RuntimeMode != config.RuntimeModeProduction || cfg.Identity.Store != config.IdentityStoreWindows {
		return identity.DeviceIdentity{}, errors.New("identity migration requires production mode with Windows credential storage")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return identity.DeviceIdentity{}, fmt.Errorf("create data directory: %w", err)
	}
	production, err := identityStoreForConfig(cfg)
	if err != nil {
		return identity.DeviceIdentity{}, err
	}
	migrated, err := identity.MigrateDevelopmentIdentity(
		identity.DevFileStore{Path: filepath.Join(cfg.DataDir, IdentityFileName)},
		production,
	)
	if err != nil {
		return identity.DeviceIdentity{}, fmt.Errorf("migrate development identity: %w", err)
	}
	return migrated, nil
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
	runCtx, cancel := context.WithCancel(context.Background())
	daemon.cancel = cancel
	daemon.mu.Unlock()
	if err := daemon.startRuntimes(runCtx); err != nil {
		if runCtx.Err() != nil && errors.Is(err, context.Canceled) {
			return daemon.Close()
		}
		_ = daemon.Close()
		return err
	}

	serveErrors := daemon.startLocalAPI()
	if serveErrors == nil {
		<-ctx.Done()
		return daemon.Close()
	}
	select {
	case <-ctx.Done():
		return daemon.Close()
	case err := <-serveErrors:
		closeErr := daemon.Close()
		if err == nil {
			return closeErr
		}
		serveErr := fmt.Errorf("serve local administration API: %w", err)
		if closeErr != nil {
			return errors.Join(serveErr, closeErr)
		}
		return serveErr
	}
}

func (daemon *Daemon) Close() error {
	if daemon == nil {
		return nil
	}
	daemon.closeOnce.Do(func() {
		daemon.mu.Lock()
		daemon.closed = true
		daemon.markLocalAPIDrainingLocked()
		cancel := daemon.cancel
		daemon.cancel = nil
		daemon.mu.Unlock()

		apiErr := daemon.shutdownLocalAPI()
		var orchestrationErr error
		if daemon.orchestrationScheduler != nil {
			drainCtx, drainCancel := context.WithTimeout(context.Background(), daemon.orchestrationDrain)
			orchestrationErr = daemon.orchestrationScheduler.Shutdown(drainCtx)
			drainCancel()
		}
		if cancel != nil {
			cancel()
		}
		daemon.runtimeWG.Wait()
		if daemon.Store != nil {
			daemon.closeErr = errors.Join(apiErr, orchestrationErr, daemon.Store.Close())
		} else {
			daemon.closeErr = errors.Join(apiErr, orchestrationErr)
		}
	})
	return daemon.closeErr
}

func (daemon *Daemon) startRuntimes(ctx context.Context) error {
	if daemon.orchestrationScheduler != nil {
		if err := daemon.orchestrationScheduler.Start(ctx); err != nil {
			return fmt.Errorf("start orchestration scheduler: %w", err)
		}
	}
	for shareID, runtime := range daemon.runtimes {
		outcomes, err := runtime.runtime.Run(ctx)
		if err != nil {
			return fmt.Errorf("start share %s runtime: %w", shareID, err)
		}
		daemon.runtimeWG.Add(1)
		go daemon.retainOutcomes(shareID, outcomes)
	}
	if err := daemon.startJobQueue(ctx); err != nil {
		return err
	}
	return nil
}

func (daemon *Daemon) retainOutcomes(shareID core.ShareID, outcomes <-chan syncengine.ScanOutcome) {
	defer daemon.runtimeWG.Done()
	for outcome := range outcomes {
		daemon.recordScanOutcome(shareID, outcome)
	}
}

func (daemon *Daemon) startJobQueue(ctx context.Context) error {
	execute := daemon.jobExecutor
	if execute == nil {
		execute = func(context.Context, core.OneWayJob) error { return ErrAutomaticPeerWorkDisabled }
	}
	baseExecute := execute
	execute = func(executeCtx context.Context, job core.OneWayJob) error {
		if err := baseExecute(executeCtx, job); err != nil {
			return err
		}
		if daemon.requestProjectIngestion != nil {
			if rootPath, exists := daemon.configuredShareRoot(job.ShareID); exists {
				if err := daemon.requestProjectIngestion(executeCtx, job.ShareID, rootPath); err != nil {
					return err
				}
			}
		}
		return nil
	}
	outcomes, err := (syncengine.OneWayJobQueue{
		Jobs: daemon.Store.OneWayJobs(),
		Authorize: func(ctx context.Context, job core.OneWayJob) error {
			return syncengine.AuthorizeAuthenticatedOneWayJob(ctx, daemon.Store.Transfers(), daemon.Store.Shares(), job, job.Remote)
		},
		Execute:       execute,
		MaxConcurrent: daemon.Config.Transfer.MaxParallelTransfers,
		PollInterval:  daemon.jobPollInterval,
		RetryBase:     daemon.jobRetryBase,
		MaxBackoff:    daemon.jobMaxBackoff,
		Now:           daemon.nowFn,
	}).Run(ctx)
	if err != nil {
		return fmt.Errorf("start one-way job queue: %w", err)
	}
	daemon.runtimeWG.Add(1)
	go func() {
		defer daemon.runtimeWG.Done()
		for range outcomes {
		}
	}()
	return nil
}

func (daemon *Daemon) configuredShareRoot(shareID core.ShareID) (string, bool) {
	for _, share := range daemon.Config.Shares {
		if core.ShareID(share.ID) == shareID {
			return share.RootPath, true
		}
	}
	return "", false
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
	report := syncengine.DiagnosticReport{GeneratedAt: daemon.currentTime(), RecentScans: recent}
	if daemon.Store == nil {
		return report
	}
	jobs, err := daemon.Store.OneWayJobs().ListOneWayJobs(context.Background())
	if err == nil {
		report.Work = syncengine.PendingOrBlockedWork(jobs)
	}
	return report
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

func (daemon *Daemon) ScanOnce(ctx context.Context, shareID core.ShareID) (syncengine.ScanDiagnostic, error) {
	if daemon == nil {
		return syncengine.ScanDiagnostic{}, ErrClosed
	}
	if ctx == nil {
		return syncengine.ScanDiagnostic{}, errors.New("scan context is required")
	}
	daemon.mu.Lock()
	if daemon.closed {
		daemon.mu.Unlock()
		return syncengine.ScanDiagnostic{}, ErrClosed
	}
	if daemon.cancel != nil {
		daemon.mu.Unlock()
		return syncengine.ScanDiagnostic{}, ErrAlreadyRunning
	}
	runtime, ok := daemon.runtimes[shareID]
	checkRoot := daemon.checkShareRoot
	daemon.mu.Unlock()
	if !ok {
		return syncengine.ScanDiagnostic{}, fmt.Errorf("share %s does not have an automatic scan runtime", shareID)
	}
	if checkRoot == nil {
		checkRoot = syncengine.CheckShareRoot
	}
	outcome := syncengine.ScanOutcome{Trigger: syncengine.ScanRequest{Reason: syncengine.ScanTriggerManual}}
	if err := checkRoot(runtime.rootPath); err != nil {
		outcome.Skipped = true
		outcome.Unavailable = true
		outcome.Err = err
		return daemon.recordScanOutcome(shareID, outcome), nil
	}
	result, err := runtime.scan(ctx)
	outcome.Started = true
	outcome.Result = result
	outcome.Err = err
	diagnostic := daemon.recordScanOutcome(shareID, outcome)
	return diagnostic, err
}

func (daemon *Daemon) recordScanOutcome(shareID core.ShareID, outcome syncengine.ScanOutcome) syncengine.ScanDiagnostic {
	diagnostic := syncengine.NewScanDiagnostic(shareID, outcome, daemon.currentTime())
	daemon.diagnosticsMu.Lock()
	daemon.recentScans = append(daemon.recentScans, diagnostic)
	if len(daemon.recentScans) > daemon.recentScanLimit {
		start := len(daemon.recentScans) - daemon.recentScanLimit
		daemon.recentScans = append([]syncengine.ScanDiagnostic(nil), daemon.recentScans[start:]...)
	}
	daemon.diagnosticsMu.Unlock()
	return diagnostic
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
			ignorePatterns := share.IgnorePatterns
			if _, projectErr := store.ProjectRegistrations().GetProjectByShare(scanCtx, core.ShareID(share.ID)); projectErr == nil {
				ignorePatterns = project.NewProjectScanPolicy(share.IgnorePatterns).EffectiveIgnorePatterns
			} else if !errors.Is(projectErr, storage.ErrNotFound) {
				return syncengine.ScanCommitResult{}, fmt.Errorf("load project scan policy: %w", projectErr)
			}
			result, err := commitService.Run(scanCtx, syncengine.ScanCommitOptions{
				Plan: syncengine.PlanScanOptions{
					ScanOptions: syncengine.ScanOptions{
						ShareID:        core.ShareID(share.ID),
						RootPath:       share.RootPath,
						IgnorePatterns: ignorePatterns,
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
				if options.RequestProjectIngestion != nil {
					if err := options.RequestProjectIngestion(scanCtx, core.ShareID(share.ID), share.RootPath); err != nil {
						return result, err
					}
				}
			}
			return result, err
		}
		runtimes[core.ShareID(share.ID)] = shareRuntime{
			rootPath: share.RootPath,
			manual:   manual,
			scan:     scan,
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
	if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, identity.ErrIdentityNotFound) {
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

func identityStoreForConfig(cfg config.Config) (identity.PrivateKeyStore, error) {
	switch cfg.Identity.Store {
	case config.IdentityStoreWindows:
		store, err := identity.NewWindowsCredentialStore(identity.WindowsCredentialStoreOptions{
			TargetName:   identity.CredentialTarget(cfg.DataDir),
			MetadataPath: filepath.Join(cfg.DataDir, IdentityMetadataFileName),
			LegacyPath:   filepath.Join(cfg.DataDir, IdentityFileName),
		})
		if err != nil {
			return nil, fmt.Errorf("configure Windows credential identity store: %w", err)
		}
		return store, nil
	case config.IdentityStoreDevelopment:
		if cfg.RuntimeMode != config.RuntimeModeDevelopment || !cfg.Identity.AllowInsecureDevelopmentFile {
			return nil, errors.New("development identity store requires explicit development runtime opt-in")
		}
		return identity.DevFileStore{Path: filepath.Join(cfg.DataDir, IdentityFileName)}, nil
	default:
		return nil, fmt.Errorf("unsupported identity store %q", cfg.Identity.Store)
	}
}
