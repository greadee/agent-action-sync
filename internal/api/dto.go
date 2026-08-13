package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"syncgate/internal/core"
)

// These types are transport DTOs. They intentionally do not expose storage,
// filesystem, or pairing package types at the HTTP boundary.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type PageInfo struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}

type ShareDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	JobID   string `json:"job_id,omitempty"`
}

type DeviceDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	State       string `json:"state"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

type JobDTO struct {
	ID        string `json:"id"`
	ShareID   string `json:"share_id"`
	State     string `json:"state"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type AuditEventDTO struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	OccurredAt string `json:"occurred_at"`
	Result     string `json:"result,omitempty"`
}

type SharePage struct {
	Items []ShareDTO `json:"items"`
	Page  PageInfo   `json:"page"`
}

type DevicePage struct {
	Items []DeviceDTO `json:"items"`
	Page  PageInfo    `json:"page"`
}

type JobPage struct {
	Items []JobDTO `json:"items"`
	Page  PageInfo `json:"page"`
}

type AuditEventPage struct {
	Items []AuditEventDTO `json:"items"`
	Page  PageInfo        `json:"page"`
}

type AcceptedCommand struct {
	Accepted bool   `json:"accepted"`
	JobID    string `json:"job_id"`
}

type JobAction struct {
	Action JobActionName `json:"action"`
}

type JobActionName string

const (
	JobActionPause  JobActionName = "pause"
	JobActionResume JobActionName = "resume"
	JobActionRetry  JobActionName = "retry"
)

func (action JobAction) Validate() error {
	switch action.Action {
	case JobActionPause, JobActionResume, JobActionRetry:
		return nil
	default:
		return fmt.Errorf("unsupported action %q", action.Action)
	}
}

type InvitationRequest struct {
	TTLSeconds   int      `json:"ttl_seconds"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func (request InvitationRequest) Validate() error {
	if request.TTLSeconds < 1 || request.TTLSeconds > 86400 {
		return fmt.Errorf("ttl_seconds must be between 1 and 86400")
	}
	if len(request.Capabilities) > 16 {
		return fmt.Errorf("capabilities cannot contain more than 16 values")
	}
	seen := make(map[string]bool, len(request.Capabilities))
	for _, capability := range request.Capabilities {
		if !core.IsShareCapability(core.Capability(capability)) {
			return fmt.Errorf("unsupported capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

type InvitationDTO struct {
	Invitation  string `json:"invitation"`
	Fingerprint string `json:"fingerprint"`
	OneTimeCode string `json:"one_time_code"`
	ExpiresAt   string `json:"expires_at"`
}

type EncodedInvitation struct {
	Invitation string `json:"invitation"`
}

func (request EncodedInvitation) Validate() error {
	if request.Invitation == "" || len(request.Invitation) > 65536 {
		return fmt.Errorf("invitation must be between 1 and 65536 bytes")
	}
	return nil
}

type InvitationInspection struct {
	Valid        bool     `json:"valid"`
	InviteID     string   `json:"invite_id"`
	DeviceID     string   `json:"device_id"`
	DeviceName   string   `json:"device_name"`
	Fingerprint  string   `json:"fingerprint"`
	CreatedAt    string   `json:"created_at"`
	ExpiresAt    string   `json:"expires_at"`
	Capabilities []string `json:"capabilities"`
}

type AcceptanceRequest struct {
	Invitation          string                `json:"invitation"`
	ExpectedFingerprint string                `json:"expected_fingerprint"`
	OneTimeCode         string                `json:"one_time_code"`
	Grants              []PairingGrantRequest `json:"grants"`
}

func (request AcceptanceRequest) Validate() error {
	if err := (EncodedInvitation{Invitation: request.Invitation}).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.ExpectedFingerprint) == "" || len(request.ExpectedFingerprint) > 256 {
		return fmt.Errorf("expected_fingerprint must be between 1 and 256 bytes")
	}
	if strings.TrimSpace(request.OneTimeCode) == "" || len(request.OneTimeCode) > 128 {
		return fmt.Errorf("one_time_code must be between 1 and 128 bytes")
	}
	if len(request.Grants) > 200 {
		return fmt.Errorf("grants cannot contain more than 200 values")
	}
	seenShares := make(map[string]bool, len(request.Grants))
	for _, grant := range request.Grants {
		if err := grant.Validate(); err != nil {
			return err
		}
		if seenShares[grant.ShareID] {
			return fmt.Errorf("duplicate grant for share %q", grant.ShareID)
		}
		seenShares[grant.ShareID] = true
	}
	return nil
}

type PairingGrantRequest struct {
	ShareID      string   `json:"share_id"`
	Capabilities []string `json:"capabilities"`
	LANOnly      *bool    `json:"lan_only,omitempty"`
}

func (request PairingGrantRequest) Validate() error {
	if strings.TrimSpace(request.ShareID) == "" || len(request.ShareID) > 256 || strings.ContainsAny(request.ShareID, "/\\") {
		return fmt.Errorf("grant share_id must be between 1 and 256 bytes and contain no path separators")
	}
	if request.ShareID != strings.TrimSpace(request.ShareID) {
		return fmt.Errorf("grant share_id must be normalized")
	}
	if len(request.Capabilities) == 0 || len(request.Capabilities) > 16 {
		return fmt.Errorf("grant capabilities must contain between 1 and 16 values")
	}
	seen := make(map[string]bool, len(request.Capabilities))
	for _, capability := range request.Capabilities {
		if !core.IsShareCapability(core.Capability(capability)) {
			return fmt.Errorf("unsupported grant capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate grant capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

type Acceptance struct {
	Accepted        bool   `json:"accepted"`
	AlreadyAccepted bool   `json:"already_accepted"`
	DeviceID        string `json:"device_id"`
	DeviceName      string `json:"device_name"`
	Fingerprint     string `json:"fingerprint"`
}

type Revocation struct {
	DeviceID       string `json:"device_id"`
	Revoked        bool   `json:"revoked"`
	AlreadyRevoked bool   `json:"already_revoked"`
}

func (action *JobAction) UnmarshalJSON(data []byte) error {
	type wire JobAction
	var decoded wire
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*action = JobAction(decoded)
	return action.Validate()
}
