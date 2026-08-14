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
