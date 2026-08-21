// Package workhistory is the supported local application boundary for writing
// portable Agent Project work history. Callers provide typed intent, never
// canonical record paths.
package workhistory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"syncgate/internal/project"
	"syncgate/internal/projector"
	"syncgate/internal/storage"
)

const MaxStateEvents = 10_000

var (
	ErrInvalidRequest    = errors.New("invalid work-history request")
	ErrInvalidTransition = errors.New("invalid work-history transition")
	ErrStateNotFound     = errors.New("work-history state not found")
)

type Metadata struct {
	RootPath           string
	IdempotencyKey     string
	OccurredAt         time.Time
	Producer           project.Producer
	Correlation        *project.Correlation
	CausationID        string
	ProjectVersion     string
	ContextVersion     string
	InstructionVersion string
}

type RecordResult struct {
	RecordID       string
	RelativePath   string
	Created        bool
	AlreadyPresent bool
}

type OperationResult struct {
	ProjectID string
	Records   []RecordResult
}

// RecoverableProjectionError means canonical portable records are durable and
// only the rebuildable SQLite projection needs to be retried.
type RecoverableProjectionError struct{ Err error }

func (err *RecoverableProjectionError) Error() string {
	return "canonical work history saved; local projection requires recovery"
}
func (err *RecoverableProjectionError) Unwrap() error { return err.Err }

type CreateWorkPackageRequest struct {
	Metadata
	WorkPackageID      string
	Objective          string
	Trade              string
	Specialization     string
	Scope              project.WorkScope
	Dependencies       []string
	Deliverables       []string
	AcceptanceCriteria []string
	ReviewRequired     bool
	TradeReference     *project.RegistryReference
}

type TaskWorkPackageRequest struct {
	WorkPackageID      string
	Objective          string
	Trade              string
	Specialization     string
	Scope              project.WorkScope
	Dependencies       []string
	Deliverables       []string
	AcceptanceCriteria []string
	ReviewRequired     bool
	Priority           project.TaskPriority
	Risk               []project.RiskDimension
	Resources          *project.ResourceConstraints
	QualityGates       []project.QualityGateReference
	TradeReference     *project.RegistryReference
}

type CreateTaskRequest struct {
	Metadata
	TaskID        string
	TaskRevision  int64
	GraphRevision int64
	Objective     string
	Priority      project.TaskPriority
	Risk          []project.RiskDimension
	Resources     *project.ResourceConstraints
	QualityGates  []project.QualityGateReference
	Barriers      []string
	WorkPackages  []TaskWorkPackageRequest
}

type TransitionWorkPackageRequest struct {
	Metadata
	WorkPackageID string
	From          project.WorkPackageState
	To            project.WorkPackageState
	ReasonCode    string
}

type StartExecutionRequest struct {
	Metadata
	WorkPackageID     string
	ExecutionID       string
	TradeReference    *project.RegistryReference
	WorkerReference   *project.RegistryReference
	ContractReference *project.RegistryReference
}

type ExecutionRequest struct {
	Metadata
	WorkPackageID string
	ExecutionID   string
}

type PauseExecutionRequest struct {
	ExecutionRequest
	ReasonCode string
}

type FailExecutionRequest struct {
	ExecutionRequest
	FailureCode string
	Summary     string
}

type CompleteExecutionRequest struct {
	ExecutionRequest
	Summary string
}

type RecordTestRequest struct {
	ExecutionRequest
	Name                 string
	Outcome              project.TestOutcome
	DurationMilliseconds int64
}

// RecordTelemetryRequest accepts only the already allowlisted portable
// telemetry summary. Detailed local intake evidence is never accepted here.
type RecordTelemetryRequest struct {
	ExecutionRequest
	Summary project.TelemetrySummaryPayload
}

type RecordReviewRequest struct {
	Metadata
	WorkPackageID string
	ExecutionID   string
	Outcome       project.ReviewOutcome
	ReviewerID    string
	Summary       string
}

type CreateHandoffRequest struct {
	ExecutionRequest
	HandoffID                 string
	CompletedWork             []string
	ChangedFiles              []string
	Decisions                 []string
	Tests                     []project.TestResult
	Limitations               []string
	UnresolvedIssues          []string
	Assumptions               []string
	FollowUpWork              []string
	ReviewRequirements        []string
	IntegrationConsiderations []string
	Confidence                project.Confidence
	FailureConditions         []string
}

type RegisterArtifactRequest struct {
	Metadata
	ArtifactID         string
	WorkPackageID      string
	ExecutionID        string
	Name               string
	MediaType          string
	SourceRelativePath string
	EmbedBlob          bool
	SourceArtifactIDs  []string
}

type AcceptWorkRequest struct {
	Metadata
	WorkPackageID string
	AcceptedBy    string
	Summary       string
}

type ProjectFunc func(context.Context, string, projector.Trigger) (projector.Report, error)

type Service struct {
	Store     storage.Store
	Projector *projector.Projector
	Project   ProjectFunc
	locks     sync.Map
}

func New(store storage.Store, projection *projector.Projector) (*Service, error) {
	if store == nil || projection == nil {
		return nil, fmt.Errorf("%w: storage and projector are required", ErrInvalidRequest)
	}
	return &Service{Store: store, Projector: projection}, nil
}
