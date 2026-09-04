package desktop

import (
	"context"
	"errors"
	"net/http"

	"syncgate/internal/api"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/taskspec"
	"syncgate/internal/workhistory"
)

type taskSpecificationStore interface {
	Get(context.Context, string, string) (taskspec.Stored, error)
}

type LocalSetupOptions struct {
	Specifications taskSpecificationStore
	History        *workhistory.Service
	Projects       storage.ProjectRegistrationStore
	Tasks          storage.ProjectTaskStore
	DeviceID       string
	Runtimes       []api.CapabilityReference
	Nodes          []api.CapabilityReference
	Dispatch       *LocalDispatchAuthority
}

type LocalSetupAdministration struct {
	specifications taskSpecificationStore
	history        *workhistory.Service
	projects       storage.ProjectRegistrationStore
	tasks          storage.ProjectTaskStore
	deviceID       string
	runtimes       []api.CapabilityReference
	nodes          []api.CapabilityReference
	dispatch       *LocalDispatchAuthority
}

func NewLocalSetupAdministration(options LocalSetupOptions) (*LocalSetupAdministration, error) {
	if options.Specifications == nil || options.History == nil || options.Projects == nil || options.Tasks == nil || options.DeviceID == "" {
		return nil, errors.New("local setup administration is incomplete")
	}
	return &LocalSetupAdministration{
		specifications: options.Specifications, history: options.History, projects: options.Projects, tasks: options.Tasks,
		deviceID: options.DeviceID, runtimes: cloneCapabilities(options.Runtimes), nodes: cloneCapabilities(options.Nodes),
		dispatch: options.Dispatch,
	}, nil
}

func (admin *LocalSetupAdministration) CreateTaskGraph(ctx context.Context, input api.TaskGraphCreateInput) (api.TaskGraphCreateResult, error) {
	if admin == nil || input.Validate() != nil {
		return api.TaskGraphCreateResult{}, setupError(http.StatusBadRequest, "invalid_task_specification", "task specification reference is invalid", nil)
	}
	stored, err := admin.specifications.Get(ctx, input.SpecificationID, input.SpecificationDigest)
	if err != nil {
		return api.TaskGraphCreateResult{}, mapTaskSpecificationError(err)
	}
	specification := stored.Specification
	if specification.ProjectID != input.ProjectID || specification.TaskID != input.TaskID || specification.TaskRevision != input.TaskRevision || specification.GraphRevision != input.GraphRevision {
		return api.TaskGraphCreateResult{}, setupError(http.StatusConflict, "task_specification_conflict", "task specification identity does not match the request", nil)
	}
	registration, err := admin.projects.GetProject(ctx, input.ProjectID)
	if err != nil {
		return api.TaskGraphCreateResult{}, mapSetupStorageError(err)
	}
	metadata := workhistory.Metadata{
		RootPath: registration.RootPath, IdempotencyKey: input.IdempotencyKey, OccurredAt: specification.CreatedAt.UTC(),
		Producer: project.Producer{DeviceID: admin.deviceID}, CausationID: input.SpecificationID,
	}
	operation, err := admin.history.CreateTask(ctx, specification.CreateRequest(metadata))
	if err != nil {
		return api.TaskGraphCreateResult{}, mapSetupHistoryError(err)
	}
	projection, err := admin.tasks.GetProjectTask(ctx, input.ProjectID, input.TaskID, input.TaskRevision)
	if err != nil {
		return api.TaskGraphCreateResult{}, mapSetupStorageError(err)
	}
	alreadyPresent := len(operation.Records) > 0
	for _, record := range operation.Records {
		if record.Created {
			alreadyPresent = false
			break
		}
	}
	return api.TaskGraphCreateResult{
		ProjectID: projection.ProjectID, TaskID: projection.TaskID, TaskRevision: projection.TaskRevision, GraphRevision: projection.GraphRevision,
		TaskRecordID: projection.TaskRecordID, TaskDigest: projection.TaskRecordHash,
		GraphRecordID: projection.GraphRecordID, GraphDigest: projection.GraphRecordHash, AlreadyPresent: alreadyPresent,
	}, nil
}

func (admin *LocalSetupAdministration) ValidateTaskGraph(ctx context.Context, input api.TaskGraphValidationInput) (api.TaskGraphValidationResult, error) {
	if admin == nil || input.Validate() != nil {
		return api.TaskGraphValidationResult{}, setupError(http.StatusBadRequest, "invalid_task_specification", "task specification reference is invalid", nil)
	}
	stored, err := admin.specifications.Get(ctx, input.SpecificationID, input.SpecificationDigest)
	if err != nil {
		return api.TaskGraphValidationResult{}, mapTaskSpecificationError(err)
	}
	if stored.Specification.ProjectID != input.ProjectID {
		return api.TaskGraphValidationResult{}, setupError(http.StatusConflict, "task_specification_conflict", "task specification project does not match the request", nil)
	}
	return api.TaskGraphValidationResult{
		Valid: true, ReasonCodes: []string{"task_specification_valid"}, TaskCount: 1,
		WorkPackageCount: len(stored.Specification.WorkPackages),
	}, nil
}

func (admin *LocalSetupAdministration) ContextPreflight(ctx context.Context, input api.ContextPreflightInput) (api.ContextPreflightResult, error) {
	if admin == nil || input.Validate() != nil {
		return api.ContextPreflightResult{}, setupError(http.StatusBadRequest, "invalid_context_preflight", "context preflight input is invalid", nil)
	}
	if admin.dispatch == nil {
		return api.ContextPreflightResult{}, setupUnavailable("context preflight is unavailable until dispatch authority is configured")
	}
	return admin.dispatch.ContextPreflight(ctx, input)
}

func (admin *LocalSetupAdministration) RuntimePreflight(ctx context.Context, input api.RuntimePreflightInput) (api.RuntimePreflightResult, error) {
	if admin == nil || input.Validate() != nil {
		return api.RuntimePreflightResult{}, setupError(http.StatusBadRequest, "invalid_runtime_preflight", "runtime preflight input is invalid", nil)
	}
	if admin.dispatch == nil {
		return api.RuntimePreflightResult{}, setupUnavailable("runtime preflight is unavailable until dispatch authority is configured")
	}
	return admin.dispatch.RuntimePreflight(ctx, input)
}

func (admin *LocalSetupAdministration) PreviewExecutionContract(ctx context.Context, input api.ExecutionContractPreviewInput) (api.ExecutionContractPreviewResult, error) {
	if admin == nil || input.Validate() != nil {
		return api.ExecutionContractPreviewResult{}, setupError(http.StatusBadRequest, "invalid_contract_preview", "execution contract preview input is invalid", nil)
	}
	if admin.dispatch == nil {
		return api.ExecutionContractPreviewResult{}, setupUnavailable("execution contract preview is unavailable until dispatch authority is configured")
	}
	return admin.dispatch.PreviewContract(ctx, input)
}

func (admin *LocalSetupAdministration) CapabilityInventory(context.Context) (api.CapabilityInventory, error) {
	if admin == nil {
		return api.CapabilityInventory{}, setupUnavailable("local setup administration is unavailable")
	}
	return api.CapabilityInventory{
		Runtimes: cloneCapabilities(admin.runtimes), Nodes: cloneCapabilities(admin.nodes),
		WorkspaceAllocationEnabled: len(admin.nodes) > 0, RuntimeExecutionEnabled: len(admin.runtimes) > 0,
	}, nil
}

func setupUnavailable(message string) error {
	return setupError(http.StatusServiceUnavailable, "unavailable", message, nil)
}

func mapTaskSpecificationError(err error) error {
	switch {
	case errors.Is(err, taskspec.ErrNotFound):
		return setupError(http.StatusNotFound, "task_specification_not_found", "task specification is not available", err)
	case errors.Is(err, taskspec.ErrConflict):
		return setupError(http.StatusConflict, "task_specification_conflict", "task specification digest conflicts with stored authority", err)
	case errors.Is(err, taskspec.ErrInvalid):
		return setupError(http.StatusBadRequest, "invalid_task_specification", "task specification is invalid", err)
	default:
		return err
	}
}

func mapSetupHistoryError(err error) error {
	var recoverable *workhistory.RecoverableProjectionError
	switch {
	case errors.As(err, &recoverable):
		return setupError(http.StatusServiceUnavailable, "projection_recovery_required", "task history is durable but its local projection requires recovery", err)
	case errors.Is(err, project.ErrRecordConflict), errors.Is(err, workhistory.ErrInvalidTransition), errors.Is(err, storage.ErrConflict):
		return setupError(http.StatusConflict, "task_graph_conflict", "task graph conflicts with existing authority history", err)
	case errors.Is(err, workhistory.ErrInvalidRequest):
		return setupError(http.StatusBadRequest, "invalid_task_specification", "task specification cannot be published", err)
	default:
		return err
	}
}

func mapSetupStorageError(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return setupError(http.StatusNotFound, "project_not_found", "project authority is not registered on this node", err)
	}
	return err
}

func setupError(status int, code, message string, internal error) error {
	return &api.APIError{Status: status, Code: code, Message: message, Internal: internal}
}

func cloneCapabilities(values []api.CapabilityReference) []api.CapabilityReference {
	result := make([]api.CapabilityReference, len(values))
	for index, value := range values {
		result[index] = value
		result[index].CapabilityIDs = append([]string(nil), value.CapabilityIDs...)
	}
	return result
}

var _ api.SetupAdministration = (*LocalSetupAdministration)(nil)
