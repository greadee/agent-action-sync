package identity

import (
	"errors"
	"fmt"
	"strings"
)

const MaxOSSecretBytes = 2048

var ErrOSSecretNotFound = errors.New("OS credential secret not found")

// OSSecretStore is a small generic wrapper over the same platform credential
// backend used for device identity. It deliberately has no file fallback:
// production provider credentials either live in the OS store or the operation
// fails closed.
type OSSecretStore struct {
	TargetName string
	Username   string
	backend    credentialBackend
}

func NewOSSecretStore(targetName, username string) (*OSSecretStore, error) {
	targetName = strings.TrimSpace(targetName)
	username = strings.TrimSpace(username)
	if targetName == "" || username == "" {
		return nil, errors.New("credential target and username are required")
	}
	return &OSSecretStore{TargetName: targetName, Username: username, backend: newPlatformCredentialBackend()}, nil
}

func (store *OSSecretStore) Save(secret []byte) error {
	if err := store.validate(); err != nil {
		return err
	}
	if len(secret) < 1 || len(secret) > MaxOSSecretBytes {
		return fmt.Errorf("credential secret must be between 1 and %d bytes", MaxOSSecretBytes)
	}
	if err := store.backend.Write(store.TargetName, store.Username, secret); err != nil {
		return fmt.Errorf("write OS credential: %w", err)
	}
	return nil
}

func (store *OSSecretStore) Load() ([]byte, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	secret, err := store.backend.Read(store.TargetName)
	if errors.Is(err, ErrIdentityNotFound) {
		return nil, ErrOSSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read OS credential: %w", err)
	}
	if len(secret) < 1 || len(secret) > MaxOSSecretBytes {
		clearBytes(secret)
		return nil, errors.New("stored OS credential has an invalid length")
	}
	return secret, nil
}

func (store *OSSecretStore) Delete() error {
	if err := store.validate(); err != nil {
		return err
	}
	if err := store.backend.Delete(store.TargetName); err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return fmt.Errorf("delete OS credential: %w", err)
	}
	return nil
}

func (store *OSSecretStore) validate() error {
	if store == nil || store.backend == nil || store.TargetName == "" || store.Username == "" {
		return errors.New("OS credential store is not configured")
	}
	return nil
}
