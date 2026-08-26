package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/computenode"
	"syncgate/internal/integrationgate"
	"syncgate/internal/orchestration"
	"syncgate/internal/resultintake"
	"syncgate/internal/scheduler"
	"syncgate/internal/storage"
)

type LocalAdministrationOptions struct {
	Scheduler *scheduler.Scheduler
	Control   orchestration.ControlService
	Inventory storage.OrchestrationControlStore
	Projects  storage.ProjectRegistrationStore
	Node      computenode.Provider
	Gate      *integrationgate.Service
	Now       func() time.Time
}

type LocalOrchestrationAdministration struct {
	scheduler *scheduler.Scheduler
	control   orchestration.ControlService
	inventory storage.OrchestrationInventoryStore
	projects  storage.ProjectRegistrationStore
	node      computenode.Provider
	gate      *integrationgate.Service
	now       func() time.Time

	mu            sync.Mutex
	schedulerKeys map[string]string
	summaries     map[string]integrationgate.IntegrationSummary
}

func NewLocalOrchestrationAdministration(options LocalAdministrationOptions) *LocalOrchestrationAdministration {
	inventory, _ := options.Inventory.(storage.OrchestrationInventoryStore)
	return &LocalOrchestrationAdministration{
		scheduler: options.Scheduler, control: options.Control, inventory: inventory,
		projects: options.Projects, node: options.Node, gate: options.Gate, now: options.Now,
		schedulerKeys: map[string]string{}, summaries: map[string]integrationgate.IntegrationSummary{},
	}
}

func (admin *LocalOrchestrationAdministration) ApproveTaskGraph(context.Context, api.TaskGraphApprovalInput) (api.TaskGraphApprovalResult, error) {
	return api.TaskGraphApprovalResult{}, localUnavailable("task graph approval is not composed in Slice 3")
}

func (admin *LocalOrchestrationAdministration) PreviewDispatch(context.Context, api.DispatchPreviewInput) (api.DispatchPreviewResult, error) {
	return api.DispatchPreviewResult{}, localUnavailable("dispatch preview is not composed in Slice 3")
}

func (admin *LocalOrchestrationAdministration) StartScheduler(ctx context.Context, input api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return admin.setScheduler(ctx, input, true)
}

func (admin *LocalOrchestrationAdministration) DisableScheduler(ctx context.Context, input api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return admin.setScheduler(ctx, input, false)
}

func (admin *LocalOrchestrationAdministration) setScheduler(ctx context.Context, input api.SchedulerControlInput, enabled bool) (api.SchedulerStatus, error) {
	if err := admin.requireProject(ctx, input.ProjectID); err != nil {
		return api.SchedulerStatus{}, err
	}
	fingerprint := localHash(input.ProjectID, input.IdempotencyKey, boolText(enabled))
	admin.mu.Lock()
	if prior, ok := admin.schedulerKeys[input.IdempotencyKey]; ok {
		admin.mu.Unlock()
		if prior != fingerprint {
			return api.SchedulerStatus{}, localConflict("scheduler idempotency key was reused")
		}
		status := admin.scheduler.Status()
		return schedulerStatus(input.ProjectID, status, true), nil
	}
	admin.mu.Unlock()
	var err error
	if enabled {
		err = admin.scheduler.Resume(ctx)
	} else {
		err = admin.scheduler.Pause(ctx)
	}
	if err != nil {
		return api.SchedulerStatus{}, localConflict("scheduler control failed")
	}
	admin.mu.Lock()
	admin.schedulerKeys[input.IdempotencyKey] = fingerprint
	admin.mu.Unlock()
	return schedulerStatus(input.ProjectID, admin.scheduler.Status(), false), nil
}

func (admin *LocalOrchestrationAdministration) ListNodes(ctx context.Context, page storage.PageRequest) (api.NodePage, error) {
	if page.Limit < 1 || page.Limit > storage.MaxAdminPageLimit || admin.node == nil {
		return api.NodePage{}, localBadRequest("node page is invalid")
	}
	definition, err := admin.node.Definition(ctx)
	if err != nil {
		return api.NodePage{}, localUnavailable("local node definition is unavailable")
	}
	capabilities := []string{"capability:inspect", "capability:write", "capability:shell", "capability:test", "capability:branch"}
	return api.NodePage{Items: []api.NodeItem{{
		NodeID: definition.NodeID, Version: definition.Version, Digest: definition.DefinitionDigest,
		Lifecycle: string(definition.Lifecycle), CapabilityIDs: capabilities,
	}}, Page: api.InventoryPage{Limit: page.Limit}}, nil
}

func (admin *LocalOrchestrationAdministration) ListAssignments(ctx context.Context, projectID string, requested storage.PageRequest) (api.AssignmentPage, error) {
	if err := admin.requireProject(ctx, projectID); err != nil {
		return api.AssignmentPage{}, err
	}
	if admin.inventory == nil {
		return api.AssignmentPage{}, localUnavailable("assignment inventory is unavailable")
	}
	page, err := admin.inventory.ListOrchestrationAssignments(ctx, projectID, requested)
	if err != nil {
		return api.AssignmentPage{}, err
	}
	items := make([]api.AssignmentItem, 0, len(page.Items))
	for _, assignment := range page.Items {
		items = append(items, assignmentItem(assignment))
	}
	info := api.InventoryPage{Limit: requested.Limit}
	if info.Limit == 0 {
		info.Limit = storage.DefaultAdminPageLimit
	}
	if page.NextCursor != nil {
		raw, _ := json.Marshal(page.NextCursor)
		info.HasMore = true
		info.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return api.AssignmentPage{Items: items, Page: info}, nil
}

func (admin *LocalOrchestrationAdministration) GetAssignment(ctx context.Context, projectID, assignmentID string) (api.AssignmentDetail, error) {
	snapshot, err := admin.control.GetAssignment(ctx, assignmentID)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	if snapshot.Assignment.ProjectID != projectID {
		return api.AssignmentDetail{}, localNotFound("assignment was not found")
	}
	return admin.detail(snapshot), nil
}

func (admin *LocalOrchestrationAdministration) ControlAssignment(ctx context.Context, input api.AssignmentControlInput) (api.AssignmentDetail, error) {
	snapshot, err := admin.control.GetAssignment(ctx, input.AssignmentID)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	if snapshot.Assignment.ProjectID != input.ProjectID {
		return api.AssignmentDetail{}, localNotFound("assignment was not found")
	}
	switch input.Action {
	case "pause":
		err = admin.scheduler.Pause(ctx)
	case "resume":
		if snapshot.Attempt.RecoveryDisposition == storage.RecoveryNeedsOperator {
			return api.AssignmentDetail{}, localConflict("uncertain execution cannot be replayed; cancel or fail it explicitly")
		}
		err = admin.scheduler.Resume(ctx)
	case "cancel":
		err = admin.scheduler.Cancel(ctx, input.AssignmentID)
		if err == nil {
			current, loadErr := admin.control.GetAssignment(ctx, input.AssignmentID)
			if loadErr == nil && !terminalAssignment(current.Attempt.State) {
				_, err = admin.recoveryTransition(ctx, current, storage.AssignmentCanceled, "operator_canceled", input.IdempotencyKey)
			}
		}
	case "fail":
		_, err = admin.recoveryTransition(ctx, snapshot, storage.AssignmentFailed, "operator_failed_recovery", input.IdempotencyKey)
	case "retry":
		if snapshot.Attempt.State != storage.AssignmentFailed && snapshot.Attempt.State != storage.AssignmentCanceled && snapshot.Attempt.State != storage.AssignmentExpired {
			return api.AssignmentDetail{}, localConflict("only failed, canceled, or expired attempts can be retried")
		}
		keyDigest := localHash("retry", input.ProjectID, input.AssignmentID, input.IdempotencyKey)
		attemptID := "attempt:retry-" + keyDigest[:24]
		_, err = admin.control.Retry(ctx, orchestration.RetryRequest{
			AssignmentID: input.AssignmentID, PreviousAttemptID: snapshot.Attempt.AttemptID, AttemptID: attemptID,
			IdempotencyDigest: keyDigest, OperationID: "operation:retry:" + keyDigest[:24],
			OperationDigest: localHash("retry-operation", keyDigest), AuditID: "audit:retry:" + keyDigest[:24], ActorID: "actor:desktop-operator",
		})
	case "evaluate":
		return admin.evaluate(ctx, snapshot, input.IdempotencyKey)
	case "reassign":
		return api.AssignmentDetail{}, localUnavailable("reassignment is not composed in Slice 3")
	default:
		return api.AssignmentDetail{}, localBadRequest("assignment action is invalid")
	}
	if err != nil {
		return api.AssignmentDetail{}, localConflict("assignment recovery action failed")
	}
	return admin.GetAssignment(ctx, input.ProjectID, input.AssignmentID)
}

func (admin *LocalOrchestrationAdministration) evaluate(ctx context.Context, snapshot storage.OrchestrationSnapshot, key string) (api.AssignmentDetail, error) {
	if snapshot.Attempt.State != storage.AssignmentCollecting || snapshot.Lease == nil || snapshot.Resources == nil || admin.gate == nil {
		return api.AssignmentDetail{}, localConflict("assignment is not ready for result evaluation")
	}
	assignment := LocalAssignmentReference(snapshot)
	summary, err := admin.gate.Evaluate(ctx, integrationgate.EvaluateRequest{
		AssignmentID: snapshot.Assignment.AssignmentID, AttemptID: snapshot.Attempt.AttemptID,
		Assignment: assignment, FencingDigest: snapshot.Lease.FencingDigest,
		ActorID: "actor:desktop-operator", CollectDigest: localHash("collect", key, snapshot.Attempt.AttemptID),
	})
	if err != nil && !errors.Is(err, integrationgate.ErrHumanRequired) {
		return api.AssignmentDetail{}, localConflict("result evaluation failed")
	}
	admin.mu.Lock()
	admin.summaries[snapshot.Assignment.AssignmentID] = summary
	admin.mu.Unlock()
	updated, loadErr := admin.control.GetAssignment(ctx, snapshot.Assignment.AssignmentID)
	if loadErr != nil {
		return api.AssignmentDetail{}, loadErr
	}
	return admin.detail(updated), nil
}

func (admin *LocalOrchestrationAdministration) DecideIntegration(ctx context.Context, input api.IntegrationDecisionInput) (api.AssignmentDetail, error) {
	snapshot, err := admin.control.GetAssignment(ctx, input.AssignmentID)
	if err != nil || snapshot.Assignment.ProjectID != input.ProjectID || snapshot.Lease == nil || admin.gate == nil {
		return api.AssignmentDetail{}, localConflict("integration decision is unavailable")
	}
	admin.mu.Lock()
	summary, ok := admin.summaries[input.AssignmentID]
	admin.mu.Unlock()
	if !ok || summary.Digest != input.SummaryDigest || summary.AttemptID != input.AttemptID {
		return api.AssignmentDetail{}, localConflict("integration summary is stale or unavailable")
	}
	decision := integrationgate.DecisionReject
	if input.Decision == "approve" {
		decision = integrationgate.DecisionApprove
	}
	_, err = admin.gate.Decide(ctx, integrationgate.DecisionRequest{
		AssignmentID: input.AssignmentID, AttemptID: input.AttemptID, FencingDigest: snapshot.Lease.FencingDigest,
		ActorID: "actor:desktop-operator", Decision: decision, ReasonCode: input.ReasonCode, Summary: summary,
	})
	if err != nil {
		return api.AssignmentDetail{}, localConflict("integration decision failed")
	}
	return admin.GetAssignment(ctx, input.ProjectID, input.AssignmentID)
}

func (admin *LocalOrchestrationAdministration) recoveryTransition(ctx context.Context, snapshot storage.OrchestrationSnapshot, target storage.AssignmentState, code, key string) (storage.OrchestrationWriteResult, error) {
	if snapshot.Lease == nil || snapshot.Attempt.LeaseGeneration < 1 || terminalAssignment(snapshot.Attempt.State) {
		return storage.OrchestrationWriteResult{}, localConflict("attempt has no active recovery fence")
	}
	digest := localHash(string(target), snapshot.Assignment.AssignmentID, snapshot.Attempt.AttemptID, key)
	return admin.control.Transition(ctx, orchestration.TransitionRequest{
		AssignmentID: snapshot.Assignment.AssignmentID, AttemptID: snapshot.Attempt.AttemptID,
		TargetState: target, LeaseGeneration: snapshot.Attempt.LeaseGeneration, FencingDigest: snapshot.Lease.FencingDigest,
		FailureCode: code, OperationID: "operation:recovery:" + digest[:24], OperationDigest: digest,
		AuditID: "audit:recovery:" + digest[:24], ActorID: "actor:desktop-operator",
	})
}

func (admin *LocalOrchestrationAdministration) detail(snapshot storage.OrchestrationSnapshot) api.AssignmentDetail {
	attempt := api.AttemptItem{
		AttemptID: snapshot.Attempt.AttemptID, AttemptNumber: snapshot.Attempt.AttemptNumber,
		State: string(snapshot.Attempt.State), RecoveryDisposition: string(snapshot.Attempt.RecoveryDisposition),
		FailureCode: snapshot.Attempt.FailureCode, CreatedAt: snapshot.Attempt.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: snapshot.Attempt.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	gates := make([]api.GateItem, 0, len(snapshot.Gates))
	for _, gate := range snapshot.Gates {
		gates = append(gates, api.GateItem{GateID: gate.GateID, Version: gate.GateVersion, Digest: gate.GateDigest, Status: string(gate.Status), ReasonCode: gate.ReasonCode})
	}
	sort.Slice(gates, func(i, j int) bool { return gates[i].GateID < gates[j].GateID })
	detail := api.AssignmentDetail{AssignmentItem: assignmentItem(snapshot.Assignment), Attempts: []api.AttemptItem{attempt}, Gates: gates, ObservedBudget: api.BudgetObservation{Completeness: "unknown"}}
	admin.mu.Lock()
	if summary, ok := admin.summaries[snapshot.Assignment.AssignmentID]; ok {
		detail.Result = resultSummary(summary)
	}
	admin.mu.Unlock()
	return detail
}

func (admin *LocalOrchestrationAdministration) requireProject(ctx context.Context, projectID string) error {
	if admin.projects == nil || strings.TrimSpace(projectID) == "" {
		return localBadRequest("project ID is required")
	}
	if _, err := admin.projects.GetProject(ctx, projectID); err != nil {
		return localNotFound("project was not found")
	}
	return nil
}

func LocalAssignmentReference(snapshot storage.OrchestrationSnapshot) resultintake.AssignmentReference {
	return resultintake.AssignmentReference{
		AssignmentID: snapshot.Assignment.AssignmentID, Version: 1,
		Digest: localHash("assignment", snapshot.Assignment.AssignmentID, snapshot.Attempt.AttemptID, snapshot.Assignment.ContractDigest),
	}
}

func assignmentItem(value storage.OrchestrationAssignment) api.AssignmentItem {
	return api.AssignmentItem{
		AssignmentID: value.AssignmentID, ProjectID: value.ProjectID, WorkPackageID: value.WorkPackageID,
		ExecutionID: value.ExecutionID, WorkerID: value.WorkerID, NodeID: value.NodeID,
		State: string(value.State), FailureCode: value.FailureCode, UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func resultSummary(value integrationgate.IntegrationSummary) *api.ResultSummary {
	evidence := make([]string, 0, len(value.Tests))
	for _, test := range value.Tests {
		evidence = append(evidence, test.EvidenceID)
	}
	review := "not_required"
	if value.Review != nil {
		review = string(value.Review.Outcome)
	}
	return &api.ResultSummary{
		ResultID: value.ResultID, BaseCommit: value.BaseCommit, CurrentCommit: value.CurrentCommit, HeadCommit: value.HeadCommit,
		ManifestDigest: value.ManifestDigest, PreviewDigest: value.PreviewDigest, TestEvidenceIDs: evidence,
		ReviewOutcome: review, Limitations: append([]string(nil), value.Limitations...), UnresolvedIssues: append([]string(nil), value.UnresolvedIssues...), SummaryDigest: value.Digest,
	}
}

func schedulerStatus(projectID string, value scheduler.Status, replay bool) api.SchedulerStatus {
	state := "disabled"
	enabled := value.Started && !value.Paused && !value.Draining
	if value.Draining {
		state = "draining"
	} else if enabled {
		state = "running"
	} else if value.Started {
		state = "paused"
	}
	return api.SchedulerStatus{ProjectID: projectID, Enabled: enabled, State: state, AlreadyPresent: replay}
}

func terminalAssignment(state storage.AssignmentState) bool {
	return state == storage.AssignmentAccepted || state == storage.AssignmentFailed || state == storage.AssignmentCanceled || state == storage.AssignmentExpired
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func localBadRequest(message string) error {
	return &api.APIError{Status: http.StatusBadRequest, Code: "invalid_request", Message: message}
}
func localNotFound(message string) error {
	return &api.APIError{Status: http.StatusNotFound, Code: "not_found", Message: message}
}
func localConflict(message string) error {
	return &api.APIError{Status: http.StatusConflict, Code: "state_conflict", Message: message}
}
func localUnavailable(message string) error {
	return &api.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: message}
}

var _ api.OrchestrationAdministration = (*LocalOrchestrationAdministration)(nil)
