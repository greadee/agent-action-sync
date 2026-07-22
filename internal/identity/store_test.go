package identity

import (
	"bytes"
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

func TestDevFileStoreRoundTrip(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	identity, err := FromKeyPair(publicKey, privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}

	store := DevFileStore{Path: filepath.Join(t.TempDir(), "identity.json")}
	if err := store.Save(identity); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DeviceID != identity.DeviceID {
		t.Fatalf("device ID = %q, want %q", got.DeviceID, identity.DeviceID)
	}
	if got.Fingerprint != identity.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", got.Fingerprint, identity.Fingerprint)
	}
}
