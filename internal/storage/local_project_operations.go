package storage

import (
	"context"
	"time"
)

const MaxLocalOperatorResultBytes = 64 << 10

// LocalIntegrationSummary keeps the authority-local decision evidence needed
// to re-open a bounded integration decision after the desktop process restarts.
// Its JSON is never returned directly to browser clients.
type LocalIntegrationSummary struct {
	AssignmentID  string
	AttemptID     string
	ProjectID     string
	SummaryDigest string
	SummaryJSON   []byte
	RecordedAt    time.Time
}

// LocalProjectPolicy is authority-local control state. It is deliberately not
// part of portable project history and contains no paths or project content.
type LocalProjectPolicy struct {
	ProjectID         string
	SchedulingEnabled bool
	MaxConcurrent     int
	UpdatedAt         time.Time
}

type LocalProjectSelection struct {
	ProjectID  string
	SelectedAt time.Time
}

type ProjectOrchestrationStatus struct {
	AssignmentCounts map[AssignmentState]int64
	GateCounts       map[GateState]int64
}

// LocalOperatorOperation is the authority-local replay ledger for explicit
// browser and CLI controls. ResultJSON contains only the sanitized API result;
// it never contains runtime handles, paths, prompts, logs, or artifact bytes.
type LocalOperatorOperation struct {
	IdempotencyKey string
	Fingerprint    string
	Action         string
	ProjectID      string
	SubjectID      string
	ResultJSON     []byte
	OccurredAt     time.Time
}

type LocalOperatorOperationResult struct {
	AlreadyPresent bool
	Operation      LocalOperatorOperation
}

// LocalProjectOperationsStore owns machine-local selection and policy. A
// single-row selection prevents multiple projects from sharing scheduler
// authority on one desktop node.
type LocalProjectOperationsStore interface {
	SetLocalProjectPolicy(context.Context, LocalProjectPolicy) error
	GetLocalProjectPolicy(context.Context, string) (LocalProjectPolicy, error)
	SelectLocalProject(context.Context, LocalProjectSelection) error
	GetSelectedLocalProject(context.Context) (LocalProjectSelection, error)
	GetProjectOrchestrationStatus(context.Context, string) (ProjectOrchestrationStatus, error)
	SaveLocalOperatorOperation(context.Context, LocalOperatorOperation) (LocalOperatorOperationResult, error)
	GetLocalOperatorOperation(context.Context, string) (LocalOperatorOperation, error)
	SaveLocalIntegrationSummary(context.Context, LocalIntegrationSummary) error
	GetLocalIntegrationSummary(context.Context, string) (LocalIntegrationSummary, error)
}
