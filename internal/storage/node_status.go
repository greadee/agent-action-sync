package storage

import (
	"context"
	"time"

	"syncgate/internal/core"
)

const MaxNodeStatusPayloadBytes = 64 << 10

type ControlPlaneGrant struct {
	DeviceID   core.DeviceID
	ReadStatus bool
	GrantedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  time.Time
}

type NodeStatusReplica struct {
	DeviceID       core.DeviceID
	Revision       int64
	Watermark      string
	Protocol       string
	SnapshotJSON   []byte
	Digest         string
	ObservedAt     time.Time
	ExpiresAt      time.Time
	ReceivedAt     time.Time
	GrantExpiresAt time.Time
}

type NodeStatusStore interface {
	SaveControlPlaneGrant(ctx context.Context, grant ControlPlaneGrant) error
	GetControlPlaneGrant(ctx context.Context, deviceID core.DeviceID) (ControlPlaneGrant, error)
	SaveNodeStatusReplica(ctx context.Context, replica NodeStatusReplica) (RegistryWriteResult, error)
	ListVisibleNodeStatusReplicas(ctx context.Context, now time.Time, limit int) ([]NodeStatusReplica, error)
}
