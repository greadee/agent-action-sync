package storage

import (
	"context"
	"time"
)

// RegistryLifecycle is immutable version metadata. A lifecycle change is a new
// version; existing versions remain available for historical references.
type RegistryLifecycle string

const (
	RegistryActive     RegistryLifecycle = "active"
	RegistryDeprecated RegistryLifecycle = "deprecated"
	RegistryDisabled   RegistryLifecycle = "disabled"
)

type RegistryEvidence struct {
	Source       string
	ObservedAt   time.Time
	Score        *float64
	SampleCount  int64
	EvidenceNote string
}

type TradeDefinition struct {
	TradeID              string
	Version              int64
	Name                 string
	Lifecycle            RegistryLifecycle
	CapabilityTags       []string
	RequiredCapabilities []string
	OptionalCapabilities []string
	Description          string
	Evidence             RegistryEvidence
	ContentHash          string
	CreatedAt            time.Time
}

type WorkerProfile struct {
	WorkerID       string
	Version        int64
	Name           string
	Lifecycle      RegistryLifecycle
	TradeID        string
	TradeVersion   int64
	InstructionID  string
	InstructionVer int64
	RuntimeID      string
	RuntimeVersion int64
	Provider       string
	Model          string
	ModelVersion   string
	ToolPolicyID   string
	ToolPolicyVer  int64
	CapabilityTags []string
	Evidence       RegistryEvidence
	ContentHash    string
	CreatedAt      time.Time
}

type ProjectTradeAdaptation struct {
	ProjectID            string
	AdaptationID         string
	Version              int64
	TradeID              string
	TradeVersion         int64
	Lifecycle            RegistryLifecycle
	RequiredCapabilities []string
	OptionalCapabilities []string
	Notes                string
	ContentHash          string
	CreatedAt            time.Time
}

type RegistryAuditEvent struct {
	AuditID     string
	Action      string
	SubjectKind string
	SubjectID   string
	SubjectVer  int64
	ProjectID   string
	ActorID     string
	ContentHash string
	OccurredAt  time.Time
}

type TradeQuery struct {
	Page      PageRequest
	TradeID   string
	Lifecycle RegistryLifecycle
}
type WorkerQuery struct {
	Page      PageRequest
	WorkerID  string
	TradeID   string
	Lifecycle RegistryLifecycle
}
type ProjectAdaptationQuery struct {
	Page      PageRequest
	ProjectID string
	TradeID   string
}
type RegistryAuditQuery struct {
	Page      PageRequest
	ProjectID string
	SubjectID string
}

type RegistryWriteResult struct{ AlreadyPresent bool }

type RegistryStore interface {
	SaveTradeDefinition(context.Context, TradeDefinition) (RegistryWriteResult, error)
	GetTradeDefinition(context.Context, string, int64) (TradeDefinition, error)
	ListTradeDefinitions(context.Context, TradeQuery) (Page[TradeDefinition], error)
	SaveWorkerProfile(context.Context, WorkerProfile) (RegistryWriteResult, error)
	GetWorkerProfile(context.Context, string, int64) (WorkerProfile, error)
	ListWorkerProfiles(context.Context, WorkerQuery) (Page[WorkerProfile], error)
	SaveProjectTradeAdaptation(context.Context, ProjectTradeAdaptation) (RegistryWriteResult, error)
	GetProjectTradeAdaptation(context.Context, string, string, int64) (ProjectTradeAdaptation, error)
	ListProjectTradeAdaptations(context.Context, ProjectAdaptationQuery) (Page[ProjectTradeAdaptation], error)
	RecordRegistryAudit(context.Context, RegistryAuditEvent) error
	ListRegistryAudit(context.Context, RegistryAuditQuery) (Page[RegistryAuditEvent], error)
}
