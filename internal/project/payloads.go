package project

type WorkPackageState string

const (
	WorkPackagePlanned    WorkPackageState = "planned"
	WorkPackageReady      WorkPackageState = "ready"
	WorkPackageInProgress WorkPackageState = "in_progress"
	WorkPackageBlocked    WorkPackageState = "blocked"
	WorkPackageReview     WorkPackageState = "review"
	WorkPackageAccepted   WorkPackageState = "accepted"
	WorkPackageFailed     WorkPackageState = "failed"
	WorkPackageCanceled   WorkPackageState = "canceled"
)

func ValidWorkPackageTransition(from, to WorkPackageState) bool {
	allowed := map[WorkPackageState]map[WorkPackageState]bool{
		WorkPackagePlanned:    {WorkPackageReady: true, WorkPackageCanceled: true},
		WorkPackageReady:      {WorkPackageInProgress: true, WorkPackageCanceled: true},
		WorkPackageInProgress: {WorkPackageBlocked: true, WorkPackageReview: true, WorkPackageFailed: true, WorkPackageCanceled: true},
		WorkPackageBlocked:    {WorkPackageInProgress: true, WorkPackageFailed: true, WorkPackageCanceled: true},
		WorkPackageReview:     {WorkPackageInProgress: true, WorkPackageFailed: true},
		WorkPackageFailed:     {WorkPackageReady: true, WorkPackageCanceled: true},
	}
	return allowed[from][to]
}

type TestOutcome string

const (
	TestPassed  TestOutcome = "passed"
	TestFailed  TestOutcome = "failed"
	TestSkipped TestOutcome = "skipped"
)

type ReviewOutcome string

const (
	ReviewApproved         ReviewOutcome = "approved"
	ReviewRejected         ReviewOutcome = "rejected"
	ReviewChangesRequested ReviewOutcome = "changes_requested"
)

type ProjectRegisteredPayload struct {
	ManifestRecordID string    `json:"manifest_record_id"`
	Authority        Authority `json:"authority"`
}

type WorkPackageCreatedPayload struct {
	DefinitionRecordID string `json:"definition_record_id"`
}

type WorkPackageStateChangedPayload struct {
	From       WorkPackageState `json:"from"`
	To         WorkPackageState `json:"to"`
	ReasonCode string           `json:"reason_code,omitempty"`
}

type ExecutionStartedPayload struct {
	ManifestRecordID string `json:"manifest_record_id"`
}

type ExecutionPausedPayload struct {
	ReasonCode string `json:"reason_code"`
}

type ExecutionFailedPayload struct {
	FailureCode string `json:"failure_code"`
	Summary     string `json:"summary,omitempty"`
}

type ExecutionCompletedPayload struct {
	Summary string `json:"summary,omitempty"`
}

type TestRecordedPayload struct {
	Name                 string      `json:"name"`
	Outcome              TestOutcome `json:"outcome"`
	DurationMilliseconds int64       `json:"duration_milliseconds,omitempty"`
	CommandID            string      `json:"command_id,omitempty"`
	CommandDigest        string      `json:"command_digest,omitempty"`
	ExitCode             *int64      `json:"exit_code,omitempty"`
	EvidenceID           string      `json:"evidence_id,omitempty"`
	EvidenceDigest       string      `json:"evidence_digest,omitempty"`
}

type HandoffCreatedPayload struct {
	HandoffRecordID string `json:"handoff_record_id"`
}

type ReviewRecordedPayload struct {
	Outcome    ReviewOutcome `json:"outcome"`
	ReviewerID string        `json:"reviewer_id"`
	Summary    string        `json:"summary,omitempty"`
}

type ArtifactRecordedPayload struct {
	ArtifactRecordID string `json:"artifact_record_id"`
	ArtifactID       string `json:"artifact_id"`
}

type WorkAcceptedPayload struct {
	AcceptedBy string `json:"accepted_by"`
	Summary    string `json:"summary,omitempty"`
}

// TelemetrySummaryPayload is the portable, allowlisted summary of an
// authority-accepted telemetry envelope. Raw prompts, command output, secrets,
// and filesystem paths are intentionally not representable here.
type TelemetrySummaryPayload struct {
	Schema              string                       `json:"schema"`
	TelemetryID         string                       `json:"telemetry_id"`
	TelemetryDigest     string                       `json:"telemetry_digest"`
	Contract            RegistryReference            `json:"contract"`
	ProjectRevision     string                       `json:"project_revision"`
	TaskID              string                       `json:"task_id"`
	TaskRecordID        string                       `json:"task_record_id"`
	TaskRevision        int64                        `json:"task_revision"`
	TaskDigest          string                       `json:"task_digest"`
	GraphRecordID       string                       `json:"graph_record_id"`
	GraphRevision       int64                        `json:"graph_revision"`
	GraphDigest         string                       `json:"graph_digest"`
	WorkPackageID       string                       `json:"work_package_id"`
	WorkPackageRecordID string                       `json:"work_package_record_id"`
	WorkPackageDigest   string                       `json:"work_package_digest"`
	Trade               RegistryReference            `json:"trade"`
	Worker              RegistryReference            `json:"worker"`
	Instruction         TelemetryBindingReference    `json:"instruction"`
	ContextDigest       string                       `json:"context_digest"`
	Runtime             TelemetryBindingReference    `json:"runtime"`
	Provider            TelemetryBindingReference    `json:"provider"`
	Model               TelemetryBindingReference    `json:"model"`
	Node                TelemetryBindingReference    `json:"node"`
	Observations        []TelemetryObservation       `json:"observations"`
	Evidence            []TelemetryEvidenceReference `json:"evidence,omitempty"`
	FinalOutcome        string                       `json:"final_outcome"`
}

type TelemetryBindingReference struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}

type TelemetryObservation struct {
	Name   string `json:"name"`
	Value  *int64 `json:"value,omitempty"`
	Source string `json:"source"`
}

type TelemetryEvidenceReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}
