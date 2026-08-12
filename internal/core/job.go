package core

import "time"

type OneWayJobState string

const (
	OneWayJobQueued    OneWayJobState = "queued"
	OneWayJobRunning   OneWayJobState = "running"
	OneWayJobRetryWait OneWayJobState = "retry_wait"
	OneWayJobPaused    OneWayJobState = "paused"
	OneWayJobCompleted OneWayJobState = "completed"
	OneWayJobFailed    OneWayJobState = "failed"
)

// OneWayJob is queue metadata for an existing transfer. Transfer bytes,
// chunks, and resume state remain in core.Transfer and transfer_chunks.
type OneWayJob struct {
	ID                 string
	TransferID         TransferID
	PeerDeviceID       DeviceID
	ShareID            ShareID
	RevisionID         RevisionID
	RelativePath       string
	RequiredCapability Capability
	State              OneWayJobState
	RetryCount         int
	NextAttemptAt      time.Time
	LastError          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
