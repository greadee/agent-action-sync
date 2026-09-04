package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"syncgate/internal/storage"
)

// SetupAdministration contains the daemon-owned operations that prepare work
// without invoking a runtime or allocating a workspace. API handlers only
// receive opaque IDs, relative source paths, versions, and digests.
type SetupAdministration interface {
	CreateTaskGraph(context.Context, TaskGraphCreateInput) (TaskGraphCreateResult, error)
	ValidateTaskGraph(context.Context, TaskGraphValidationInput) (TaskGraphValidationResult, error)
	ContextPreflight(context.Context, ContextPreflightInput) (ContextPreflightResult, error)
	RuntimePreflight(context.Context, RuntimePreflightInput) (RuntimePreflightResult, error)
	PreviewExecutionContract(context.Context, ExecutionContractPreviewInput) (ExecutionContractPreviewResult, error)
	CapabilityInventory(context.Context) (CapabilityInventory, error)
}

type TaskGraphCreateInput struct {
	ProjectID           string `json:"project_id"`
	TaskID              string `json:"task_id"`
	TaskRevision        int64  `json:"task_revision"`
	GraphRevision       int64  `json:"graph_revision"`
	SpecificationID     string `json:"specification_id"`
	SpecificationDigest string `json:"specification_digest"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (v TaskGraphCreateInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.TaskID, "task:") || v.TaskRevision < 1 || v.GraphRevision < 1 || !validSetupID(v.SpecificationID) || !validSetupDigest(v.SpecificationDigest) || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	return nil
}

type TaskGraphCreateResult struct {
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
	TaskRevision   int64  `json:"task_revision"`
	GraphRevision  int64  `json:"graph_revision"`
	TaskRecordID   string `json:"task_record_id"`
	TaskDigest     string `json:"task_digest"`
	GraphRecordID  string `json:"graph_record_id"`
	GraphDigest    string `json:"graph_digest"`
	AlreadyPresent bool   `json:"already_present"`
}
type TaskGraphValidationInput struct {
	ProjectID           string `json:"project_id"`
	SpecificationID     string `json:"specification_id"`
	SpecificationDigest string `json:"specification_digest"`
}

func (v TaskGraphValidationInput) Validate() error {
	if !validSetupID(v.ProjectID) || !validSetupID(v.SpecificationID) || !validSetupDigest(v.SpecificationDigest) {
		return errBadRequest
	}
	return nil
}

type TaskGraphValidationResult struct {
	Valid            bool     `json:"valid"`
	ReasonCodes      []string `json:"reason_codes"`
	TaskCount        int      `json:"task_count"`
	WorkPackageCount int      `json:"work_package_count"`
}

type ContextPreflightInput struct {
	ProjectID       string `json:"project_id"`
	WorkPackageID   string `json:"work_package_id"`
	TradeID         string `json:"trade_id"`
	TradeVersion    int64  `json:"trade_version"`
	TradeDigest     string `json:"trade_digest"`
	SourceSetDigest string `json:"source_set_digest"`
}

func (v ContextPreflightInput) Validate() error {
	if !validSetupID(v.ProjectID) || !validSetupID(v.WorkPackageID) || !namespacedSetup(v.TradeID, "trade:") || v.TradeVersion < 1 || !validSetupDigest(v.TradeDigest) || v.SourceSetDigest != "" && !validSetupDigest(v.SourceSetDigest) {
		return errBadRequest
	}
	return nil
}

type ContextPreflightResult struct {
	CompilerVersion string   `json:"compiler_version"`
	ContextDigest   string   `json:"context_digest"`
	SourceSetDigest string   `json:"source_set_digest"`
	EstimatedTokens int64    `json:"estimated_tokens"`
	SourceCount     int      `json:"source_count"`
	OmissionCodes   []string `json:"omission_codes"`
	WarningCodes    []string `json:"warning_codes"`
}

type RuntimePreflightInput struct {
	ProjectID       string `json:"project_id"`
	ContractID      string `json:"contract_id"`
	ContractVersion int64  `json:"contract_version"`
	ContractDigest  string `json:"contract_digest"`
	RuntimeID       string `json:"runtime_id"`
	RuntimeVersion  int64  `json:"runtime_version"`
	RuntimeDigest   string `json:"runtime_digest"`
	NodeID          string `json:"node_id"`
	NodeVersion     int64  `json:"node_version"`
	NodeDigest      string `json:"node_digest"`
}

func (v RuntimePreflightInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.ContractID, "contract:") || v.ContractVersion < 1 || !validSetupDigest(v.ContractDigest) || !namespacedSetup(v.RuntimeID, "runtime:") || v.RuntimeVersion < 1 || !validSetupDigest(v.RuntimeDigest) || !namespacedSetup(v.NodeID, "node:") || v.NodeVersion < 1 || !validSetupDigest(v.NodeDigest) {
		return errBadRequest
	}
	return nil
}

type RuntimePreflightResult struct {
	Ready               bool     `json:"ready"`
	RuntimeCapabilities []string `json:"runtime_capabilities"`
	NodeEligibility     bool     `json:"node_eligibility"`
	WorkspaceReady      bool     `json:"workspace_ready"`
	ReasonCodes         []string `json:"reason_codes"`
}

type ExecutionContractPreviewInput struct {
	ProjectID              string   `json:"project_id"`
	TaskID                 string   `json:"task_id"`
	TaskRevision           int64    `json:"task_revision"`
	GraphRevision          int64    `json:"graph_revision"`
	WorkPackageID          string   `json:"work_package_id"`
	WorkerID               string   `json:"worker_id"`
	WorkerVersion          int64    `json:"worker_version"`
	WorkerDigest           string   `json:"worker_digest"`
	RequestedCapabilityIDs []string `json:"requested_capability_ids"`
	IdempotencyKey         string   `json:"idempotency_key"`
}

func (v ExecutionContractPreviewInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.TaskID, "task:") || v.TaskRevision < 1 || v.GraphRevision < 1 || !validSetupID(v.WorkPackageID) || !namespacedSetup(v.WorkerID, "worker:") || v.WorkerVersion < 1 || !validSetupDigest(v.WorkerDigest) || !validSetupID(v.IdempotencyKey) || len(v.RequestedCapabilityIDs) > storage.MaxAdminPageLimit {
		return errBadRequest
	}
	for _, id := range v.RequestedCapabilityIDs {
		if !validSetupID(id) {
			return errBadRequest
		}
	}
	return nil
}

type ExecutionContractPreviewResult struct {
	ContractID             string   `json:"contract_id"`
	ContractVersion        int64    `json:"contract_version"`
	ContractDigest         string   `json:"contract_digest"`
	EffectiveCapabilityIDs []string `json:"effective_capability_ids"`
	RequiredGateIDs        []string `json:"required_gate_ids"`
	MaxTokens              int64    `json:"max_tokens"`
	MaxCostMicros          int64    `json:"max_cost_micros"`
	MaxWallClockSeconds    int64    `json:"max_wall_clock_seconds"`
	PreviewOnly            bool     `json:"preview_only"`
}

type CapabilityInventory struct {
	Runtimes                   []CapabilityReference `json:"runtimes"`
	Nodes                      []CapabilityReference `json:"nodes"`
	WorkspaceAllocationEnabled bool                  `json:"workspace_allocation_enabled"`
	RuntimeExecutionEnabled    bool                  `json:"runtime_execution_enabled"`
}
type CapabilityReference struct {
	ID            string   `json:"id"`
	Version       int64    `json:"version"`
	Digest        string   `json:"digest"`
	Lifecycle     string   `json:"lifecycle"`
	CapabilityIDs []string `json:"capability_ids"`
}

type SetupTaskItem struct {
	TaskID          string `json:"task_id"`
	TaskRevision    int64  `json:"task_revision"`
	GraphRevision   int64  `json:"graph_revision"`
	TaskRecordID    string `json:"task_record_id"`
	TaskDigest      string `json:"task_digest"`
	GraphRecordID   string `json:"graph_record_id"`
	GraphDigest     string `json:"graph_digest"`
	State           string `json:"state"`
	ExplanationCode string `json:"explanation_code"`
	EventWatermark  string `json:"event_watermark"`
}
type SetupTaskPage struct {
	Items []SetupTaskItem `json:"items"`
	Page  InventoryPage   `json:"page"`
}
type SetupReadiness struct {
	TaskID          string               `json:"task_id"`
	TaskRevision    int64                `json:"task_revision"`
	GraphRevision   int64                `json:"graph_revision"`
	State           string               `json:"state"`
	ExplanationCode string               `json:"explanation_code"`
	EventWatermark  string               `json:"event_watermark"`
	Nodes           []SetupReadinessNode `json:"nodes"`
	Page            InventoryPage        `json:"page"`
}
type SetupReadinessNode struct {
	WorkPackageID      string   `json:"work_package_id"`
	DefinitionRecordID string   `json:"definition_record_id"`
	DefinitionDigest   string   `json:"definition_digest"`
	State              string   `json:"state"`
	ExplanationCode    string   `json:"explanation_code"`
	Dependencies       []string `json:"dependencies"`
	Barrier            bool     `json:"barrier"`
}
type SetupTradeItem struct {
	TradeID       string   `json:"trade_id"`
	Version       int64    `json:"version"`
	Digest        string   `json:"digest"`
	Lifecycle     string   `json:"lifecycle"`
	CapabilityIDs []string `json:"capability_ids"`
	CreatedAt     string   `json:"created_at"`
}
type SetupTradePage struct {
	Items []SetupTradeItem `json:"items"`
	Page  InventoryPage    `json:"page"`
}
type SetupWorkerItem struct {
	WorkerID      string `json:"worker_id"`
	Version       int64  `json:"version"`
	Digest        string `json:"digest"`
	Lifecycle     string `json:"lifecycle"`
	TradeID       string `json:"trade_id"`
	TradeVersion  int64  `json:"trade_version"`
	InstructionID string `json:"instruction_id"`
	RuntimeID     string `json:"runtime_id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	CreatedAt     string `json:"created_at"`
}
type SetupWorkerPage struct {
	Items []SetupWorkerItem `json:"items"`
	Page  InventoryPage     `json:"page"`
}
type TelemetryItem struct {
	TelemetryID     string `json:"telemetry_id"`
	TelemetryDigest string `json:"telemetry_digest"`
	ExecutionID     string `json:"execution_id"`
	ContractID      string `json:"contract_id"`
	ContractVersion int64  `json:"contract_version"`
	ContractDigest  string `json:"contract_digest"`
	FinalOutcome    string `json:"final_outcome"`
	CreatedAt       string `json:"created_at"`
}
type TelemetryPage struct {
	Items []TelemetryItem `json:"items"`
	Page  InventoryPage   `json:"page"`
}

func (service *LocalAdministrationService) ListSetupTasks(ctx context.Context, projectID string, page storage.PageRequest) (storage.Page[storage.ProjectTaskProjection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil || validateProjectAPIPage(page, projectCursorByTask) != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, errBadRequest
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, projectID); err != nil {
		return storage.Page[storage.ProjectTaskProjection]{}, err
	}
	return service.projectStore.ProjectTasks().ListProjectTasks(ctx, storage.ProjectTaskQuery{ProjectID: projectID, Page: page})
}
func (service *LocalAdministrationService) SetupReadiness(ctx context.Context, projectID, taskID string, revision, graph int64, page storage.PageRequest) (storage.ProjectTaskProjection, storage.Page[storage.ProjectTaskNodeProjection], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.ProjectTaskProjection{}, storage.Page[storage.ProjectTaskNodeProjection]{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil || !namespacedSetup(taskID, "task:") || revision < 1 || graph < 1 || validateProjectAPIPage(page, projectCursorByID) != nil {
		return storage.ProjectTaskProjection{}, storage.Page[storage.ProjectTaskNodeProjection]{}, errBadRequest
	}
	task, err := service.projectStore.ProjectTasks().GetProjectTask(ctx, projectID, taskID, revision)
	if err != nil {
		return storage.ProjectTaskProjection{}, storage.Page[storage.ProjectTaskNodeProjection]{}, err
	}
	if task.GraphRevision != graph {
		return storage.ProjectTaskProjection{}, storage.Page[storage.ProjectTaskNodeProjection]{}, errNotFound
	}
	nodes, err := service.projectStore.ProjectTaskNodes().ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{ProjectID: projectID, TaskID: taskID, TaskRevision: revision, GraphRevision: graph, Page: page})
	return task, nodes, err
}
func (service *LocalAdministrationService) ListSetupTrades(ctx context.Context, page storage.PageRequest) (storage.Page[storage.TradeDefinition], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.TradeDefinition]{}, err
	}
	if validateProjectAPIPage(page, projectCursorByID) != nil {
		return storage.Page[storage.TradeDefinition]{}, errBadRequest
	}
	return service.projectStore.Registry().ListTradeDefinitions(ctx, storage.TradeQuery{Page: page})
}
func (service *LocalAdministrationService) ListSetupWorkers(ctx context.Context, page storage.PageRequest) (storage.Page[storage.WorkerProfile], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.WorkerProfile]{}, err
	}
	if validateProjectAPIPage(page, projectCursorByID) != nil {
		return storage.Page[storage.WorkerProfile]{}, errBadRequest
	}
	return service.projectStore.Registry().ListWorkerProfiles(ctx, storage.WorkerQuery{Page: page})
}
func (service *LocalAdministrationService) ListSetupTelemetry(ctx context.Context, projectID, executionID string, page storage.PageRequest) (storage.Page[storage.ExecutionTelemetryRecord], error) {
	if err := service.requireProjects(ctx); err != nil {
		return storage.Page[storage.ExecutionTelemetryRecord]{}, err
	}
	if err := validateProjectAPIID(projectID); err != nil || validateProjectAPIFilters(executionID) != nil || validateProjectAPIPage(page, projectCursorByID) != nil {
		return storage.Page[storage.ExecutionTelemetryRecord]{}, errBadRequest
	}
	if _, err := service.projectStore.ProjectRegistrations().GetProject(ctx, projectID); err != nil {
		return storage.Page[storage.ExecutionTelemetryRecord]{}, err
	}
	records, err := service.projectStore.ExecutionTelemetry().ListExecutionTelemetry(ctx, projectID, executionID)
	if err != nil {
		return storage.Page[storage.ExecutionTelemetryRecord]{}, err
	}
	return pageTelemetry(records, page), nil
}

func pageTelemetry(values []storage.ExecutionTelemetryRecord, page storage.PageRequest) storage.Page[storage.ExecutionTelemetryRecord] {
	start := 0
	if page.Cursor.ID != "" {
		for i, value := range values {
			if value.TelemetryID == page.Cursor.ID {
				start = i + 1
				break
			}
		}
	}
	limit := page.Limit
	if limit == 0 {
		limit = storage.DefaultAdminPageLimit
	}
	end := start + limit
	if end > len(values) {
		end = len(values)
	}
	result := storage.Page[storage.ExecutionTelemetryRecord]{Items: append([]storage.ExecutionTelemetryRecord(nil), values[start:end]...)}
	if end < len(values) {
		cursor := storage.PageCursor{ID: values[end-1].TelemetryID}
		result.NextCursor = &cursor
	}
	return result
}

func validSetupID(value string) bool {
	return len(value) > 0 && len(value) <= 128 && !strings.ContainsAny(value, "/\\\r\n\x00")
}
func namespacedSetup(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && validSetupID(value)
}
func validSetupDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func NewSetupHandler(read *LocalAdministrationService, commands SetupAdministration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleSetup(w, r, read, commands) })
}
func handleSetup(w http.ResponseWriter, r *http.Request, read *LocalAdministrationService, commands SetupAdministration) {
	if read == nil {
		writeError(w, r, errUnavailable)
		return
	}
	path := r.URL.Path
	if path == "/api/v1/orchestration/trades" {
		handleTrades(w, r, read)
		return
	}
	if path == "/api/v1/orchestration/workers" {
		handleWorkers(w, r, read)
		return
	}
	if path == "/api/v1/orchestration/capabilities" {
		if r.Method != http.MethodGet || commands == nil {
			writeError(w, r, errUnavailable)
			return
		}
		result, err := commands.CapabilityInventory(r.Context())
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, r, errNotFound)
		return
	}
	projectID := parts[0]
	if commands == nil && r.Method == http.MethodPost {
		switch strings.Join(parts[1:], "/") {
		case "tasks", "tasks/validate", "context/preflight", "runtime/preflight", "execution-contracts/preview":
			writeError(w, r, errUnavailable)
			return
		}
	}
	if len(parts) == 4 && parts[1] == "tasks" && parts[3] == "readiness" && r.Method == http.MethodGet {
		handleReadiness(w, r, read, projectID, parts[2])
		return
	}
	switch strings.Join(parts[1:], "/") {
	case "tasks":
		if r.Method == http.MethodGet {
			handleTasks(w, r, read, projectID)
			return
		}
		if r.Method == http.MethodPost && commands != nil {
			var input TaskGraphCreateInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.CreateTaskGraph(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	case "tasks/validate":
		if r.Method == http.MethodPost && commands != nil {
			var input TaskGraphValidationInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.ValidateTaskGraph(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	case "context/preflight":
		if r.Method == http.MethodPost && commands != nil {
			var input ContextPreflightInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.ContextPreflight(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	case "runtime/preflight":
		if r.Method == http.MethodPost && commands != nil {
			var input RuntimePreflightInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.RuntimePreflight(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	case "execution-contracts/preview":
		if r.Method == http.MethodPost && commands != nil {
			var input ExecutionContractPreviewInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.PreviewExecutionContract(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			result.PreviewOnly = true
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	case "telemetry":
		if r.Method == http.MethodGet {
			handleTelemetry(w, r, read, projectID)
			return
		}
	}
	writeError(w, r, errNotFound)
}

func handleTasks(w http.ResponseWriter, r *http.Request, service *LocalAdministrationService, projectID string) {
	page, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	values, err := service.ListSetupTasks(r.Context(), projectID, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]SetupTaskItem, 0, len(values.Items))
	for _, value := range values.Items {
		items = append(items, SetupTaskItem{TaskID: value.TaskID, TaskRevision: value.TaskRevision, GraphRevision: value.GraphRevision, TaskRecordID: value.TaskRecordID, TaskDigest: value.TaskRecordHash, GraphRecordID: value.GraphRecordID, GraphDigest: value.GraphRecordHash, State: value.State, ExplanationCode: value.ExplanationCode, EventWatermark: value.EventWatermark})
	}
	writeJSON(w, r, http.StatusOK, SetupTaskPage{Items: items, Page: pageInfo(values, page.Limit)})
}
func handleReadiness(w http.ResponseWriter, r *http.Request, service *LocalAdministrationService, projectID, taskID string) {
	page, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("task_revision"), 10, 64)
	if err != nil {
		writeError(w, r, errBadRequest)
		return
	}
	graph, err := strconv.ParseInt(r.URL.Query().Get("graph_revision"), 10, 64)
	if err != nil {
		writeError(w, r, errBadRequest)
		return
	}
	task, nodes, err := service.SetupReadiness(r.Context(), projectID, taskID, revision, graph, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]SetupReadinessNode, 0, len(nodes.Items))
	for _, value := range nodes.Items {
		items = append(items, SetupReadinessNode{WorkPackageID: value.WorkPackageID, DefinitionRecordID: value.DefinitionRecordID, DefinitionDigest: value.DefinitionHash, State: value.Readiness, ExplanationCode: value.ExplanationCode, Dependencies: append([]string(nil), value.Dependencies...), Barrier: value.Barrier})
	}
	writeJSON(w, r, http.StatusOK, SetupReadiness{TaskID: task.TaskID, TaskRevision: task.TaskRevision, GraphRevision: task.GraphRevision, State: task.State, ExplanationCode: task.ExplanationCode, EventWatermark: task.EventWatermark, Nodes: items, Page: pageInfo(nodes, page.Limit)})
}
func handleTrades(w http.ResponseWriter, r *http.Request, service *LocalAdministrationService) {
	if r.Method != http.MethodGet {
		writeError(w, r, errMethodNotAllowed)
		return
	}
	page, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	values, err := service.ListSetupTrades(r.Context(), page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]SetupTradeItem, 0, len(values.Items))
	for _, value := range values.Items {
		items = append(items, SetupTradeItem{TradeID: value.TradeID, Version: value.Version, Digest: value.ContentHash, Lifecycle: string(value.Lifecycle), CapabilityIDs: append([]string(nil), value.CapabilityTags...), CreatedAt: value.CreatedAt.UTC().Format(timeFormat)})
	}
	writeJSON(w, r, http.StatusOK, SetupTradePage{Items: items, Page: pageInfo(values, page.Limit)})
}
func handleWorkers(w http.ResponseWriter, r *http.Request, service *LocalAdministrationService) {
	if r.Method != http.MethodGet {
		writeError(w, r, errMethodNotAllowed)
		return
	}
	page, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	values, err := service.ListSetupWorkers(r.Context(), page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]SetupWorkerItem, 0, len(values.Items))
	for _, value := range values.Items {
		items = append(items, SetupWorkerItem{WorkerID: value.WorkerID, Version: value.Version, Digest: value.ContentHash, Lifecycle: string(value.Lifecycle), TradeID: value.TradeID, TradeVersion: value.TradeVersion, InstructionID: value.InstructionID, RuntimeID: value.RuntimeID, Provider: sanitizeMessage(value.Provider), Model: sanitizeMessage(value.Model), CreatedAt: value.CreatedAt.UTC().Format(timeFormat)})
	}
	writeJSON(w, r, http.StatusOK, SetupWorkerPage{Items: items, Page: pageInfo(values, page.Limit)})
}
func handleTelemetry(w http.ResponseWriter, r *http.Request, service *LocalAdministrationService, projectID string) {
	page, err := parsePageRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	values, err := service.ListSetupTelemetry(r.Context(), projectID, r.URL.Query().Get("execution_id"), page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]TelemetryItem, 0, len(values.Items))
	for _, value := range values.Items {
		items = append(items, TelemetryItem{TelemetryID: value.TelemetryID, TelemetryDigest: value.TelemetryDigest, ExecutionID: value.ExecutionID, ContractID: value.ContractID, ContractVersion: value.ContractVersion, ContractDigest: value.ContractDigest, FinalOutcome: value.FinalOutcome, CreatedAt: value.CreatedAt.UTC().Format(timeFormat)})
	}
	writeJSON(w, r, http.StatusOK, TelemetryPage{Items: items, Page: pageInfo(values, page.Limit)})
}
