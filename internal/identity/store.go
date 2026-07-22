package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type PrivateKeyStore interface {
	Save(identity DeviceIdentity) error
	Load() (DeviceIdentity, error)
}

type DevFileStore struct {
	Path string
}

type persistedIdentity struct {
	DeviceID    string `json:"device_id"`
	PublicKey   string `json:"public_key"`
	PrivateKey  string `json:"private_key"`
	Fingerprint string `json:"fingerprint"`
	Warning     string `json:"warning"`
}

func (store DevFileStore) Save(identity DeviceIdentity) error {
	if store.Path == "" {
		return fmt.Errorf("identity store path is required")
	}
	if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
		return fmt.Errorf("create identity store directory: %w", err)
	}

	payload := persistedIdentity{
		DeviceID:    string(identity.DeviceID),
		PublicKey:   base64.StdEncoding.EncodeToString(identity.PublicKey),
		PrivateKey:  base64.StdEncoding.EncodeToString(identity.PrivateKey),
		Fingerprint: identity.Fingerprint,
		Warning:     "development fallback only; replace with OS credential storage before production use",
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}
	if err := os.WriteFile(store.Path, raw, 0o600); err != nil {
		return fmt.Errorf("write identity file: %w", err)
	}
	return nil
}

func (store DevFileStore) Load() (DeviceIdentity, error) {
	raw, err := os.ReadFile(store.Path)
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("read identity file: %w", err)
	}
	var payload persistedIdentity
	if err := json.Unmarshal(raw, &payload); err != nil {
		return DeviceIdentity{}, fmt.Errorf("parse identity file: %w", err)
	}

	publicKey, err := base64.StdEncoding.DecodeString(payload.PublicKey)
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("decode public key: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(payload.PrivateKey)
	if err != nil {
		return DeviceIdentity{}, fmt.Errorf("decode private key: %w", err)
	}
	return FromKeyPair(ed25519.PublicKey(publicKey), ed25519.PrivateKey(privateKey))
}
