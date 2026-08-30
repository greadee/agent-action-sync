package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/identity"
	"syncgate/internal/nodestatus"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestFederatedNodeAPIExposesOnlyVisibleSanitizedReadModels(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "federation-api.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{61}, ed25519.SeedSize))
	remote, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
	if err != nil {
		t.Fatalf("FromKeyPair: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.Devices().TrustDevice(ctx, storage.Device{ID: remote.DeviceID, DisplayName: "paired desktop", PublicKey: remote.PublicKey, Fingerprint: remote.Fingerprint, TrustState: storage.TrustTrusted}); err != nil {
		t.Fatalf("TrustDevice: %v", err)
	}
	if err := store.NodeStatus().SaveControlPlaneGrant(ctx, storage.ControlPlaneGrant{DeviceID: remote.DeviceID, ReadStatus: true, GrantedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("SaveControlPlaneGrant: %v", err)
	}
	digest := sha256.Sum256([]byte("api-status-watermark"))
	snapshot := nodestatus.Snapshot{Protocol: nodestatus.Protocol, SourceDeviceID: remote.DeviceID, Revision: 3, Watermark: hex.EncodeToString(digest[:]), Health: "degraded", Lifecycle: "running", ObservedAt: now.Add(-30 * time.Second), ExpiresAt: now.Add(30 * time.Second), Projects: []nodestatus.ProjectSummary{{ProjectID: "project:alpha", DisplayName: "alpha", SchedulerState: "paused"}}}
	envelope, err := nodestatus.Build(remote, snapshot)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	receiver := nodestatus.Receiver{Devices: store.Devices(), Store: store.NodeStatus(), Now: func() time.Time { return now }}
	if _, err := receiver.Apply(ctx, envelope); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: store, Ready: func() bool { return true }, NodeStatus: store.NodeStatus(), Now: func() time.Time { return now.Add(time.Minute) }})
	if err != nil {
		t.Fatalf("NewAdministrationService: %v", err)
	}
	handler := NewAdminV1Handler(service)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/federation/nodes", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var page FederatedNodePage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Connectivity != "offline" || page.Items[0].Health != "degraded" || len(page.Items[0].Projects) != 1 {
		t.Fatalf("federated page = %+v", page)
	}
	for _, forbidden := range []string{"public_key", "fingerprint", "private", "runtime_session", "shell", "root_path", "command"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("federated response exposed forbidden field %q: %s", forbidden, recorder.Body.String())
		}
	}

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/federation/nodes", strings.NewReader(`{}`)))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.Code)
	}
}
