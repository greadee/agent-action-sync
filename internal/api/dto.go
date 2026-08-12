package api

import (
	"encoding/json"
	"fmt"
	"strings"
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
	return nil
}

type InvitationDTO struct {
	Invitation string `json:"invitation"`
	ExpiresAt  string `json:"expires_at"`
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
	ExpiresAt    string   `json:"expires_at"`
	DeviceName   string   `json:"device_name,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type AcceptanceRequest struct {
	Invitation  string `json:"invitation"`
	DisplayName string `json:"display_name,omitempty"`
}

func (request AcceptanceRequest) Validate() error {
	return (EncodedInvitation{Invitation: request.Invitation}).Validate()
}

type Acceptance struct {
	Accepted bool   `json:"accepted"`
	DeviceID string `json:"device_id"`
}

type Revocation struct {
	DeviceID string `json:"device_id"`
	Revoked  bool   `json:"revoked"`
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
