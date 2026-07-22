package storage

import (
	"time"

	"syncgate/internal/core"
)

type TrustState string

const (
	TrustPending TrustState = "pending"
	TrustTrusted TrustState = "trusted"
	TrustRevoked TrustState = "revoked"
)

type Device struct {
	ID          core.DeviceID
	DisplayName string
	PublicKey   []byte
	Fingerprint string
	TrustState  TrustState
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastSeenAt  time.Time
}

type ShareMode string

const (
	ShareSendOnce      ShareMode = "send_once"
	ShareOneWaySource  ShareMode = "one_way_source"
	ShareOneWayTarget  ShareMode = "one_way_target"
	ShareUploadOnly    ShareMode = "upload_only"
	ShareReadOnly      ShareMode = "read_only"
	ShareTwoWayPlanned ShareMode = "two_way_planned"
)

type Share struct {
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

type AuditEvent struct {
	ID            string
	EventName     string
	DeviceID      core.DeviceID
	PeerDeviceID  core.DeviceID
	ShareID       core.ShareID
	TransferID    core.TransferID
	RevisionID    core.RevisionID
	TransportType string
	Severity      string
	Metadata      map[string]string
	OccurredAt    time.Time
}
