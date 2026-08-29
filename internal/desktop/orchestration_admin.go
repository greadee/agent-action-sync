package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
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
	Scheduler           *scheduler.Scheduler
	Control             orchestration.ControlService
	Inventory           storage.OrchestrationControlStore
	Projects            storage.ProjectRegistrationStore
	Operations          storage.LocalProjectOperationsStore
	Tasks               storage.ProjectTaskStore
	TaskNodes           storage.ProjectTaskNodeStore
	Registry            storage.RegistryStore
	AuthorizedProjectID string
	MaxConcurrent       int
	Node                computenode.Provider
	Gate                *integrationgate.Service
	Now                 func() time.Time
}

type LocalOrchestrationAdministration struct {
	scheduler           *scheduler.Scheduler
	control             orchestration.ControlService
	inventory           storage.OrchestrationInventoryStore
	projects            storage.ProjectRegistrationStore
	operations          storage.LocalProjectOperationsStore
	tasks               storage.ProjectTaskStore
	taskNodes           storage.ProjectTaskNodeStore
	registry            storage.RegistryStore
	authorizedProjectID string
	maxConcurrent       int
	node                computenode.Provider
	gate                *integrationgate.Service
	now                 func() time.Time

	mu          sync.Mutex
	projectKeys map[string]string
	summaries   map[string]integrationgate.IntegrationSummary
}

func NewLocalOrchestrationAdministration(options LocalAdministrationOptions) *LocalOrchestrationAdministration {
	inventory, _ := options.Inventory.(storage.OrchestrationInventoryStore)
	return &LocalOrchestrationAdministration{
		scheduler: options.Scheduler, control: options.Control, inventory: inventory,
		projects: options.Projects, operations: options.Operations, authorizedProjectID: options.AuthorizedProjectID,
		tasks: options.Tasks, taskNodes: options.TaskNodes, registry: options.Registry,
		maxConcurrent: options.MaxConcurrent, node: options.Node, gate: options.Gate, now: options.Now,
		projectKeys: map[string]string{}, summaries: map[string]integrationgate.IntegrationSummary{},
	}
}

func (admin *LocalOrchestrationAdministration) ListLocalProjects(ctx context.Context, requested storage.PageRequest) (api.LocalProjectPage, error) {
	if admin.projects == nil || admin.operations == nil {
		return api.LocalProjectPage{}, localUnavailable("local project inventory is unavailable")
	}
	page, err := admin.projects.ListProjects(ctx, requested)
	if err != nil {
		return api.LocalProjectPage{}, err
	}
	selected, selectedErr := admin.operations.GetSelectedLocalProject(ctx)
	if selectedErr != nil && !errors.Is(selectedErr, storage.ErrNotFound) {
		return api.LocalProjectPage{}, localUnavailable("local project selection is unavailable")
	}
	items := make([]api.LocalProjectItem, 0, len(page.Items))
	for _, registration := range page.Items {
		item, err := admin.localProjectItem(ctx, registration, selected.ProjectID)
		if err != nil {
			return api.LocalProjectPage{}, err
		}
		items = append(items, item)
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
	return api.LocalProjectPage{Items: items, Page: info}, nil
}

func (admin *LocalOrchestrationAdministration) SelectLocalProject(ctx context.Context, input api.LocalProjectSelectionInput) (api.LocalProjectItem, error) {
	if err := input.Validate(); err != nil {
		return api.LocalProjectItem{}, localBadRequest("local project selection is invalid")
	}
	registration, err := admin.project(ctx, input.ProjectID)
	if err != nil {
		return api.LocalProjectItem{}, err
	}
	if admin.operations == nil || admin.now == nil {
		return api.LocalProjectItem{}, localUnavailable("local project selection is unavailable")
	}
	if admin.scheduler != nil {
		status := admin.scheduler.Status()
		if status.Started && !status.Paused {
			return api.LocalProjectItem{}, localConflict("disable the selected project scheduler before changing projects")
		}
	}
	fingerprint := localHash("select-project", input.ProjectID, input.IdempotencyKey)
	if err := admin.rememberProjectCommand(input.IdempotencyKey, fingerprint); err != nil {
		return api.LocalProjectItem{}, err
	}
	if err := admin.operations.SelectLocalProject(ctx, storage.LocalProjectSelection{ProjectID: input.ProjectID, SelectedAt: admin.now().UTC()}); err != nil {
		return api.LocalProjectItem{}, localConflict("local project selection failed")
	}
	return admin.localProjectItem(ctx, registration, input.ProjectID)
}

func (admin *LocalOrchestrationAdministration) SetLocalProjectPolicy(ctx context.Context, input api.LocalProjectPolicyInput) (api.LocalProjectItem, error) {
	if err := input.Validate(); err != nil {
		return api.LocalProjectItem{}, localBadRequest("local project policy is invalid")
	}
	registration, err := admin.project(ctx, input.ProjectID)
	if err != nil {
		return api.LocalProjectItem{}, err
	}
	if admin.operations == nil || admin.now == nil || admin.maxConcurrent < 1 {
		return api.LocalProjectItem{}, localUnavailable("local project policy is unavailable")
	}
	if input.MaxConcurrent > admin.maxConcurrent {
		return api.LocalProjectItem{}, localBadRequest("project concurrency exceeds the authorized node ceiling")
	}
	selected, _ := admin.operations.GetSelectedLocalProject(ctx)
	if selected.ProjectID == input.ProjectID && admin.scheduler != nil {
		status := admin.scheduler.Status()
		if status.Started && !status.Paused {
			return api.LocalProjectItem{}, localConflict("disable the project scheduler before changing its policy")
		}
	}
	fingerprint := localHash("project-policy", input.ProjectID, boolText(*input.SchedulingEnabled), fmt.Sprintf("%d", input.MaxConcurrent), input.IdempotencyKey)
	if err := admin.rememberProjectCommand(input.IdempotencyKey, fingerprint); err != nil {
		return api.LocalProjectItem{}, err
	}
	if err := admin.operations.SetLocalProjectPolicy(ctx, storage.LocalProjectPolicy{ProjectID: input.ProjectID, SchedulingEnabled: *input.SchedulingEnabled, MaxConcurrent: input.MaxConcurrent, UpdatedAt: admin.now().UTC()}); err != nil {
		return api.LocalProjectItem{}, localConflict("local project policy update failed")
	}
	return admin.localProjectItem(ctx, registration, selected.ProjectID)
}

func (admin *LocalOrchestrationAdministration) ApproveTaskGraph(ctx context.Context, input api.TaskGraphApprovalInput) (api.TaskGraphApprovalResult, error) {
	if err := input.Validate(); err != nil {
		return api.TaskGraphApprovalResult{}, localBadRequest("task graph approval is invalid")
	}
	if err := admin.requireExecutionAuthority(ctx, input.ProjectID); err != nil {
		return api.TaskGraphApprovalResult{}, err
	}
	if admin.tasks == nil {
		return api.TaskGraphApprovalResult{}, localUnavailable("task graph approval is unavailable")
	}
	task, err := admin.tasks.GetProjectTask(ctx, input.ProjectID, input.TaskID, input.TaskRevision)
	if err != nil {
		return api.TaskGraphApprovalResult{}, localNotFound("task graph was not found")
	}
	if task.GraphRevision != input.GraphRevision || task.GraphRecordHash != input.ApprovalDigest {
		return api.TaskGraphApprovalResult{}, localConflict("task graph approval targets a stale revision")
	}
	fingerprint := localHash("approve-task", input.ProjectID, input.TaskID, fmt.Sprint(input.TaskRevision), fmt.Sprint(input.GraphRevision), input.ApprovalDigest)
	var result api.TaskGraphApprovalResult
	if replay, err := admin.replayOperatorResult(ctx, input.IdempotencyKey, fingerprint, &result); err != nil {
		return api.TaskGraphApprovalResult{}, err
	} else if replay {
		result.AlreadyPresent = true
		return result, nil
	}
	result = api.TaskGraphApprovalResult{ProjectID: input.ProjectID, TaskID: input.TaskID, TaskRevision: input.TaskRevision, GraphRevision: input.GraphRevision, ApprovalDigest: input.ApprovalDigest}
	stored, err := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, "approve_task_graph", input.ProjectID, input.TaskID, result)
	if err != nil {
		return api.TaskGraphApprovalResult{}, err
	}
	if stored.AlreadyPresent {
		if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
			return api.TaskGraphApprovalResult{}, localUnavailable("task graph approval replay is unreadable")
		}
		result.AlreadyPresent = true
	}
	return result, nil
}

func (admin *LocalOrchestrationAdministration) PreviewDispatch(ctx context.Context, input api.DispatchPreviewInput) (api.DispatchPreviewResult, error) {
	if err := input.Validate(); err != nil {
		return api.DispatchPreviewResult{}, localBadRequest("dispatch preview is invalid")
	}
	if err := admin.requireExecutionAuthority(ctx, input.ProjectID); err != nil {
		return api.DispatchPreviewResult{}, err
	}
	if admin.tasks == nil || admin.taskNodes == nil {
		return api.DispatchPreviewResult{}, localUnavailable("dispatch preview is unavailable")
	}
	task, err := admin.tasks.GetProjectTask(ctx, input.ProjectID, input.TaskID, input.TaskRevision)
	if err != nil || task.GraphRevision != input.GraphRevision {
		return api.DispatchPreviewResult{}, localConflict("dispatch preview targets a stale task graph")
	}
	fingerprint := localHash("preview-dispatch", input.ProjectID, input.TaskID, fmt.Sprint(input.TaskRevision), fmt.Sprint(input.GraphRevision))
	var result api.DispatchPreviewResult
	if replay, err := admin.replayOperatorResult(ctx, input.IdempotencyKey, fingerprint, &result); err != nil {
		return api.DispatchPreviewResult{}, err
	} else if replay {
		result.AlreadyPresent = true
		return result, nil
	}
	page, err := admin.taskNodes.ListProjectTaskNodes(ctx, storage.ProjectTaskNodeQuery{
		ProjectID: input.ProjectID, TaskID: input.TaskID, TaskRevision: input.TaskRevision, GraphRevision: input.GraphRevision,
		Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit},
	})
	if err != nil {
		return api.DispatchPreviewResult{}, localUnavailable("dispatch readiness is unavailable")
	}
	items := make([]api.DispatchPreviewItem, 0, len(page.Items))
	for _, item := range page.Items {
		state := "blocked"
		if item.Readiness == "ready" {
			state = "eligible"
		}
		items = append(items, api.DispatchPreviewItem{WorkPackageID: item.WorkPackageID, State: state, ReasonCodes: []string{item.ExplanationCode}})
	}
	result = api.DispatchPreviewResult{ProjectID: input.ProjectID, TaskID: input.TaskID, Items: items}
	stored, err := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, "preview_dispatch", input.ProjectID, input.TaskID, result)
	if err != nil {
		return api.DispatchPreviewResult{}, err
	}
	if stored.AlreadyPresent {
		if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
			return api.DispatchPreviewResult{}, localUnavailable("dispatch preview replay is unreadable")
		}
		result.AlreadyPresent = true
	}
	return result, nil
}

func (admin *LocalOrchestrationAdministration) StartScheduler(ctx context.Context, input api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return admin.setScheduler(ctx, input, true)
}

func (admin *LocalOrchestrationAdministration) DisableScheduler(ctx context.Context, input api.SchedulerControlInput) (api.SchedulerStatus, error) {
	return admin.setScheduler(ctx, input, false)
}

func (admin *LocalOrchestrationAdministration) setScheduler(ctx context.Context, input api.SchedulerControlInput, enabled bool) (api.SchedulerStatus, error) {
	if err := input.Validate(); err != nil {
		return api.SchedulerStatus{}, localBadRequest("scheduler control is invalid")
	}
	if admin.scheduler == nil {
		return api.SchedulerStatus{}, localUnavailable("scheduler control is unavailable")
	}
	if err := admin.requireExecutionAuthority(ctx, input.ProjectID); err != nil {
		return api.SchedulerStatus{}, err
	}
	fingerprint := localHash("scheduler", input.ProjectID, boolText(enabled))
	var replay api.SchedulerStatus
	if present, err := admin.replayOperatorResult(ctx, input.IdempotencyKey, fingerprint, &replay); err != nil {
		return api.SchedulerStatus{}, err
	} else if present {
		replay.AlreadyPresent = true
		return replay, nil
	}
	if enabled {
		policy, err := admin.operations.GetLocalProjectPolicy(ctx, input.ProjectID)
		if errors.Is(err, storage.ErrNotFound) || err == nil && !policy.SchedulingEnabled {
			return api.SchedulerStatus{}, localConflict("project policy does not allow scheduling")
		}
		if err != nil {
			return api.SchedulerStatus{}, localUnavailable("project policy is unavailable")
		}
		if err := admin.scheduler.SetMaxConcurrent(ctx, policy.MaxConcurrent); err != nil {
			return api.SchedulerStatus{}, localConflict("project concurrency policy could not be applied")
		}
	}
	var err error
	if enabled {
		err = admin.scheduler.Resume(ctx)
	} else {
		err = admin.scheduler.Pause(ctx)
	}
	if err != nil {
		return api.SchedulerStatus{}, localConflict("scheduler control failed")
	}
	result := schedulerStatus(input.ProjectID, admin.scheduler.Status(), false)
	action := "disable_scheduler"
	if enabled {
		action = "start_scheduler"
	}
	stored, err := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, action, input.ProjectID, input.ProjectID, result)
	if err != nil {
		return api.SchedulerStatus{}, err
	}
	if stored.AlreadyPresent {
		if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
			return api.SchedulerStatus{}, localUnavailable("scheduler control replay is unreadable")
		}
		result.AlreadyPresent = true
	}
	return result, nil
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
	audit, err := admin.control.ListAudit(ctx, assignmentID)
	if err != nil {
		return api.AssignmentDetail{}, localUnavailable("assignment audit timeline is unavailable")
	}
	return admin.detail(snapshot, audit), nil
}

func (admin *LocalOrchestrationAdministration) ControlAssignment(ctx context.Context, input api.AssignmentControlInput) (api.AssignmentDetail, error) {
	if err := input.Validate(); err != nil {
		return api.AssignmentDetail{}, localBadRequest("assignment control is invalid")
	}
	if err := admin.requireExecutionAuthority(ctx, input.ProjectID); err != nil {
		return api.AssignmentDetail{}, err
	}
	fingerprint := localHash("assignment-control", input.ProjectID, input.AssignmentID, input.Action, input.WorkerID)
	var replay api.AssignmentDetail
	if present, err := admin.replayOperatorResult(ctx, input.IdempotencyKey, fingerprint, &replay); err != nil {
		return api.AssignmentDetail{}, err
	} else if present {
		replay.AlreadyPresent = true
		return replay, nil
	}
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
	case "retry", "reassign":
		if snapshot.Attempt.State != storage.AssignmentFailed && snapshot.Attempt.State != storage.AssignmentCanceled && snapshot.Attempt.State != storage.AssignmentExpired {
			return api.AssignmentDetail{}, localConflict("only failed, canceled, or expired attempts can be retried or reassigned")
		}
		workerID := snapshot.Assignment.WorkerID
		reasonCode := "operator_retry"
		if input.Action == "reassign" {
			if input.WorkerID == workerID || !admin.activeWorker(ctx, input.WorkerID) {
				return api.AssignmentDetail{}, localConflict("reassignment requires a different active worker")
			}
			workerID, reasonCode = input.WorkerID, "operator_reassign"
		}
		keyDigest := localHash(input.Action, input.ProjectID, input.AssignmentID, input.WorkerID, input.IdempotencyKey)
		attemptID := "attempt:" + input.Action + "-" + keyDigest[:24]
		_, err = admin.control.Retry(ctx, orchestration.RetryRequest{
			AssignmentID: input.AssignmentID, PreviousAttemptID: snapshot.Attempt.AttemptID, AttemptID: attemptID,
			WorkerID: workerID, Action: input.Action, ReasonCode: reasonCode,
			IdempotencyDigest: keyDigest, OperationID: "operation:retry:" + keyDigest[:24],
			OperationDigest: localHash("retry-operation", keyDigest), AuditID: "audit:retry:" + keyDigest[:24], ActorID: "actor:desktop-operator",
		})
	case "evaluate":
		result, evaluateErr := admin.evaluate(ctx, snapshot, input.IdempotencyKey)
		if evaluateErr != nil {
			return api.AssignmentDetail{}, evaluateErr
		}
		stored, storeErr := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, "assignment_evaluate", input.ProjectID, input.AssignmentID, result)
		if storeErr != nil {
			return api.AssignmentDetail{}, storeErr
		}
		if stored.AlreadyPresent {
			if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
				return api.AssignmentDetail{}, localUnavailable("assignment control replay is unreadable")
			}
			result.AlreadyPresent = true
		}
		return result, nil
	default:
		return api.AssignmentDetail{}, localBadRequest("assignment action is invalid")
	}
	if err != nil {
		return api.AssignmentDetail{}, localConflict("assignment recovery action failed")
	}
	result, err := admin.GetAssignment(ctx, input.ProjectID, input.AssignmentID)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	stored, err := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, "assignment_"+input.Action, input.ProjectID, input.AssignmentID, result)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	if stored.AlreadyPresent {
		if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
			return api.AssignmentDetail{}, localUnavailable("assignment control replay is unreadable")
		}
		result.AlreadyPresent = true
	}
	return result, nil
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
	audit, auditErr := admin.control.ListAudit(ctx, snapshot.Assignment.AssignmentID)
	if auditErr != nil {
		return api.AssignmentDetail{}, localUnavailable("assignment audit timeline is unavailable")
	}
	return admin.detail(updated, audit), nil
}

func (admin *LocalOrchestrationAdministration) DecideIntegration(ctx context.Context, input api.IntegrationDecisionInput) (api.AssignmentDetail, error) {
	if err := input.Validate(); err != nil {
		return api.AssignmentDetail{}, localBadRequest("integration decision is invalid")
	}
	if err := admin.requireExecutionAuthority(ctx, input.ProjectID); err != nil {
		return api.AssignmentDetail{}, err
	}
	fingerprint := localHash("integration-decision", input.ProjectID, input.AssignmentID, input.AttemptID, input.Decision, input.SummaryDigest, input.ReasonCode)
	var replay api.AssignmentDetail
	if present, err := admin.replayOperatorResult(ctx, input.IdempotencyKey, fingerprint, &replay); err != nil {
		return api.AssignmentDetail{}, err
	} else if present {
		replay.AlreadyPresent = true
		return replay, nil
	}
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
	result, err := admin.GetAssignment(ctx, input.ProjectID, input.AssignmentID)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	stored, err := admin.rememberOperatorResult(ctx, input.IdempotencyKey, fingerprint, "integration_"+input.Decision, input.ProjectID, input.AssignmentID, result)
	if err != nil {
		return api.AssignmentDetail{}, err
	}
	if stored.AlreadyPresent {
		if err := json.Unmarshal(stored.Operation.ResultJSON, &result); err != nil {
			return api.AssignmentDetail{}, localUnavailable("integration decision replay is unreadable")
		}
		result.AlreadyPresent = true
	}
	return result, nil
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

func (admin *LocalOrchestrationAdministration) detail(snapshot storage.OrchestrationSnapshot, auditEvents []storage.OrchestrationAuditEvent) api.AssignmentDetail {
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
	audit := make([]api.AuditTimelineItem, 0, len(auditEvents))
	for _, event := range auditEvents {
		audit = append(audit, api.AuditTimelineItem{AuditID: event.AuditID, AttemptID: event.AttemptID, Action: event.Action, FromState: string(event.FromState), ToState: string(event.ToState), ReasonCode: event.ReasonCode, OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano)})
	}
	detail := api.AssignmentDetail{AssignmentItem: assignmentItem(snapshot.Assignment), Attempts: []api.AttemptItem{attempt}, Gates: gates, Audit: audit, ObservedBudget: api.BudgetObservation{Completeness: "unknown"}}
	admin.mu.Lock()
	if summary, ok := admin.summaries[snapshot.Assignment.AssignmentID]; ok {
		detail.Result = resultSummary(summary)
	}
	admin.mu.Unlock()
	return detail
}

func (admin *LocalOrchestrationAdministration) activeWorker(ctx context.Context, workerID string) bool {
	if admin.registry == nil || !strings.HasPrefix(workerID, "worker:") {
		return false
	}
	page, err := admin.registry.ListWorkerProfiles(ctx, storage.WorkerQuery{Page: storage.PageRequest{Limit: storage.MaxAdminPageLimit}, WorkerID: workerID, Lifecycle: storage.RegistryActive})
	if err != nil {
		return false
	}
	for _, worker := range page.Items {
		if worker.WorkerID == workerID && worker.Lifecycle == storage.RegistryActive {
			return true
		}
	}
	return false
}

func (admin *LocalOrchestrationAdministration) replayOperatorResult(ctx context.Context, key, fingerprint string, destination any) (bool, error) {
	if admin.operations == nil {
		return false, localUnavailable("operator replay ledger is unavailable")
	}
	operation, err := admin.operations.GetLocalOperatorOperation(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, localUnavailable("operator replay ledger is unavailable")
	}
	if operation.Fingerprint != fingerprint {
		return false, localConflict("operator idempotency key was reused for a different command")
	}
	if err := json.Unmarshal(operation.ResultJSON, destination); err != nil {
		return false, localUnavailable("operator replay result is unreadable")
	}
	return true, nil
}

func (admin *LocalOrchestrationAdministration) rememberOperatorResult(ctx context.Context, key, fingerprint, action, projectID, subjectID string, value any) (storage.LocalOperatorOperationResult, error) {
	if admin.operations == nil || admin.now == nil {
		return storage.LocalOperatorOperationResult{}, localUnavailable("operator replay ledger is unavailable")
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > storage.MaxLocalOperatorResultBytes {
		return storage.LocalOperatorOperationResult{}, localUnavailable("operator replay result is unavailable")
	}
	result, err := admin.operations.SaveLocalOperatorOperation(ctx, storage.LocalOperatorOperation{
		IdempotencyKey: key, Fingerprint: fingerprint, Action: action, ProjectID: projectID, SubjectID: subjectID,
		ResultJSON: encoded, OccurredAt: admin.now().UTC(),
	})
	if errors.Is(err, storage.ErrConflict) {
		return storage.LocalOperatorOperationResult{}, localConflict("operator idempotency key was reused for a different command")
	}
	if err != nil {
		return storage.LocalOperatorOperationResult{}, localUnavailable("operator replay result could not be recorded")
	}
	return result, nil
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

func (admin *LocalOrchestrationAdministration) project(ctx context.Context, projectID string) (storage.ProjectRegistration, error) {
	if err := admin.requireProject(ctx, projectID); err != nil {
		return storage.ProjectRegistration{}, err
	}
	registration, err := admin.projects.GetProject(ctx, projectID)
	if err != nil {
		return storage.ProjectRegistration{}, localNotFound("project was not found")
	}
	return registration, nil
}

func (admin *LocalOrchestrationAdministration) requireExecutionAuthority(ctx context.Context, projectID string) error {
	if err := admin.requireProject(ctx, projectID); err != nil {
		return err
	}
	if admin.operations == nil || projectID != admin.authorizedProjectID {
		return localConflict("project is not authorized for this local runtime")
	}
	selected, err := admin.operations.GetSelectedLocalProject(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return localConflict("project is not the explicit local selection")
	}
	if err != nil {
		return localUnavailable("local project selection is unavailable")
	}
	if selected.ProjectID != projectID {
		return localConflict("project is not the explicit local selection")
	}
	return nil
}

func (admin *LocalOrchestrationAdministration) rememberProjectCommand(key, fingerprint string) error {
	admin.mu.Lock()
	defer admin.mu.Unlock()
	if prior, ok := admin.projectKeys[key]; ok && prior != fingerprint {
		return localConflict("project command idempotency key was reused")
	}
	admin.projectKeys[key] = fingerprint
	return nil
}

func (admin *LocalOrchestrationAdministration) localProjectItem(ctx context.Context, registration storage.ProjectRegistration, selectedProjectID string) (api.LocalProjectItem, error) {
	policy := storage.LocalProjectPolicy{ProjectID: registration.ProjectID, MaxConcurrent: 1}
	storedPolicy, err := admin.operations.GetLocalProjectPolicy(ctx, registration.ProjectID)
	if err == nil {
		policy = storedPolicy
	} else if !errors.Is(err, storage.ErrNotFound) {
		return api.LocalProjectItem{}, localUnavailable("local project policy is unavailable")
	}
	status, err := admin.operations.GetProjectOrchestrationStatus(ctx, registration.ProjectID)
	if err != nil {
		return api.LocalProjectItem{}, localUnavailable("local project status is unavailable")
	}
	item := api.LocalProjectItem{
		ProjectID: registration.ProjectID, DisplayName: localDisplayName(registration.Name),
		RegisteredAt:        registration.RegisteredAt.UTC().Format(time.RFC3339Nano),
		Selected:            selectedProjectID == registration.ProjectID,
		ExecutionAuthorized: registration.ProjectID == admin.authorizedProjectID,
		SchedulingEnabled:   policy.SchedulingEnabled, MaxConcurrent: policy.MaxConcurrent,
		SchedulerState: "not_selected", AssignmentCounts: assignmentStatusCounts(status.AssignmentCounts), GateCounts: gateStatusCounts(status.GateCounts),
	}
	if item.Selected {
		item.SchedulerState = "paused"
		if admin.scheduler != nil {
			item.SchedulerState = schedulerStatus(registration.ProjectID, admin.scheduler.Status(), false).State
		}
	}
	return item, nil
}

func localDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) {
		return "registered project"
	}
	return value
}

func assignmentStatusCounts(values map[storage.AssignmentState]int64) []api.StatusCount {
	items := make([]api.StatusCount, 0, len(values))
	for state, count := range values {
		items = append(items, api.StatusCount{State: string(state), Count: count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].State < items[j].State })
	return items
}

func gateStatusCounts(values map[storage.GateState]int64) []api.StatusCount {
	items := make([]api.StatusCount, 0, len(values))
	for state, count := range values {
		items = append(items, api.StatusCount{State: string(state), Count: count})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].State < items[j].State })
	return items
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
