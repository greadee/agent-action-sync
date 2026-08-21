package storage

import (
	"context"
	"errors"
	"time"

	"syncgate/internal/core"
)

var (
	ErrNotFound = errors.New("storage record not found")
)

type Store interface {
	Migrate(ctx context.Context) error
	Devices() DeviceStore
	Shares() ShareStore
	Revisions() RevisionStore
	FileIndex() FileIndexStore
	Tombstones() TombstoneStore
	Transfers() TransferStore
	OneWayJobs() OneWayJobStore
	OneWayWork() OneWayWorkStore
	Pairings() PairingStore
	Audit() AuditStore
	ProjectRegistrations() ProjectRegistrationStore
	ProjectEvents() ProjectEventStore
	ProjectArtifacts() ProjectArtifactStore
	ProjectTasks() ProjectTaskStore
	ProjectTaskNodes() ProjectTaskNodeStore
	ProjectCheckpoints() ProjectCheckpointStore
	ProjectRejections() ProjectRejectionStore
	ProjectProjections() ProjectProjectionStore
	ProjectInsights() ProjectInsightStore
	ProjectInsightProjections() ProjectInsightProjectionStore
	Registry() RegistryStore
	ExecutionContracts() ExecutionContractStore
	ResultIntake() ResultIntakeStore
	ExecutionTelemetry() ExecutionTelemetryStore
	OrchestrationControl() OrchestrationControlStore
	Close() error
}

type DeviceStore interface {
	TrustDevice(ctx context.Context, device Device) error
	RevokeDevice(ctx context.Context, id core.DeviceID) error
	GetDevice(ctx context.Context, id core.DeviceID) (Device, error)
}

type ShareStore interface {
	SaveShare(ctx context.Context, share Share) error
	SetPermission(ctx context.Context, permission core.SharePermission) error
	Authorize(ctx context.Context, deviceID core.DeviceID, shareID core.ShareID, capability core.Capability, remote bool) error
}

type RevisionStore interface {
	RecordRevision(ctx context.Context, revision core.Revision) error
	GetRevision(ctx context.Context, id core.RevisionID) (core.Revision, error)
	GetCurrentRevision(ctx context.Context, shareID core.ShareID, relativePath string) (core.Revision, error)
}

type TombstoneRequest struct {
	ID                  core.TombstoneID
	TombstoneRevisionID core.RevisionID
	ExpiresAt           time.Time
}

type AuthoritativeStateStore interface {
	CommitSnapshotAndTombstones(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, revisions []core.Revision, tombstones []TombstoneRequest, scannedAt time.Time) ([]Tombstone, error)
}

// OneWayApplyCommit describes one receiver-side revision transition. Entry is
// required for an active revision and must be nil for a deletion revision.
// ExpectedCurrentRevisionID is checked in the same transaction that advances
// the file index, preventing stale preparation results from being committed.
type OneWayApplyCommit struct {
	ShareID                   core.ShareID
	Revision                  core.Revision
	ExpectedCurrentRevisionID core.RevisionID
	Entry                     *core.FileIndexEntry
	Tombstone                 *TombstoneRequest
	AppliedAt                 time.Time
	AllowTargetDrift          bool
}

type OneWayApplyCommitResult struct {
	Tombstone      *Tombstone
	AlreadyApplied bool
}

type OneWayApplyStore interface {
	CommitOneWayApply(ctx context.Context, commit OneWayApplyCommit) (OneWayApplyCommitResult, error)
}

type FileIndexStore interface {
	AuthoritativeStateStore
	OneWayApplyStore
	SaveSnapshot(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, scannedAt time.Time) error
	CommitSnapshot(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, revisions []core.Revision, scannedAt time.Time) error
	Get(ctx context.Context, shareID core.ShareID, relativePath string) (core.FileIndexEntry, error)
	List(ctx context.Context, shareID core.ShareID) ([]core.FileIndexEntry, error)
}

type TombstoneStore interface {
	RecordDeletion(ctx context.Context, tombstoneID core.TombstoneID, tombstoneRevisionID core.RevisionID, expiresAt time.Time) (Tombstone, error)
	Get(ctx context.Context, shareID core.ShareID, relativePath string) (Tombstone, error)
	List(ctx context.Context, shareID core.ShareID) ([]Tombstone, error)
	ListActive(ctx context.Context, shareID core.ShareID) ([]Tombstone, error)
}

type TransferStore interface {
	SaveTransfer(ctx context.Context, transfer core.Transfer) error
	GetTransfer(ctx context.Context, id core.TransferID) (core.Transfer, error)
	SaveChunk(ctx context.Context, chunk core.TransferChunk) error
	VerifiedChunks(ctx context.Context, transferID core.TransferID) ([]core.TransferChunk, error)
}

type OneWayJobStore interface {
	SaveOneWayJob(ctx context.Context, job core.OneWayJob) error
	GetOneWayJob(ctx context.Context, id string) (core.OneWayJob, error)
	ListOneWayJobs(ctx context.Context) ([]core.OneWayJob, error)
	ListRunnableOneWayJobs(ctx context.Context, now time.Time) ([]core.OneWayJob, error)
	ClaimOneWayJob(ctx context.Context, id string, now time.Time) (core.OneWayJob, error)
	UpdateOneWayJob(ctx context.Context, job core.OneWayJob) error
	RecoverRunningOneWayJobs(ctx context.Context, now time.Time) error
}

type OneWayWorkStore interface {
	CreateAuthenticatedOneWayWork(ctx context.Context, work AuthenticatedOneWayWork) (AuthenticatedOneWayWorkResult, error)
}

type AuditStore interface {
	Record(ctx context.Context, event AuditEvent) error
	Get(ctx context.Context, id string) (AuditEvent, error)
	ListRecent(ctx context.Context, limit int) ([]AuditEvent, error)
}

type PairingStore interface {
	Accept(ctx context.Context, acceptance PairingAcceptance) (PairingAcceptanceResult, error)
	Revoke(ctx context.Context, revocation PairingRevocation) (PairingRevocationResult, error)
}
