package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/config"
	"syncgate/internal/core"
	syncengine "syncgate/internal/sync"
)

func TestPrintDiagnosticsIncludesRequiredSections(t *testing.T) {
	report := syncengine.DiagnosticReport{
		GeneratedAt: time.Unix(100, 0).UTC(),
		RecentScans: []syncengine.ScanDiagnostic{
			{ShareID: "share-old", Trigger: syncengine.ScanTriggerPeriodic, Committed: true, FinishedAt: time.Unix(101, 0).UTC()},
			{ShareID: "share-1", Trigger: syncengine.ScanTriggerManual, Blocked: true, Revisions: 2, Reasons: []string{"blocked by deletion guard"}, FinishedAt: time.Unix(102, 0).UTC()},
		},
		Work: []syncengine.WorkDiagnostic{{
			JobID:        "job-1",
			TransferID:   core.TransferID("transfer-1"),
			ShareID:      "share-1",
			RelativePath: "notes.txt",
			State:        core.OneWayJobRetryWait,
			RetryCount:   1,
			LastError:    "temporary network error",
		}},
		IgnoredPaths: []syncengine.IgnoreDiagnostic{{
			ShareID:      "share-1",
			RelativePath: "debug.log",
			Pattern:      "*.log",
		}},
	}

	var out bytes.Buffer
	printDiagnostics(&out, report, 1)
	got := out.String()
	for _, want := range []string{"recent_scans:", "pending_or_blocked_work:", "ignored_paths:", "share=share-1", "state=retry_wait", `pattern="*.log"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "share-old") {
		t.Fatalf("diagnostics output did not apply recent limit:\n%s", got)
	}
}

func TestAdminClientUsesDaemonAPIWithoutOpeningSQLite(t *testing.T) {
	credential := []byte("ERERERERERERERERERERERERERERERERERERERERERE")
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+string(credential) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(api.AdminStatus{Status: "running", APIVersion: api.APIVersion})
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	dataDir := t.TempDir()
	shareRoot := t.TempDir()
	store, err := api.NewAdminCredentialStore(api.AdminCredentialStoreOptions{
		DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment, AllowInsecureDevelopmentFile: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dataDir, "syncgate.db")
	if err := os.WriteFile(databasePath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().(*net.TCPAddr)
	cfg := config.Config{
		DeviceName: "CLI-TEST", DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment,
		Identity: config.IdentityConfig{Store: config.IdentityStoreDevelopment, AllowInsecureDevelopmentFile: true},
		LocalAPI: config.LocalAPIConfig{Host: "127.0.0.1", Port: address.Port},
		Shares:   []config.ShareConfig{{ID: "drop", Name: "Drop", RootPath: shareRoot, Mode: "upload_only"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	client, err := newAdminClient(configPath)
	if err != nil {
		t.Fatalf("newAdminClient: %v", err)
	}
	defer client.Close()
	status, err := client.Status(context.Background())
	if err != nil || status.Status != "running" {
		t.Fatalf("Status = %+v, err=%v", status, err)
	}
	remaining, err := os.ReadFile(databasePath)
	if err != nil || string(remaining) != "not a SQLite database" {
		t.Fatalf("online command touched SQLite: %q, err=%v", remaining, err)
	}
}

func TestBrowserSessionURLUsesLaptopLoopbackPortAndFragment(t *testing.T) {
	credential := []byte("TATATATATATATATATATATATATATATATATATATATATAT")
	token := strings.Repeat("t", 32)
	expiresAt := time.Date(2026, time.September, 4, 19, 30, 0, 0, time.UTC)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/browser-sessions" || request.Header.Get("Authorization") != "Bearer "+string(credential) {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(writer).Encode(api.BrowserSessionTicketResponse{BootstrapToken: token, ExpiresAt: expiresAt})
	}))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	dataDir, shareRoot := t.TempDir(), t.TempDir()
	store, err := api.NewAdminCredentialStore(api.AdminCredentialStoreOptions{DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment, AllowInsecureDevelopmentFile: true})
	if err != nil {
		t.Fatalf("create test credential store: %v", err)
	}
	if err := store.Save(credential); err != nil {
		t.Fatalf("save test credential: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	cfg := config.Config{DeviceName: "CLI-REMOTE-TEST", DataDir: dataDir, RuntimeMode: config.RuntimeModeDevelopment, Identity: config.IdentityConfig{Store: config.IdentityStoreDevelopment, AllowInsecureDevelopmentFile: true}, LocalAPI: config.LocalAPIConfig{Host: "127.0.0.1", Port: port}, Shares: []config.ShareConfig{{ID: "drop", Name: "Drop", RootPath: shareRoot, Mode: "upload_only"}}}
	raw, _ := json.Marshal(cfg)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	value, actualExpiry, err := browserSessionURL(configPath, "127.0.0.1", 57820)
	if err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:57820/ui/#bootstrap=" + url.QueryEscape(token)
	if value != want || actualExpiry != expiresAt || strings.Contains(value, "?bootstrap=") {
		t.Fatalf("session URL=%q expires=%s", value, actualExpiry)
	}
}

func TestParsePairingGrantRequiresExplicitSupportedCapabilities(t *testing.T) {
	grant, err := parsePairingGrant("drop=sync,upload", true)
	if err != nil {
		t.Fatalf("parsePairingGrant: %v", err)
	}
	if grant.ShareID != "drop" || !grant.LANOnly || len(grant.Capabilities) != 2 {
		t.Fatalf("grant = %+v", grant)
	}
	for _, invalid := range []string{"drop=", "=read", "drop=remote_access", "drop=read,read"} {
		if _, err := parsePairingGrant(invalid, true); err == nil {
			t.Fatalf("expected grant %q to fail", invalid)
		}
	}
}
