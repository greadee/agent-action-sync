package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"syncgate/internal/api"
	"syncgate/internal/core"
	"syncgate/internal/identity"
	"syncgate/internal/insights"
	"syncgate/internal/pairing"
	"syncgate/internal/project"
	"syncgate/internal/projectmigration"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
)

type localAdminServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type localAPIState struct {
	server    localAdminServer
	listener  net.Listener
	serveDone chan error
	lifecycle api.LifecycleState
	serving   bool
}

func (daemon *Daemon) ConfigureLocalAPI(options Options) error {
	if daemon == nil {
		return errors.New("daemon is required")
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
	if daemon.localAPI != nil {
		daemon.mu.Unlock()
		return errors.New("local administration API is already configured")
	}
	daemon.mu.Unlock()

	queries, ok := daemon.Store.(storage.AdministrationQueryStore)
	if !ok {
		return errors.New("local storage does not support administration queries")
	}
	address := net.JoinHostPort(daemon.Config.LocalAPI.Host, strconv.Itoa(daemon.Config.LocalAPI.Port))
	listen := options.ListenLocalAPI
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind local administration API %s: %w", address, err)
	}
	keepListener := false
	defer func() {
		if !keepListener {
			_ = listener.Close()
		}
	}()

	credentialStore := options.AdminCredentialStore
	if credentialStore == nil {
		credentialStore, err = api.NewAdminCredentialStore(api.AdminCredentialStoreOptions{
			DataDir: daemon.Config.DataDir, RuntimeMode: daemon.Config.RuntimeMode,
			AllowInsecureDevelopmentFile: daemon.Config.Identity.AllowInsecureDevelopmentFile,
		})
		if err != nil {
			return fmt.Errorf("configure local administration credential: %w", err)
		}
	}
	credential, err := api.LoadOrCreateAdminCredential(credentialStore, options.AdminCredentialRandom)
	if err != nil {
		return fmt.Errorf("initialize local administration credential: %w", err)
	}
	authenticator, err := api.NewAdminAuthenticator(credential)
	for index := range credential {
		credential[index] = 0
	}
	if err != nil {
		return fmt.Errorf("configure local administration authentication: %w", err)
	}

	pairingCoordinator := &api.PairingCoordinator{
		Service: pairing.Service{
			Pairings: daemon.Store.Pairings(), Audit: daemon.Store.Audit(), Now: daemon.currentTime,
		},
		CurrentIdentity: func() (identity.DeviceIdentity, string, error) {
			return daemon.Identity, daemon.Config.DeviceName, nil
		},
	}
	migration := &projectmigration.Service{
		Store: daemon.Store, Now: daemon.currentTime,
		Bootstrapper: project.ProjectBootstrapper{Now: daemon.currentTime},
		Projector:    &projector.Projector{Store: daemon.Store, Now: daemon.currentTime},
		Insights:     &insights.Calculator{Store: daemon.Store, Now: daemon.currentTime},
		RequestScan:  daemon.RequestScan,
	}
	orchestration := options.OrchestrationAdministration
	if orchestration == nil {
		orchestration = daemon.orchestrationAdmin
	}
	if orchestration == nil {
		orchestration = disabledOrchestrationFacade{}
	}
	service, err := api.NewAdministrationService(api.AdministrationServiceOptions{
		Queries:       queries,
		Ready:         daemon.localAPIReady,
		Runtime:       daemon.localAPIRuntimeSnapshot,
		Diagnostics:   daemon.Diagnostics,
		Scan:          daemon.RequestScan,
		Control:       api.ControlWithJobStore(daemon.Store.OneWayJobs(), daemon.currentTime),
		Pairing:       pairingCoordinator,
		ProjectStore:  daemon.Store,
		Setup:         disabledSetupFacade{},
		Orchestration: orchestration,
		ProjectMigrationPreflight: func(ctx context.Context, input api.ProjectMigrationInput) (api.ProjectMigrationPreflight, error) {
			request, err := daemon.projectMigrationRequest(input)
			if err != nil {
				return api.ProjectMigrationPreflight{}, err
			}
			result, err := migration.Preflight(ctx, request)
			if err != nil {
				return api.ProjectMigrationPreflight{}, mapProjectMigrationError(err)
			}
			return projectMigrationPreflightDTO(result), nil
		},
		ProjectMigrationApply: func(ctx context.Context, input api.ProjectMigrationApplyInput) (api.ProjectMigrationApplyResult, error) {
			request, err := daemon.projectMigrationRequest(input.ProjectMigrationInput)
			if err != nil {
				return api.ProjectMigrationApplyResult{}, err
			}
			result, err := migration.Apply(ctx, request, input.Confirmation)
			if err != nil {
				return api.ProjectMigrationApplyResult{}, mapProjectMigrationError(err)
			}
			return api.ProjectMigrationApplyResult{
				Status: result.Status, ProjectID: result.ProjectID, ShareID: string(result.ShareID), CreatedRecords: result.CreatedRecords,
				ProjectedEvents: result.ProjectedEvents, ProjectedArtifacts: result.ProjectedArtifacts, InsightCount: result.InsightCount,
				EventWatermark: result.EventWatermark, AuditEventID: result.AuditEventID, ScanRequested: result.ScanRequested,
			}, nil
		},
		ProjectRebuild: func(ctx context.Context, projectID string) (api.ProjectRebuildResult, error) {
			registration, err := daemon.Store.ProjectRegistrations().GetProject(ctx, projectID)
			if err != nil {
				return api.ProjectRebuildResult{}, err
			}
			projection := &projector.Projector{Store: daemon.Store, Now: daemon.currentTime}
			report, err := projection.Rebuild(ctx, registration.RootPath)
			if err != nil {
				return api.ProjectRebuildResult{}, err
			}
			calculator := &insights.Calculator{Store: daemon.Store, Now: daemon.currentTime}
			snapshots, err := calculator.Rebuild(ctx, projectID)
			if err != nil {
				return api.ProjectRebuildResult{}, err
			}
			watermark := ""
			if len(snapshots) > 0 {
				watermark = snapshots[0].SourceEventWatermark
			}
			return api.ProjectRebuildResult{EventWatermark: watermark, InsightCount: len(snapshots), EventCount: report.ProjectedEvents, ArtifactCount: report.ProjectedArtifacts}, nil
		},
	})
	if err != nil {
		return fmt.Errorf("configure local administration service: %w", err)
	}
	server, err := api.NewServer(api.ServerOptions{
		Address: address, Service: service, Authenticator: authenticator,
		V1Handler: api.NewAdminV1Handler(service),
	})
	if err != nil {
		return fmt.Errorf("configure local administration server: %w", err)
	}

	daemon.mu.Lock()
	if daemon.closed || daemon.cancel != nil || daemon.localAPI != nil {
		daemon.mu.Unlock()
		return errors.New("daemon lifecycle changed while configuring local administration API")
	}
	daemon.localAPI = &localAPIState{
		server: server, listener: listener, serveDone: make(chan error, 1), lifecycle: api.LifecycleStarting,
	}
	daemon.mu.Unlock()
	keepListener = true
	return nil
}

// disabledSetupFacade makes the setup capability boundary observable without
// granting the local API any runtime, worktree, or graph-publication power.
// Later slices may replace individual methods with authority-owned services.
type disabledSetupFacade struct{}

// disabledOrchestrationFacade keeps Slice 8 routes explicit until the
// supervised daemon composition owns their durable command and read models.
type disabledOrchestrationFacade struct{}

func unavailableOrchestration() error {
	return &api.APIError{Status: 503, Code: "unavailable", Message: "orchestration administration is unavailable"}
}
func (disabledOrchestrationFacade) ApproveTaskGraph(context.Context, api.TaskGraphApprovalInput) (api.TaskGraphApprovalResult, error) {
	return api.TaskGraphApprovalResult{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) PreviewDispatch(context.Context, api.DispatchPreviewInput) (api.DispatchPreviewResult, error) {
	return api.DispatchPreviewResult{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) StartScheduler(context.Context, api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return api.SchedulerStatus{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) DisableScheduler(context.Context, api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return api.SchedulerStatus{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) ListNodes(context.Context, storage.PageRequest) (api.NodePage, error) {
	return api.NodePage{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) ListAssignments(context.Context, string, storage.PageRequest) (api.AssignmentPage, error) {
	return api.AssignmentPage{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) GetAssignment(context.Context, string, string) (api.AssignmentDetail, error) {
	return api.AssignmentDetail{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) ControlAssignment(context.Context, api.AssignmentControlInput) (api.AssignmentDetail, error) {
	return api.AssignmentDetail{}, unavailableOrchestration()
}
func (disabledOrchestrationFacade) DecideIntegration(context.Context, api.IntegrationDecisionInput) (api.AssignmentDetail, error) {
	return api.AssignmentDetail{}, unavailableOrchestration()
}

func (disabledSetupFacade) CreateTaskGraph(context.Context, api.TaskGraphCreateInput) (api.TaskGraphCreateResult, error) {
	return api.TaskGraphCreateResult{}, &api.APIError{Status: 503, Code: "unavailable", Message: "task graph publication is unavailable"}
}
func (disabledSetupFacade) ValidateTaskGraph(context.Context, api.TaskGraphValidationInput) (api.TaskGraphValidationResult, error) {
	return api.TaskGraphValidationResult{}, &api.APIError{Status: 503, Code: "unavailable", Message: "task graph validation is unavailable"}
}
func (disabledSetupFacade) ContextPreflight(context.Context, api.ContextPreflightInput) (api.ContextPreflightResult, error) {
	return api.ContextPreflightResult{}, &api.APIError{Status: 503, Code: "unavailable", Message: "context preflight is unavailable"}
}
func (disabledSetupFacade) RuntimePreflight(context.Context, api.RuntimePreflightInput) (api.RuntimePreflightResult, error) {
	return api.RuntimePreflightResult{}, &api.APIError{Status: 503, Code: "unavailable", Message: "runtime preflight is unavailable"}
}
func (disabledSetupFacade) PreviewExecutionContract(context.Context, api.ExecutionContractPreviewInput) (api.ExecutionContractPreviewResult, error) {
	return api.ExecutionContractPreviewResult{}, &api.APIError{Status: 503, Code: "unavailable", Message: "execution contract preview is unavailable"}
}
func (disabledSetupFacade) CapabilityInventory(context.Context) (api.CapabilityInventory, error) {
	return api.CapabilityInventory{Runtimes: []api.CapabilityReference{}, Nodes: []api.CapabilityReference{}, WorkspaceAllocationEnabled: false, RuntimeExecutionEnabled: false}, nil
}

func (daemon *Daemon) projectMigrationRequest(input api.ProjectMigrationInput) (projectmigration.Request, error) {
	for _, share := range daemon.Config.Shares {
		if share.ID != input.ShareID {
			continue
		}
		return projectmigration.Request{
			ShareID: core.ShareID(share.ID), RootPath: share.RootPath, ShareMode: storage.ShareMode(share.Mode),
			ConfiguredIgnorePatterns: append([]string(nil), share.IgnorePatterns...), ProjectID: input.ProjectID,
			Name: input.Name, AuthorityDeviceID: daemon.Identity.DeviceID,
		}, nil
	}
	return projectmigration.Request{}, storage.ErrNotFound
}

func projectMigrationPreflightDTO(result projectmigration.Preflight) api.ProjectMigrationPreflight {
	issues := make([]api.ProjectMigrationIssue, 0, len(result.Issues))
	for _, issue := range result.Issues {
		issues = append(issues, api.ProjectMigrationIssue{RelativePath: issue.RelativePath, Reason: issue.Reason})
	}
	return api.ProjectMigrationPreflight{
		Status: string(result.Status), ShareID: string(result.ShareID), ProjectID: result.ProjectID, Name: result.Name,
		RootIdentity: result.RootIdentity, Issues: issues, ExcludedPaths: append([]string(nil), result.ExcludedPaths...),
		RequiredIgnorePatterns:          append([]string(nil), result.RequiredIgnorePatterns...),
		MissingConfiguredIgnorePatterns: append([]string(nil), result.MissingConfiguredIgnorePatterns...),
		ConfigurationChangeRequired:     result.ConfigurationChangeRequired,
		ExpectedPortableRecords:         append([]string(nil), result.ExpectedPortableRecords...),
		ExistingProjectID:               result.ExistingProjectID, Confirmation: result.Confirmation,
	}
}

func mapProjectMigrationError(err error) error {
	switch {
	case errors.Is(err, projectmigration.ErrBlocked), errors.Is(err, projectmigration.ErrPreflightChanged),
		errors.Is(err, projectmigration.ErrConflict), errors.Is(err, project.ErrRecordConflict), errors.Is(err, storage.ErrConflict):
		return &api.APIError{Status: 409, Code: "project_migration_conflict", Message: "project migration conflicts with current state", Internal: err}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return err
	}
}

func (daemon *Daemon) startLocalAPI() <-chan error {
	daemon.mu.Lock()
	state := daemon.localAPI
	if state == nil {
		daemon.mu.Unlock()
		return nil
	}
	state.lifecycle = api.LifecycleRunning
	state.serving = true
	daemon.mu.Unlock()

	go func() {
		err := state.server.Serve(state.listener)
		daemon.mu.Lock()
		draining := state.lifecycle == api.LifecycleDraining || daemon.closed
		daemon.mu.Unlock()
		if err == nil && !draining {
			err = errors.New("local administration server stopped unexpectedly")
		}
		state.serveDone <- err
	}()
	return state.serveDone
}

func (daemon *Daemon) markLocalAPIDrainingLocked() {
	if daemon.localAPI != nil {
		daemon.localAPI.lifecycle = api.LifecycleDraining
	}
}

func (daemon *Daemon) shutdownLocalAPI() error {
	daemon.mu.Lock()
	state := daemon.localAPI
	serving := state != nil && state.serving
	daemon.mu.Unlock()
	if state == nil {
		return nil
	}
	if !serving {
		return errors.Join(state.listener.Close(), state.server.Close())
	}
	if err := state.server.Shutdown(context.Background()); err != nil {
		return errors.Join(err, state.server.Close())
	}
	return nil
}

func (daemon *Daemon) localAPIReady() bool {
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	return !daemon.closed && daemon.localAPI != nil && daemon.localAPI.lifecycle == api.LifecycleRunning
}

func (daemon *Daemon) localAPIRuntimeSnapshot() api.RuntimeSnapshot {
	daemon.mu.Lock()
	lifecycle := api.LifecycleStarting
	if daemon.localAPI != nil {
		lifecycle = daemon.localAPI.lifecycle
	}
	daemon.mu.Unlock()
	return api.RuntimeSnapshot{
		DeviceID: string(daemon.Identity.DeviceID), Fingerprint: daemon.Identity.Fingerprint,
		StartedAt: daemon.startedAt, Lifecycle: lifecycle, ActiveShareCount: len(daemon.runtimes),
		PeerExecutorEnabled: daemon.jobExecutor != nil,
	}
}

func (daemon *Daemon) LocalAPIAddress() string {
	if daemon == nil {
		return ""
	}
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	if daemon.localAPI == nil || daemon.localAPI.listener == nil {
		return ""
	}
	return daemon.localAPI.listener.Addr().String()
}
