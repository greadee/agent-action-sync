package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/config"
)

func TestDiagnosticsExportContainsOnlySanitizedStatusAndOpaqueIDs(t *testing.T) {
	settings := initializedSettingsManager(t)
	cfg, roots, err := settings.Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	shareID := "sensitive-client-project"
	shareName := "Confidential Client Name"
	shareRoot := filepath.Join(t.TempDir(), "private-project")
	if err := os.MkdirAll(shareRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg.Shares = []config.ShareConfig{{ID: shareID, Name: shareName, RootPath: shareRoot, Mode: "read_only"}}
	runtimePath := filepath.Join(t.TempDir(), "private-runtime.exe")
	if err := os.WriteFile(runtimePath, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := strings.Repeat("a", 64)
	cfg.Node.Execution = config.ExecutionConfig{ProviderID: "codex", RuntimeExecutable: runtimePath, PreflightReceipt: receipt}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		t.Fatal(err)
	}
	if err := activateDirect(settings.Base, cfg); err != nil {
		t.Fatal(err)
	}
	deviceID := "device-sensitive-identity"
	fingerprint := "sensitive-fingerprint"
	metadata := map[string]string{"device_id": deviceID, "public_key": "public-key-material", "fingerprint": fingerprint}
	if err := writeJSONAtomic(filepath.Join(roots.DataDir, "identity-public.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roots.DataDir, "syncgate.db"), []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(roots.WorktreeRoot, "private-worktree"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := []byte("provider-key-super-secret")
	credentialStore := &memorySecretStore{secret: bytes.Clone(secret)}
	exporter := DiagnosticsExporter{
		Base:        settings.Base,
		Credentials: ProviderCredentials{Open: func(_, _ string) (CredentialStore, error) { return credentialStore, nil }},
		Now:         func() time.Time { return time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC) },
	}
	outputPath := filepath.Join(t.TempDir(), "diagnostics.json")
	report, err := exporter.Export(context.Background(), outputPath)
	if err != nil {
		t.Fatalf("export diagnostics: %v", err)
	}
	if report.Schema != DiagnosticsSchema || report.Node.OpaqueNodeID == "" || len(report.Node.OpaqueShareIDs) != 1 || !report.Credential.Configured {
		t.Fatalf("diagnostics report = %#v", report)
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range [][]byte{
		secret, []byte(roots.DataDir), []byte(roots.WorktreeRoot), []byte(shareID), []byte(shareName),
		[]byte(shareRoot), []byte(deviceID), []byte(fingerprint), []byte(receipt), []byte(runtimePath),
	} {
		if bytes.Contains(raw, privateValue) {
			t.Fatalf("diagnostics leaked private value %q: %s", privateValue, raw)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("diagnostics JSON: %v", err)
	}
	view, err := settings.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settingsJSON, err := json.Marshal(view)
	if err != nil || bytes.Contains(settingsJSON, secret) || bytes.Contains(settingsJSON, []byte(receipt)) {
		t.Fatalf("settings export leaked credential/receipt: %s err=%v", settingsJSON, err)
	}
}

func TestIdentityStatusUsesPublicMetadataOnly(t *testing.T) {
	dataDir := t.TempDir()
	status, err := ReadIdentityStatus(dataDir, config.IdentityStoreWindows)
	if err != nil || status.State != "uninitialized" || status.DeviceID != "" {
		t.Fatalf("uninitialized identity = %#v err=%v", status, err)
	}
	metadata := map[string]string{"device_id": "device-one", "public_key": "public", "fingerprint": "fingerprint-one"}
	if err := writeJSONAtomic(filepath.Join(dataDir, "identity-public.json"), metadata, 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = ReadIdentityStatus(dataDir, config.IdentityStoreWindows)
	if err != nil || status.State != "initialized" || status.DeviceID != "device-one" || status.Fingerprint != "fingerprint-one" {
		t.Fatalf("initialized identity = %#v err=%v", status, err)
	}
}
