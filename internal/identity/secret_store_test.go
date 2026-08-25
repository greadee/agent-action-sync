package identity

import (
	"bytes"
	"errors"
	"testing"
)

type secretMemoryBackend struct {
	values map[string][]byte
}

func (backend *secretMemoryBackend) Read(target string) ([]byte, error) {
	value, ok := backend.values[target]
	if !ok {
		return nil, ErrIdentityNotFound
	}
	return bytes.Clone(value), nil
}
func (backend *secretMemoryBackend) Write(target, _ string, secret []byte) error {
	backend.values[target] = bytes.Clone(secret)
	return nil
}
func (backend *secretMemoryBackend) Delete(target string) error {
	if _, ok := backend.values[target]; !ok {
		return ErrIdentityNotFound
	}
	delete(backend.values, target)
	return nil
}

func TestOSSecretStoreLifecycle(t *testing.T) {
	backend := &secretMemoryBackend{values: map[string][]byte{}}
	store := &OSSecretStore{TargetName: "syncgate/provider/test", Username: "test", backend: backend}
	if _, err := store.Load(); !errors.Is(err, ErrOSSecretNotFound) {
		t.Fatalf("missing secret error = %v", err)
	}
	secret := []byte("provider-secret-value")
	if err := store.Save(secret); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || !bytes.Equal(loaded, secret) {
		t.Fatalf("loaded secret = %q err=%v", loaded, err)
	}
	clearBytes(loaded)
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrOSSecretNotFound) {
		t.Fatalf("deleted secret error = %v", err)
	}
}

func TestOSSecretStoreRejectsOversizedSecret(t *testing.T) {
	store := &OSSecretStore{TargetName: "target", Username: "user", backend: &secretMemoryBackend{values: map[string][]byte{}}}
	if err := store.Save(make([]byte, MaxOSSecretBytes+1)); err == nil {
		t.Fatal("expected oversized secret to fail")
	}
}
