package storage

import (
	"context"
	"time"
)

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

// LocalProjectOperationsStore owns machine-local selection and policy. A
// single-row selection prevents multiple projects from sharing scheduler
// authority on one desktop node.
type LocalProjectOperationsStore interface {
	SetLocalProjectPolicy(context.Context, LocalProjectPolicy) error
	GetLocalProjectPolicy(context.Context, string) (LocalProjectPolicy, error)
	SelectLocalProject(context.Context, LocalProjectSelection) error
	GetSelectedLocalProject(context.Context) (LocalProjectSelection, error)
	GetProjectOrchestrationStatus(context.Context, string) (ProjectOrchestrationStatus, error)
}
