package api

import (
	"context"
	"net/http"
	"strings"

	"syncgate/internal/storage"
)

// OrchestrationAdministration is the daemon-owned boundary for supervised
// orchestration. Implementations expose only opaque IDs and sanitized evidence;
// handlers never receive runtime sessions, worktree paths, prompts, or bytes.
type OrchestrationAdministration interface {
	ListLocalProjects(context.Context, storage.PageRequest) (LocalProjectPage, error)
	SelectLocalProject(context.Context, LocalProjectSelectionInput) (LocalProjectItem, error)
	SetLocalProjectPolicy(context.Context, LocalProjectPolicyInput) (LocalProjectItem, error)
	ApproveTaskGraph(context.Context, TaskGraphApprovalInput) (TaskGraphApprovalResult, error)
	PreviewDispatch(context.Context, DispatchPreviewInput) (DispatchPreviewResult, error)
	StartScheduler(context.Context, SchedulerControlInput) (SchedulerStatus, error)
	DisableScheduler(context.Context, SchedulerControlInput) (SchedulerStatus, error)
	ListNodes(context.Context, storage.PageRequest) (NodePage, error)
	ListAssignments(context.Context, string, storage.PageRequest) (AssignmentPage, error)
	GetAssignment(context.Context, string, string) (AssignmentDetail, error)
	ControlAssignment(context.Context, AssignmentControlInput) (AssignmentDetail, error)
	DecideIntegration(context.Context, IntegrationDecisionInput) (AssignmentDetail, error)
}

type StatusCount struct {
	State string `json:"state"`
	Count int64  `json:"count"`
}

type LocalProjectItem struct {
	ProjectID           string        `json:"project_id"`
	DisplayName         string        `json:"display_name"`
	RegisteredAt        string        `json:"registered_at"`
	Selected            bool          `json:"selected"`
	ExecutionAuthorized bool          `json:"execution_authorized"`
	SchedulingEnabled   bool          `json:"scheduling_enabled"`
	MaxConcurrent       int           `json:"max_concurrent"`
	SchedulerState      string        `json:"scheduler_state"`
	AssignmentCounts    []StatusCount `json:"assignment_counts"`
	GateCounts          []StatusCount `json:"gate_counts"`
}

type LocalProjectPage struct {
	Items []LocalProjectItem `json:"items"`
	Page  InventoryPage      `json:"page"`
}

type LocalProjectSelectionInput struct {
	ProjectID      string `json:"project_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v LocalProjectSelectionInput) Validate() error {
	if !validSetupID(v.ProjectID) || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	return nil
}

type LocalProjectPolicyInput struct {
	ProjectID         string `json:"project_id"`
	SchedulingEnabled *bool  `json:"scheduling_enabled"`
	MaxConcurrent     int    `json:"max_concurrent"`
	IdempotencyKey    string `json:"idempotency_key"`
}

func (v LocalProjectPolicyInput) Validate() error {
	if !validSetupID(v.ProjectID) || !validSetupID(v.IdempotencyKey) || v.SchedulingEnabled == nil || v.MaxConcurrent < 1 || v.MaxConcurrent > 2 {
		return errBadRequest
	}
	return nil
}

type TaskGraphApprovalInput struct {
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
	TaskRevision   int64  `json:"task_revision"`
	GraphRevision  int64  `json:"graph_revision"`
	ApprovalDigest string `json:"approval_digest"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v TaskGraphApprovalInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.TaskID, "task:") || v.TaskRevision < 1 || v.GraphRevision < 1 || !validSetupDigest(v.ApprovalDigest) || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	return nil
}

type TaskGraphApprovalResult struct {
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
	TaskRevision   int64  `json:"task_revision"`
	GraphRevision  int64  `json:"graph_revision"`
	ApprovalDigest string `json:"approval_digest"`
	AlreadyPresent bool   `json:"already_present"`
}

type DispatchPreviewInput struct {
	ProjectID      string `json:"project_id"`
	TaskID         string `json:"task_id"`
	TaskRevision   int64  `json:"task_revision"`
	GraphRevision  int64  `json:"graph_revision"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v DispatchPreviewInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.TaskID, "task:") || v.TaskRevision < 1 || v.GraphRevision < 1 || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	return nil
}

type DispatchPreviewItem struct {
	WorkPackageID string   `json:"work_package_id"`
	State         string   `json:"state"`
	ReasonCodes   []string `json:"reason_codes"`
	WorkerID      string   `json:"worker_id,omitempty"`
	NodeID        string   `json:"node_id,omitempty"`
}
type DispatchPreviewResult struct {
	ProjectID      string                `json:"project_id"`
	TaskID         string                `json:"task_id"`
	Items          []DispatchPreviewItem `json:"items"`
	AlreadyPresent bool                  `json:"already_present"`
}

type SchedulerControlInput struct {
	ProjectID      string `json:"project_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v SchedulerControlInput) Validate() error {
	if !validSetupID(v.ProjectID) || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	return nil
}

type SchedulerStatus struct {
	ProjectID      string `json:"project_id"`
	Enabled        bool   `json:"enabled"`
	State          string `json:"state"`
	AlreadyPresent bool   `json:"already_present"`
}

type NodeItem struct {
	NodeID        string   `json:"node_id"`
	Version       int64    `json:"version"`
	Digest        string   `json:"digest"`
	Lifecycle     string   `json:"lifecycle"`
	CapabilityIDs []string `json:"capability_ids"`
}
type NodePage struct {
	Items []NodeItem    `json:"items"`
	Page  InventoryPage `json:"page"`
}

type BudgetObservation struct {
	TokenCount   *int64   `json:"token_count,omitempty"`
	CostMicros   *int64   `json:"cost_micros,omitempty"`
	ToolCalls    *int64   `json:"tool_calls,omitempty"`
	Completeness string   `json:"completeness"`
	WeakEvidence []string `json:"weak_evidence,omitempty"`
}
type GateItem struct {
	GateID     string `json:"gate_id"`
	Version    int64  `json:"version"`
	Digest     string `json:"digest"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
}
type AttemptItem struct {
	AttemptID           string `json:"attempt_id"`
	AttemptNumber       int64  `json:"attempt_number"`
	State               string `json:"state"`
	RecoveryDisposition string `json:"recovery_disposition"`
	FailureCode         string `json:"failure_code,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}
type ResultSummary struct {
	ResultID         string            `json:"result_id"`
	BaseCommit       string            `json:"base_commit"`
	CurrentCommit    string            `json:"current_commit"`
	HeadCommit       string            `json:"head_commit"`
	ManifestDigest   string            `json:"manifest_digest"`
	PreviewDigest    string            `json:"preview_digest"`
	Tests            []TestOutcomeItem `json:"tests"`
	ReviewOutcome    string            `json:"review_outcome"`
	Limitations      []string          `json:"limitations"`
	UnresolvedIssues []string          `json:"unresolved_issues"`
	EvidenceAt       string            `json:"evidence_at"`
	ReadyForDecision bool              `json:"ready_for_decision"`
	SummaryDigest    string            `json:"summary_digest"`
}

type TestOutcomeItem struct {
	GateID               string `json:"gate_id"`
	Outcome              string `json:"outcome"`
	EvidenceID           string `json:"evidence_id"`
	EvidenceDigest       string `json:"evidence_digest"`
	DurationMilliseconds int64  `json:"duration_milliseconds,omitempty"`
}

type TelemetrySummaryItem struct {
	TelemetryID     string            `json:"telemetry_id"`
	TelemetryDigest string            `json:"telemetry_digest"`
	FinalOutcome    string            `json:"final_outcome"`
	CreatedAt       string            `json:"created_at"`
	Completeness    string            `json:"completeness"`
	WeakEvidence    []string          `json:"weak_evidence,omitempty"`
	ObservedBudget  BudgetObservation `json:"observed_budget"`
}

type AcceptedHistoryItem struct {
	AuditID       string `json:"audit_id"`
	AttemptID     string `json:"attempt_id"`
	ReasonCode    string `json:"reason_code"`
	AcceptedAt    string `json:"accepted_at"`
	SummaryDigest string `json:"summary_digest,omitempty"`
}

type IncidentItem struct {
	Kind            string   `json:"kind"`
	Severity        string   `json:"severity"`
	Status          string   `json:"status"`
	EvidenceCode    string   `json:"evidence_code"`
	RecoveryActions []string `json:"recovery_actions"`
}
type AssignmentItem struct {
	AssignmentID  string `json:"assignment_id"`
	ProjectID     string `json:"project_id"`
	WorkPackageID string `json:"work_package_id"`
	ExecutionID   string `json:"execution_id"`
	WorkerID      string `json:"worker_id"`
	NodeID        string `json:"node_id"`
	State         string `json:"state"`
	FailureCode   string `json:"failure_code,omitempty"`
	UpdatedAt     string `json:"updated_at"`
}
type AssignmentPage struct {
	Items []AssignmentItem `json:"items"`
	Page  InventoryPage    `json:"page"`
}
type AssignmentDetail struct {
	AssignmentItem
	Attempts        []AttemptItem          `json:"attempts"`
	Gates           []GateItem             `json:"gates"`
	Audit           []AuditTimelineItem    `json:"audit"`
	ObservedBudget  BudgetObservation      `json:"observed_budget"`
	Telemetry       []TelemetrySummaryItem `json:"telemetry"`
	AcceptedHistory []AcceptedHistoryItem  `json:"accepted_history"`
	Incidents       []IncidentItem         `json:"incidents"`
	Result          *ResultSummary         `json:"result,omitempty"`
	AlreadyPresent  bool                   `json:"already_present"`
}

type AuditTimelineItem struct {
	AuditID    string `json:"audit_id"`
	AttemptID  string `json:"attempt_id"`
	Action     string `json:"action"`
	FromState  string `json:"from_state,omitempty"`
	ToState    string `json:"to_state"`
	ReasonCode string `json:"reason_code"`
	OccurredAt string `json:"occurred_at"`
}

type AssignmentControlInput struct {
	ProjectID      string `json:"project_id"`
	AssignmentID   string `json:"assignment_id"`
	Action         string `json:"action"`
	WorkerID       string `json:"worker_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v AssignmentControlInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.AssignmentID, "assignment:") || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	switch v.Action {
	case "reassign":
		if !namespacedSetup(v.WorkerID, "worker:") {
			return errBadRequest
		}
		return nil
	case "pause", "resume", "cancel", "retry", "fail", "evaluate":
		if v.WorkerID != "" {
			return errBadRequest
		}
		return nil
	default:
		return errBadRequest
	}
}

type IntegrationDecisionInput struct {
	ProjectID      string `json:"project_id"`
	AssignmentID   string `json:"assignment_id"`
	AttemptID      string `json:"attempt_id"`
	Decision       string `json:"decision"`
	SummaryDigest  string `json:"summary_digest"`
	ReasonCode     string `json:"reason_code"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (v IntegrationDecisionInput) Validate() error {
	if !validSetupID(v.ProjectID) || !namespacedSetup(v.AssignmentID, "assignment:") || !namespacedSetup(v.AttemptID, "attempt:") || !validSetupDigest(v.SummaryDigest) || !validSetupID(v.ReasonCode) || !validSetupID(v.IdempotencyKey) {
		return errBadRequest
	}
	if v.Decision != "approve" && v.Decision != "reject" {
		return errBadRequest
	}
	return nil
}

func NewOrchestrationHandler(commands OrchestrationAdministration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handleOrchestration(w, r, commands) })
}

func handleOrchestration(w http.ResponseWriter, r *http.Request, commands OrchestrationAdministration) {
	if commands == nil {
		writeError(w, r, errUnavailable)
		return
	}
	if r.URL.Path == "/api/v1/orchestration/projects" {
		if r.Method != http.MethodGet {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		page, err := parsePageRequest(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		result, err := commands.ListLocalProjects(r.Context(), page)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/orchestration/projects/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/orchestration/projects/"), "/")
		if len(parts) != 2 || !validSetupID(parts[0]) || (parts[1] != "selection" && parts[1] != "policy") {
			writeError(w, r, errNotFound)
			return
		}
		if parts[1] == "selection" && r.Method != http.MethodPost || parts[1] == "policy" && r.Method != http.MethodPut {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		if parts[1] == "selection" {
			var input LocalProjectSelectionInput
			if err := decodeJSON(r, &input); err != nil || input.ProjectID != parts[0] {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.SelectLocalProject(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
		var input LocalProjectPolicyInput
		if err := decodeJSON(r, &input); err != nil || input.ProjectID != parts[0] {
			writeError(w, r, errBadRequest)
			return
		}
		result, err := commands.SetLocalProjectPolicy(r.Context(), input)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	if r.URL.Path == "/api/v1/orchestration/nodes" {
		if r.Method != http.MethodGet {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		page, err := parsePageRequest(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		result, err := commands.ListNodes(r.Context(), page)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/projects/"), "/")
	if len(parts) < 2 || !validSetupID(parts[0]) {
		writeError(w, r, errNotFound)
		return
	}
	projectID, suffix := parts[0], strings.Join(parts[1:], "/")
	switch suffix {
	case "dispatch/preview":
		if r.Method != http.MethodPost {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		var input DispatchPreviewInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, r, err)
			return
		}
		if input.ProjectID != projectID {
			writeError(w, r, errBadRequest)
			return
		}
		result, err := commands.PreviewDispatch(r.Context(), input)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	case "scheduler/start", "scheduler/disable":
		if r.Method != http.MethodPost {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		var input SchedulerControlInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, r, err)
			return
		}
		if input.ProjectID != projectID {
			writeError(w, r, errBadRequest)
			return
		}
		var result SchedulerStatus
		var err error
		if suffix == "scheduler/start" {
			result, err = commands.StartScheduler(r.Context(), input)
		} else {
			result, err = commands.DisableScheduler(r.Context(), input)
		}
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	case "assignments":
		if r.Method != http.MethodGet {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		page, err := parsePageRequest(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		result, err := commands.ListAssignments(r.Context(), projectID, page)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	if len(parts) == 4 && parts[1] == "tasks" && parts[3] == "approve" {
		if r.Method != http.MethodPost {
			writeError(w, r, errMethodNotAllowed)
			return
		}
		var input TaskGraphApprovalInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, r, err)
			return
		}
		if input.ProjectID != projectID || input.TaskID != parts[2] {
			writeError(w, r, errBadRequest)
			return
		}
		result, err := commands.ApproveTaskGraph(r.Context(), input)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, r, http.StatusOK, result)
		return
	}
	if len(parts) >= 3 && parts[1] == "assignments" && namespacedSetup(parts[2], "assignment:") {
		if len(parts) == 3 && r.Method == http.MethodGet {
			result, err := commands.GetAssignment(r.Context(), projectID, parts[2])
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
		if len(parts) == 4 && (parts[3] == "controls" || parts[3] == "integration") && r.Method == http.MethodPost {
			if parts[3] == "controls" {
				var input AssignmentControlInput
				if err := decodeJSON(r, &input); err != nil {
					writeError(w, r, err)
					return
				}
				if input.ProjectID != projectID || input.AssignmentID != parts[2] {
					writeError(w, r, errBadRequest)
					return
				}
				result, err := commands.ControlAssignment(r.Context(), input)
				if err != nil {
					writeError(w, r, err)
					return
				}
				writeJSON(w, r, http.StatusOK, result)
				return
			}
			var input IntegrationDecisionInput
			if err := decodeJSON(r, &input); err != nil {
				writeError(w, r, err)
				return
			}
			if input.ProjectID != projectID || input.AssignmentID != parts[2] {
				writeError(w, r, errBadRequest)
				return
			}
			result, err := commands.DecideIntegration(r.Context(), input)
			if err != nil {
				writeError(w, r, err)
				return
			}
			writeJSON(w, r, http.StatusOK, result)
			return
		}
	}
	writeError(w, r, errNotFound)
}
