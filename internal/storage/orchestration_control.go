package storage

import (
	"context"
	"time"
)

type AssignmentState string

const (
	AssignmentPlanned       AssignmentState = "planned"
	AssignmentLeased        AssignmentState = "leased"
	AssignmentPreparing     AssignmentState = "preparing"
	AssignmentRunning       AssignmentState = "running"
	AssignmentPaused        AssignmentState = "paused"
	AssignmentCollecting    AssignmentState = "collecting"
	AssignmentAwaitingGates AssignmentState = "awaiting_gates"
	AssignmentAccepted      AssignmentState = "accepted"
	AssignmentFailed        AssignmentState = "failed"
	AssignmentCanceled      AssignmentState = "canceled"
	AssignmentExpired       AssignmentState = "expired"
)

type RecoveryDisposition string

const (
	RecoveryNone          RecoveryDisposition = "none"
	RecoveryResume        RecoveryDisposition = "resume"
	RecoveryReconcile     RecoveryDisposition = "reconcile"
	RecoveryNeedsOperator RecoveryDisposition = "needs_operator"
)

type LeaseState string

const (
	LeaseActive   LeaseState = "active"
	LeaseReleased LeaseState = "released"
	LeaseExpired  LeaseState = "expired"
)

type GateState string

const (
	GatePending   GateState = "pending"
	GateSatisfied GateState = "satisfied"
	GateFailed    GateState = "failed"
	GateWaived    GateState = "waived"
)

type OrchestrationAssignment struct {
	AssignmentID        string
	ProjectID           string
	TaskID              string
	TaskRevision        int64
	GraphRevision       int64
	WorkPackageID       string
	ExecutionID         string
	ContractID          string
	ContractVersion     int64
	ContractDigest      string
	WorkerID            string
	NodeID              string
	State               AssignmentState
	CurrentAttemptID    string
	CurrentAttempt      int64
	IdempotencyDigest   string
	RecoveryDisposition RecoveryDisposition
	FailureCode         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type OrchestrationAttempt struct {
	AttemptID           string
	AssignmentID        string
	ProjectID           string
	WorkPackageID       string
	ExecutionID         string
	AttemptNumber       int64
	SupersedesAttemptID string
	State               AssignmentState
	LeaseGeneration     int64
	RuntimeSessionID    string
	WorkspaceID         string
	IdempotencyDigest   string
	RecoveryDisposition RecoveryDisposition
	FailureCode         string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type OrchestrationLease struct {
	LeaseID       string
	AssignmentID  string
	AttemptID     string
	Generation    int64
	OwnerNodeID   string
	OwnerRuntime  string
	FencingDigest string
	State         LeaseState
	AcquiredAt    time.Time
	HeartbeatAt   time.Time
	ExpiresAt     time.Time
	ReleasedAt    time.Time
}

type OrchestrationResourceBinding struct {
	AttemptID           string
	LeaseGeneration     int64
	RuntimeSessionID    string
	RuntimeResumeKey    string
	RuntimeState        string
	WorkspaceID         string
	WorkspaceGeneration int64
	WorkspaceState      string
	UpdatedAt           time.Time
}

// OrchestrationAttemptBinding records the immutable contract and compiled
// context provenance that authorized this attempt. BindingJSON is a bounded
// canonical metadata envelope containing IDs, digests, policies, and notices;
// it never contains the context body, credentials, or local paths.
type OrchestrationAttemptBinding struct {
	AttemptID              string
	ContractID             string
	ContractVersion        int64
	ContractDigest         string
	ContextDigest          string
	ContextCompilerVersion string
	BindingDigest          string
	BindingJSON            []byte
	CreatedAt              time.Time
}

type OrchestrationGateStatus struct {
	AssignmentID string
	AttemptID    string
	GateID       string
	GateVersion  int64
	GateDigest   string
	Status       GateState
	EvidenceID   string
	ReasonCode   string
	UpdatedAt    time.Time
}

type OrchestrationOperatorDecision struct {
	DecisionID        string
	AssignmentID      string
	AttemptID         string
	Decision          string
	ReasonCode        string
	ActorID           string
	IdempotencyDigest string
	DecidedAt         time.Time
}

type OrchestrationAuditEvent struct {
	AuditID         string
	AssignmentID    string
	AttemptID       string
	Action          string
	FromState       AssignmentState
	ToState         AssignmentState
	ReasonCode      string
	ActorID         string
	LeaseGeneration int64
	OccurredAt      time.Time
}

type OrchestrationSnapshot struct {
	Assignment OrchestrationAssignment
	Attempt    OrchestrationAttempt
	Lease      *OrchestrationLease
	Resources  *OrchestrationResourceBinding
	Binding    *OrchestrationAttemptBinding
	Gates      []OrchestrationGateStatus
	Decisions  []OrchestrationOperatorDecision
}

type OrchestrationWriteResult struct {
	AlreadyPresent bool
	Snapshot       OrchestrationSnapshot
}

type OrchestrationPlanRequest struct {
	Assignment      OrchestrationAssignment
	Attempt         OrchestrationAttempt
	Binding         OrchestrationAttemptBinding
	OperationID     string
	OperationDigest string
	Audit           OrchestrationAuditEvent
}

type OrchestrationClaimRequest struct {
	AssignmentID    string
	AttemptID       string
	ExpectedState   AssignmentState
	Lease           OrchestrationLease
	OperationID     string
	OperationDigest string
	Audit           OrchestrationAuditEvent
}

type OrchestrationTransitionRequest struct {
	AssignmentID    string
	AttemptID       string
	ExpectedState   AssignmentState
	TargetState     AssignmentState
	LeaseGeneration int64
	FencingDigest   string
	FailureCode     string
	Recovery        RecoveryDisposition
	OperationID     string
	OperationDigest string
	UpdatedAt       time.Time
	Audit           OrchestrationAuditEvent
}

type OrchestrationResourceRequest struct {
	AssignmentID    string
	AttemptID       string
	ExpectedState   AssignmentState
	LeaseGeneration int64
	FencingDigest   string
	Resources       OrchestrationResourceBinding
	OperationID     string
	OperationDigest string
	Audit           OrchestrationAuditEvent
}

type OrchestrationHeartbeatRequest struct {
	AssignmentID    string
	AttemptID       string
	LeaseID         string
	LeaseGeneration int64
	FencingDigest   string
	HeartbeatAt     time.Time
	ExpiresAt       time.Time
}

type OrchestrationRetryRequest struct {
	AssignmentID      string
	PreviousAttemptID string
	Attempt           OrchestrationAttempt
	OperationID       string
	OperationDigest   string
	Audit             OrchestrationAuditEvent
}

type OrchestrationGateRequest struct {
	Gate  OrchestrationGateStatus
	Audit OrchestrationAuditEvent
}

type OrchestrationDecisionRequest struct {
	Decision OrchestrationOperatorDecision
	Audit    OrchestrationAuditEvent
}

type OrchestrationControlStore interface {
	PlanAssignment(context.Context, OrchestrationPlanRequest) (OrchestrationWriteResult, error)
	ClaimAssignment(context.Context, OrchestrationClaimRequest) (OrchestrationWriteResult, error)
	BindAttemptResources(context.Context, OrchestrationResourceRequest) (OrchestrationWriteResult, error)
	TransitionAssignment(context.Context, OrchestrationTransitionRequest) (OrchestrationWriteResult, error)
	HeartbeatLease(context.Context, OrchestrationHeartbeatRequest) (OrchestrationSnapshot, error)
	RetryAssignment(context.Context, OrchestrationRetryRequest) (OrchestrationWriteResult, error)
	ReconcileAssignments(context.Context, time.Time, string) ([]OrchestrationSnapshot, error)
	GetAssignment(context.Context, string) (OrchestrationSnapshot, error)
	SaveGateStatus(context.Context, OrchestrationGateRequest) (RegistryWriteResult, error)
	SaveOperatorDecision(context.Context, OrchestrationDecisionRequest) (RegistryWriteResult, error)
	ListOrchestrationAudit(context.Context, string) ([]OrchestrationAuditEvent, error)
}
