package identity

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsCredentialStoreRoundTripKeepsPrivateKeyOutOfMetadata(t *testing.T) {
	backend := newMemoryCredentialBackend()
	metadataPath := filepath.Join(t.TempDir(), "identity-public.json")
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: metadataPath,
		backend:      backend,
	}
	deviceIdentity := deterministicIdentity(t, 7)
	if err := store.Save(deviceIdentity); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.DeviceID != deviceIdentity.DeviceID || !bytes.Equal(loaded.PrivateKey, deviceIdentity.PrivateKey) {
		t.Fatalf("loaded identity does not match saved identity")
	}

	metadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	privateEncoding := base64.StdEncoding.EncodeToString(deviceIdentity.PrivateKey)
	if bytes.Contains(metadata, []byte(privateEncoding)) || bytes.Contains(metadata, []byte("private_key")) {
		t.Fatalf("public metadata contains private key material: %s", metadata)
	}
	if !bytes.Equal(backend.secrets[store.TargetName], deviceIdentity.PrivateKey) {
		t.Fatal("credential backend did not receive private key")
	}
}

func TestWindowsCredentialStoreFailsClosedWhenSecretIsMissingOrMismatched(t *testing.T) {
	backend := newMemoryCredentialBackend()
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(t.TempDir(), "identity-public.json"),
		backend:      backend,
	}
	first := deterministicIdentity(t, 8)
	if err := store.Save(first); err != nil {
		t.Fatalf("Save: %v", err)
	}
	delete(backend.secrets, store.TargetName)
	if _, err := store.Load(); !errors.Is(err, ErrIdentitySecretMissing) {
		t.Fatalf("missing secret error = %v, want %v", err, ErrIdentitySecretMissing)
	}
	backend.secrets[store.TargetName] = append([]byte(nil), deterministicIdentity(t, 9).PrivateKey...)
	if _, err := store.Load(); !errors.Is(err, ErrIdentityMetadataMismatch) {
		t.Fatalf("mismatched secret error = %v, want %v", err, ErrIdentityMetadataMismatch)
	}
}

func TestWindowsCredentialStoreRequiresExplicitLegacyMigration(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "identity.json")
	if err := (DevFileStore{Path: legacyPath}).Save(deterministicIdentity(t, 10)); err != nil {
		t.Fatalf("save legacy identity: %v", err)
	}
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(filepath.Dir(legacyPath), "identity-public.json"),
		LegacyPath:   legacyPath,
		backend:      newMemoryCredentialBackend(),
	}
	if _, err := store.Load(); !errors.Is(err, ErrIdentityMigrationNeeded) {
		t.Fatalf("legacy load error = %v, want %v", err, ErrIdentityMigrationNeeded)
	}
}

func TestMigrateDevelopmentIdentityVerifiesCredentialAndRemovesPlaintext(t *testing.T) {
	directory := t.TempDir()
	legacy := DevFileStore{Path: filepath.Join(directory, "identity.json")}
	original := deterministicIdentity(t, 11)
	if err := legacy.Save(original); err != nil {
		t.Fatalf("save legacy identity: %v", err)
	}
	production := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(directory, "identity-public.json"),
		backend:      newMemoryCredentialBackend(),
	}
	migrated, err := MigrateDevelopmentIdentity(legacy, production)
	if err != nil {
		t.Fatalf("MigrateDevelopmentIdentity: %v", err)
	}
	if migrated.DeviceID != original.DeviceID {
		t.Fatalf("migrated device ID = %s, want %s", migrated.DeviceID, original.DeviceID)
	}
	if _, err := os.Stat(legacy.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy plaintext still exists: %v", err)
	}
}

func TestMigrateDevelopmentIdentityRefusesToOverwriteProductionIdentity(t *testing.T) {
	directory := t.TempDir()
	legacy := DevFileStore{Path: filepath.Join(directory, "identity.json")}
	legacyIdentity := deterministicIdentity(t, 21)
	if err := legacy.Save(legacyIdentity); err != nil {
		t.Fatalf("save legacy identity: %v", err)
	}
	production := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(directory, "identity-public.json"),
		backend:      newMemoryCredentialBackend(),
	}
	productionIdentity := deterministicIdentity(t, 22)
	if err := production.Save(productionIdentity); err != nil {
		t.Fatalf("save production identity: %v", err)
	}

	if _, err := MigrateDevelopmentIdentity(legacy, production); err == nil {
		t.Fatal("expected migration over a different production identity to fail")
	}
	loaded, err := production.Load()
	if err != nil {
		t.Fatalf("load production identity: %v", err)
	}
	if loaded.DeviceID != productionIdentity.DeviceID {
		t.Fatalf("migration replaced production identity with %s", loaded.DeviceID)
	}
	if _, err := os.Stat(legacy.Path); err != nil {
		t.Fatalf("failed migration removed legacy identity: %v", err)
	}
}

func TestWindowsCredentialStoreRecoversMissingPublicMetadata(t *testing.T) {
	backend := newMemoryCredentialBackend()
	metadataPath := filepath.Join(t.TempDir(), "identity-public.json")
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: metadataPath,
		backend:      backend,
	}
	original := deterministicIdentity(t, 23)
	backend.secrets[store.TargetName] = append([]byte(nil), original.PrivateKey...)

	recovered, err := store.Load()
	if err != nil {
		t.Fatalf("recover metadata: %v", err)
	}
	if recovered.DeviceID != original.DeviceID {
		t.Fatalf("recovered identity = %s, want %s", recovered.DeviceID, original.DeviceID)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("recovered metadata was not written: %v", err)
	}
}

func TestRotateIdentityReplacesCredentialAndPublicMetadata(t *testing.T) {
	backend := newMemoryCredentialBackend()
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(t.TempDir(), "identity-public.json"),
		backend:      backend,
	}
	original := deterministicIdentity(t, 24)
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rotated, err := RotateIdentity(store, bytes.NewReader(bytes.Repeat([]byte{25}, 64)))
	if err != nil {
		t.Fatalf("RotateIdentity: %v", err)
	}
	if rotated.DeviceID == original.DeviceID {
		t.Fatal("rotation retained the old device identity")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load rotated identity: %v", err)
	}
	if loaded.DeviceID != rotated.DeviceID {
		t.Fatalf("loaded identity = %s, want rotated %s", loaded.DeviceID, rotated.DeviceID)
	}
}

func TestRotateIdentityPreservesOldCredentialOnWriteFailure(t *testing.T) {
	backend := newMemoryCredentialBackend()
	store := &WindowsCredentialStore{
		TargetName:   "syncgate/test",
		MetadataPath: filepath.Join(t.TempDir(), "identity-public.json"),
		backend:      backend,
	}
	original := deterministicIdentity(t, 12)
	if err := store.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	backend.writeErr = errors.New("credential vault unavailable")
	if _, err := RotateIdentity(store, bytes.NewReader(bytes.Repeat([]byte{13}, 64))); err == nil {
		t.Fatal("expected rotation failure")
	}
	backend.writeErr = nil
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load original after failed rotation: %v", err)
	}
	if loaded.DeviceID != original.DeviceID {
		t.Fatalf("failed rotation changed identity to %s", loaded.DeviceID)
	}
}

func TestCredentialTargetDoesNotExposeDataDirectory(t *testing.T) {
	dataDir := `C:\Users\alex\Sensitive SyncGate State`
	target := CredentialTarget(dataDir)
	if strings.Contains(strings.ToLower(target), "alex") || strings.Contains(target, dataDir) {
		t.Fatalf("credential target exposes data directory: %q", target)
	}
}

func deterministicIdentity(t *testing.T, value byte) DeviceIdentity {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{value}, ed25519.SeedSize))
	identity, err := FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	return identity
}

type memoryCredentialBackend struct {
	secrets   map[string][]byte
	writeErr  error
	readErr   error
	deleteErr error
}

func newMemoryCredentialBackend() *memoryCredentialBackend {
	return &memoryCredentialBackend{secrets: make(map[string][]byte)}
}

func (backend *memoryCredentialBackend) Read(target string) ([]byte, error) {
	if backend.readErr != nil {
		return nil, backend.readErr
	}
	secret, ok := backend.secrets[target]
	if !ok {
		return nil, ErrIdentityNotFound
	}
	return append([]byte(nil), secret...), nil
}

func (backend *memoryCredentialBackend) Write(target, _ string, secret []byte) error {
	if backend.writeErr != nil {
		return backend.writeErr
	}
	backend.secrets[target] = append([]byte(nil), secret...)
	return nil
}

func (backend *memoryCredentialBackend) Delete(target string) error {
	if backend.deleteErr != nil {
		return backend.deleteErr
	}
	if _, ok := backend.secrets[target]; !ok {
		return ErrIdentityNotFound
	}
	delete(backend.secrets, target)
	return nil
}
