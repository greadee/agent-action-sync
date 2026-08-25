package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"syncgate/internal/orchestration"
	"syncgate/internal/storage"
)

type controlFixture struct {
	t       *testing.T
	store   *Store
	service orchestration.ControlService
	now     time.Time
	project string
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	store := newTestStore(t)
	projectID := "project-control"
	saveProjectRegistration(t, store, projectID)
	fixture := &controlFixture{
		t: t, store: store, project: projectID,
		now: time.Date(2026, time.August, 21, 12, 0, 0, 0, time.UTC),
	}
	fixture.service = orchestration.ControlService{
		Store: store.OrchestrationControl(), Now: func() time.Time { return fixture.now }, LeaseDuration: 5 * time.Minute,
	}
	return fixture
}

func (fixture *controlFixture) plan(key string) storage.OrchestrationSnapshot {
	return fixture.planWithReason(key, "")
}

func (fixture *controlFixture) planWithReason(key, assignmentReason string) storage.OrchestrationSnapshot {
	fixture.t.Helper()
	result, err := fixture.service.Plan(context.Background(), orchestration.PlanRequest{
		AssignmentID: "assignment-" + key, AttemptID: "attempt-" + key + "-1", ProjectID: fixture.project,
		TaskID: "task-" + key, TaskRevision: 1, GraphRevision: 1, WorkPackageID: "work-" + key,
		ExecutionID: "execution-" + key, ContractID: "contract-" + key, ContractVersion: 1,
		ContractDigest: controlDigest("contract-" + key), WorkerID: "worker-1", NodeID: "node-1",
		IdempotencyDigest: controlDigest("plan-idempotency-" + key), OperationID: "operation-plan-" + key,
		OperationDigest: controlDigest("operation-plan-" + key), AuditID: "audit-plan-" + key, ActorID: "scheduler", AssignmentReason: assignmentReason,
		Binding: testAttemptBinding("attempt-"+key+"-1", "contract-"+key, 1, controlDigest("contract-"+key), fixture.now),
	})
	if err != nil {
		fixture.t.Fatalf("Plan(%s): %v", key, err)
	}
	return result.Snapshot
}

func TestOrchestrationPlanAuditsExplicitOperatorOverride(t *testing.T) {
	fixture := newControlFixture(t)
	fixture.planWithReason("override", "operator_override")
	events, err := fixture.store.OrchestrationControl().ListOrchestrationAudit(context.Background(), "assignment-override")
	if err != nil || len(events) != 1 || events[0].Action != "assign" || events[0].ReasonCode != "operator_override" {
		t.Fatalf("override assignment audit = %#v, err=%v", events, err)
	}
}

func (fixture *controlFixture) claimRequest(key, claimant string) orchestration.ClaimRequest {
	return orchestration.ClaimRequest{
		AssignmentID: "assignment-" + key, AttemptID: "attempt-" + key + "-1", LeaseID: "lease-" + key + "-" + claimant,
		OwnerNodeID: "node-" + claimant, OwnerRuntimeID: "runtime-owner-" + claimant, FencingDigest: controlDigest("fence-" + key + "-" + claimant),
		OperationID: "operation-claim-" + key + "-" + claimant, OperationDigest: controlDigest("operation-claim-" + key + "-" + claimant),
		AuditID: "audit-claim-" + key + "-" + claimant, ActorID: "scheduler",
	}
}

func TestOrchestrationClaimFencingRollbackAndAudit(t *testing.T) {
	fixture := newControlFixture(t)
	ctx := context.Background()
	fixture.plan("atomic")
	fixture.service.LeaseDuration = 25 * time.Hour
	if _, err := fixture.service.Claim(ctx, fixture.claimRequest("atomic", "invalid-duration")); !errors.Is(err, orchestration.ErrInvalidControl) {
		t.Fatalf("invalid lease duration error = %v", err)
	}
	fixture.service.LeaseDuration = 5 * time.Minute

	requests := []orchestration.ClaimRequest{fixture.claimRequest("atomic", "a"), fixture.claimRequest("atomic", "b")}
	type claimResult struct {
		index  int
		result storage.OrchestrationWriteResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan claimResult, len(requests))
	var workers sync.WaitGroup
	for index, request := range requests {
		workers.Add(1)
		go func(index int, request orchestration.ClaimRequest) {
			defer workers.Done()
			<-start
			result, err := fixture.service.Claim(ctx, request)
			results <- claimResult{index: index, result: result, err: err}
		}(index, request)
	}
	close(start)
	workers.Wait()
	close(results)

	winner := -1
	for result := range results {
		if result.err == nil {
			if winner != -1 {
				t.Fatal("two concurrent claims succeeded")
			}
			winner = result.index
			if result.result.Snapshot.Attempt.LeaseGeneration != 1 {
				t.Fatalf("lease generation = %d", result.result.Snapshot.Attempt.LeaseGeneration)
			}
		} else if !errors.Is(result.err, storage.ErrConflict) {
			t.Fatalf("losing claim error = %v", result.err)
		}
	}
	if winner == -1 {
		t.Fatal("no concurrent claim succeeded")
	}
	winningRequest := requests[winner]
	replay, err := fixture.service.Claim(ctx, winningRequest)
	if err != nil || !replay.AlreadyPresent {
		t.Fatalf("claim replay = %#v, err=%v", replay, err)
	}

	var activeLeases int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orchestration_leases WHERE attempt_id = ? AND state = 'active'`, winningRequest.AttemptID).Scan(&activeLeases); err != nil || activeLeases != 1 {
		t.Fatalf("active leases = %d, err=%v", activeLeases, err)
	}

	if _, err := fixture.service.Heartbeat(ctx, orchestration.HeartbeatRequest{
		AssignmentID: winningRequest.AssignmentID, AttemptID: winningRequest.AttemptID, LeaseID: winningRequest.LeaseID,
		LeaseGeneration: 2, FencingDigest: winningRequest.FencingDigest, ExtendBy: time.Minute,
	}); !errors.Is(err, orchestration.ErrStaleFence) {
		t.Fatalf("stale heartbeat error = %v", err)
	}

	badBind := orchestration.BindResourcesRequest{
		AssignmentID: winningRequest.AssignmentID, AttemptID: winningRequest.AttemptID, LeaseGeneration: 1,
		FencingDigest: winningRequest.FencingDigest, RuntimeSessionID: "runtime-session-atomic", RuntimeResumeKey: controlDigest("resume-atomic"),
		WorkspaceID: "workspace-atomic", WorkspaceGeneration: 1, OperationID: "operation-bind-atomic-bad",
		OperationDigest: controlDigest("operation-bind-atomic-bad"), AuditID: "audit-plan-atomic", ActorID: "scheduler",
	}
	if _, err := fixture.service.BindResources(ctx, badBind); err == nil {
		t.Fatal("binding with a duplicate audit ID should fail")
	}
	afterRollback, err := fixture.store.OrchestrationControl().GetAssignment(ctx, winningRequest.AssignmentID)
	if err != nil || afterRollback.Attempt.State != storage.AssignmentLeased || afterRollback.Resources != nil {
		t.Fatalf("failed bind was not rolled back: %#v, err=%v", afterRollback, err)
	}

	bind := badBind
	bind.OperationID = "operation-bind-atomic"
	bind.OperationDigest = controlDigest(bind.OperationID)
	bind.AuditID = "audit-bind-atomic"
	bound, err := fixture.service.BindResources(ctx, bind)
	if err != nil || bound.Snapshot.Attempt.State != storage.AssignmentPreparing || bound.Snapshot.Resources == nil {
		t.Fatalf("BindResources = %#v, err=%v", bound, err)
	}
	replayedBind, err := fixture.service.BindResources(ctx, bind)
	if err != nil || !replayedBind.AlreadyPresent {
		t.Fatalf("bind replay = %#v, err=%v", replayedBind, err)
	}

	started, err := fixture.service.Transition(ctx, orchestration.TransitionRequest{
		AssignmentID: winningRequest.AssignmentID, AttemptID: winningRequest.AttemptID, TargetState: storage.AssignmentRunning,
		LeaseGeneration: 1, FencingDigest: winningRequest.FencingDigest, OperationID: "operation-start-atomic",
		OperationDigest: controlDigest("operation-start-atomic"), AuditID: "audit-start-atomic", ActorID: "scheduler",
	})
	if err != nil || started.Snapshot.Attempt.State != storage.AssignmentRunning {
		t.Fatalf("start = %#v, err=%v", started, err)
	}
	if _, err := fixture.service.Heartbeat(ctx, orchestration.HeartbeatRequest{
		AssignmentID: winningRequest.AssignmentID, AttemptID: winningRequest.AttemptID, LeaseID: winningRequest.LeaseID,
		LeaseGeneration: 1, FencingDigest: winningRequest.FencingDigest, ExtendBy: 10 * time.Minute,
	}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	events, err := fixture.store.OrchestrationControl().ListOrchestrationAudit(ctx, winningRequest.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, event := range events {
		actions[event.Action] = true
	}
	for _, action := range []string{"assign", "claim", "prepare", "start"} {
		if !actions[action] {
			t.Errorf("audit trail is missing %q: %#v", action, events)
		}
	}
}

func TestOrchestrationGateAndOperatorDecisionsAreReducedAndAudited(t *testing.T) {
	fixture := newControlFixture(t)
	ctx := context.Background()
	fixture.plan("gate")

	pending := orchestration.GateStatusRequest{
		AssignmentID: "assignment-gate", AttemptID: "attempt-gate-1", GateID: "tests", GateVersion: 1,
		GateDigest: controlDigest("gate-tests-v1"), Status: storage.GatePending, ReasonCode: "awaiting_evidence",
		AuditID: "audit-gate-pending", ActorID: "gate-evaluator",
	}
	if result, err := fixture.service.RecordGateStatus(ctx, pending); err != nil || result.AlreadyPresent {
		t.Fatalf("pending gate = %#v, err=%v", result, err)
	}
	satisfied := pending
	satisfied.Status = storage.GateSatisfied
	satisfied.EvidenceID = "evidence-tests"
	satisfied.ReasonCode = "tests_passed"
	satisfied.AuditID = "audit-gate-satisfied"
	if result, err := fixture.service.RecordGateStatus(ctx, satisfied); err != nil || result.AlreadyPresent {
		t.Fatalf("satisfied gate = %#v, err=%v", result, err)
	}
	if result, err := fixture.service.RecordGateStatus(ctx, satisfied); err != nil || !result.AlreadyPresent {
		t.Fatalf("gate replay = %#v, err=%v", result, err)
	}
	rewrite := satisfied
	rewrite.Status = storage.GateFailed
	rewrite.ReasonCode = "rewritten"
	rewrite.AuditID = "audit-gate-rewrite"
	if _, err := fixture.service.RecordGateStatus(ctx, rewrite); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("terminal gate rewrite error = %v", err)
	}

	decision := orchestration.OperatorDecisionRequest{
		DecisionID: "decision-gate", AssignmentID: "assignment-gate", AttemptID: "attempt-gate-1", Decision: "gate_waive",
		ReasonCode: "approved_exception", ActorID: "operator-1", IdempotencyDigest: controlDigest("decision-gate"), AuditID: "audit-decision-gate",
	}
	if result, err := fixture.service.RecordOperatorDecision(ctx, decision); err != nil || result.AlreadyPresent {
		t.Fatalf("operator decision = %#v, err=%v", result, err)
	}
	if result, err := fixture.service.RecordOperatorDecision(ctx, decision); err != nil || !result.AlreadyPresent {
		t.Fatalf("decision replay = %#v, err=%v", result, err)
	}
	conflict := decision
	conflict.ReasonCode = "different_reason"
	conflict.AuditID = "audit-decision-conflict"
	if _, err := fixture.service.RecordOperatorDecision(ctx, conflict); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("decision conflict error = %v", err)
	}
	snapshot, err := fixture.store.OrchestrationControl().GetAssignment(ctx, "assignment-gate")
	if err != nil || len(snapshot.Gates) != 1 || snapshot.Gates[0].Status != storage.GateSatisfied || len(snapshot.Decisions) != 1 || snapshot.Decisions[0].DecisionID != decision.DecisionID {
		t.Fatalf("decision snapshot = %#v, err=%v", snapshot, err)
	}

	events, err := fixture.store.OrchestrationControl().ListOrchestrationAudit(ctx, "assignment-gate")
	if err != nil {
		t.Fatal(err)
	}
	var gateEvents, decisionEvents int
	for _, event := range events {
		if event.Action == "gate" {
			gateEvents++
		}
		if event.Action == "operator_gate_waive" {
			decisionEvents++
		}
	}
	if gateEvents != 2 || decisionEvents != 1 {
		t.Fatalf("decision audit counts: gate=%d operator=%d events=%#v", gateEvents, decisionEvents, events)
	}
}

func TestOrchestrationRestartClassifiesRecoveryWithoutReplayingRuntimeWork(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "restart.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	projectID := "project-restart"
	saveProjectRegistration(t, store, projectID)
	now := time.Date(2026, time.August, 21, 13, 0, 0, 0, time.UTC)
	service := orchestration.ControlService{Store: store.OrchestrationControl(), Now: func() time.Time { return now }, LeaseDuration: 5 * time.Minute}
	plan := func(key string) {
		_, err := service.Plan(ctx, orchestration.PlanRequest{
			AssignmentID: "assignment-" + key, AttemptID: "attempt-" + key + "-1", ProjectID: projectID, TaskID: "task-" + key,
			TaskRevision: 1, GraphRevision: 1, WorkPackageID: "work-" + key, ExecutionID: "execution-" + key,
			ContractID: "contract-" + key, ContractVersion: 1, ContractDigest: controlDigest("contract-" + key),
			WorkerID: "worker-1", NodeID: "node-1", IdempotencyDigest: controlDigest("plan-" + key),
			OperationID: "operation-plan-" + key, OperationDigest: controlDigest("operation-plan-" + key), AuditID: "audit-plan-" + key, ActorID: "scheduler",
			Binding: testAttemptBinding("attempt-"+key+"-1", "contract-"+key, 1, controlDigest("contract-"+key), now),
		})
		if err != nil {
			t.Fatalf("plan %s: %v", key, err)
		}
	}
	plan("resume")
	plan("reconcile")
	plan("operator")

	claim := func(key string) orchestration.ClaimRequest {
		request := orchestration.ClaimRequest{
			AssignmentID: "assignment-" + key, AttemptID: "attempt-" + key + "-1", LeaseID: "lease-" + key,
			OwnerNodeID: "node-1", OwnerRuntimeID: "runtime-owner-1", FencingDigest: controlDigest("fence-" + key),
			OperationID: "operation-claim-" + key, OperationDigest: controlDigest("operation-claim-" + key), AuditID: "audit-claim-" + key, ActorID: "scheduler",
		}
		if _, err := service.Claim(ctx, request); err != nil {
			t.Fatalf("claim %s: %v", key, err)
		}
		return request
	}
	claim("reconcile")
	operatorClaim := claim("operator")
	if _, err := service.BindResources(ctx, orchestration.BindResourcesRequest{
		AssignmentID: operatorClaim.AssignmentID, AttemptID: operatorClaim.AttemptID, LeaseGeneration: 1, FencingDigest: operatorClaim.FencingDigest,
		RuntimeSessionID: "runtime-session-operator", RuntimeResumeKey: controlDigest("resume-operator"), WorkspaceID: "workspace-operator", WorkspaceGeneration: 1,
		OperationID: "operation-bind-operator", OperationDigest: controlDigest("operation-bind-operator"), AuditID: "audit-bind-operator", ActorID: "scheduler",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Transition(ctx, orchestration.TransitionRequest{
		AssignmentID: operatorClaim.AssignmentID, AttemptID: operatorClaim.AttemptID, TargetState: storage.AssignmentRunning,
		LeaseGeneration: 1, FencingDigest: operatorClaim.FencingDigest, OperationID: "operation-start-operator",
		OperationDigest: controlDigest("operation-start-operator"), AuditID: "audit-start-operator", ActorID: "scheduler",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	recovered, err := (orchestration.ControlService{Store: reopened.OrchestrationControl(), Now: func() time.Time { return now }}).Reconcile(ctx, "startup-reconciler")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 3 {
		t.Fatalf("recovered assignments = %d, want 3", len(recovered))
	}
	want := map[string]storage.RecoveryDisposition{
		"assignment-resume": storage.RecoveryResume, "assignment-reconcile": storage.RecoveryReconcile, "assignment-operator": storage.RecoveryNeedsOperator,
	}
	for _, snapshot := range recovered {
		if snapshot.Assignment.RecoveryDisposition != want[snapshot.Assignment.AssignmentID] {
			t.Errorf("%s recovery = %s, want %s", snapshot.Assignment.AssignmentID, snapshot.Assignment.RecoveryDisposition, want[snapshot.Assignment.AssignmentID])
		}
		if snapshot.Assignment.AssignmentID == "assignment-operator" && snapshot.Attempt.State != storage.AssignmentRunning {
			t.Errorf("ambiguous runtime state was replayed or rewritten: %s", snapshot.Attempt.State)
		}
	}
}

func TestOrchestrationTimeoutRetryAndCanonicalProjectionIsolation(t *testing.T) {
	fixture := newControlFixture(t)
	ctx := context.Background()
	fixture.service.LeaseDuration = time.Minute
	fixture.plan("retry")
	firstClaim := fixture.claimRequest("retry", "first")
	if _, err := fixture.service.Claim(ctx, firstClaim); err != nil {
		t.Fatal(err)
	}
	fixture.now = fixture.now.Add(2 * time.Minute)
	start := make(chan struct{})
	var cancelErr, reconcileErr error
	var recovered []storage.OrchestrationSnapshot
	var race sync.WaitGroup
	race.Add(2)
	go func() {
		defer race.Done()
		<-start
		_, cancelErr = fixture.service.Transition(ctx, orchestration.TransitionRequest{
			AssignmentID: firstClaim.AssignmentID, AttemptID: firstClaim.AttemptID, TargetState: storage.AssignmentCanceled,
			LeaseGeneration: 1, FencingDigest: firstClaim.FencingDigest, OperationID: "operation-cancel-late",
			OperationDigest: controlDigest("operation-cancel-late"), AuditID: "audit-cancel-late", ActorID: "operator-1",
		})
	}()
	go func() {
		defer race.Done()
		<-start
		recovered, reconcileErr = fixture.service.Reconcile(ctx, "startup-reconciler")
	}()
	close(start)
	race.Wait()
	if !errors.Is(cancelErr, orchestration.ErrStaleFence) && !errors.Is(cancelErr, orchestration.ErrInvalidTransition) {
		t.Fatalf("cancellation/timeout race error = %v", cancelErr)
	}
	if reconcileErr != nil || len(recovered) != 1 || recovered[0].Attempt.State != storage.AssignmentExpired || recovered[0].Attempt.RecoveryDisposition != storage.RecoveryReconcile {
		t.Fatalf("expiration recovery = %#v, err=%v", recovered, reconcileErr)
	}
	if _, err := fixture.service.Retry(ctx, orchestration.RetryRequest{
		AssignmentID: firstClaim.AssignmentID, PreviousAttemptID: firstClaim.AttemptID, AttemptID: "attempt-retry-reused",
		IdempotencyDigest: recovered[0].Attempt.IdempotencyDigest, OperationID: "operation-retry-reused", OperationDigest: controlDigest("operation-retry-reused"),
		AuditID: "audit-retry-reused", ActorID: "operator-1",
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("reused retry idempotency error = %v", err)
	}

	retry, err := fixture.service.Retry(ctx, orchestration.RetryRequest{
		AssignmentID: firstClaim.AssignmentID, PreviousAttemptID: firstClaim.AttemptID, AttemptID: "attempt-retry-2",
		IdempotencyDigest: controlDigest("retry-attempt-2"), OperationID: "operation-retry-2", OperationDigest: controlDigest("operation-retry-2"),
		AuditID: "audit-retry-2", ActorID: "operator-1",
	})
	if err != nil || retry.Snapshot.Attempt.AttemptNumber != 2 || retry.Snapshot.Attempt.SupersedesAttemptID != firstClaim.AttemptID {
		t.Fatalf("Retry = %#v, err=%v", retry, err)
	}
	var activeAttempts int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orchestration_attempts WHERE project_id = ? AND work_package_id = ? AND state IN ('planned','leased','preparing','running','paused','collecting','awaiting_gates')`, fixture.project, "work-retry").Scan(&activeAttempts); err != nil || activeAttempts != 1 {
		t.Fatalf("active attempts = %d, err=%v", activeAttempts, err)
	}

	before, err := fixture.store.OrchestrationControl().GetAssignment(ctx, firstClaim.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ProjectProjections().RebuildProjectProjection(ctx, fixture.project, func(storage.ProjectProjectionWriter) error { return nil }); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.store.OrchestrationControl().GetAssignment(ctx, firstClaim.AssignmentID)
	if err != nil || after.Attempt.AttemptID != before.Attempt.AttemptID || after.Attempt.State != before.Attempt.State {
		t.Fatalf("canonical rebuild changed local control state: before=%#v after=%#v err=%v", before, after, err)
	}
	var canonicalEvents, leases int
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_events WHERE project_id = ?`, fixture.project).Scan(&canonicalEvents); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orchestration_leases WHERE assignment_id = ?`, firstClaim.AssignmentID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if canonicalEvents != 0 || leases != 1 {
		t.Fatalf("canonical/local separation failed: events=%d leases=%d", canonicalEvents, leases)
	}

	events, err := fixture.store.OrchestrationControl().ListOrchestrationAudit(ctx, firstClaim.AssignmentID)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, event := range events {
		actions[event.Action] = true
	}
	for _, action := range []string{"timeout", "retry", "recovery", "release"} {
		if !actions[action] {
			t.Errorf("audit trail is missing %q: %#v", action, events)
		}
	}
}

func controlDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func testAttemptBinding(attemptID, contractID string, version int64, contractDigest string, createdAt time.Time) storage.OrchestrationAttemptBinding {
	return storage.OrchestrationAttemptBinding{AttemptID: attemptID, ContractID: contractID, ContractVersion: version, ContractDigest: contractDigest, ContextDigest: controlDigest("context-" + attemptID), ContextCompilerVersion: "context-compiler:v1", BindingDigest: controlDigest("binding-" + attemptID), BindingJSON: []byte(`{"schema":"syncgate.attempt-binding.v1"}`), CreatedAt: createdAt}
}
