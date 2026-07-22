package identity

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestGenerateDeviceIdentity(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)

	identity, err := FromKeyPair(publicKey, privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	if identity.DeviceID == "" {
		t.Fatal("expected device ID")
	}
	if identity.Fingerprint == "" {
		t.Fatal("expected fingerprint")
	}
	if !strings.Contains(identity.Fingerprint, "-") {
		t.Fatalf("fingerprint should be grouped, got %q", identity.Fingerprint)
	}
}

func TestFromKeyPairRejectsMismatchedKeys(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize))
	otherPrivateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, ed25519.SeedSize))
	publicKey := otherPrivateKey.Public().(ed25519.PublicKey)

	if _, err := FromKeyPair(publicKey, privateKey); err == nil {
		t.Fatal("expected mismatched public and private key to be rejected")
	}
}

func TestGeneratePairingCode(t *testing.T) {
	code, err := GeneratePairingCode(bytes.NewReader(bytes.Repeat([]byte{9}, PairingCodeByteCount)))
	if err != nil {
		t.Fatalf("GeneratePairingCode: %v", err)
	}
	if len(strings.Split(code, "-")) != 4 {
		t.Fatalf("expected grouped pairing code, got %q", code)
	}
}
