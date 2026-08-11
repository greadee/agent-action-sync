package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestPairingInviteRoundTrip(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	identity, err := FromKeyPair(publicKey, privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	invite, err := NewPairingInviteAt(identity, "Laptop", time.Minute, []core.Capability{core.CapabilityUpload}, bytes.NewReader(bytes.Repeat([]byte{8}, PairingCodeByteCount)), now)
	if err != nil {
		t.Fatalf("NewPairingInvite: %v", err)
	}
	encoded, err := EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("EncodePairingInvite: %v", err)
	}
	decoded, err := DecodePairingInviteAt(encoded, now)
	if err != nil {
		t.Fatalf("DecodePairingInvite: %v", err)
	}
	if decoded.DeviceID != identity.DeviceID {
		t.Fatalf("device ID = %q", decoded.DeviceID)
	}
	if decoded.OneTimeCode != "" || strings.Contains(encoded, invite.OneTimeCode) {
		t.Fatal("encoded invitation exposed the one-time code")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode raw invitation: %v", err)
	}
	if bytes.Contains(raw, []byte(invite.OneTimeCode)) || bytes.Contains(raw, []byte("one_time_code")) {
		t.Fatal("invitation JSON exposed the one-time code")
	}
	if _, err := ConfirmPairingInvite(decoded, identity.Fingerprint, invite.OneTimeCode, now); err != nil {
		t.Fatalf("ConfirmPairingInvite: %v", err)
	}
}

func TestDecodePairingInviteRejectsExpiredInvite(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, ed25519.SeedSize))
	deviceIdentity, err := FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	now := time.Unix(200, 0).UTC()
	invite, err := NewPairingInviteAt(deviceIdentity, "Laptop", time.Minute, nil, bytes.NewReader(bytes.Repeat([]byte{9}, PairingCodeByteCount)), now)
	if err != nil {
		t.Fatalf("NewPairingInviteAt: %v", err)
	}
	encoded, err := EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("EncodePairingInvite: %v", err)
	}
	if _, err := DecodePairingInviteAt(encoded, now.Add(time.Minute)); !errors.Is(err, ErrPairingInviteExpired) {
		t.Fatalf("expired invite error = %v, want %v", err, ErrPairingInviteExpired)
	}
}

func TestPairingInviteRejectsTamperingAndConfirmationMismatch(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{6}, ed25519.SeedSize))
	deviceIdentity, err := FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	now := time.Unix(300, 0).UTC()
	invite, err := NewPairingInviteAt(deviceIdentity, "Laptop", time.Minute, nil, bytes.NewReader(bytes.Repeat([]byte{10}, PairingCodeByteCount)), now)
	if err != nil {
		t.Fatalf("NewPairingInviteAt: %v", err)
	}

	if _, err := ConfirmPairingInvite(invite, "WRONG", invite.OneTimeCode, now); !errors.Is(err, ErrPairingFingerprintMismatch) {
		t.Fatalf("fingerprint mismatch error = %v", err)
	}
	if _, err := ConfirmPairingInvite(invite, invite.Fingerprint, "WRONG-CODE", now); !errors.Is(err, ErrPairingCodeMismatch) {
		t.Fatalf("code mismatch error = %v", err)
	}
	invite.DisplayName = "Attacker"
	if _, err := ValidatePairingInvite(invite, now); !errors.Is(err, ErrPairingInviteInvalid) {
		t.Fatalf("tampered invite error = %v", err)
	}
}

func TestPairingInviteRejectsExcessiveLifetime(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	deviceIdentity, err := FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	_, err = NewPairingInviteAt(
		deviceIdentity, "Laptop", MaxPairingInviteTTL+time.Second, nil,
		bytes.NewReader(bytes.Repeat([]byte{11}, PairingCodeByteCount)), time.Unix(400, 0).UTC(),
	)
	if err == nil {
		t.Fatal("expected excessive invitation lifetime to fail")
	}
}
