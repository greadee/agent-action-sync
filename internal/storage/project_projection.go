package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"syncgate/internal/core"
)

const (
	MaxProjectProjectionPayloadBytes = 1 << 20
	MaxProjectProjectionPathBytes    = 4096
	MaxProjectProjectionFilterBytes  = 256
)

var ErrConflict = errors.New("storage record conflict")

type ProjectRegistration struct {
	ProjectID          string
	ShareID            core.ShareID
	RootPath           string
	Name               string
	AuthorityDeviceID  core.DeviceID
	ManifestRecordID   string
	ManifestRecordHash string
	ManifestPath       string
	RegisteredAt       time.Time
}

type ProjectRegistrationResult struct {
	AlreadyPresent bool
}

type ProjectEventProjection struct {
	ProjectID        string
	EventID          string
	RecordHash       string
	EventType        string
	OccurredAt       time.Time
	WorkPackageID    string
	ExecutionID      string
	ProducerWorkerID string
	ProducerDeviceID core.DeviceID
	ProducerTrade    string
	ProducerProvider string
	ProducerModel    string
	Status           string
	RecordPath       string
	PayloadJSON      []byte
}

type ProjectEventProjectionResult struct {
	AlreadyPresent bool
}

type ProjectEventQuery struct {
	Page          PageRequest
	ProjectID     string
	EventType     string
	WorkPackageID string
	ExecutionID   string
}

type ProjectArtifactProjection struct {
	ProjectID        string
	ArtifactID       string
	RecordID         string
	RecordHash       string
	Name             string
	MediaType        string
	Size             int64
	ContentHash      string
	BlobPath         string
	WorkPackageID    string
	ExecutionID      string
	ProducerWorkerID string
	ProducerDeviceID core.DeviceID
	ProducerModel    string
	CreatedAt        time.Time
	RecordPath       string
}

type ProjectArtifactProjectionResult struct {
	AlreadyPresent bool
}

type ProjectArtifactQuery struct {
	Page          PageRequest
	ProjectID     string
	WorkPackageID string
	ExecutionID   string
	MediaType     string
}

const MaxProjectInsightValueBytes = 1 << 20

type ProjectInsightProjection struct {
	ProjectID            string
	Scope                string
	MetricName           string
	DefinitionVersion    int
	SourceEventWatermark string
	WindowStart          *time.Time
	WindowEnd            *time.Time
	ValueJSON            []byte
	SampleCount          int64
	Completeness         string
	Evidence             string
	CalculatedAt         time.Time
}

type ProjectInsightQuery struct {
	ProjectID  string
	Scope      string
	MetricName string
}

type ProjectProjectionCheckpoint struct {
	ProjectID      string
	Stream         string
	LastRecordPath string
	LastRecordHash string
	UpdatedAt      time.Time
}

type ProjectProjectionRejection struct {
	ProjectID      string
	RecordPath     string
	ObservedHash   string
	ReasonCode     string
	QuarantinePath string
	RejectedAt     time.Time
}

type ProjectRegistrationStore interface {
	RegisterProject(ctx context.Context, registration ProjectRegistration) (ProjectRegistrationResult, error)
	GetProject(ctx context.Context, projectID string) (ProjectRegistration, error)
}

type ProjectEventStore interface {
	SaveProjectEvent(ctx context.Context, event ProjectEventProjection) (ProjectEventProjectionResult, error)
	GetProjectEvent(ctx context.Context, projectID, eventID string) (ProjectEventProjection, error)
	ListProjectEvents(ctx context.Context, query ProjectEventQuery) (Page[ProjectEventProjection], error)
}

type ProjectArtifactStore interface {
	SaveProjectArtifact(ctx context.Context, artifact ProjectArtifactProjection) (ProjectArtifactProjectionResult, error)
	GetProjectArtifact(ctx context.Context, projectID, artifactID string) (ProjectArtifactProjection, error)
	ListProjectArtifacts(ctx context.Context, query ProjectArtifactQuery) (Page[ProjectArtifactProjection], error)
}

type ProjectCheckpointStore interface {
	SetProjectCheckpoint(ctx context.Context, checkpoint ProjectProjectionCheckpoint) error
	GetProjectCheckpoint(ctx context.Context, projectID, stream string) (ProjectProjectionCheckpoint, error)
}

type ProjectRejectionStore interface {
	RecordProjectRejection(ctx context.Context, rejection ProjectProjectionRejection) error
}

type ProjectInsightStore interface {
	GetProjectInsight(ctx context.Context, projectID, scope, metricName string, definitionVersion int) (ProjectInsightProjection, error)
	ListProjectInsights(ctx context.Context, query ProjectInsightQuery) ([]ProjectInsightProjection, error)
}

type ProjectInsightProjectionStore interface {
	ReplaceProjectInsights(ctx context.Context, projectID string, replace func(ProjectInsightWriter) error) error
}

type ProjectInsightWriter interface {
	SaveProjectInsight(ctx context.Context, insight ProjectInsightProjection) error
}

type ProjectProjectionWriter interface {
	ProjectEventStore
	ProjectArtifactStore
	ProjectCheckpointStore
	ProjectRejectionStore
}

type ProjectProjectionStore interface {
	ApplyProjectProjection(ctx context.Context, projectID string, apply func(ProjectProjectionWriter) error) error
	ClearProjectProjection(ctx context.Context, projectID string) error
	RebuildProjectProjection(ctx context.Context, projectID string, rebuild func(ProjectProjectionWriter) error) error
}

func ValidateProjectProjectionID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > MaxProjectProjectionFilterBytes {
		return errors.New("project projection identifier is required and must be at most 256 bytes")
	}
	return nil
}

func ValidateProjectProjectionPath(value string, required bool) error {
	if value == "" && !required {
		return nil
	}
	if strings.TrimSpace(value) == "" || len(value) > MaxProjectProjectionPathBytes || strings.ContainsRune(value, 0) {
		return errors.New("project projection path is invalid")
	}
	if strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return errors.New("project projection path must be canonical and relative")
	}
	return nil
}

func ValidateProjectProjectionPayload(value []byte) error {
	if len(value) == 0 || len(value) > MaxProjectProjectionPayloadBytes || !json.Valid(value) {
		return errors.New("project event payload must be valid bounded JSON")
	}
	return nil
}

func ValidateProjectQueryFilter(value string) error {
	if len(value) > MaxProjectProjectionFilterBytes {
		return errors.New("project query filter is too long")
	}
	return nil
}
