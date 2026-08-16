package storage

import (
	"context"
	"errors"
	"time"

	"syncgate/internal/core"
)

const (
	DefaultAdminPageLimit = 50
	MaxAdminPageLimit     = 200
	maxAdminFilterBytes   = 256
)

type PageRequest struct {
	Limit  int
	Cursor PageCursor
}

type PageCursor struct {
	Timestamp   time.Time
	ID          string
	SecondaryID string
	Version     int `json:"Version,omitempty"`
}

type Page[T any] struct {
	Items      []T
	NextCursor *PageCursor
}

type AdminShare struct {
	ID                   core.ShareID
	Name                 string
	RootPath             string
	Mode                 ShareMode
	CasePolicy           string
	VersionPolicy        string
	DeletionLimitCount   int
	DeletionLimitPercent int
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type AdminDevice struct {
	ID          core.DeviceID
	DisplayName string
	PublicKey   []byte
	Fingerprint string
	TrustState  TrustState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastSeenAt  time.Time
}

type AdminPermission struct {
	ShareID      core.ShareID
	DeviceID     core.DeviceID
	Capabilities map[core.Capability]bool
	LANOnly      bool
}

type AdminJob struct {
	ID            string
	TransferID    core.TransferID
	PeerDeviceID  core.DeviceID
	ShareID       core.ShareID
	State         core.OneWayJobState
	RetryCount    int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type AdminAuditEvent struct {
	ID            string
	EventName     string
	DeviceID      core.DeviceID
	PeerDeviceID  core.DeviceID
	ShareID       core.ShareID
	TransferID    core.TransferID
	RevisionID    core.RevisionID
	TransportType string
	Severity      string
	OccurredAt    time.Time
}

type PermissionQuery struct {
	Page     PageRequest
	ShareID  core.ShareID
	DeviceID core.DeviceID
}

type JobQuery struct {
	Page    PageRequest
	ShareID core.ShareID
	State   core.OneWayJobState
}

type AuditEventQuery struct {
	Page      PageRequest
	EventName string
	DeviceID  core.DeviceID
	ShareID   core.ShareID
}

type AdministrationQueryStore interface {
	ListAdminShares(ctx context.Context, page PageRequest) (Page[AdminShare], error)
	ListAdminDevices(ctx context.Context, page PageRequest) (Page[AdminDevice], error)
	GetAdminDevice(ctx context.Context, id core.DeviceID) (AdminDevice, error)
	ListAdminPermissions(ctx context.Context, query PermissionQuery) (Page[AdminPermission], error)
	ListAdminJobs(ctx context.Context, query JobQuery) (Page[AdminJob], error)
	GetAdminJob(ctx context.Context, id string) (AdminJob, error)
	ListAdminAuditEvents(ctx context.Context, query AuditEventQuery) (Page[AdminAuditEvent], error)
}

func NormalizePageRequest(request PageRequest) (PageRequest, error) {
	if request.Limit == 0 {
		request.Limit = DefaultAdminPageLimit
	}
	if request.Limit < 1 || request.Limit > MaxAdminPageLimit {
		return PageRequest{}, errors.New("page limit must be between 1 and 200")
	}
	if len(request.Cursor.ID) > maxAdminFilterBytes || len(request.Cursor.SecondaryID) > maxAdminFilterBytes {
		return PageRequest{}, errors.New("page cursor is too long")
	}
	if request.Cursor.Version < 0 {
		return PageRequest{}, errors.New("page cursor version cannot be negative")
	}
	return request, nil
}

func ValidateAdminFilter(value string) error {
	if len(value) > maxAdminFilterBytes {
		return errors.New("administration query filter is too long")
	}
	return nil
}

func CloneAdminDevice(device AdminDevice) AdminDevice {
	device.PublicKey = append([]byte(nil), device.PublicKey...)
	return device
}

func CloneAdminPermission(permission AdminPermission) AdminPermission {
	permission.Capabilities = cloneCapabilities(permission.Capabilities)
	return permission
}

func cloneCapabilities(source map[core.Capability]bool) map[core.Capability]bool {
	result := make(map[core.Capability]bool, len(core.ShareCapabilities()))
	for _, capability := range core.ShareCapabilities() {
		result[capability] = source[capability]
	}
	return result
}
