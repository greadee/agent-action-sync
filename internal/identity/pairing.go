package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"syncgate/internal/core"
)

type PairingInvite struct {
	DeviceID      core.DeviceID     `json:"device_id"`
	DisplayName   string            `json:"display_name"`
	PublicKey     string            `json:"public_key"`
	Fingerprint   string            `json:"fingerprint"`
	Protocol      string            `json:"protocol"`
	Coordinator   string            `json:"coordinator,omitempty"`
	ExpiresAt     time.Time         `json:"expires_at"`
	OneTimeCode   string            `json:"one_time_code"`
	RequestedCaps []core.Capability `json:"requested_capabilities,omitempty"`
}

func NewPairingInvite(identity DeviceIdentity, displayName string, ttl time.Duration, requestedCaps []core.Capability, reader io.Reader) (PairingInvite, error) {
	if ttl <= 0 {
		return PairingInvite{}, fmt.Errorf("pairing invite ttl must be positive")
	}
	code, err := GeneratePairingCode(reader)
	if err != nil {
		return PairingInvite{}, err
	}
	return PairingInvite{
		DeviceID:      identity.DeviceID,
		DisplayName:   displayName,
		PublicKey:     base64.StdEncoding.EncodeToString(identity.PublicKey),
		Fingerprint:   identity.Fingerprint,
		Protocol:      "syncgate-pairing-v1",
		ExpiresAt:     time.Now().UTC().Add(ttl),
		OneTimeCode:   code,
		RequestedCaps: append([]core.Capability(nil), requestedCaps...),
	}, nil
}

func EncodePairingInvite(invite PairingInvite) (string, error) {
	raw, err := json.Marshal(invite)
	if err != nil {
		return "", fmt.Errorf("marshal pairing invite: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodePairingInvite(encoded string) (PairingInvite, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return PairingInvite{}, fmt.Errorf("decode pairing invite: %w", err)
	}
	var invite PairingInvite
	if err := json.Unmarshal(raw, &invite); err != nil {
		return PairingInvite{}, fmt.Errorf("parse pairing invite: %w", err)
	}
	if invite.Protocol != "syncgate-pairing-v1" {
		return PairingInvite{}, fmt.Errorf("unsupported pairing protocol %q", invite.Protocol)
	}
	if time.Now().UTC().After(invite.ExpiresAt) {
		return PairingInvite{}, fmt.Errorf("pairing invite expired")
	}
	return invite, nil
}
