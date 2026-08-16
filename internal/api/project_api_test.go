package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/insights"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
)

func TestProjectRoutesAreAuthenticatedBoundedAndPrivate(t *testing.T) {
	service, root := projectAPIServiceFixture(t, nil)
	credential := []byte(strings.Repeat("A", 43))
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Authenticate(NewAdminV1Handler(service))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil))
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), "project-api") {
		t.Fatalf("unauthorized response leaked data: %d %s", unauthorized.Code, unauthorized.Body.String())
	}

	for _, path := range []string{
		"/api/v1/projects?limit=1", "/api/v1/projects/project-api",
		"/api/v1/projects/project-api/history?limit=1&event_type=TEST_RECORDED",
		"/api/v1/projects/project-api/artifacts?limit=1&media_type=text/plain",
		"/api/v1/projects/project-api/insights?limit=1&scope=project",
		"/api/v1/projects/project-api/rejections?limit=1",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+string(credential))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		for _, forbidden := range []string{root, "private-value", "payload_json", "blob_path", "quarantine_path"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s leaked %q: %s", path, forbidden, body)
			}
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s missing no-store", path)
		}
		if path == "/api/v1/projects/project-api" && (!strings.Contains(body, `"history":"available"`) || strings.Contains(body, `"History"`)) {
			t.Fatalf("project projection state does not match the contract: %s", body)
		}
	}

	rebuild := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-api/projections/rebuild", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(rebuild, request)
	if rebuild.Code != http.StatusOK || !strings.Contains(rebuild.Body.String(), `"status":"rebuilt"`) {
		t.Fatalf("rebuild status=%d body=%s", rebuild.Code, rebuild.Body.String())
	}
	if strings.Contains(rebuild.Body.String(), root) || rebuild.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe rebuild response: %s", rebuild.Body.String())
	}

	bad := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-api/history?limit=201", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(bad, request)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("oversized history status=%d", bad.Code)
	}
	badCursor := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects?cursor="+encodeAdminCursor(storage.PageCursor{Timestamp: time.Now()}), nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(badCursor, request)
	if badCursor.Code != http.StatusBadRequest {
		t.Fatalf("invalid project cursor status=%d body=%s", badCursor.Code, badCursor.Body.String())
	}
	badFilter := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-api/history?event_type="+strings.Repeat("x", storage.MaxProjectProjectionFilterBytes+1), nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(badFilter, request)
	if badFilter.Code != http.StatusBadRequest {
		t.Fatalf("oversized project filter status=%d body=%s", badFilter.Code, badFilter.Body.String())
	}
	missing := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/other", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("cross-project status=%d", missing.Code)
	}
	for _, path := range []string{"/api/v1/projects/other/history", "/api/v1/projects/other/artifacts", "/api/v1/projects/other/insights", "/api/v1/projects/other/rejections"} {
		missing = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+string(credential))
		handler.ServeHTTP(missing, request)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("cross-project %s status=%d", path, missing.Code)
		}
	}
	unknown := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/v1/projects/project-api/unknown", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(unknown, request)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown project subresource status=%d", unknown.Code)
	}
	method := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodDelete, "/api/v1/projects/project-api/insights", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	handler.ServeHTTP(method, request)
	if method.Code != http.StatusMethodNotAllowed || method.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response=%d %#v", method.Code, method.Header())
	}
}

func TestProjectRebuildIsSingleFlightAndReportsRebuilding(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	service, _ := projectAPIServiceFixture(t, func(ctx context.Context, _ string) (ProjectRebuildResult, error) {
		close(started)
		select {
		case <-release:
			return ProjectRebuildResult{EventWatermark: strings.Repeat("b", 64), InsightCount: len(insights.Definitions)}, nil
		case <-ctx.Done():
			return ProjectRebuildResult{}, ctx.Err()
		}
	})
	done := make(chan error, 1)
	go func() { _, err := service.RebuildProject(context.Background(), "project-api"); done <- err }()
	<-started
	state, err := service.ProjectProjectionState(context.Background(), "project-api")
	if err != nil {
		t.Fatal(err)
	}
	if state.History != "rebuilding" || state.Insights != "rebuilding" {
		t.Fatalf("state=%+v", state)
	}
	if _, err := service.RebuildProject(context.Background(), "project-api"); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProjectQueriesHonorCancellation(t *testing.T) {
	service, _ := projectAPIServiceFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.ListProjectHistory(ctx, storage.ProjectEventQuery{ProjectID: "project-api", Page: storage.PageRequest{Limit: 1}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled project query error=%v", err)
	}
}

func TestInsightFreshnessRequiresEveryCurrentDefinition(t *testing.T) {
	rows := make([]storage.ProjectInsightProjection, 0, len(insights.Definitions))
	for _, definition := range insights.Definitions {
		rows = append(rows, storage.ProjectInsightProjection{MetricName: definition.Name, DefinitionVersion: definition.Version})
	}
	if insightsStale(rows) {
		t.Fatal("current complete definition set reported stale")
	}
	if !insightsStale(rows[:len(rows)-1]) {
		t.Fatal("incomplete definition set reported current")
	}
	rows[0].DefinitionVersion++
	if !insightsStale(rows) {
		t.Fatal("old definition version reported current")
	}
}

func projectAPIServiceFixture(t *testing.T, rebuild ProjectRebuildFunc) (*LocalAdministrationService, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: core.ShareID("share-api"), Name: "API", RootPath: root, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("a", 64)
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{ProjectID: "project-api", ShareID: "share-api", RootPath: root, Name: "project-api", AuthorityDeviceID: "device-api", ManifestRecordID: "manifest-api", ManifestRecordHash: hash, ManifestPath: ".agent-project/manifest.json", RegisteredAt: when}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"summary": "private-value"})
	if _, err := store.ProjectEvents().SaveProjectEvent(ctx, storage.ProjectEventProjection{ProjectID: "project-api", EventID: "event-api", RecordHash: hash, EventType: "TEST_RECORDED", OccurredAt: when, WorkPackageID: "wp-api", ExecutionID: "exec-api", ProducerDeviceID: "device-api", ProducerProvider: "provider-api", ProducerModel: "model-api", Status: "accepted", RecordPath: ".agent-project/history/events/2026/08/14/event-api.json", PayloadJSON: payload}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectArtifacts().SaveProjectArtifact(ctx, storage.ProjectArtifactProjection{ProjectID: "project-api", ArtifactID: "artifact-api", RecordID: "artifact-record", RecordHash: hash, Name: "result", MediaType: "text/plain", Size: 10, ContentHash: hash, BlobPath: "artifacts/blobs/sha256/" + hash, WorkPackageID: "wp-api", ProducerDeviceID: "device-api", CreatedAt: when, RecordPath: ".agent-project/artifacts/manifests/artifact-api.json"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectCheckpoints().SetProjectCheckpoint(ctx, storage.ProjectProjectionCheckpoint{ProjectID: "project-api", Stream: "portable-record-set-v1", LastRecordPath: ".agent-project/manifest.json", LastRecordHash: hash, UpdatedAt: when}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectRejections().RecordProjectRejection(ctx, storage.ProjectProjectionRejection{ProjectID: "project-api", RecordPath: ".agent-project/history/events/invalid.json", ObservedHash: hash, ReasonCode: "invalid_record", QuarantinePath: ".agent-project/local/quarantine/private-value.json", RejectedAt: when}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectInsightProjections().ReplaceProjectInsights(ctx, "project-api", func(writer storage.ProjectInsightWriter) error {
		for _, definition := range insights.Definitions {
			raw := []byte(`{"known":false}`)
			if err := writer.SaveProjectInsight(ctx, storage.ProjectInsightProjection{ProjectID: "project-api", Scope: "project", MetricName: definition.Name, DefinitionVersion: definition.Version, SourceEventWatermark: hash, ValueJSON: raw, Completeness: "partial", Evidence: "weak", CalculatedAt: when}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rebuild == nil {
		rebuild = func(context.Context, string) (ProjectRebuildResult, error) {
			return ProjectRebuildResult{EventWatermark: hash, EventCount: 1, ArtifactCount: 1, InsightCount: len(insights.Definitions)}, nil
		}
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }, ProjectStore: store, ProjectRebuild: rebuild})
	if err != nil {
		t.Fatal(err)
	}
	return service, root
}
