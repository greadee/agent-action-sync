package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"syncgate/internal/api"
	"syncgate/internal/core"
	"syncgate/internal/orchestration"
	"syncgate/internal/storage"
	"syncgate/internal/storage/sqlite"
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
	admin := NewLocalOrchestrationAdministration(LocalAdministrationOptions{Control: control, Inventory: store.OrchestrationControl(), Projects: store.ProjectRegistrations(), Now: func() time.Time { return now }})
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
