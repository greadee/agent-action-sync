package api

import (
	"crypto/rand"
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

const (
	adminCredentialRandomBytes = 32
	adminCredentialVersion     = 1
	adminRuntimeProduction     = "production"
	adminRuntimeDevelopment    = "development"
)

var (
	ErrAdminCredentialNotFound         = errors.New("local administration credential not found")
	ErrAdminCredentialMissing          = errors.New("local administration credential is missing after initialization")
	ErrAdminCredentialStoreUnavailable = errors.New("local administration credential store unavailable")
)

type AdminCredentialStore interface {
	Load() ([]byte, error)
	Save([]byte) error
}

type AdminCredentialStoreOptions struct {
	DataDir                      string
	RuntimeMode                  string
	AllowInsecureDevelopmentFile bool
}

type adminCredentialBackend interface {
	Read(target string) ([]byte, error)
	Write(target, username string, secret []byte) error
	Delete(target string) error
}

type WindowsAdminCredentialStore struct {
	TargetName string
	MarkerPath string
	backend    adminCredentialBackend
}

type DevelopmentAdminCredentialStore struct {
	Path string
}

type adminCredentialMarker struct {
	Version int `json:"version"`
}

func NewAdminCredentialStore(options AdminCredentialStoreOptions) (AdminCredentialStore, error) {
	if strings.TrimSpace(options.DataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	switch options.RuntimeMode {
	case adminRuntimeProduction:
		return NewWindowsAdminCredentialStore(
			AdminCredentialTarget(options.DataDir),
			filepath.Join(options.DataDir, "admin-credential.json"),
		)
	case adminRuntimeDevelopment:
		if !options.AllowInsecureDevelopmentFile {
			return nil, errors.New("development credential file requires explicit insecure-storage opt-in")
		}
		return &DevelopmentAdminCredentialStore{Path: filepath.Join(options.DataDir, "admin-credential.development")}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime mode %q", options.RuntimeMode)
	}
}

func NewWindowsAdminCredentialStore(targetName, markerPath string) (*WindowsAdminCredentialStore, error) {
	if strings.TrimSpace(targetName) == "" {
		return nil, errors.New("credential target name is required")
	}
	if strings.TrimSpace(markerPath) == "" {
		return nil, errors.New("credential marker path is required")
	}
	return &WindowsAdminCredentialStore{
		TargetName: targetName,
		MarkerPath: markerPath,
		backend:    newAdminCredentialBackend(),
	}, nil
}

func AdminCredentialTarget(dataDir string) string {
	normalized := filepath.Clean(dataDir)
	if absolute, err := filepath.Abs(normalized); err == nil {
		normalized = absolute
	}
	normalized = strings.ToLower(normalized)
	digest := sha256.Sum256([]byte(normalized))
	return "syncgate/local-admin/" + hex.EncodeToString(digest[:16])
}

func LoadOrCreateAdminCredential(store AdminCredentialStore, source io.Reader) ([]byte, error) {
	if store == nil {
		return nil, errors.New("administration credential store is required")
	}
	credential, err := store.Load()
	if err == nil {
		return credential, nil
	}
	if !errors.Is(err, ErrAdminCredentialNotFound) {
		return nil, fmt.Errorf("load administration credential: %w", err)
	}
	if source == nil {
		source = rand.Reader
	}
	randomBytes := make([]byte, adminCredentialRandomBytes)
	if _, err := io.ReadFull(source, randomBytes); err != nil {
		return nil, fmt.Errorf("generate administration credential: %w", err)
	}
	defer clearCredential(randomBytes)
	credential = []byte(base64.RawURLEncoding.EncodeToString(randomBytes))
	if err := store.Save(credential); err != nil {
		clearCredential(credential)
		return nil, fmt.Errorf("save administration credential: %w", err)
	}
	return credential, nil
}

func LoadAdminCredential(store AdminCredentialStore) ([]byte, error) {
	if store == nil {
		return nil, errors.New("administration credential store is required")
	}
	credential, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("load administration credential: %w", err)
	}
	return credential, nil
}

func (store *WindowsAdminCredentialStore) Load() ([]byte, error) {
	if err := store.validate(); err != nil {
		return nil, err
	}
	_, markerErr := readAdminCredentialMarker(store.MarkerPath)
	credential, err := store.backend.Read(store.TargetName)
	if err != nil {
		if errors.Is(err, ErrAdminCredentialNotFound) {
			if markerErr == nil {
				return nil, ErrAdminCredentialMissing
			}
			if errors.Is(markerErr, os.ErrNotExist) {
				return nil, ErrAdminCredentialNotFound
			}
		}
		return nil, fmt.Errorf("read administration credential: %w", err)
	}
	if err := validateAdminCredential(credential); err != nil {
		clearCredential(credential)
		return nil, fmt.Errorf("validate stored administration credential: %w", err)
	}
	if markerErr != nil {
		if !errors.Is(markerErr, os.ErrNotExist) {
			clearCredential(credential)
			return nil, markerErr
		}
		if err := writeAdminCredentialMarker(store.MarkerPath); err != nil {
			clearCredential(credential)
			return nil, fmt.Errorf("recover administration credential marker: %w", err)
		}
	}
	return credential, nil
}

func (store *WindowsAdminCredentialStore) Save(credential []byte) error {
	if err := store.validate(); err != nil {
		return err
	}
	if err := validateAdminCredential(credential); err != nil {
		return err
	}
	previous, previousErr := store.backend.Read(store.TargetName)
	if previousErr != nil && !errors.Is(previousErr, ErrAdminCredentialNotFound) {
		return fmt.Errorf("read previous administration credential: %w", previousErr)
	}
	defer clearCredential(previous)
	if err := store.backend.Write(store.TargetName, "local-admin", credential); err != nil {
		return fmt.Errorf("write administration credential: %w", err)
	}
	if err := writeAdminCredentialMarker(store.MarkerPath); err != nil {
		rollbackErr := store.rollback(previous, previousErr)
		if rollbackErr != nil {
			return fmt.Errorf("write administration credential marker: %w; rollback failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("write administration credential marker: %w", err)
	}
	return nil
}

func (store *WindowsAdminCredentialStore) validate() error {
	if store == nil || store.backend == nil {
		return errors.New("administration credential backend is required")
	}
	if store.TargetName == "" || store.MarkerPath == "" {
		return errors.New("administration credential target and marker path are required")
	}
	return nil
}

func (store *WindowsAdminCredentialStore) rollback(previous []byte, previousErr error) error {
	if previousErr == nil {
		return store.backend.Write(store.TargetName, "local-admin", previous)
	}
	if err := store.backend.Delete(store.TargetName); err != nil && !errors.Is(err, ErrAdminCredentialNotFound) {
		return err
	}
	return nil
}

func (store *DevelopmentAdminCredentialStore) Load() ([]byte, error) {
	if store == nil || store.Path == "" {
		return nil, errors.New("development administration credential path is required")
	}
	credential, err := os.ReadFile(store.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrAdminCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read development administration credential: %w", err)
	}
	if err := validateAdminCredential(credential); err != nil {
		clearCredential(credential)
		return nil, err
	}
	return credential, nil
}

func (store *DevelopmentAdminCredentialStore) Save(credential []byte) error {
	if store == nil || store.Path == "" {
		return errors.New("development administration credential path is required")
	}
	if err := validateAdminCredential(credential); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return fmt.Errorf("create development administration credential directory: %w", err)
	}
	if err := os.WriteFile(store.Path, credential, 0o600); err != nil {
		return fmt.Errorf("write development administration credential: %w", err)
	}
	return nil
}

func validateAdminCredential(credential []byte) error {
	if len(credential) != base64.RawURLEncoding.EncodedLen(adminCredentialRandomBytes) {
		return errors.New("administration credential has invalid length")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(string(credential))
	if err != nil || len(decoded) != adminCredentialRandomBytes {
		return errors.New("administration credential has invalid encoding")
	}
	clearCredential(decoded)
	return nil
}

func readAdminCredentialMarker(path string) (adminCredentialMarker, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return adminCredentialMarker{}, err
	}
	var marker adminCredentialMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return adminCredentialMarker{}, fmt.Errorf("parse administration credential marker: %w", err)
	}
	if marker.Version != adminCredentialVersion {
		return adminCredentialMarker{}, fmt.Errorf("unsupported administration credential marker version %d", marker.Version)
	}
	return marker, nil
}

func writeAdminCredentialMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create administration credential marker directory: %w", err)
	}
	raw, err := json.Marshal(adminCredentialMarker{Version: adminCredentialVersion})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".admin-credential-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func clearCredential(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
