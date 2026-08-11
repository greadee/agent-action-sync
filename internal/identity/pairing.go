package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"syncgate/internal/core"
)

const (
	PairingProtocol     = "syncgate-pairing-v2"
	MaxPairingInviteTTL = 24 * time.Hour
	pairingClockSkew    = 5 * time.Minute
)

var (
	ErrPairingInviteExpired       = errors.New("pairing invite expired")
	ErrPairingInviteInvalid       = errors.New("pairing invite is invalid")
	ErrPairingCodeMismatch        = errors.New("pairing code does not match")
	ErrPairingFingerprintMismatch = errors.New("pairing fingerprint does not match")
)

type PairingInvite struct {
	InviteID      string            `json:"invite_id"`
	DeviceID      core.DeviceID     `json:"device_id"`
	DisplayName   string            `json:"display_name"`
	PublicKey     string            `json:"public_key"`
	Fingerprint   string            `json:"fingerprint"`
	Protocol      string            `json:"protocol"`
	Coordinator   string            `json:"coordinator,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
	CodeHash      string            `json:"code_hash"`
	RequestedCaps []core.Capability `json:"requested_capabilities,omitempty"`
	Signature     string            `json:"signature"`
	OneTimeCode   string            `json:"-"`
}

func NewPairingInvite(identity DeviceIdentity, displayName string, ttl time.Duration, requestedCaps []core.Capability, reader io.Reader) (PairingInvite, error) {
	return NewPairingInviteAt(identity, displayName, ttl, requestedCaps, reader, time.Now().UTC())
}

func NewPairingInviteAt(deviceIdentity DeviceIdentity, displayName string, ttl time.Duration, requestedCaps []core.Capability, reader io.Reader, now time.Time) (PairingInvite, error) {
	if ttl <= 0 {
		return PairingInvite{}, fmt.Errorf("pairing invite ttl must be positive")
	}
	if ttl > MaxPairingInviteTTL {
		return PairingInvite{}, fmt.Errorf("pairing invite ttl cannot exceed %s", MaxPairingInviteTTL)
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		return PairingInvite{}, errors.New("pairing display name is required")
	}
	validated, err := FromKeyPair(deviceIdentity.PublicKey, deviceIdentity.PrivateKey)
	if err != nil {
		return PairingInvite{}, fmt.Errorf("validate pairing identity: %w", err)
	}
	if err := validateRequestedCapabilities(requestedCaps); err != nil {
		return PairingInvite{}, err
	}
	code, err := GeneratePairingCode(reader)
	if err != nil {
		return PairingInvite{}, err
	}
	codeHash := sha256.Sum256([]byte(normalizePairingCode(code)))
	invite := PairingInvite{
		DeviceID:      validated.DeviceID,
		DisplayName:   displayName,
		PublicKey:     base64.StdEncoding.EncodeToString(validated.PublicKey),
		Fingerprint:   validated.Fingerprint,
		Protocol:      PairingProtocol,
		CreatedAt:     now.UTC(),
		ExpiresAt:     now.UTC().Add(ttl),
		CodeHash:      base64.RawURLEncoding.EncodeToString(codeHash[:]),
		OneTimeCode:   code,
		RequestedCaps: append([]core.Capability(nil), requestedCaps...),
	}
	invite.InviteID = pairingInviteID(invite)
	payload, err := pairingSigningPayload(invite)
	if err != nil {
		return PairingInvite{}, err
	}
	invite.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(validated.PrivateKey, payload))
	return invite, nil
}

func EncodePairingInvite(invite PairingInvite) (string, error) {
	raw, err := json.Marshal(invite)
	if err != nil {
		return "", fmt.Errorf("marshal pairing invite: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodePairingInvite(encoded string) (PairingInvite, error) {
	return DecodePairingInviteAt(encoded, time.Now().UTC())
}

func DecodePairingInviteAt(encoded string, now time.Time) (PairingInvite, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return PairingInvite{}, fmt.Errorf("decode pairing invite: %w", err)
	}
	var invite PairingInvite
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&invite); err != nil {
		return PairingInvite{}, fmt.Errorf("parse pairing invite: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return PairingInvite{}, err
	}
	if _, err := ValidatePairingInvite(invite, now); err != nil {
		return PairingInvite{}, err
	}
	return invite, nil
}

type PairingPeer struct {
	DeviceID    core.DeviceID
	DisplayName string
	PublicKey   ed25519.PublicKey
	Fingerprint string
}

func ValidatePairingInvite(invite PairingInvite, now time.Time) (PairingPeer, error) {
	if invite.Protocol != PairingProtocol {
		return PairingPeer{}, fmt.Errorf("%w: unsupported protocol %q", ErrPairingInviteInvalid, invite.Protocol)
	}
	if invite.InviteID == "" || invite.CodeHash == "" || invite.Signature == "" {
		return PairingPeer{}, fmt.Errorf("%w: required invitation proof is missing", ErrPairingInviteInvalid)
	}
	createdAt := invite.CreatedAt.UTC()
	expiresAt := invite.ExpiresAt.UTC()
	if createdAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(createdAt) || expiresAt.Sub(createdAt) > MaxPairingInviteTTL {
		return PairingPeer{}, fmt.Errorf("%w: invitation lifetime is invalid", ErrPairingInviteInvalid)
	}
	if createdAt.After(now.UTC().Add(pairingClockSkew)) {
		return PairingPeer{}, fmt.Errorf("%w: creation time is too far in the future", ErrPairingInviteInvalid)
	}
	if !now.UTC().Before(expiresAt) {
		return PairingPeer{}, ErrPairingInviteExpired
	}
	if strings.TrimSpace(invite.DisplayName) == "" {
		return PairingPeer{}, fmt.Errorf("%w: display name is required", ErrPairingInviteInvalid)
	}
	if err := validateRequestedCapabilities(invite.RequestedCaps); err != nil {
		return PairingPeer{}, fmt.Errorf("%w: %v", ErrPairingInviteInvalid, err)
	}
	publicKey, err := base64.StdEncoding.DecodeString(invite.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return PairingPeer{}, fmt.Errorf("%w: public key is malformed", ErrPairingInviteInvalid)
	}
	fingerprint := Fingerprint(ed25519.PublicKey(publicKey))
	if fingerprint != invite.Fingerprint || DeviceIDFromFingerprint(fingerprint) != invite.DeviceID {
		return PairingPeer{}, fmt.Errorf("%w: public identity metadata does not match", ErrPairingInviteInvalid)
	}
	if pairingInviteID(invite) != invite.InviteID {
		return PairingPeer{}, fmt.Errorf("%w: invitation ID does not match its contents", ErrPairingInviteInvalid)
	}
	signature, err := base64.RawURLEncoding.DecodeString(invite.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return PairingPeer{}, fmt.Errorf("%w: signature is malformed", ErrPairingInviteInvalid)
	}
	payload, err := pairingSigningPayload(invite)
	if err != nil {
		return PairingPeer{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return PairingPeer{}, fmt.Errorf("%w: signature verification failed", ErrPairingInviteInvalid)
	}
	return PairingPeer{
		DeviceID:    invite.DeviceID,
		DisplayName: strings.TrimSpace(invite.DisplayName),
		PublicKey:   append(ed25519.PublicKey(nil), publicKey...),
		Fingerprint: fingerprint,
	}, nil
}

func ConfirmPairingInvite(invite PairingInvite, expectedFingerprint, code string, now time.Time) (PairingPeer, error) {
	peer, err := ValidatePairingInvite(invite, now)
	if err != nil {
		return PairingPeer{}, err
	}
	if normalizeFingerprint(expectedFingerprint) != normalizeFingerprint(peer.Fingerprint) {
		return PairingPeer{}, ErrPairingFingerprintMismatch
	}
	expectedCodeHash, err := base64.RawURLEncoding.DecodeString(invite.CodeHash)
	if err != nil || len(expectedCodeHash) != sha256.Size {
		return PairingPeer{}, fmt.Errorf("%w: code proof is malformed", ErrPairingInviteInvalid)
	}
	providedCodeHash := sha256.Sum256([]byte(normalizePairingCode(code)))
	if subtle.ConstantTimeCompare(expectedCodeHash, providedCodeHash[:]) != 1 {
		return PairingPeer{}, ErrPairingCodeMismatch
	}
	return peer, nil
}

type pairingUnsignedInvite struct {
	InviteID      string            `json:"invite_id"`
	DeviceID      core.DeviceID     `json:"device_id"`
	DisplayName   string            `json:"display_name"`
	PublicKey     string            `json:"public_key"`
	Fingerprint   string            `json:"fingerprint"`
	Protocol      string            `json:"protocol"`
	Coordinator   string            `json:"coordinator,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
	CodeHash      string            `json:"code_hash"`
	RequestedCaps []core.Capability `json:"requested_capabilities,omitempty"`
}

func pairingSigningPayload(invite PairingInvite) ([]byte, error) {
	payload, err := json.Marshal(pairingUnsignedInvite{
		InviteID: invite.InviteID, DeviceID: invite.DeviceID, DisplayName: invite.DisplayName,
		PublicKey: invite.PublicKey, Fingerprint: invite.Fingerprint, Protocol: invite.Protocol,
		Coordinator: invite.Coordinator, CreatedAt: invite.CreatedAt.UTC(), ExpiresAt: invite.ExpiresAt.UTC(), CodeHash: invite.CodeHash,
		RequestedCaps: invite.RequestedCaps,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal pairing signature payload: %w", err)
	}
	return payload, nil
}

func pairingInviteID(invite PairingInvite) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		invite.Protocol,
		string(invite.DeviceID),
		invite.PublicKey,
		invite.CodeHash,
		invite.CreatedAt.UTC().Format(time.RFC3339Nano),
		invite.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

func validateRequestedCapabilities(capabilities []core.Capability) error {
	seen := make(map[core.Capability]bool, len(capabilities))
	for _, capability := range capabilities {
		if !core.IsShareCapability(capability) {
			return fmt.Errorf("unsupported requested capability %q", capability)
		}
		if seen[capability] {
			return fmt.Errorf("duplicate requested capability %q", capability)
		}
		seen[capability] = true
	}
	return nil
}

func normalizePairingCode(code string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

func normalizeFingerprint(fingerprint string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", ":", "", " ", "").Replace(strings.TrimSpace(fingerprint)))
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrPairingInviteInvalid)
		}
		return fmt.Errorf("parse pairing invite trailer: %w", err)
	}
	return nil
}
