package desktop

import (
	"bytes"
	"errors"
	"testing"

	"syncgate/internal/identity"
)

type memorySecretStore struct{ secret []byte }

func (store *memorySecretStore) Save(secret []byte) error {
	store.secret = bytes.Clone(secret)
	return nil
}
func (store *memorySecretStore) Load() ([]byte, error) {
	if len(store.secret) == 0 {
		return nil, identity.ErrOSSecretNotFound
	}
	return bytes.Clone(store.secret), nil
}
func (store *memorySecretStore) Delete() error {
	store.secret = nil
	return nil
}

func TestProviderCredentialsNeverReturnSecret(t *testing.T) {
	store := &memorySecretStore{}
	service := ProviderCredentials{
		ScopeRoot: t.TempDir(),
		Open:      func(_, _ string) (CredentialStore, error) { return store, nil },
	}
	secret := []byte("provider-key-super-secret")
	status, err := service.Set("codex", secret)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Storage != "os_credential_store" || status.OpaqueTargetID == "" {
		t.Fatalf("credential status = %#v", status)
	}
	encoded := []byte(status.ProviderID + status.Storage + status.OpaqueTargetID)
	if bytes.Contains(encoded, secret) {
		t.Fatal("credential status contains secret")
	}
	status, err = service.Status("codex")
	if err != nil || !status.Configured {
		t.Fatalf("credential status = %#v err=%v", status, err)
	}
	status, err = service.Delete("codex")
	if err != nil || status.Configured {
		t.Fatalf("deleted status = %#v err=%v", status, err)
	}
}

func TestProviderCredentialsReportMissingWithoutFileFallback(t *testing.T) {
	service := ProviderCredentials{
		ScopeRoot: t.TempDir(),
		Open:      func(_, _ string) (CredentialStore, error) { return &memorySecretStore{}, nil },
	}
	status, err := service.Status("codex")
	if err != nil || status.Configured {
		t.Fatalf("missing status = %#v err=%v", status, err)
	}
	if _, err := service.Status("Codex"); err == nil || errors.Is(err, identity.ErrOSSecretNotFound) {
		t.Fatalf("invalid provider error = %v", err)
	}
}
