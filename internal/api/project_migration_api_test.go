package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectMigrationRoutesRequireAuthenticationAndExposeNoRoot(t *testing.T) {
	confirmation := strings.Repeat("a", 64)
	service, err := NewAdministrationService(AdministrationServiceOptions{
		Queries: &stubAdministrationQueries{}, Ready: func() bool { return true },
		ProjectMigrationPreflight: func(_ context.Context, input ProjectMigrationInput) (ProjectMigrationPreflight, error) {
			return ProjectMigrationPreflight{
				Status: "ready", ShareID: input.ShareID, ProjectID: input.ProjectID, Name: input.Name,
				RootIdentity: "sha256:" + strings.Repeat("b", 64), Issues: []ProjectMigrationIssue{},
				ExcludedPaths: []string{".git"}, RequiredIgnorePatterns: []string{".git", ".git/**"},
				MissingConfiguredIgnorePatterns: []string{".git"}, ExpectedPortableRecords: []string{".agent-project/manifest.json", "PROJECT_REGISTERED"},
				Confirmation: confirmation,
			}, nil
		},
		ProjectMigrationApply: func(_ context.Context, input ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error) {
			return ProjectMigrationApplyResult{Status: "applied", ProjectID: input.ProjectID, ShareID: input.ShareID, CreatedRecords: 2, ScanRequested: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	credential := []byte(strings.Repeat("M", 43))
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Authenticate(NewAdminV1Handler(service))
	body := `{"share_id":"share-1","project_id":"project-1","name":"Project"}`
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/project-migrations/preflight", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), "project-1") {
		t.Fatalf("unauthorized response=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	preflight := authenticatedMigrationRequest(t, handler, credential, "/api/v1/project-migrations/preflight", body)
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), confirmation) {
		t.Fatalf("preflight=%d %s", preflight.Code, preflight.Body.String())
	}
	for _, forbidden := range []string{"C:\\", "/Users/", "root_path", "TOKEN="} {
		if strings.Contains(preflight.Body.String(), forbidden) {
			t.Fatalf("preflight leaked %q: %s", forbidden, preflight.Body.String())
		}
	}
	applyRaw, _ := json.Marshal(ProjectMigrationApplyInput{ProjectMigrationInput: ProjectMigrationInput{ShareID: "share-1", ProjectID: "project-1", Name: "Project"}, Confirmation: confirmation})
	apply := authenticatedMigrationRequest(t, handler, credential, "/api/v1/project-migrations/apply", string(applyRaw))
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"status":"applied"`) || apply.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("apply=%d %s", apply.Code, apply.Body.String())
	}
	bad := authenticatedMigrationRequest(t, handler, credential, "/api/v1/project-migrations/apply", `{"share_id":"share-1","project_id":"project-1","name":"Project","confirmation":"bad"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad confirmation=%d %s", bad.Code, bad.Body.String())
	}
}

func TestProjectMigrationApplyIsSingleFlightPerShare(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	service, err := NewAdministrationService(AdministrationServiceOptions{
		Queries: &stubAdministrationQueries{}, Ready: func() bool { return true },
		ProjectMigrationApply: func(ctx context.Context, _ ProjectMigrationApplyInput) (ProjectMigrationApplyResult, error) {
			close(started)
			select {
			case <-release:
				return ProjectMigrationApplyResult{Status: "applied"}, nil
			case <-ctx.Done():
				return ProjectMigrationApplyResult{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := ProjectMigrationApplyInput{ProjectMigrationInput: ProjectMigrationInput{ShareID: "share-1", ProjectID: "project-1", Name: "Project"}, Confirmation: strings.Repeat("a", 64)}
	done := make(chan error, 1)
	go func() { _, err := service.ApplyProjectMigration(context.Background(), input); done <- err }()
	<-started
	if _, err := service.ApplyProjectMigration(context.Background(), input); !errors.Is(err, errConflict) {
		t.Fatalf("second apply error=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func authenticatedMigrationRequest(t *testing.T, handler http.Handler, credential []byte, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+string(credential))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}
