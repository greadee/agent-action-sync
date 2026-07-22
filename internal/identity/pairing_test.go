package identity

import (
	"bytes"
	"crypto/ed25519"
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

	invite, err := NewPairingInvite(identity, "Laptop", time.Minute, []core.Capability{core.CapabilityUpload}, bytes.NewReader(bytes.Repeat([]byte{8}, PairingCodeByteCount)))
	if err != nil {
		t.Fatalf("NewPairingInvite: %v", err)
	}
	encoded, err := EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("EncodePairingInvite: %v", err)
	}
	decoded, err := DecodePairingInvite(encoded)
	if err != nil {
		t.Fatalf("DecodePairingInvite: %v", err)
	}
	if decoded.DeviceID != identity.DeviceID {
		t.Fatalf("device ID = %q", decoded.DeviceID)
	}
	if decoded.OneTimeCode == "" {
		t.Fatal("expected one-time code")
	}
}

func TestDecodePairingInviteRejectsExpiredInvite(t *testing.T) {
	invite := PairingInvite{
		Protocol:    "syncgate-pairing-v1",
		ExpiresAt:   time.Now().UTC().Add(-time.Minute),
		OneTimeCode: "ABCD",
	}
	encoded, err := EncodePairingInvite(invite)
	if err != nil {
		t.Fatalf("EncodePairingInvite: %v", err)
	}
	if _, err := DecodePairingInvite(encoded); err == nil {
		t.Fatal("expected expired invite to be rejected")
	}
}
