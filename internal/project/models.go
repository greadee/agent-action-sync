// Package project defines the portable Agent Project contracts and validation
// rules. Filesystem publication, local projection, and ingestion are composed
// in later layers; this package does not depend on sync or SQLite.
package project

import (
	"encoding/json"
	"time"
)

const (
	SchemaFamily        = "syncgate.agent-project"
	SchemaMajor         = 1
	SchemaMinor         = 0
	HashAlgorithmSHA256 = "sha256"

	MaxRecordBytes       = 1 << 20
	MaxIdentifierBytes   = 128
	MaxNameBytes         = 256
	MaxMediaTypeBytes    = 256
	MaxShortTextBytes    = 1024
	MaxTextBytes         = 8192
	MaxListItems         = 256
	MaxRelativePathBytes = 4096
)

type RecordKind string

const (
	RecordProjectManifest RecordKind = "project_manifest"
	RecordWorkPackage     RecordKind = "work_package_definition"
	RecordExecution       RecordKind = "execution_manifest"
	RecordWorkEvent       RecordKind = "work_event"
	RecordHandoff         RecordKind = "handoff"
	RecordArtifact        RecordKind = "artifact_manifest"
)

type SchemaVersion struct {
	Family string `json:"family"`
	Major  int    `json:"major"`
	Minor  int    `json:"minor"`
}

func CurrentSchemaVersion() SchemaVersion {
	return SchemaVersion{Family: SchemaFamily, Major: SchemaMajor, Minor: SchemaMinor}
}

func NewRecordHeader(kind RecordKind, recordID, projectID string) RecordHeader {
	return RecordHeader{
		Schema:     CurrentSchemaVersion(),
		RecordKind: kind,
		RecordID:   recordID,
		ProjectID:  projectID,
	}
}

type Integrity struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

type RecordHeader struct {
	Schema     SchemaVersion `json:"schema"`
	RecordKind RecordKind    `json:"record_kind"`
	RecordID   string        `json:"record_id"`
	ProjectID  string        `json:"project_id"`
	Integrity  Integrity     `json:"integrity"`
}

type Authority struct {
	DeviceID string `json:"device_id"`
	ShareID  string `json:"share_id"`
}

type Producer struct {
	WorkerID       string `json:"worker_id,omitempty"`
	DeviceID       string `json:"device_id"`
	Trade          string `json:"trade,omitempty"`
	Specialization string `json:"specialization,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	ModelVersion   string `json:"model_version,omitempty"`
}

type Provenance struct {
	Producer           Producer  `json:"producer"`
	WorkPackageID      string    `json:"work_package_id,omitempty"`
	ExecutionID        string    `json:"execution_id,omitempty"`
	SourceArtifactIDs  []string  `json:"source_artifact_ids,omitempty"`
	ProjectVersion     string    `json:"project_version,omitempty"`
	ContextVersion     string    `json:"context_version,omitempty"`
	InstructionVersion string    `json:"instruction_version,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type ProjectManifest struct {
	RecordHeader
	Name      string    `json:"name"`
	Authority Authority `json:"authority"`
	CreatedAt time.Time `json:"created_at"`
}

type WorkScope struct {
	Allowed   []string `json:"allowed,omitempty"`
	Inspect   []string `json:"inspect,omitempty"`
	Forbidden []string `json:"forbidden,omitempty"`
}

type WorkPackageDefinition struct {
	RecordHeader
	WorkPackageID      string     `json:"work_package_id"`
	Objective          string     `json:"objective"`
	Trade              string     `json:"trade"`
	Specialization     string     `json:"specialization,omitempty"`
	Scope              WorkScope  `json:"scope"`
	Dependencies       []string   `json:"dependencies,omitempty"`
	Deliverables       []string   `json:"deliverables"`
	AcceptanceCriteria []string   `json:"acceptance_criteria"`
	ReviewRequired     bool       `json:"review_required"`
	CreatedAt          time.Time  `json:"created_at"`
	Provenance         Provenance `json:"provenance"`
}

type ExecutionState string

const (
	ExecutionPending   ExecutionState = "pending"
	ExecutionRunning   ExecutionState = "running"
	ExecutionPaused    ExecutionState = "paused"
	ExecutionFailed    ExecutionState = "failed"
	ExecutionCompleted ExecutionState = "completed"
)

type ExecutionManifest struct {
	RecordHeader
	ExecutionID   string         `json:"execution_id"`
	WorkPackageID string         `json:"work_package_id"`
	State         ExecutionState `json:"state"`
	Producer      Producer       `json:"producer"`
	CreatedAt     time.Time      `json:"created_at"`
	Provenance    Provenance     `json:"provenance"`
}

type EventType string

const (
	EventProjectRegistered       EventType = "PROJECT_REGISTERED"
	EventWorkPackageCreated      EventType = "WORK_PACKAGE_CREATED"
	EventWorkPackageStateChanged EventType = "WORK_PACKAGE_STATE_CHANGED"
	EventExecutionStarted        EventType = "EXECUTION_STARTED"
	EventExecutionPaused         EventType = "EXECUTION_PAUSED"
	EventExecutionFailed         EventType = "EXECUTION_FAILED"
	EventExecutionCompleted      EventType = "EXECUTION_COMPLETED"
	EventTestRecorded            EventType = "TEST_RECORDED"
	EventHandoffCreated          EventType = "HANDOFF_CREATED"
	EventReviewRecorded          EventType = "REVIEW_RECORDED"
	EventArtifactRecorded        EventType = "ARTIFACT_RECORDED"
	EventWorkAccepted            EventType = "WORK_ACCEPTED"
)

type Correlation struct {
	AuditID    string `json:"audit_id,omitempty"`
	RevisionID string `json:"revision_id,omitempty"`
}

type WorkEvent struct {
	RecordHeader
	EventType     EventType       `json:"event_type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	WorkPackageID string          `json:"work_package_id,omitempty"`
	ExecutionID   string          `json:"execution_id,omitempty"`
	Producer      Producer        `json:"producer"`
	CausationID   string          `json:"causation_id,omitempty"`
	Correlation   *Correlation    `json:"correlation,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

type TestResult struct {
	Name    string      `json:"name"`
	Outcome TestOutcome `json:"outcome"`
}

type Handoff struct {
	RecordHeader
	HandoffID                 string       `json:"handoff_id"`
	WorkPackageID             string       `json:"work_package_id"`
	ExecutionID               string       `json:"execution_id"`
	CompletedWork             []string     `json:"completed_work"`
	ChangedFiles              []string     `json:"changed_files,omitempty"`
	Decisions                 []string     `json:"decisions,omitempty"`
	Tests                     []TestResult `json:"tests,omitempty"`
	Limitations               []string     `json:"limitations,omitempty"`
	UnresolvedIssues          []string     `json:"unresolved_issues,omitempty"`
	Assumptions               []string     `json:"assumptions,omitempty"`
	FollowUpWork              []string     `json:"follow_up_work,omitempty"`
	ReviewRequirements        []string     `json:"review_requirements,omitempty"`
	IntegrationConsiderations []string     `json:"integration_considerations,omitempty"`
	Confidence                Confidence   `json:"confidence"`
	FailureConditions         []string     `json:"failure_conditions,omitempty"`
	CreatedAt                 time.Time    `json:"created_at"`
	Provenance                Provenance   `json:"provenance"`
}

type ArtifactManifest struct {
	RecordHeader
	ArtifactID       string     `json:"artifact_id"`
	Name             string     `json:"name"`
	MediaType        string     `json:"media_type"`
	Size             int64      `json:"size"`
	HashAlgorithm    string     `json:"hash_algorithm"`
	ContentHash      string     `json:"content_hash"`
	BlobRelativePath string     `json:"blob_relative_path,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	Provenance       Provenance `json:"provenance"`
}
