package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrIdentityNotFound           = errors.New("identity secret not found")
	ErrIdentitySecretMissing      = errors.New("identity public metadata exists but private secret is missing")
	ErrIdentityMetadataMismatch   = errors.New("identity public metadata does not match private secret")
	ErrIdentityMigrationNeeded    = errors.New("development identity requires explicit credential-store migration")
	ErrCredentialStoreUnavailable = errors.New("Windows credential store unavailable")
)

type IdentitySecretDeleter interface {
	Delete() error
}

type credentialBackend interface {
	Read(target string) ([]byte, error)
	Write(target, username string, secret []byte) error
	Delete(target string) error
}

type WindowsCredentialStoreOptions struct {
	TargetName   string
	MetadataPath string
	LegacyPath   string
}

type WindowsCredentialStore struct {
	TargetName   string
	MetadataPath string
	LegacyPath   string
	backend      credentialBackend
}

type publicIdentityMetadata struct {
	DeviceID    string `json:"device_id"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

func NewWindowsCredentialStore(options WindowsCredentialStoreOptions) (*WindowsCredentialStore, error) {
	if options.TargetName == "" {
		return nil, errors.New("credential target name is required")
	}
	if options.MetadataPath == "" {
		return nil, errors.New("identity metadata path is required")
	}
	return &WindowsCredentialStore{
		TargetName:   options.TargetName,
		MetadataPath: options.MetadataPath,
		LegacyPath:   options.LegacyPath,
		backend:      newPlatformCredentialBackend(),
	}, nil
}

func CredentialTarget(dataDir string) string {
	normalized := filepath.Clean(dataDir)
	if absolute, err := filepath.Abs(normalized); err == nil {
		normalized = absolute
	}
	// Windows paths are case-insensitive. Normalizing case prevents an identity
	// from being orphaned when only the spelling of the configured path changes.
	normalized = strings.ToLower(normalized)
	sum := sha256.Sum256([]byte(normalized))
	return "syncgate/device-identity/" + hex.EncodeToString(sum[:16])
}

func (store *WindowsCredentialStore) Save(deviceIdentity DeviceIdentity) error {
	if err := store.validate(); err != nil {
		return err
	}
	validated, err := FromKeyPair(deviceIdentity.PublicKey, deviceIdentity.PrivateKey)
	if err != nil {
		return fmt.Errorf("validate identity before credential write: %w", err)
	}

	oldSecret, oldErr := store.backend.Read(store.TargetName)
	if oldErr != nil && !errors.Is(oldErr, ErrIdentityNotFound) {
		return fmt.Errorf("read previous credential: %w", oldErr)
	}
	defer clearBytes(oldSecret)
	if err := store.backend.Write(store.TargetName, string(validated.DeviceID), validated.PrivateKey); err != nil {
		return fmt.Errorf("write identity credential: %w", err)
	}
	if err := writePublicIdentityMetadata(store.MetadataPath, validated); err != nil {
		rollbackErr := store.rollbackCredential(oldSecret, oldErr)
		if rollbackErr != nil {
			return fmt.Errorf("write identity metadata: %w; credential rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("write identity metadata: %w", err)
	}
	return nil
}

func (store *WindowsCredentialStore) Load() (DeviceIdentity, error) {
	if err := store.validate(); err != nil {
		return DeviceIdentity{}, err
	}
	metadata, metadataErr := readPublicIdentityMetadata(store.MetadataPath)
	secret, err := store.backend.Read(store.TargetName)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			switch {
			case metadataErr == nil:
				return DeviceIdentity{}, ErrIdentitySecretMissing
			case errors.Is(metadataErr, os.ErrNotExist) && store.legacyIdentityExists():
				return DeviceIdentity{}, ErrIdentityMigrationNeeded
			case errors.Is(metadataErr, os.ErrNotExist):
				return DeviceIdentity{}, ErrIdentityNotFound
			}
		}
		return DeviceIdentity{}, fmt.Errorf("read identity credential: %w", err)
	}
	defer clearBytes(secret)
	if len(secret) != ed25519.PrivateKeySize {
		return DeviceIdentity{}, fmt.Errorf("identity credential has invalid private key length %d", len(secret))
	}
	privateKey := ed25519.PrivateKey(append([]byte(nil), secret...))
	deviceIdentity, err := FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("decode identity credential: %w", err)
	}
	if metadataErr != nil {
		if !errors.Is(metadataErr, os.ErrNotExist) {
			return DeviceIdentity{}, metadataErr
		}
		if err := writePublicIdentityMetadata(store.MetadataPath, deviceIdentity); err != nil {
			return DeviceIdentity{}, fmt.Errorf("recover identity metadata: %w", err)
		}
		return deviceIdentity, nil
	}
	if metadata.DeviceID != string(deviceIdentity.DeviceID) ||
		metadata.Fingerprint != deviceIdentity.Fingerprint ||
		metadata.PublicKey != base64.StdEncoding.EncodeToString(deviceIdentity.PublicKey) {
		return DeviceIdentity{}, ErrIdentityMetadataMismatch
	}
	return deviceIdentity, nil
}

func (store *WindowsCredentialStore) Delete() error {
	if err := store.validate(); err != nil {
		return err
	}
	if err := store.backend.Delete(store.TargetName); err != nil && !errors.Is(err, ErrIdentityNotFound) {
		return fmt.Errorf("delete identity credential: %w", err)
	}
	if err := os.Remove(store.MetadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete identity metadata: %w", err)
	}
	return nil
}

func RotateIdentity(store PrivateKeyStore, reader io.Reader) (DeviceIdentity, error) {
	if store == nil {
		return DeviceIdentity{}, errors.New("identity store is required")
	}
	rotated, err := GenerateDeviceIdentity(reader)
	if err != nil {
		return DeviceIdentity{}, err
	}
	if err := store.Save(rotated); err != nil {
		return DeviceIdentity{}, fmt.Errorf("save rotated identity: %w", err)
	}
	return rotated, nil
}

func MigrateDevelopmentIdentity(legacy DevFileStore, production PrivateKeyStore) (DeviceIdentity, error) {
	deleter, ok := production.(IdentitySecretDeleter)
	if !ok {
		return DeviceIdentity{}, errors.New("production identity store must support rollback deletion")
	}
	deviceIdentity, err := legacy.Load()
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("load development identity for migration: %w", err)
	}
	existing, existingErr := production.Load()
	switch {
	case existingErr == nil:
		if existing.DeviceID != deviceIdentity.DeviceID ||
			!publicKeysEqual(existing.PublicKey, deviceIdentity.PublicKey) {
			return DeviceIdentity{}, errors.New("refusing to overwrite a different production identity")
		}
		if err := legacy.Delete(); err != nil {
			return DeviceIdentity{}, fmt.Errorf("remove redundant development identity after migration: %w", err)
		}
		return existing, nil
	case errors.Is(existingErr, ErrIdentityNotFound), errors.Is(existingErr, ErrIdentityMigrationNeeded):
		// The destination is empty. ErrIdentityMigrationNeeded is expected when
		// the production store is configured to detect this legacy file.
	default:
		return DeviceIdentity{}, fmt.Errorf("inspect production identity before migration: %w", existingErr)
	}
	if err := production.Save(deviceIdentity); err != nil {
		return DeviceIdentity{}, fmt.Errorf("save production identity during migration: %w", err)
	}
	verified, err := production.Load()
	if err != nil || verified.DeviceID != deviceIdentity.DeviceID {
		_ = deleter.Delete()
		if err != nil {
			return DeviceIdentity{}, fmt.Errorf("verify migrated identity: %w", err)
		}
		return DeviceIdentity{}, errors.New("verify migrated identity: device ID changed")
	}
	if err := legacy.Delete(); err != nil {
		_ = deleter.Delete()
		return DeviceIdentity{}, fmt.Errorf("remove development identity after migration: %w", err)
	}
	return verified, nil
}

func (store *WindowsCredentialStore) validate() error {
	if store == nil || store.backend == nil {
		return errors.New("credential backend is required")
	}
	if store.TargetName == "" || store.MetadataPath == "" {
		return errors.New("credential target and identity metadata path are required")
	}
	return nil
}

func (store *WindowsCredentialStore) rollbackCredential(oldSecret []byte, oldErr error) error {
	if oldErr == nil {
		privateKey := ed25519.PrivateKey(oldSecret)
		if len(privateKey) != ed25519.PrivateKeySize {
			return errors.New("previous credential has invalid private key length")
		}
		deviceID := DeviceIDFromFingerprint(Fingerprint(privateKey.Public().(ed25519.PublicKey)))
		return store.backend.Write(store.TargetName, string(deviceID), oldSecret)
	}
	return store.backend.Delete(store.TargetName)
}

func (store *WindowsCredentialStore) legacyIdentityExists() bool {
	if store.LegacyPath == "" {
		return false
	}
	_, err := os.Stat(store.LegacyPath)
	return err == nil
}

func writePublicIdentityMetadata(path string, deviceIdentity DeviceIdentity) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create identity metadata directory: %w", err)
	}
	payload := publicIdentityMetadata{
		DeviceID:    string(deviceIdentity.DeviceID),
		PublicKey:   base64.StdEncoding.EncodeToString(deviceIdentity.PublicKey),
		Fingerprint: deviceIdentity.Fingerprint,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity metadata: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".identity-public-*")
	if err != nil {
		return fmt.Errorf("create temporary identity metadata: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure identity metadata: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write identity metadata: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush identity metadata: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close identity metadata: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit identity metadata: %w", err)
	}
	return nil
}

func readPublicIdentityMetadata(path string) (publicIdentityMetadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return publicIdentityMetadata{}, err
	}
	var metadata publicIdentityMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return publicIdentityMetadata{}, fmt.Errorf("parse identity metadata: %w", err)
	}
	return metadata, nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
