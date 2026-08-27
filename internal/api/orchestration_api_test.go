package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"syncgate/internal/storage"
)

func TestOrchestrationRoutesAuthenticateBindAndSanitize(t *testing.T) {
	credential := clientTestCredential(0x73)
	authenticator, err := NewAdminAuthenticator(credential)
	if err != nil {
		t.Fatal(err)
	}
	facade := &orchestrationFacadeStub{}
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }, Orchestration: facade})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Authenticate(NewAdminV1Handler(service))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/orchestration/nodes?limit=1", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || facade.nodes {
		t.Fatalf("unauthorized=%d nodes=%v", recorder.Code, facade.nodes)
	}
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("nodes=%d %s", recorder.Code, recorder.Body.String())
	}
	for _, forbidden := range []string{`C:\\`, "workspace", "session", "prompt", "raw", "artifact_bytes", "quarantine"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
	request = httptest.NewRequest(http.MethodPut, "/api/v1/orchestration/projects/project-one/policy", strings.NewReader(`{"project_id":"project-one","max_concurrent":1,"idempotency_key":"policy-one"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("omitted scheduling policy=%d body=%s", recorder.Code, recorder.Body.String())
	}
	input := AssignmentControlInput{ProjectID: "project-one", AssignmentID: "assignment:one", Action: "pause", IdempotencyKey: "operation-one"}
	raw, _ := json.Marshal(input)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-other/assignments/assignment:one/controls", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || facade.controlled {
		t.Fatalf("cross project=%d controlled=%v", recorder.Code, facade.controlled)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/projects/project-one/assignments/assignment:one/controls", strings.NewReader(`{"project_id":"project-one","unknown":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+string(credential))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field=%d", recorder.Code)
	}
}

func TestOrchestrationRoutesAreUnavailableWithoutDaemonFacade(t *testing.T) {
	service, err := NewAdministrationService(AdministrationServiceOptions{Queries: &stubAdministrationQueries{}, Ready: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewAdminV1Handler(service).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/orchestration/nodes", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type orchestrationFacadeStub struct{ nodes, controlled bool }

func (stub *orchestrationFacadeStub) ListLocalProjects(context.Context, storage.PageRequest) (LocalProjectPage, error) {
	return LocalProjectPage{Items: []LocalProjectItem{{ProjectID: "project-one", DisplayName: "One", SchedulerState: "paused", AssignmentCounts: []StatusCount{}, GateCounts: []StatusCount{}}}, Page: InventoryPage{Limit: 1}}, nil
}
func (stub *orchestrationFacadeStub) SelectLocalProject(context.Context, LocalProjectSelectionInput) (LocalProjectItem, error) {
	return LocalProjectItem{}, nil
}
func (stub *orchestrationFacadeStub) SetLocalProjectPolicy(context.Context, LocalProjectPolicyInput) (LocalProjectItem, error) {
	return LocalProjectItem{}, nil
}
func (stub *orchestrationFacadeStub) ApproveTaskGraph(context.Context, TaskGraphApprovalInput) (TaskGraphApprovalResult, error) {
	return TaskGraphApprovalResult{}, nil
}
func (stub *orchestrationFacadeStub) PreviewDispatch(context.Context, DispatchPreviewInput) (DispatchPreviewResult, error) {
	return DispatchPreviewResult{}, nil
}
func (stub *orchestrationFacadeStub) StartScheduler(context.Context, SchedulerControlInput) (SchedulerStatus, error) {
	return SchedulerStatus{}, nil
}
func (stub *orchestrationFacadeStub) DisableScheduler(context.Context, SchedulerControlInput) (SchedulerStatus, error) {
	return SchedulerStatus{}, nil
}
func (stub *orchestrationFacadeStub) ListNodes(context.Context, storage.PageRequest) (NodePage, error) {
	stub.nodes = true
	return NodePage{Items: []NodeItem{{NodeID: "node-one", Version: 1, Digest: strings.Repeat("a", 64), Lifecycle: "ready", CapabilityIDs: []string{}}}, Page: InventoryPage{Limit: 1}}, nil
}
func (stub *orchestrationFacadeStub) ListAssignments(context.Context, string, storage.PageRequest) (AssignmentPage, error) {
	return AssignmentPage{}, nil
}
func (stub *orchestrationFacadeStub) GetAssignment(context.Context, string, string) (AssignmentDetail, error) {
	return AssignmentDetail{}, nil
}
func (stub *orchestrationFacadeStub) ControlAssignment(context.Context, AssignmentControlInput) (AssignmentDetail, error) {
	stub.controlled = true
	return AssignmentDetail{}, nil
}
func (stub *orchestrationFacadeStub) DecideIntegration(context.Context, IntegrationDecisionInput) (AssignmentDetail, error) {
	return AssignmentDetail{}, nil
}
