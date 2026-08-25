package storage

import (
	"context"
	"errors"
	"time"
)

type ProjectTaskProjection struct {
	ProjectID       string
	TaskID          string
	TaskRevision    int64
	GraphRevision   int64
	TaskRecordID    string
	TaskRecordHash  string
	TaskRecordPath  string
	GraphRecordID   string
	GraphRecordHash string
	GraphRecordPath string
	Objective       string
	Priority        string
	State           string
	ExplanationCode string
	EventWatermark  string
	ParallelReady   []string
	CreatedAt       time.Time
}

type ProjectTaskQuery struct {
	Page      PageRequest
	ProjectID string
	TaskID    string
}

type ProjectTaskNodeProjection struct {
	ProjectID          string
	TaskID             string
	TaskRevision       int64
	GraphRevision      int64
	WorkPackageID      string
	DefinitionRecordID string
	DefinitionHash     string
	DefinitionPath     string
	CanonicalState     string
	Readiness          string
	ExplanationCode    string
	Dependencies       []string
	Barrier            bool
}

type ProjectTaskNodeQuery struct {
	Page          PageRequest
	ProjectID     string
	TaskID        string
	TaskRevision  int64
	GraphRevision int64
	Readiness     string
}

type ProjectTaskProjectionResult struct {
	AlreadyPresent bool
}

type ProjectTaskStore interface {
	SaveProjectTask(ctx context.Context, task ProjectTaskProjection) (ProjectTaskProjectionResult, error)
	GetProjectTask(ctx context.Context, projectID, taskID string, taskRevision int64) (ProjectTaskProjection, error)
	ListProjectTasks(ctx context.Context, query ProjectTaskQuery) (Page[ProjectTaskProjection], error)
}

type ProjectTaskNodeStore interface {
	SaveProjectTaskNode(ctx context.Context, node ProjectTaskNodeProjection) (ProjectTaskProjectionResult, error)
	ListProjectTaskNodes(ctx context.Context, query ProjectTaskNodeQuery) (Page[ProjectTaskNodeProjection], error)
}

func ValidateTaskProjectionState(value string) error {
	switch value {
	case "planned", "waiting", "ready", "blocked", "review", "accepted", "failed", "canceled":
		return nil
	default:
		return errors.New("task projection state is unsupported")
	}
}
