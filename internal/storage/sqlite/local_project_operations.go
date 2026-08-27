package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"syncgate/internal/storage"
)

type localProjectOperationsStore struct{ db *sql.DB }

func (store *Store) LocalProjectOperations() storage.LocalProjectOperationsStore {
	return localProjectOperationsStore{db: store.db}
}

func (store localProjectOperationsStore) SetLocalProjectPolicy(ctx context.Context, policy storage.LocalProjectPolicy) error {
	if err := storage.ValidateProjectProjectionID(policy.ProjectID); err != nil || policy.MaxConcurrent < 1 || policy.MaxConcurrent > 2 || policy.UpdatedAt.IsZero() {
		return errors.New("local project policy is invalid")
	}
	result, err := store.db.ExecContext(ctx, `
INSERT INTO local_project_policies(project_id, scheduling_enabled, max_concurrent, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(project_id) DO UPDATE SET
    scheduling_enabled = excluded.scheduling_enabled,
    max_concurrent = excluded.max_concurrent,
    updated_at = excluded.updated_at`,
		policy.ProjectID, boolInt(policy.SchedulingEnabled), policy.MaxConcurrent, formatTime(policy.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("set local project policy: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return fmt.Errorf("set local project policy: expected one row")
	}
	return nil
}

func (store localProjectOperationsStore) GetLocalProjectPolicy(ctx context.Context, projectID string) (storage.LocalProjectPolicy, error) {
	if err := storage.ValidateProjectProjectionID(projectID); err != nil {
		return storage.LocalProjectPolicy{}, err
	}
	var policy storage.LocalProjectPolicy
	var enabled int
	var updatedAt string
	err := store.db.QueryRowContext(ctx, `
SELECT project_id, scheduling_enabled, max_concurrent, updated_at
FROM local_project_policies WHERE project_id = ?`, projectID).Scan(
		&policy.ProjectID, &enabled, &policy.MaxConcurrent, &updatedAt,
	)
	if err != nil {
		return storage.LocalProjectPolicy{}, mapNotFound(err, "local project policy", projectID)
	}
	policy.SchedulingEnabled = enabled != 0
	policy.UpdatedAt = parseStoredTime(updatedAt)
	return policy, nil
}

func (store localProjectOperationsStore) SelectLocalProject(ctx context.Context, selection storage.LocalProjectSelection) error {
	if err := storage.ValidateProjectProjectionID(selection.ProjectID); err != nil || selection.SelectedAt.IsZero() {
		return errors.New("local project selection is invalid")
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO local_project_selection(singleton, project_id, selected_at)
VALUES (1, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET project_id = excluded.project_id, selected_at = excluded.selected_at`,
		selection.ProjectID, formatTime(selection.SelectedAt),
	)
	if err != nil {
		return fmt.Errorf("select local project: %w", err)
	}
	return nil
}

func (store localProjectOperationsStore) GetSelectedLocalProject(ctx context.Context) (storage.LocalProjectSelection, error) {
	var selection storage.LocalProjectSelection
	var selectedAt string
	err := store.db.QueryRowContext(ctx, `SELECT project_id, selected_at FROM local_project_selection WHERE singleton = 1`).Scan(&selection.ProjectID, &selectedAt)
	if err != nil {
		return storage.LocalProjectSelection{}, mapNotFound(err, "local project selection", "active")
	}
	selection.SelectedAt = parseStoredTime(selectedAt)
	return selection, nil
}

func (store localProjectOperationsStore) GetProjectOrchestrationStatus(ctx context.Context, projectID string) (storage.ProjectOrchestrationStatus, error) {
	if err := storage.ValidateProjectProjectionID(projectID); err != nil {
		return storage.ProjectOrchestrationStatus{}, err
	}
	status := storage.ProjectOrchestrationStatus{
		AssignmentCounts: map[storage.AssignmentState]int64{},
		GateCounts:       map[storage.GateState]int64{},
	}
	assignmentRows, err := store.db.QueryContext(ctx, `
SELECT state, COUNT(*) FROM orchestration_assignments WHERE project_id = ? GROUP BY state`, projectID)
	if err != nil {
		return storage.ProjectOrchestrationStatus{}, fmt.Errorf("count project assignments: %w", err)
	}
	for assignmentRows.Next() {
		var state storage.AssignmentState
		var count int64
		if err := assignmentRows.Scan(&state, &count); err != nil {
			_ = assignmentRows.Close()
			return storage.ProjectOrchestrationStatus{}, fmt.Errorf("scan project assignment count: %w", err)
		}
		status.AssignmentCounts[state] = count
	}
	if err := assignmentRows.Close(); err != nil {
		return storage.ProjectOrchestrationStatus{}, err
	}
	gateRows, err := store.db.QueryContext(ctx, `
SELECT g.status, COUNT(*)
FROM orchestration_gate_status g
JOIN orchestration_assignments a ON a.assignment_id = g.assignment_id
WHERE a.project_id = ? GROUP BY g.status`, projectID)
	if err != nil {
		return storage.ProjectOrchestrationStatus{}, fmt.Errorf("count project gates: %w", err)
	}
	defer gateRows.Close()
	for gateRows.Next() {
		var state storage.GateState
		var count int64
		if err := gateRows.Scan(&state, &count); err != nil {
			return storage.ProjectOrchestrationStatus{}, fmt.Errorf("scan project gate count: %w", err)
		}
		status.GateCounts[state] = count
	}
	if err := gateRows.Err(); err != nil {
		return storage.ProjectOrchestrationStatus{}, err
	}
	return status, nil
}

var _ storage.LocalProjectOperationsStore = localProjectOperationsStore{}
