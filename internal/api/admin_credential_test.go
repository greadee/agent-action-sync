package api

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminCredentialCreationAndStableReload(t *testing.T) {
	store := &memoryAdminCredentialStore{}
	credential, err := LoadOrCreateAdminCredential(store, bytes.NewReader(bytes.Repeat([]byte{0x42}, adminCredentialRandomBytes)))
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	if err := validateAdminCredential(credential); err != nil {
		t.Fatalf("generated credential: %v", err)
	}
	reloaded, err := LoadOrCreateAdminCredential(store, bytes.NewReader(bytes.Repeat([]byte{0x99}, adminCredentialRandomBytes)))
	if err != nil {
		t.Fatalf("reload credential: %v", err)
	}
	if !bytes.Equal(credential, reloaded) {
		t.Fatal("credential changed across reload")
	}
}

func TestWindowsAdminCredentialStoreFailsClosedWhenSecretDisappears(t *testing.T) {
	directory := t.TempDir()
	backend := newMemoryAdminCredentialBackend()
	store := &WindowsAdminCredentialStore{
		TargetName: "syncgate/test", MarkerPath: filepath.Join(directory, "marker.json"), backend: backend,
	}
	credential := testAdminCredential(0x31)
	if err := store.Save(credential); err != nil {
		t.Fatalf("save: %v", err)
	}
	delete(backend.secrets, store.TargetName)
	if _, err := LoadOrCreateAdminCredential(store, bytes.NewReader(bytes.Repeat([]byte{0x44}, adminCredentialRandomBytes))); !errors.Is(err, ErrAdminCredentialMissing) {
		t.Fatalf("missing initialized secret error = %v", err)
	}
}

func TestWindowsAdminCredentialStoreRecoversMissingMarker(t *testing.T) {
	directory := t.TempDir()
	backend := newMemoryAdminCredentialBackend()
	credential := testAdminCredential(0x51)
	backend.secrets["syncgate/test"] = append([]byte(nil), credential...)
	markerPath := filepath.Join(directory, "marker.json")
	store := &WindowsAdminCredentialStore{TargetName: "syncgate/test", MarkerPath: markerPath, backend: backend}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(loaded, credential) {
		t.Fatal("loaded credential differs")
	}
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read recovered marker: %v", err)
	}
	if bytes.Contains(raw, credential) {
		t.Fatal("marker contains administration credential")
	}
}

func TestDevelopmentAdminCredentialStoreRequiresExplicitOptIn(t *testing.T) {
	options := AdminCredentialStoreOptions{DataDir: t.TempDir(), RuntimeMode: adminRuntimeDevelopment}
	if _, err := NewAdminCredentialStore(options); err == nil {
		t.Fatal("development file store was allowed without explicit opt-in")
	}
	options.AllowInsecureDevelopmentFile = true
	store, err := NewAdminCredentialStore(options)
	if err != nil {
		t.Fatalf("opted-in development store: %v", err)
	}
	if _, ok := store.(*DevelopmentAdminCredentialStore); !ok {
		t.Fatalf("store type = %T", store)
	}
}

func TestAdminCredentialIsAbsentFromMarkerTargetAndErrors(t *testing.T) {
	directory := t.TempDir()
	backend := newMemoryAdminCredentialBackend()
	markerPath := filepath.Join(directory, "admin-credential.json")
	store := &WindowsAdminCredentialStore{
		TargetName: AdminCredentialTarget(directory), MarkerPath: markerPath, backend: backend,
	}
	credential := testAdminCredential(0x72)
	if err := store.Save(credential); err != nil {
		t.Fatalf("save: %v", err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	for name, value := range map[string]string{
		"marker": string(marker), "target": store.TargetName,
		"mapped error": mapError(errors.New("storage failed with credential " + string(credential))).Message,
	} {
		if strings.Contains(value, string(credential)) {
			t.Fatalf("%s exposed administration credential", name)
		}
	}
}

func TestAdminCredentialTargetIsStableAndHidesDataDirectory(t *testing.T) {
	directory := filepath.Join("C:\\", "Users", "owner", "private")
	first := AdminCredentialTarget(directory)
	second := AdminCredentialTarget(filepath.Clean(directory))
	if first != second {
		t.Fatalf("target is unstable: %q != %q", first, second)
	}
	if strings.Contains(strings.ToLower(first), "owner") || strings.Contains(strings.ToLower(first), "private") {
		t.Fatalf("target exposes data directory: %q", first)
	}
}

type memoryAdminCredentialStore struct {
	credential []byte
	err        error
}

func (store *memoryAdminCredentialStore) Load() ([]byte, error) {
	if store.err != nil {
		return nil, store.err
	}
	if store.credential == nil {
		return nil, ErrAdminCredentialNotFound
	}
	return append([]byte(nil), store.credential...), nil
}

func (store *memoryAdminCredentialStore) Save(credential []byte) error {
	store.credential = append([]byte(nil), credential...)
	return nil
}

type memoryAdminCredentialBackend struct {
	secrets map[string][]byte
}

func newMemoryAdminCredentialBackend() *memoryAdminCredentialBackend {
	return &memoryAdminCredentialBackend{secrets: make(map[string][]byte)}
}

func (backend *memoryAdminCredentialBackend) Read(target string) ([]byte, error) {
	secret, ok := backend.secrets[target]
	if !ok {
		return nil, ErrAdminCredentialNotFound
	}
	return append([]byte(nil), secret...), nil
}

func (backend *memoryAdminCredentialBackend) Write(target, _ string, secret []byte) error {
	backend.secrets[target] = append([]byte(nil), secret...)
	return nil
}

func (backend *memoryAdminCredentialBackend) Delete(target string) error {
	delete(backend.secrets, target)
	return nil
}

func testAdminCredential(fill byte) []byte {
	return []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, adminCredentialRandomBytes)))
}
