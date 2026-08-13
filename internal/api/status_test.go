package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
	syncengine "syncgate/internal/sync"
)

func TestAdministrationStatusReportsRuntimeAndQueueTruthfully(t *testing.T) {
	report := syncengine.DiagnosticReport{Work: []syncengine.WorkDiagnostic{
		{State: core.OneWayJobQueued}, {State: core.OneWayJobRetryWait},
		{State: core.OneWayJobRunning}, {State: core.OneWayJobPaused}, {State: core.OneWayJobFailed},
	}}
	service := newStatusService(t, RuntimeSnapshot{
		DeviceID: "device-local", Fingerprint: "FINGERPRINT", StartedAt: time.Unix(10, 0).UTC(),
		Lifecycle: LifecycleRunning, ActiveShareCount: 3, PeerExecutorEnabled: false,
	}, report)
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.APIVersion != APIVersion || status.Lifecycle != LifecycleRunning || status.ActiveShareCount != 3 || status.PeerExecutorEnabled {
		t.Fatalf("status = %#v", status)
	}
	if status.Queue != (QueueSummary{Pending: 2, Running: 1, Paused: 1, Failed: 1}) {
		t.Fatalf("queue summary = %#v", status.Queue)
	}
}

func TestAdministrationDiagnosticsLimitsAndRetainsSanitizedReport(t *testing.T) {
	report := syncengine.DiagnosticReport{
		GeneratedAt:  time.Unix(20, 0).UTC(),
		RecentScans:  []syncengine.ScanDiagnostic{{ShareID: "share-1", FinishedAt: time.Unix(1, 0).UTC()}, {ShareID: "share-2", FinishedAt: time.Unix(2, 0).UTC()}},
		Work:         []syncengine.WorkDiagnostic{{JobID: "job-1", RelativePath: "safe.txt", LastError: "open [redacted-path]"}, {JobID: "job-2"}},
		IgnoredPaths: []syncengine.IgnoreDiagnostic{{ShareID: "share-1", RelativePath: ".sync-history/state.json", Pattern: ".sync-history/**"}},
	}
	service := newStatusService(t, RuntimeSnapshot{Lifecycle: LifecycleRunning}, report)
	diagnostics, err := service.Diagnostics(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.RecentScans) != 1 || diagnostics.RecentScans[0].ShareID != "share-2" || len(diagnostics.Work) != 1 || len(diagnostics.IgnoredPaths) != 1 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	encoded, err := json.Marshal(diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"C:\\Users\\owner", "token=", "password=", "invite-code"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, encoded)
		}
	}
	if _, err := service.Diagnostics(context.Background(), MaxDiagnosticsItemLimit+1); err == nil {
		t.Fatal("oversized diagnostics limit was accepted")
	}
}

func TestStatusDiagnosticsHandlerRoutesAndSanitizesErrors(t *testing.T) {
	service := newStatusService(t, RuntimeSnapshot{Lifecycle: LifecycleStarting}, syncengine.DiagnosticReport{})
	handler := NewStatusDiagnosticsHandler(service)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status response = %d headers %#v", recorder.Code, recorder.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics?limit=0", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid diagnostics limit status = %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d headers %#v", recorder.Code, recorder.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/other", nil)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown response = %d", recorder.Code)
	}
}

func TestAdministrationStatusRequiresRuntimeAndDiagnosticsProviders(t *testing.T) {
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Status(context.Background()); err == nil {
		t.Fatal("status succeeded without runtime provider")
	}
	service.runtime = func() RuntimeSnapshot { return RuntimeSnapshot{Lifecycle: LifecycleRunning} }
	if _, err := service.Diagnostics(context.Background(), 1); err == nil {
		t.Fatal("diagnostics succeeded without diagnostics provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled status error = %v", err)
	}
}

func newStatusService(t *testing.T, snapshot RuntimeSnapshot, report syncengine.DiagnosticReport) *LocalAdministrationService {
	t.Helper()
	service, err := NewAdministrationService(AdministrationServiceOptions{
		Queries: &stubAdministrationQueries{}, Ready: func() bool { return snapshot.Lifecycle == LifecycleRunning },
		Runtime: func() RuntimeSnapshot { return snapshot }, Diagnostics: func() syncengine.DiagnosticReport { return report },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

var _ storage.AdministrationQueryStore = (*stubAdministrationQueries)(nil)
