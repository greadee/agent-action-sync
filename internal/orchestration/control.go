package orchestration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"syncgate/internal/storage"
)

var (
	ErrInvalidControl    = errors.New("invalid orchestration control request")
	ErrInvalidTransition = errors.New("invalid assignment state transition")
	ErrStaleFence        = errors.New("stale orchestration fence")
	ErrNeedsOperator     = errors.New("orchestration attempt needs operator reconciliation")
	controlIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

const (
	defaultLeaseDuration = 5 * time.Minute
	maxLeaseDuration     = 24 * time.Hour
)

type ControlService struct {
	Store         storage.OrchestrationControlStore
	Now           func() time.Time
	LeaseDuration time.Duration
}

type PlanRequest struct {
	AssignmentID      string
	AttemptID         string
	ProjectID         string
	TaskID            string
	TaskRevision      int64
	GraphRevision     int64
	WorkPackageID     string
	ExecutionID       string
	ContractID        string
	ContractVersion   int64
	ContractDigest    string
	WorkerID          string
	NodeID            string
	IdempotencyDigest string
	OperationID       string
	OperationDigest   string
	AuditID           string
	ActorID           string
	AssignmentReason  string
	Binding           storage.OrchestrationAttemptBinding
}

type ClaimRequest struct {
	AssignmentID    string
	AttemptID       string
	LeaseID         string
	OwnerNodeID     string
	OwnerRuntimeID  string
	FencingDigest   string
	OperationID     string
	OperationDigest string
	AuditID         string
	ActorID         string
}

type BindResourcesRequest struct {
	AssignmentID        string
	AttemptID           string
	LeaseGeneration     int64
	FencingDigest       string
	RuntimeSessionID    string
	RuntimeResumeKey    string
	WorkspaceID         string
	WorkspaceGeneration int64
	OperationID         string
	OperationDigest     string
	AuditID             string
	ActorID             string
}

type TransitionRequest struct {
	AssignmentID    string
	AttemptID       string
	TargetState     storage.AssignmentState
	LeaseGeneration int64
	FencingDigest   string
	FailureCode     string
	Recovery        storage.RecoveryDisposition
	OperationID     string
	OperationDigest string
	AuditID         string
	ActorID         string
}

type HeartbeatRequest struct {
	AssignmentID    string
	AttemptID       string
	LeaseID         string
	LeaseGeneration int64
	FencingDigest   string
	ExtendBy        time.Duration
}

type RetryRequest struct {
	AssignmentID      string
	PreviousAttemptID string
	AttemptID         string
	WorkerID          string
	Action            string
	ReasonCode        string
	IdempotencyDigest string
	OperationID       string
	OperationDigest   string
	AuditID           string
	ActorID           string
}

type GateStatusRequest struct {
	AssignmentID string
	AttemptID    string
	GateID       string
	GateVersion  int64
	GateDigest   string
	Status       storage.GateState
	EvidenceID   string
	ReasonCode   string
	AuditID      string
	ActorID      string
}

type OperatorDecisionRequest struct {
	DecisionID        string
	AssignmentID      string
	AttemptID         string
	Decision          string
	ReasonCode        string
	ActorID           string
	IdempotencyDigest string
	AuditID           string
}

func ReduceAssignmentState(current, target storage.AssignmentState) error {
	allowed := map[storage.AssignmentState]map[storage.AssignmentState]bool{
		storage.AssignmentPlanned:       {storage.AssignmentLeased: true, storage.AssignmentCanceled: true},
		storage.AssignmentLeased:        {storage.AssignmentPreparing: true, storage.AssignmentExpired: true, storage.AssignmentCanceled: true},
		storage.AssignmentPreparing:     {storage.AssignmentRunning: true, storage.AssignmentPaused: true, storage.AssignmentCollecting: true, storage.AssignmentFailed: true, storage.AssignmentCanceled: true, storage.AssignmentExpired: true},
		storage.AssignmentRunning:       {storage.AssignmentPaused: true, storage.AssignmentCollecting: true, storage.AssignmentFailed: true, storage.AssignmentCanceled: true, storage.AssignmentExpired: true},
		storage.AssignmentPaused:        {storage.AssignmentRunning: true, storage.AssignmentCollecting: true, storage.AssignmentFailed: true, storage.AssignmentCanceled: true, storage.AssignmentExpired: true},
		storage.AssignmentCollecting:    {storage.AssignmentAwaitingGates: true, storage.AssignmentFailed: true, storage.AssignmentCanceled: true},
		storage.AssignmentAwaitingGates: {storage.AssignmentAccepted: true, storage.AssignmentFailed: true, storage.AssignmentCanceled: true},
	}
	if !allowed[current][target] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, target)
	}
	return nil
}

func RecoveryForState(state storage.AssignmentState, hasResources bool) storage.RecoveryDisposition {
	switch state {
	case storage.AssignmentPlanned, storage.AssignmentAwaitingGates:
		return storage.RecoveryResume
	case storage.AssignmentLeased:
		if hasResources {
			return storage.RecoveryNeedsOperator
		}
		return storage.RecoveryReconcile
	case storage.AssignmentPreparing, storage.AssignmentRunning, storage.AssignmentPaused, storage.AssignmentCollecting:
		return storage.RecoveryNeedsOperator
	default:
		return storage.RecoveryNone
	}
}

func ReduceGateState(current, target storage.GateState) error {
	if current == target {
		return nil
	}
	if current == storage.GatePending && (target == storage.GateSatisfied || target == storage.GateFailed || target == storage.GateWaived) {
		return nil
	}
	return fmt.Errorf("%w: gate %s -> %s", ErrInvalidTransition, current, target)
}

func (service ControlService) Plan(ctx context.Context, request PlanRequest) (storage.OrchestrationWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if err := validatePlan(request); err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	reason := request.AssignmentReason
	if reason == "" {
		reason = "planned"
	}
	assignment := storage.OrchestrationAssignment{
		AssignmentID: request.AssignmentID, ProjectID: request.ProjectID, TaskID: request.TaskID, TaskRevision: request.TaskRevision,
		GraphRevision: request.GraphRevision, WorkPackageID: request.WorkPackageID, ExecutionID: request.ExecutionID,
		ContractID: request.ContractID, ContractVersion: request.ContractVersion, ContractDigest: request.ContractDigest,
		WorkerID: request.WorkerID, NodeID: request.NodeID, State: storage.AssignmentPlanned, CurrentAttemptID: request.AttemptID,
		CurrentAttempt: 1, IdempotencyDigest: request.IdempotencyDigest, RecoveryDisposition: storage.RecoveryNone, CreatedAt: now, UpdatedAt: now,
	}
	attempt := storage.OrchestrationAttempt{
		AttemptID: request.AttemptID, AssignmentID: request.AssignmentID, ProjectID: request.ProjectID, WorkPackageID: request.WorkPackageID,
		ExecutionID: request.ExecutionID, AttemptNumber: 1, State: storage.AssignmentPlanned, IdempotencyDigest: request.IdempotencyDigest,
		RecoveryDisposition: storage.RecoveryNone, CreatedAt: now, UpdatedAt: now,
	}
	return service.Store.PlanAssignment(ctx, storage.OrchestrationPlanRequest{
		Assignment: assignment, Attempt: attempt, Binding: request.Binding, OperationID: request.OperationID, OperationDigest: request.OperationDigest,
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, "assign", "", storage.AssignmentPlanned, reason, request.ActorID, 0, now),
	})
}

func (service ControlService) Claim(ctx context.Context, request ClaimRequest) (storage.OrchestrationWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if !validIDs(request.AssignmentID, request.AttemptID, request.LeaseID, request.OwnerNodeID, request.OwnerRuntimeID, request.OperationID, request.AuditID, request.ActorID) || !validDigests(request.FencingDigest, request.OperationDigest) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	duration, err := service.leaseDuration()
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	lease := storage.OrchestrationLease{
		LeaseID: request.LeaseID, AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Generation: 1,
		OwnerNodeID: request.OwnerNodeID, OwnerRuntime: request.OwnerRuntimeID, FencingDigest: request.FencingDigest,
		State: storage.LeaseActive, AcquiredAt: now, HeartbeatAt: now, ExpiresAt: now.Add(duration),
	}
	return service.Store.ClaimAssignment(ctx, storage.OrchestrationClaimRequest{
		AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, ExpectedState: storage.AssignmentPlanned, Lease: lease,
		OperationID: request.OperationID, OperationDigest: request.OperationDigest,
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, "claim", storage.AssignmentPlanned, storage.AssignmentLeased, "leased", request.ActorID, 1, now),
	})
}

func (service ControlService) BindResources(ctx context.Context, request BindResourcesRequest) (storage.OrchestrationWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if request.LeaseGeneration < 1 || request.WorkspaceGeneration < 1 || !validIDs(request.AssignmentID, request.AttemptID, request.RuntimeSessionID, request.WorkspaceID, request.OperationID, request.AuditID, request.ActorID) || !validDigests(request.FencingDigest, request.RuntimeResumeKey, request.OperationDigest) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	result, err := service.Store.BindAttemptResources(ctx, storage.OrchestrationResourceRequest{
		AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, ExpectedState: storage.AssignmentLeased,
		LeaseGeneration: request.LeaseGeneration, FencingDigest: request.FencingDigest,
		Resources:   storage.OrchestrationResourceBinding{AttemptID: request.AttemptID, LeaseGeneration: request.LeaseGeneration, RuntimeSessionID: request.RuntimeSessionID, RuntimeResumeKey: request.RuntimeResumeKey, RuntimeState: "preparing", WorkspaceID: request.WorkspaceID, WorkspaceGeneration: request.WorkspaceGeneration, WorkspaceState: "allocated", UpdatedAt: now},
		OperationID: request.OperationID, OperationDigest: request.OperationDigest,
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, "prepare", storage.AssignmentLeased, storage.AssignmentPreparing, "resources_bound", request.ActorID, request.LeaseGeneration, now),
	})
	return result, fenceError(err)
}

func (service ControlService) Transition(ctx context.Context, request TransitionRequest) (storage.OrchestrationWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if request.LeaseGeneration < 1 || !validIDs(request.AssignmentID, request.AttemptID, request.OperationID, request.AuditID, request.ActorID) || !validDigests(request.FencingDigest, request.OperationDigest) || (request.FailureCode != "" && !validID(request.FailureCode)) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	current, err := service.Store.GetAssignment(ctx, request.AssignmentID)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if current.Attempt.AttemptID != request.AttemptID || current.Attempt.LeaseGeneration != request.LeaseGeneration {
		return storage.OrchestrationWriteResult{}, ErrStaleFence
	}
	if err := ReduceAssignmentState(current.Attempt.State, request.TargetState); err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	recovery := request.Recovery
	if recovery == "" {
		recovery = storage.RecoveryNone
	}
	reason := request.FailureCode
	if reason == "" {
		reason = string(request.TargetState)
	}
	result, err := service.Store.TransitionAssignment(ctx, storage.OrchestrationTransitionRequest{
		AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, ExpectedState: current.Attempt.State, TargetState: request.TargetState,
		LeaseGeneration: request.LeaseGeneration, FencingDigest: request.FencingDigest, FailureCode: request.FailureCode, Recovery: recovery,
		OperationID: request.OperationID, OperationDigest: request.OperationDigest, UpdatedAt: now,
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, transitionAction(current.Attempt.State, request.TargetState), current.Attempt.State, request.TargetState, reason, request.ActorID, request.LeaseGeneration, now),
	})
	return result, fenceError(err)
}

func (service ControlService) Heartbeat(ctx context.Context, request HeartbeatRequest) (storage.OrchestrationSnapshot, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationSnapshot{}, err
	}
	if request.LeaseGeneration < 1 || request.ExtendBy <= 0 || request.ExtendBy > maxLeaseDuration || !validIDs(request.AssignmentID, request.AttemptID, request.LeaseID) || !validDigest(request.FencingDigest) {
		return storage.OrchestrationSnapshot{}, ErrInvalidControl
	}
	snapshot, err := service.Store.HeartbeatLease(ctx, storage.OrchestrationHeartbeatRequest{
		AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, LeaseID: request.LeaseID, LeaseGeneration: request.LeaseGeneration,
		FencingDigest: request.FencingDigest, HeartbeatAt: now, ExpiresAt: now.Add(request.ExtendBy),
	})
	return snapshot, fenceError(err)
}

func (service ControlService) Retry(ctx context.Context, request RetryRequest) (storage.OrchestrationWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	if !validIDs(request.AssignmentID, request.PreviousAttemptID, request.AttemptID, request.OperationID, request.AuditID, request.ActorID) || !validDigests(request.IdempotencyDigest, request.OperationDigest) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	current, err := service.Store.GetAssignment(ctx, request.AssignmentID)
	if err != nil {
		return storage.OrchestrationWriteResult{}, err
	}
	replayCandidate := current.Attempt.AttemptID == request.AttemptID
	if !replayCandidate && (current.Attempt.AttemptID != request.PreviousAttemptID || (current.Attempt.State != storage.AssignmentFailed && current.Attempt.State != storage.AssignmentCanceled && current.Attempt.State != storage.AssignmentExpired)) {
		return storage.OrchestrationWriteResult{}, ErrInvalidTransition
	}
	if !replayCandidate && current.Attempt.IdempotencyDigest == request.IdempotencyDigest {
		return storage.OrchestrationWriteResult{}, fmt.Errorf("%w: retry must use a new idempotency digest", storage.ErrConflict)
	}
	workerID := request.WorkerID
	if workerID == "" {
		workerID = current.Assignment.WorkerID
	}
	if !validID(workerID) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	action, reason := request.Action, request.ReasonCode
	if action == "" {
		action = "retry"
	}
	if reason == "" {
		reason = "operator_retry"
	}
	if (action != "retry" && action != "reassign") || !validID(reason) {
		return storage.OrchestrationWriteResult{}, ErrInvalidControl
	}
	attemptNumber := current.Attempt.AttemptNumber + 1
	if replayCandidate {
		attemptNumber = current.Attempt.AttemptNumber
	}
	attempt := storage.OrchestrationAttempt{
		AttemptID: request.AttemptID, AssignmentID: request.AssignmentID, ProjectID: current.Assignment.ProjectID,
		WorkPackageID: current.Assignment.WorkPackageID, ExecutionID: current.Assignment.ExecutionID, AttemptNumber: attemptNumber,
		SupersedesAttemptID: request.PreviousAttemptID, State: storage.AssignmentPlanned, IdempotencyDigest: request.IdempotencyDigest,
		RecoveryDisposition: storage.RecoveryNone, CreatedAt: now, UpdatedAt: now,
	}
	return service.Store.RetryAssignment(ctx, storage.OrchestrationRetryRequest{
		AssignmentID: request.AssignmentID, PreviousAttemptID: request.PreviousAttemptID, WorkerID: workerID, Attempt: attempt,
		OperationID: request.OperationID, OperationDigest: request.OperationDigest,
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, action, current.Attempt.State, storage.AssignmentPlanned, reason, request.ActorID, 0, now),
	})
}

func (service ControlService) ListAudit(ctx context.Context, assignmentID string) ([]storage.OrchestrationAuditEvent, error) {
	if _, err := service.ready(ctx); err != nil || !validID(assignmentID) {
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidControl
	}
	return service.Store.ListOrchestrationAudit(ctx, assignmentID)
}

func (service ControlService) Reconcile(ctx context.Context, actorID string) ([]storage.OrchestrationSnapshot, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return nil, err
	}
	if !validID(actorID) {
		return nil, ErrInvalidControl
	}
	return service.Store.ReconcileAssignments(ctx, now, actorID)
}

func (service ControlService) GetAssignment(ctx context.Context, assignmentID string) (storage.OrchestrationSnapshot, error) {
	if _, err := service.ready(ctx); err != nil || !validID(assignmentID) {
		if err != nil {
			return storage.OrchestrationSnapshot{}, err
		}
		return storage.OrchestrationSnapshot{}, ErrInvalidControl
	}
	return service.Store.GetAssignment(ctx, assignmentID)
}

func (service ControlService) RecordGateStatus(ctx context.Context, request GateStatusRequest) (storage.RegistryWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if request.GateVersion < 1 || !validIDs(request.AssignmentID, request.AttemptID, request.GateID, request.ReasonCode, request.AuditID, request.ActorID) || !validDigest(request.GateDigest) ||
		(request.EvidenceID != "" && !validID(request.EvidenceID)) || !validGateState(request.Status) {
		return storage.RegistryWriteResult{}, ErrInvalidControl
	}
	snapshot, err := service.Store.GetAssignment(ctx, request.AssignmentID)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if snapshot.Attempt.AttemptID != request.AttemptID {
		return storage.RegistryWriteResult{}, ErrStaleFence
	}
	return service.Store.SaveGateStatus(ctx, storage.OrchestrationGateRequest{
		Gate: storage.OrchestrationGateStatus{
			AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, GateID: request.GateID, GateVersion: request.GateVersion,
			GateDigest: request.GateDigest, Status: request.Status, EvidenceID: request.EvidenceID, ReasonCode: request.ReasonCode, UpdatedAt: now,
		},
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, "gate", snapshot.Attempt.State, snapshot.Attempt.State, request.ReasonCode, request.ActorID, snapshot.Attempt.LeaseGeneration, now),
	})
}

func (service ControlService) RecordOperatorDecision(ctx context.Context, request OperatorDecisionRequest) (storage.RegistryWriteResult, error) {
	now, err := service.ready(ctx)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if !validIDs(request.DecisionID, request.AssignmentID, request.AttemptID, request.Decision, request.ReasonCode, request.ActorID, request.AuditID) ||
		!validDigest(request.IdempotencyDigest) || !validOperatorDecision(request.Decision) {
		return storage.RegistryWriteResult{}, ErrInvalidControl
	}
	snapshot, err := service.Store.GetAssignment(ctx, request.AssignmentID)
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if snapshot.Attempt.AttemptID != request.AttemptID {
		return storage.RegistryWriteResult{}, ErrStaleFence
	}
	return service.Store.SaveOperatorDecision(ctx, storage.OrchestrationDecisionRequest{
		Decision: storage.OrchestrationOperatorDecision{
			DecisionID: request.DecisionID, AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Decision: request.Decision,
			ReasonCode: request.ReasonCode, ActorID: request.ActorID, IdempotencyDigest: request.IdempotencyDigest, DecidedAt: now,
		},
		Audit: audit(request.AuditID, request.AssignmentID, request.AttemptID, "operator_"+request.Decision, snapshot.Attempt.State, snapshot.Attempt.State, request.ReasonCode, request.ActorID, snapshot.Attempt.LeaseGeneration, now),
	})
}

func (service ControlService) ready(ctx context.Context) (time.Time, error) {
	if service.Store == nil || ctx == nil {
		return time.Time{}, ErrInvalidControl
	}
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	return now, nil
}

func (service ControlService) leaseDuration() (time.Duration, error) {
	if service.LeaseDuration < 0 || service.LeaseDuration > maxLeaseDuration {
		return 0, ErrInvalidControl
	}
	if service.LeaseDuration > 0 {
		return service.LeaseDuration, nil
	}
	return defaultLeaseDuration, nil
}

func validatePlan(request PlanRequest) error {
	if request.TaskRevision < 1 || request.GraphRevision < 1 || request.ContractVersion < 1 ||
		!validIDs(request.AssignmentID, request.AttemptID, request.ProjectID, request.TaskID, request.WorkPackageID, request.ExecutionID, request.ContractID, request.WorkerID, request.NodeID, request.OperationID, request.AuditID, request.ActorID) ||
		!validDigests(request.ContractDigest, request.IdempotencyDigest, request.OperationDigest) || (request.AssignmentReason != "" && !validID(request.AssignmentReason)) ||
		!validAttemptBinding(request.Binding, request.AttemptID, request.ContractID, request.ContractVersion, request.ContractDigest) {
		return ErrInvalidControl
	}
	return nil
}

func validAttemptBinding(binding storage.OrchestrationAttemptBinding, attemptID, contractID string, contractVersion int64, contractDigest string) bool {
	return binding.AttemptID == attemptID && binding.ContractID == contractID && binding.ContractVersion == contractVersion && binding.ContractDigest == contractDigest &&
		validDigest(binding.ContextDigest) && validID(binding.ContextCompilerVersion) && validDigest(binding.BindingDigest) && len(binding.BindingJSON) > 0 && len(binding.BindingJSON) <= storage.MaxProjectProjectionPayloadBytes && !binding.CreatedAt.IsZero()
}

func audit(id, assignmentID, attemptID, action string, from, to storage.AssignmentState, reason, actor string, generation int64, at time.Time) storage.OrchestrationAuditEvent {
	return storage.OrchestrationAuditEvent{AuditID: id, AssignmentID: assignmentID, AttemptID: attemptID, Action: action, FromState: from, ToState: to, ReasonCode: reason, ActorID: actor, LeaseGeneration: generation, OccurredAt: at}
}

func transitionAction(current, target storage.AssignmentState) string {
	switch target {
	case storage.AssignmentRunning:
		if current == storage.AssignmentPaused {
			return "resume"
		}
		return "start"
	case storage.AssignmentPaused:
		return "pause"
	case storage.AssignmentCanceled:
		return "cancel"
	case storage.AssignmentExpired:
		return "timeout"
	case storage.AssignmentAccepted:
		return "gate_decision"
	default:
		return string(target)
	}
}

func validIDs(values ...string) bool {
	for _, value := range values {
		if !validID(value) {
			return false
		}
	}
	return true
}

func validID(value string) bool { return controlIdentifier.MatchString(value) }

func validDigests(values ...string) bool {
	for _, value := range values {
		if !validDigest(value) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func validGateState(state storage.GateState) bool {
	return state == storage.GatePending || state == storage.GateSatisfied || state == storage.GateFailed || state == storage.GateWaived
}

func validOperatorDecision(decision string) bool {
	switch decision {
	case "cancel", "retry", "accept", "gate_waive", "reconcile":
		return true
	default:
		return false
	}
}

func fenceError(err error) error {
	if errors.Is(err, storage.ErrConflict) {
		return fmt.Errorf("%w: %v", ErrStaleFence, err)
	}
	return err
}
