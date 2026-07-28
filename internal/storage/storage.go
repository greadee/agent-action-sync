package storage

import (
	"context"
	"time"

	"syncgate/internal/core"
)

type Store interface {
	Migrate(ctx context.Context) error
	Devices() DeviceStore
	Shares() ShareStore
	Revisions() RevisionStore
	FileIndex() FileIndexStore
	Transfers() TransferStore
	Audit() AuditStore
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

type FileIndexStore interface {
	SaveSnapshot(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, scannedAt time.Time) error
	CommitSnapshot(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, revisions []core.Revision, scannedAt time.Time) error
	Get(ctx context.Context, shareID core.ShareID, relativePath string) (core.FileIndexEntry, error)
	List(ctx context.Context, shareID core.ShareID) ([]core.FileIndexEntry, error)
}

type TransferStore interface {
	SaveTransfer(ctx context.Context, transfer core.Transfer) error
	GetTransfer(ctx context.Context, id core.TransferID) (core.Transfer, error)
	SaveChunk(ctx context.Context, chunk core.TransferChunk) error
	VerifiedChunks(ctx context.Context, transferID core.TransferID) ([]core.TransferChunk, error)
}

type AuditStore interface {
	Record(ctx context.Context, event AuditEvent) error
}
