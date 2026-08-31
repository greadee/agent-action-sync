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

func TestCrossNodeProjectAPISeparatesAuthorityReplicasAndCompatibility(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "cross-node-projects.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	watermarkA := crossNodeDigest("accepted-history-a")
	watermarkB := crossNodeDigest("accepted-history-b")
	replicas := []struct {
		seed     byte
		protocol string
		observed time.Time
		expires  time.Time
		history  string
	}{
		{seed: 71, protocol: nodestatus.Protocol, observed: now.Add(-20 * time.Second), expires: now.Add(time.Minute), history: watermarkA},
		{seed: 72, protocol: nodestatus.Protocol, observed: now.Add(-3 * time.Minute), expires: now.Add(time.Minute), history: watermarkB},
		{seed: 73, protocol: nodestatus.Protocol, observed: now.Add(-4 * time.Minute), expires: now.Add(-10 * time.Second), history: watermarkA},
		{seed: 74, protocol: nodestatus.LegacyProtocol, observed: now.Add(-20 * time.Second), expires: now.Add(time.Minute), history: ""},
	}
	for index, replica := range replicas {
		privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{replica.seed}, ed25519.SeedSize))
		remote, err := identity.FromKeyPair(privateKey.Public().(ed25519.PublicKey), privateKey)
		if err != nil {
			t.Fatalf("identity %d: %v", index, err)
		}
		if err := store.Devices().TrustDevice(ctx, storage.Device{ID: remote.DeviceID, DisplayName: "paired node " + string(rune('a'+index)), PublicKey: remote.PublicKey, Fingerprint: remote.Fingerprint, TrustState: storage.TrustTrusted}); err != nil {
			t.Fatalf("TrustDevice %d: %v", index, err)
		}
		if err := store.NodeStatus().SaveControlPlaneGrant(ctx, storage.ControlPlaneGrant{DeviceID: remote.DeviceID, ReadStatus: true, GrantedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatalf("SaveControlPlaneGrant %d: %v", index, err)
		}
		snapshot := nodestatus.Snapshot{Protocol: replica.protocol, SourceDeviceID: remote.DeviceID, Revision: 1, Watermark: crossNodeDigest("snapshot-" + string(rune('a'+index))), Health: "healthy", Lifecycle: "running", ObservedAt: replica.observed, ExpiresAt: replica.expires, Projects: []nodestatus.ProjectSummary{{ProjectID: "project:alpha", DisplayName: "alpha", SchedulerState: "paused", AcceptedHistoryWatermark: replica.history}}}
		envelope, err := nodestatus.Build(remote, snapshot)
		if err != nil {
			t.Fatalf("Build %d: %v", index, err)
		}
		receiverNow := now
		if replica.expires.Before(now) {
			receiverNow = now.Add(-20 * time.Second)
		}
		receiver := nodestatus.Receiver{Devices: store.Devices(), Store: store.NodeStatus(), Now: func() time.Time { return receiverNow }}
		if _, err := receiver.Apply(ctx, envelope); err != nil {
			t.Fatalf("Apply %d: %v", index, err)
		}
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: store, Ready: func() bool { return true }, NodeStatus: store.NodeStatus(), Orchestration: &orchestrationFacadeStub{projects: []LocalProjectItem{{ProjectID: "project:alpha", DisplayName: "alpha", SchedulerState: "running"}}}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("NewAdministrationService: %v", err)
	}
	recorder := httptest.NewRecorder()
	NewAdminV1Handler(service).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/federation/projects", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var page CrossNodeProjectPage
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Authority != "local_control_store" || len(page.Items[0].Observations) != 4 {
		t.Fatalf("cross-node page = %+v", page)
	}
	kinds := map[string]bool{}
	for _, observation := range page.Items[0].Observations {
		kinds[observation.ObservationKind] = true
		if observation.ObservationKind == "authority" {
			t.Fatalf("remote replica was promoted to authority: %+v", observation)
		}
	}
	for _, kind := range []string{"replica", "stale", "offline", "incompatible"} {
		if !kinds[kind] {
			t.Fatalf("observation kinds = %#v, missing %q", kinds, kind)
		}
	}
	insights := strings.Join(page.Items[0].Insights, ",")
	for _, insight := range []string{"accepted_history_watermarks_differ", "local_control_store_is_scheduler_authority", "legacy_schema_downgrade", "offline_replica_observation", "stale_replica_observation"} {
		if !strings.Contains(insights, insight) {
			t.Fatalf("insights = %q, missing %q", insights, insight)
		}
	}
	for _, forbidden := range []string{"command", "remote_control", "runtime_session", "root_path", "shell"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("cross-node response exposed forbidden field %q: %s", forbidden, recorder.Body.String())
		}
	}
	post := httptest.NewRecorder()
	NewAdminV1Handler(service).ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/federation/projects", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.Code)
	}
}

func TestCrossNodeProjectsRemainReadableWithoutLocalOrchestration(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "cross-node-empty.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: store, Ready: func() bool { return true }, NodeStatus: store.NodeStatus(), Orchestration: &orchestrationFacadeStub{projectErr: &APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "orchestration administration is unavailable"}}})
	if err != nil {
		t.Fatalf("NewAdministrationService: %v", err)
	}
	page, err := service.ListCrossNodeProjects(ctx)
	if err != nil {
		t.Fatalf("ListCrossNodeProjects: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("page = %+v", page)
	}
}

func crossNodeDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
