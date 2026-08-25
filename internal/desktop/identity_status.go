package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type IdentityStatus struct {
	State           string `json:"state"`
	CredentialStore string `json:"credential_store"`
	DeviceID        string `json:"device_id,omitempty"`
	Fingerprint     string `json:"fingerprint,omitempty"`
}

type identityPublicMetadata struct {
	DeviceID    string `json:"device_id"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
}

func ReadIdentityStatus(dataDir, storeName string) (IdentityStatus, error) {
	path := filepath.Join(dataDir, "identity-public.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return IdentityStatus{State: "uninitialized", CredentialStore: storeName}, nil
	}
	if err != nil {
		return IdentityStatus{}, fmt.Errorf("open public identity metadata: %w", err)
	}
	defer file.Close()
	var metadata identityPublicMetadata
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return IdentityStatus{}, fmt.Errorf("parse public identity metadata: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return IdentityStatus{}, errors.New("public identity metadata contains trailing data")
	}
	if metadata.DeviceID == "" || metadata.PublicKey == "" || metadata.Fingerprint == "" {
		return IdentityStatus{}, errors.New("public identity metadata is incomplete")
	}
	return IdentityStatus{
		State: "initialized", CredentialStore: storeName,
		DeviceID: metadata.DeviceID, Fingerprint: metadata.Fingerprint,
	}, nil
}
