package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"syncgate/internal/storage"
)

func TestLocalProjectOperationsPersistExclusiveSelectionAndPolicies(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-local-a")
	saveProjectRegistration(t, store, "project-local-b")
	now := time.Date(2026, 8, 26, 15, 0, 0, 0, time.UTC)
	operations := store.LocalProjectOperations()

	if _, err := operations.GetSelectedLocalProject(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("empty selection error = %v", err)
	}
	if err := operations.SetLocalProjectPolicy(ctx, storage.LocalProjectPolicy{ProjectID: "project-local-a", SchedulingEnabled: true, MaxConcurrent: 2, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := operations.SelectLocalProject(ctx, storage.LocalProjectSelection{ProjectID: "project-local-a", SelectedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := operations.SelectLocalProject(ctx, storage.LocalProjectSelection{ProjectID: "project-local-b", SelectedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	selected, err := operations.GetSelectedLocalProject(ctx)
	if err != nil || selected.ProjectID != "project-local-b" {
		t.Fatalf("selected = %+v, err=%v", selected, err)
	}
	policy, err := operations.GetLocalProjectPolicy(ctx, "project-local-a")
	if err != nil || !policy.SchedulingEnabled || policy.MaxConcurrent != 2 {
		t.Fatalf("policy = %+v, err=%v", policy, err)
	}
}

func TestProjectOrchestrationStatusIsProjectScoped(t *testing.T) {
	ctx := context.Background()
	fixture := newControlFixture(t)
	other := "project-status-other"
	saveProjectRegistration(t, fixture.store, other)
	fixture.plan("status-a")

	status, err := fixture.store.LocalProjectOperations().GetProjectOrchestrationStatus(ctx, fixture.project)
	if err != nil || status.AssignmentCounts[storage.AssignmentPlanned] != 1 {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	otherStatus, err := fixture.store.LocalProjectOperations().GetProjectOrchestrationStatus(ctx, other)
	if err != nil || len(otherStatus.AssignmentCounts) != 0 || len(otherStatus.GateCounts) != 0 {
		t.Fatalf("other status = %+v, err=%v", otherStatus, err)
	}
}

func TestLocalOperatorOperationLedgerReplaysAndRejectsKeyReuse(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-operator-ledger")
	operations := store.LocalProjectOperations()
	operation := storage.LocalOperatorOperation{
		IdempotencyKey: "browser-control-one", Fingerprint: projectionHash("a"), Action: "disable_scheduler",
		ProjectID: "project-operator-ledger", SubjectID: "project-operator-ledger", ResultJSON: []byte(`{"state":"paused"}`),
		OccurredAt: time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC),
	}
	first, err := operations.SaveLocalOperatorOperation(ctx, operation)
	if err != nil || first.AlreadyPresent {
		t.Fatalf("first operation = %+v, err=%v", first, err)
	}
	replay, err := operations.SaveLocalOperatorOperation(ctx, operation)
	if err != nil || !replay.AlreadyPresent || string(replay.Operation.ResultJSON) != string(operation.ResultJSON) {
		t.Fatalf("replay = %+v, err=%v", replay, err)
	}
	conflict := operation
	conflict.Fingerprint = projectionHash("b")
	if _, err := operations.SaveLocalOperatorOperation(ctx, conflict); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflicting reuse error = %v", err)
	}
	loaded, err := operations.GetLocalOperatorOperation(ctx, operation.IdempotencyKey)
	if err != nil || loaded.Action != operation.Action || string(loaded.ResultJSON) != string(operation.ResultJSON) {
		t.Fatalf("loaded operation = %+v, err=%v", loaded, err)
	}
	found, err := operations.FindLatestLocalOperatorOperation(ctx, operation.Action, operation.ProjectID, operation.SubjectID)
	if err != nil || found.IdempotencyKey != operation.IdempotencyKey || string(found.ResultJSON) != string(operation.ResultJSON) {
		t.Fatalf("found operation = %+v, err=%v", found, err)
	}
}

func TestLocalIntegrationSummaryPersistsLatestAttemptEvidence(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	saveProjectRegistration(t, store, "project-integration-summary")
	operations := store.LocalProjectOperations()
	first := storage.LocalIntegrationSummary{
		AssignmentID: "assignment:summary", AttemptID: "attempt:summary-one", ProjectID: "project-integration-summary",
		SummaryDigest: projectionHash("a"), SummaryJSON: []byte(`{"summary":"one"}`), RecordedAt: time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
	}
	if err := operations.SaveLocalIntegrationSummary(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.AttemptID, second.SummaryDigest, second.SummaryJSON, second.RecordedAt = "attempt:summary-two", projectionHash("b"), []byte(`{"summary":"two"}`), first.RecordedAt.Add(time.Minute)
	if err := operations.SaveLocalIntegrationSummary(ctx, second); err != nil {
		t.Fatal(err)
	}
	loaded, err := operations.GetLocalIntegrationSummary(ctx, first.AssignmentID)
	if err != nil || loaded.AttemptID != second.AttemptID || loaded.SummaryDigest != second.SummaryDigest || string(loaded.SummaryJSON) != string(second.SummaryJSON) {
		t.Fatalf("loaded summary = %+v, err=%v", loaded, err)
	}
}
