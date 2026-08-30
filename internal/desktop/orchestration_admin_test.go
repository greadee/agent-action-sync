package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/core"
	"syncgate/internal/integrationgate"
	"syncgate/internal/orchestration"
	"syncgate/internal/project"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
	"syncgate/internal/telemetry"
)

func TestLocalAdministrationRequiresExplicitResolutionBeforeRetryingUncertainWork(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	projectRoot := t.TempDir()
	shareID := core.ShareID("share-local-recovery")
	if err := store.Shares().SaveShare(ctx, storage.Share{ID: shareID, Name: "Recovery", RootPath: projectRoot, Mode: storage.ShareOneWaySource}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProjectRegistrations().RegisterProject(ctx, storage.ProjectRegistration{
		ProjectID: "project-local-recovery", ShareID: shareID, RootPath: projectRoot, Name: "Recovery",
		AuthorityDeviceID: "device-local", ManifestRecordID: "manifest-local", ManifestRecordHash: localHash("manifest"), ManifestPath: ".agent-project/manifest.json", RegisteredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	control := orchestration.ControlService{Store: store.OrchestrationControl(), Now: func() time.Time { return now }, LeaseDuration: 5 * time.Minute}
	contractDigest := localHash("contract")
	if _, err := control.Plan(ctx, orchestration.PlanRequest{
		AssignmentID: "assignment:uncertain", AttemptID: "attempt:uncertain-1", ProjectID: "project-local-recovery", TaskID: "task:local", TaskRevision: 1, GraphRevision: 1,
		WorkPackageID: "work:local", ExecutionID: "execution:local", ContractID: "contract:local", ContractVersion: 1, ContractDigest: contractDigest,
		WorkerID: "worker:local", NodeID: "node:local", IdempotencyDigest: localHash("plan"), OperationID: "operation:plan", OperationDigest: localHash("plan-operation"), AuditID: "audit:plan", ActorID: "scheduler:desktop",
		Binding: storage.OrchestrationAttemptBinding{AttemptID: "attempt:uncertain-1", ContractID: "contract:local", ContractVersion: 1, ContractDigest: contractDigest, ContextDigest: localHash("context"), ContextCompilerVersion: "context-compiler:v1", BindingDigest: localHash("binding"), BindingJSON: []byte(`{"schema":"syncgate.attempt-binding.v1"}`), CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	fence := localHash("fence")
	if _, err := control.Claim(ctx, orchestration.ClaimRequest{AssignmentID: "assignment:uncertain", AttemptID: "attempt:uncertain-1", LeaseID: "lease:uncertain", OwnerNodeID: "node:local", OwnerRuntimeID: "runtime:local", FencingDigest: fence, OperationID: "operation:claim", OperationDigest: localHash("claim"), AuditID: "audit:claim", ActorID: "scheduler:desktop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.BindResources(ctx, orchestration.BindResourcesRequest{
		AssignmentID: "assignment:uncertain", AttemptID: "attempt:uncertain-1", LeaseGeneration: 1, FencingDigest: fence,
		RuntimeSessionID: "runtime-session:uncertain", RuntimeResumeKey: localHash("resume"), WorkspaceID: "workspace:uncertain", WorkspaceGeneration: 1,
		OperationID: "operation:bind", OperationDigest: localHash("bind"), AuditID: "audit:bind", ActorID: "scheduler:desktop",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Transition(ctx, orchestration.TransitionRequest{AssignmentID: "assignment:uncertain", AttemptID: "attempt:uncertain-1", TargetState: storage.AssignmentRunning, LeaseGeneration: 1, FencingDigest: fence, OperationID: "operation:start", OperationDigest: localHash("start"), AuditID: "audit:start", ActorID: "scheduler:desktop"}); err != nil {
		t.Fatal(err)
	}
	recovered, err := control.Reconcile(ctx, "scheduler:startup")
	if err != nil || len(recovered) != 1 || recovered[0].Attempt.RecoveryDisposition != storage.RecoveryNeedsOperator {
		t.Fatalf("recovery = %+v, err=%v", recovered, err)
	}
	if err := store.LocalProjectOperations().SelectLocalProject(ctx, storage.LocalProjectSelection{ProjectID: "project-local-recovery", SelectedAt: now}); err != nil {
		t.Fatal(err)
	}
	telemetryRecord := localTelemetryRecord(t, "project-local-recovery", "execution:local", "contract:local", contractDigest, now)
	if _, err := store.ExecutionTelemetry().SaveExecutionTelemetry(ctx, telemetryRecord); err != nil {
		t.Fatal(err)
	}
	admin := NewLocalOrchestrationAdministration(LocalAdministrationOptions{Control: control, Inventory: store.OrchestrationControl(), Projects: store.ProjectRegistrations(), Operations: store.LocalProjectOperations(), Telemetry: store.ExecutionTelemetry(), AuthorizedProjectID: "project-local-recovery", MaxConcurrent: 1, Now: func() time.Time { return now }})
	detail, err := admin.GetAssignment(ctx, "project-local-recovery", "assignment:uncertain")
	if err != nil || detail.ObservedBudget.TokenCount == nil || *detail.ObservedBudget.TokenCount != 140 || len(detail.Telemetry) != 1 || len(detail.Incidents) != 1 || detail.Incidents[0].Kind != "uncertain_runtime" || strings.Join(detail.Incidents[0].RecoveryActions, ",") != "cancel,fail" {
		t.Fatalf("slice 8 detail = %+v, err=%v", detail, err)
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"runtime-session", "workspace:", "C:\\", "prompt", "shell_output"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("slice 8 detail exposed %q: %s", forbidden, encoded)
		}
	}
	if _, err := admin.ControlAssignment(ctx, api.AssignmentControlInput{ProjectID: "project-local-recovery", AssignmentID: "assignment:uncertain", Action: "resume", IdempotencyKey: "resume-uncertain"}); err == nil {
		t.Fatal("uncertain work was eligible for automatic resume")
	} else {
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != 409 {
			t.Fatalf("resume error = %v", err)
		}
	}
	failed, err := admin.ControlAssignment(ctx, api.AssignmentControlInput{ProjectID: "project-local-recovery", AssignmentID: "assignment:uncertain", Action: "fail", IdempotencyKey: "fail-uncertain"})
	if err != nil || failed.State != string(storage.AssignmentFailed) {
		t.Fatalf("explicit fail = %+v, err=%v", failed, err)
	}
	retried, err := admin.ControlAssignment(ctx, api.AssignmentControlInput{ProjectID: "project-local-recovery", AssignmentID: "assignment:uncertain", Action: "retry", IdempotencyKey: "retry-failed"})
	if err != nil || retried.State != string(storage.AssignmentPlanned) || len(retried.Attempts) != 1 || retried.Attempts[0].AttemptNumber != 2 {
		t.Fatalf("explicit retry = %+v, err=%v", retried, err)
	}
}

func localTelemetryRecord(t *testing.T, projectID, executionID, contractID, contractDigest string, now time.Time) storage.ExecutionTelemetryRecord {
	t.Helper()
	input, output, cost, tools := int64(100), int64(40), int64(50), int64(3)
	build, raw, err := telemetry.BuildEnvelope(telemetry.Envelope{
		TelemetryID: "telemetry:local-recovery", IdempotencyKeyDigest: localHash("telemetry-key"), CreatedAt: now,
		Summary: project.TelemetrySummaryPayload{
			Contract: project.RegistryReference{ID: contractID, Version: 1, Digest: contractDigest}, ProjectRevision: localHash("project"),
			TaskID: "task:local", TaskRecordID: "record:task-local", TaskRevision: 1, TaskDigest: localHash("task"),
			GraphRecordID: "record:graph-local", GraphRevision: 1, GraphDigest: localHash("graph"), WorkPackageID: "work:local", WorkPackageRecordID: "record:work-local", WorkPackageDigest: localHash("work"),
			Trade: project.RegistryReference{ID: "trade:local", Version: 1, Digest: localHash("trade")}, Worker: project.RegistryReference{ID: "worker:local", Version: 1, Digest: localHash("worker")},
			Instruction: project.TelemetryBindingReference{ID: "instruction:local", Version: 1, Digest: localHash("instruction")}, ContextDigest: localHash("context"),
			Runtime: project.TelemetryBindingReference{ID: "runtime:local", Version: 1, Digest: localHash("runtime")}, Provider: project.TelemetryBindingReference{ID: "provider:local", Version: 1, Digest: localHash("provider")}, Model: project.TelemetryBindingReference{ID: "model:local", Version: 1, Digest: localHash("model")}, Node: project.TelemetryBindingReference{ID: "node:local", Version: 1, Digest: localHash("node")},
			FinalOutcome: "failed", Observations: []project.TelemetryObservation{{Name: "input_tokens", Value: &input, Source: "provider_reported"}, {Name: "output_tokens", Value: &output, Source: "provider_reported"}, {Name: "provider_cost_micros", Value: &cost, Source: "provider_reported"}, {Name: "tool_calls", Value: &tools, Source: "locally_measured"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return storage.ExecutionTelemetryRecord{TelemetryID: build.TelemetryID, TelemetryDigest: build.Digest, IdempotencyKeyDigest: build.IdempotencyKeyDigest, ProjectID: projectID, ExecutionID: executionID, ContractID: contractID, ContractVersion: 1, ContractDigest: contractDigest, FinalOutcome: build.Summary.FinalOutcome, SummaryJSON: raw, CreatedAt: now}
}

func TestSliceEightDecisionEvidenceUsesClosedOutcomesAndIncidentCodes(t *testing.T) {
	summary := resultSummary(integrationgate.IntegrationSummary{
		ResultID: "result:summary", Review: &integrationgate.ReviewResult{Outcome: "approved"}, ReadyForDecision: true, Digest: localHash("summary"),
		Tests: []integrationgate.GateEvidence{{GateID: "gate:test", Outcome: "passed", EvidenceID: "evidence:test", EvidenceDigest: localHash("test"), DurationMilliseconds: 17}},
	})
	if summary == nil || !summary.ReadyForDecision || len(summary.Tests) != 1 || summary.Tests[0].Outcome != "passed" || summary.Tests[0].EvidenceID != "evidence:test" {
		t.Fatalf("result summary = %+v", summary)
	}
	accepted := acceptedHistory([]storage.OrchestrationAuditEvent{{AuditID: "audit:accepted", AttemptID: "attempt:summary", ToState: storage.AssignmentAccepted, ReasonCode: "accepted", OccurredAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)}}, summary)
	if len(accepted) != 1 || accepted[0].SummaryDigest != summary.SummaryDigest || accepted[0].ReasonCode != "accepted" {
		t.Fatalf("accepted history = %+v", accepted)
	}
	for _, test := range []struct{ code, kind string }{
		{"context_leak_detected", "leaked_context_report"}, {"unsafe_output_detected", "unsafe_output"}, {"runaway_process_detected", "runaway_process"},
	} {
		kind, severity, ok := classifiedIncident(test.code)
		if !ok || kind != test.kind || severity != "critical" {
			t.Fatalf("incident %q = %q %q %v", test.code, kind, severity, ok)
		}
	}
	if _, _, ok := classifiedIncident("arbitrary_runtime_message"); ok {
		t.Fatal("arbitrary failure code became a browser incident")
	}
}
