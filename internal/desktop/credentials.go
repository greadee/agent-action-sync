package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"syncgate/internal/identity"
)

type CredentialStore interface {
	Save([]byte) error
	Load() ([]byte, error)
	Delete() error
}

type CredentialStatus struct {
	ProviderID     string `json:"provider_id"`
	Configured     bool   `json:"configured"`
	Storage        string `json:"storage"`
	OpaqueTargetID string `json:"opaque_target_id"`
}

type ProviderCredentials struct {
	ScopeRoot string
	Open      func(target, providerID string) (CredentialStore, error)
}

func (service ProviderCredentials) Set(providerID string, secret []byte) (CredentialStatus, error) {
	store, status, err := service.open(providerID)
	if err != nil {
		return CredentialStatus{}, err
	}
	if len(secret) < 1 || len(secret) > identity.MaxOSSecretBytes {
		return CredentialStatus{}, fmt.Errorf("provider credential must be between 1 and %d bytes", identity.MaxOSSecretBytes)
	}
	if err := store.Save(secret); err != nil {
		return CredentialStatus{}, err
	}
	status.Configured = true
	return status, nil
}

func (service ProviderCredentials) Status(providerID string) (CredentialStatus, error) {
	store, status, err := service.open(providerID)
	if err != nil {
		return CredentialStatus{}, err
	}
	secret, err := store.Load()
	if errors.Is(err, identity.ErrOSSecretNotFound) {
		return status, nil
	}
	if err != nil {
		return CredentialStatus{}, err
	}
	clearSecret(secret)
	status.Configured = true
	return status, nil
}

func (service ProviderCredentials) Delete(providerID string) (CredentialStatus, error) {
	store, status, err := service.open(providerID)
	if err != nil {
		return CredentialStatus{}, err
	}
	if err := store.Delete(); err != nil {
		return CredentialStatus{}, err
	}
	return status, nil
}

func (service ProviderCredentials) open(providerID string) (CredentialStore, CredentialStatus, error) {
	providerID = strings.TrimSpace(providerID)
	if !desktopIdentifier(providerID) {
		return nil, CredentialStatus{}, errors.New("provider ID must be a lowercase identifier")
	}
	scopeRoot := filepath.Clean(strings.TrimSpace(service.ScopeRoot))
	if !filepath.IsAbs(scopeRoot) {
		return nil, CredentialStatus{}, errors.New("provider credential node scope must be absolute")
	}
	targetDigest := sha256.Sum256([]byte(strings.ToLower(scopeRoot) + "\x00" + providerID))
	target := "syncgate/provider/" + hex.EncodeToString(targetDigest[:16]) + "/" + providerID
	opaque := sha256.Sum256([]byte(target))
	open := service.Open
	if open == nil {
		open = func(target, username string) (CredentialStore, error) {
			return identity.NewOSSecretStore(target, username)
		}
	}
	store, err := open(target, providerID)
	if err != nil {
		return nil, CredentialStatus{}, err
	}
	return store, CredentialStatus{
		ProviderID: providerID, Storage: "os_credential_store",
		OpaqueTargetID: "credential:" + hex.EncodeToString(opaque[:12]),
	}, nil
}

func desktopIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || index > 0 && character == '-' {
			continue
		}
		return false
	}
	return true
}

func clearSecret(secret []byte) {
	for index := range secret {
		secret[index] = 0
	}
}
