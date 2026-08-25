package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupRoutesAuthenticateValidateBindingsAndRemainNoStore(t *testing.T) {
	credential := clientTestCredential(0x71)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	facade := &setupFacadeStub{}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }, Setup: facade})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Authenticate(NewAdminV1Handler(service))
	input := TaskGraphCreateInput{ProjectID: "project-one", TaskID: "task:one", TaskRevision: 1, GraphRevision: 1, SpecificationID: "spec-one", SpecificationDigest: strings.Repeat("a", 64), IdempotencyKey: "operation-one"}
	raw, _ := json.Marshal(input)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-one/tasks", strings.NewReader(string(raw))))
	if unauthorized.Code != http.StatusUnauthorized || facade.created {
		t.Fatalf("unauthorized=%d created=%v", unauthorized.Code, facade.created)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-one/tasks", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(credential))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !facade.created || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create=%d %s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"C:\\", "root_path", "workspace_handle", "secret", "terminal_output"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q", forbidden)
		}
	}

	wrong := input
	wrong.ProjectID = "project-other"
	raw, _ = json.Marshal(wrong)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-one/tasks", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(credential))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross project=%d %s", response.Code, response.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/orchestration/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+string(credential))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"runtime_execution_enabled":false`) {
		t.Fatalf("capabilities=%d %s", response.Code, response.Body.String())
	}
}

func TestSetupCommandsAreUnavailableWithoutDaemonFacade(t *testing.T) {
	credential := clientTestCredential(0x72)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-one/context/preflight", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+string(credential))
	response := httptest.NewRecorder()
	authenticator.Authenticate(NewAdminV1Handler(service)).ServeHTTP(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled facade=%d %s", response.Code, response.Body.String())
	}
}

type setupFacadeStub struct{ created bool }

func (stub *setupFacadeStub) CreateTaskGraph(_ context.Context, input TaskGraphCreateInput) (TaskGraphCreateResult, error) {
	stub.created = true
	return TaskGraphCreateResult{ProjectID: input.ProjectID, TaskID: input.TaskID, TaskRevision: input.TaskRevision, GraphRevision: input.GraphRevision, TaskRecordID: "task-record", TaskDigest: strings.Repeat("b", 64), GraphRecordID: "graph-record", GraphDigest: strings.Repeat("c", 64)}, nil
}
func (stub *setupFacadeStub) ValidateTaskGraph(context.Context, TaskGraphValidationInput) (TaskGraphValidationResult, error) {
	return TaskGraphValidationResult{Valid: true}, nil
}
func (stub *setupFacadeStub) ContextPreflight(context.Context, ContextPreflightInput) (ContextPreflightResult, error) {
	return ContextPreflightResult{}, nil
}
func (stub *setupFacadeStub) RuntimePreflight(context.Context, RuntimePreflightInput) (RuntimePreflightResult, error) {
	return RuntimePreflightResult{RuntimeCapabilities: []string{}, WorkspaceReady: false}, nil
}
func (stub *setupFacadeStub) PreviewExecutionContract(context.Context, ExecutionContractPreviewInput) (ExecutionContractPreviewResult, error) {
	return ExecutionContractPreviewResult{PreviewOnly: true}, nil
}
func (stub *setupFacadeStub) CapabilityInventory(context.Context) (CapabilityInventory, error) {
	return CapabilityInventory{Runtimes: []CapabilityReference{}, Nodes: []CapabilityReference{}, WorkspaceAllocationEnabled: false, RuntimeExecutionEnabled: false}, nil
}
