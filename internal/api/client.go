package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"syncgate/internal/core"
)

const maxClientResponseBytes int64 = 4 << 20

type Client struct {
	baseURL    string
	credential []byte
	httpClient *http.Client
}

type ClientOptions struct {
	Address    string
	Credential []byte
	HTTPClient *http.Client
}

type ClientError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (clientError *ClientError) Error() string {
	if clientError == nil {
		return "local administration API request failed"
	}
	if clientError.Code == "" {
		return fmt.Sprintf("local administration API returned HTTP %d", clientError.StatusCode)
	}
	return fmt.Sprintf("local administration API %s: %s", clientError.Code, clientError.Message)
}

func NewClient(options ClientOptions) (*Client, error) {
	if err := validateLoopbackAddress(options.Address); err != nil {
		return nil, err
	}
	if err := validateAdminCredential(options.Credential); err != nil {
		return nil, fmt.Errorf("validate local administration credential: %w", err)
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL: "http://" + options.Address, credential: bytes.Clone(options.Credential), httpClient: httpClient,
	}, nil
}

func (client *Client) Close() {
	if client == nil {
		return
	}
	for index := range client.credential {
		client.credential[index] = 0
	}
	client.credential = nil
}

func (client *Client) Status(ctx context.Context) (AdminStatus, error) {
	var result AdminStatus
	err := client.do(ctx, http.MethodGet, "/api/v1/status", nil, &result)
	return result, err
}

func (client *Client) Diagnostics(ctx context.Context, limit int) (AdminDiagnostics, error) {
	path := "/api/v1/diagnostics"
	if limit > 0 {
		path += "?limit=" + url.QueryEscape(fmt.Sprint(limit))
	}
	var result AdminDiagnostics
	err := client.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (client *Client) ListShares(ctx context.Context, limit int) (ShareInventoryPage, error) {
	var result ShareInventoryPage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/shares", limit), nil, &result)
	return result, err
}

func (client *Client) ListDevices(ctx context.Context, limit int) (DeviceInventoryPage, error) {
	var result DeviceInventoryPage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/devices", limit), nil, &result)
	return result, err
}

func (client *Client) GetDevice(ctx context.Context, deviceID core.DeviceID) (DeviceDetail, error) {
	if strings.TrimSpace(string(deviceID)) == "" || strings.ContainsAny(string(deviceID), "/\\") {
		return DeviceDetail{}, errors.New("device ID is invalid")
	}
	var result DeviceDetail
	err := client.do(ctx, http.MethodGet, "/api/v1/devices/"+url.PathEscape(string(deviceID)), nil, &result)
	return result, err
}

func (client *Client) ListJobs(ctx context.Context, limit int) (JobInventoryPage, error) {
	var result JobInventoryPage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/jobs", limit), nil, &result)
	return result, err
}

func (client *Client) GetJob(ctx context.Context, jobID string) (JobInventory, error) {
	if strings.TrimSpace(jobID) == "" || strings.ContainsAny(jobID, "/\\") {
		return JobInventory{}, errors.New("job ID is invalid")
	}
	var result JobInventory
	err := client.do(ctx, http.MethodGet, "/api/v1/jobs/"+url.PathEscape(jobID), nil, &result)
	return result, err
}

func (client *Client) ListAuditEvents(ctx context.Context, limit int) (AuditInventoryPage, error) {
	var result AuditInventoryPage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/audit-events", limit), nil, &result)
	return result, err
}

func (client *Client) PreflightProjectMigration(ctx context.Context, input ProjectMigrationInput) (ProjectMigrationPreflight, error) {
	input = normalizeMigrationInput(input)
	if err := validateMigrationInput(input); err != nil {
		return ProjectMigrationPreflight{}, errors.New("project migration input is invalid")
	}
	var result ProjectMigrationPreflight
	err := client.do(ctx, http.MethodPost, "/api/v1/project-migrations/preflight", input, &result)
	return result, err
}

func (client *Client) ApplyProjectMigration(ctx context.Context, input ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error) {
	input.ProjectMigrationInput = normalizeMigrationInput(input.ProjectMigrationInput)
	input.Confirmation = strings.TrimSpace(input.Confirmation)
	if err := validateMigrationInput(input.ProjectMigrationInput); err != nil || !validMigrationConfirmation(input.Confirmation) {
		return ProjectMigrationApplyResult{}, errors.New("project migration apply input is invalid")
	}
	var result ProjectMigrationApplyResult
	err := client.do(ctx, http.MethodPost, "/api/v1/project-migrations/apply", input, &result)
	return result, err
}

func (client *Client) CreateTaskGraph(ctx context.Context, input TaskGraphCreateInput) (TaskGraphCreateResult, error) {
	if err := input.Validate(); err != nil {
		return TaskGraphCreateResult{}, errors.New("task graph input is invalid")
	}
	var result TaskGraphCreateResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "tasks"), input, &result)
	return result, err
}
func (client *Client) ValidateTaskGraph(ctx context.Context, input TaskGraphValidationInput) (TaskGraphValidationResult, error) {
	if err := input.Validate(); err != nil {
		return TaskGraphValidationResult{}, errors.New("task graph validation input is invalid")
	}
	var result TaskGraphValidationResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "tasks/validate"), input, &result)
	return result, err
}
func (client *Client) ListSetupTasks(ctx context.Context, projectID string, limit int) (SetupTaskPage, error) {
	if !validSetupID(projectID) {
		return SetupTaskPage{}, errors.New("project ID is invalid")
	}
	var result SetupTaskPage
	err := client.do(ctx, http.MethodGet, collectionPath(setupProjectPath(projectID, "tasks"), limit), nil, &result)
	return result, err
}
func (client *Client) GetTaskReadiness(ctx context.Context, projectID, taskID string, taskRevision, graphRevision int64, limit int) (SetupReadiness, error) {
	if !validSetupID(projectID) || !namespacedSetup(taskID, "task:") || taskRevision < 1 || graphRevision < 1 {
		return SetupReadiness{}, errors.New("task readiness input is invalid")
	}
	path := setupProjectPath(projectID, "tasks/"+url.PathEscape(taskID)+"/readiness") + "?task_revision=" + url.QueryEscape(fmt.Sprint(taskRevision)) + "&graph_revision=" + url.QueryEscape(fmt.Sprint(graphRevision))
	if limit > 0 {
		path += "&limit=" + url.QueryEscape(fmt.Sprint(limit))
	}
	var result SetupReadiness
	err := client.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}
func (client *Client) ListSetupTrades(ctx context.Context, limit int) (SetupTradePage, error) {
	var result SetupTradePage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/orchestration/trades", limit), nil, &result)
	return result, err
}
func (client *Client) ListSetupWorkers(ctx context.Context, limit int) (SetupWorkerPage, error) {
	var result SetupWorkerPage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/orchestration/workers", limit), nil, &result)
	return result, err
}
func (client *Client) CapabilityInventory(ctx context.Context) (CapabilityInventory, error) {
	var result CapabilityInventory
	err := client.do(ctx, http.MethodGet, "/api/v1/orchestration/capabilities", nil, &result)
	return result, err
}
func (client *Client) PreflightContext(ctx context.Context, input ContextPreflightInput) (ContextPreflightResult, error) {
	if err := input.Validate(); err != nil {
		return ContextPreflightResult{}, errors.New("context preflight input is invalid")
	}
	var result ContextPreflightResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "context/preflight"), input, &result)
	return result, err
}
func (client *Client) PreflightRuntime(ctx context.Context, input RuntimePreflightInput) (RuntimePreflightResult, error) {
	if err := input.Validate(); err != nil {
		return RuntimePreflightResult{}, errors.New("runtime preflight input is invalid")
	}
	var result RuntimePreflightResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "runtime/preflight"), input, &result)
	return result, err
}
func (client *Client) PreviewExecutionContract(ctx context.Context, input ExecutionContractPreviewInput) (ExecutionContractPreviewResult, error) {
	if err := input.Validate(); err != nil {
		return ExecutionContractPreviewResult{}, errors.New("execution contract preview input is invalid")
	}
	var result ExecutionContractPreviewResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "execution-contracts/preview"), input, &result)
	return result, err
}
func (client *Client) ListSetupTelemetry(ctx context.Context, projectID, executionID string, limit int) (TelemetryPage, error) {
	if !validSetupID(projectID) || (executionID != "" && !validSetupID(executionID)) {
		return TelemetryPage{}, errors.New("telemetry query is invalid")
	}
	path := collectionPath(setupProjectPath(projectID, "telemetry"), limit)
	if executionID != "" {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		path += separator + "execution_id=" + url.QueryEscape(executionID)
	}
	var result TelemetryPage
	err := client.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (client *Client) ApproveTaskGraph(ctx context.Context, input TaskGraphApprovalInput) (TaskGraphApprovalResult, error) {
	if err := input.Validate(); err != nil {
		return TaskGraphApprovalResult{}, errors.New("task graph approval input is invalid")
	}
	var result TaskGraphApprovalResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "tasks/"+url.PathEscape(input.TaskID)+"/approve"), input, &result)
	return result, err
}
func (client *Client) PreviewDispatch(ctx context.Context, input DispatchPreviewInput) (DispatchPreviewResult, error) {
	if err := input.Validate(); err != nil {
		return DispatchPreviewResult{}, errors.New("dispatch preview input is invalid")
	}
	var result DispatchPreviewResult
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "dispatch/preview"), input, &result)
	return result, err
}
func (client *Client) StartOrchestrationScheduler(ctx context.Context, input SchedulerControlInput) (SchedulerStatus, error) {
	return client.controlOrchestrationScheduler(ctx, "start", input)
}
func (client *Client) DisableOrchestrationScheduler(ctx context.Context, input SchedulerControlInput) (SchedulerStatus, error) {
	return client.controlOrchestrationScheduler(ctx, "disable", input)
}
func (client *Client) controlOrchestrationScheduler(ctx context.Context, action string, input SchedulerControlInput) (SchedulerStatus, error) {
	if err := input.Validate(); err != nil {
		return SchedulerStatus{}, errors.New("scheduler control input is invalid")
	}
	var result SchedulerStatus
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "scheduler/"+action), input, &result)
	return result, err
}
func (client *Client) ListOrchestrationNodes(ctx context.Context, limit int) (NodePage, error) {
	var result NodePage
	err := client.do(ctx, http.MethodGet, collectionPath("/api/v1/orchestration/nodes", limit), nil, &result)
	return result, err
}
func (client *Client) ListAssignments(ctx context.Context, projectID string, limit int) (AssignmentPage, error) {
	if !validSetupID(projectID) {
		return AssignmentPage{}, errors.New("project ID is invalid")
	}
	var result AssignmentPage
	err := client.do(ctx, http.MethodGet, collectionPath(setupProjectPath(projectID, "assignments"), limit), nil, &result)
	return result, err
}
func (client *Client) GetAssignment(ctx context.Context, projectID, assignmentID string) (AssignmentDetail, error) {
	if !validSetupID(projectID) || !namespacedSetup(assignmentID, "assignment:") {
		return AssignmentDetail{}, errors.New("assignment query is invalid")
	}
	var result AssignmentDetail
	err := client.do(ctx, http.MethodGet, setupProjectPath(projectID, "assignments/"+url.PathEscape(assignmentID)), nil, &result)
	return result, err
}
func (client *Client) ControlAssignment(ctx context.Context, input AssignmentControlInput) (AssignmentDetail, error) {
	if err := input.Validate(); err != nil {
		return AssignmentDetail{}, errors.New("assignment control input is invalid")
	}
	var result AssignmentDetail
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "assignments/"+url.PathEscape(input.AssignmentID)+"/controls"), input, &result)
	return result, err
}
func (client *Client) DecideIntegration(ctx context.Context, input IntegrationDecisionInput) (AssignmentDetail, error) {
	if err := input.Validate(); err != nil {
		return AssignmentDetail{}, errors.New("integration decision input is invalid")
	}
	var result AssignmentDetail
	err := client.do(ctx, http.MethodPost, setupProjectPath(input.ProjectID, "assignments/"+url.PathEscape(input.AssignmentID)+"/integration"), input, &result)
	return result, err
}

func setupProjectPath(projectID, suffix string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/" + suffix
}

func (client *Client) RequestScan(ctx context.Context, shareID core.ShareID) (ScanAccepted, error) {
	if strings.TrimSpace(string(shareID)) == "" || strings.ContainsAny(string(shareID), "/\\") {
		return ScanAccepted{}, errors.New("share ID is invalid")
	}
	var result ScanAccepted
	err := client.do(ctx, http.MethodPost, "/api/v1/shares/"+url.PathEscape(string(shareID))+"/scans", struct{}{}, &result)
	return result, err
}

func (client *Client) ControlJob(ctx context.Context, jobID string, action JobActionName) (JobInventory, error) {
	if strings.TrimSpace(jobID) == "" || strings.ContainsAny(jobID, "/\\") {
		return JobInventory{}, errors.New("job ID is invalid")
	}
	var result JobInventory
	err := client.do(ctx, http.MethodPost, "/api/v1/jobs/"+url.PathEscape(jobID)+"/actions", JobAction{Action: action}, &result)
	return result, err
}

func (client *Client) CreatePairingInvitation(ctx context.Context, request InvitationRequest) (InvitationDTO, error) {
	var result InvitationDTO
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/invitations", request, &result)
	return result, err
}

func (client *Client) InspectPairingInvitation(ctx context.Context, invitation string) (InvitationInspection, error) {
	var result InvitationInspection
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/inspect", EncodedInvitation{Invitation: invitation}, &result)
	return result, err
}

func (client *Client) AcceptPairingInvitation(ctx context.Context, request AcceptanceRequest) (Acceptance, error) {
	var result Acceptance
	err := client.do(ctx, http.MethodPost, "/api/v1/pairing/acceptances", request, &result)
	return result, err
}

func (client *Client) RevokePairingDevice(ctx context.Context, deviceID core.DeviceID) (Revocation, error) {
	if strings.TrimSpace(string(deviceID)) == "" || strings.ContainsAny(string(deviceID), "/\\") {
		return Revocation{}, errors.New("device ID is invalid")
	}
	var result Revocation
	err := client.do(ctx, http.MethodPost, "/api/v1/devices/"+url.PathEscape(string(deviceID))+"/revocation", struct{}{}, &result)
	return result, err
}

func (client *Client) do(ctx context.Context, method, path string, requestValue, responseValue any) error {
	if client == nil || len(client.credential) == 0 || client.httpClient == nil {
		return errors.New("local administration API client is closed or unconfigured")
	}
	if ctx == nil {
		return errors.New("local administration API context is required")
	}
	var body io.Reader
	if requestValue != nil {
		raw, err := json.Marshal(requestValue)
		if err != nil {
			return fmt.Errorf("encode local administration API request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create local administration API request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+string(client.credential))
	request.Header.Set("Accept", "application/json")
	if requestValue != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("connect to local administration API at %s: %w", client.baseURL, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxClientResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read local administration API response: %w", err)
	}
	if int64(len(raw)) > maxClientResponseBytes {
		return errors.New("local administration API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope ErrorEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return &ClientError{StatusCode: response.StatusCode}
		}
		return &ClientError{
			StatusCode: response.StatusCode, Code: envelope.Error.Code,
			Message: envelope.Error.Message, RequestID: envelope.Error.RequestID,
		}
	}
	if responseValue == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(responseValue); err != nil {
		return fmt.Errorf("decode local administration API response: %w", err)
	}
	return nil
}

func collectionPath(path string, limit int) string {
	if limit > 0 {
		return path + "?limit=" + url.QueryEscape(fmt.Sprint(limit))
	}
	return path
}
