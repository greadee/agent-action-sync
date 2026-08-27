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
