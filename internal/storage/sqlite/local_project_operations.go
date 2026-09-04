package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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

func (store localProjectOperationsStore) SaveLocalOperatorOperation(ctx context.Context, operation storage.LocalOperatorOperation) (storage.LocalOperatorOperationResult, error) {
	if err := validateLocalOperatorOperation(operation); err != nil {
		return storage.LocalOperatorOperationResult{}, err
	}
	result, err := store.db.ExecContext(ctx, `
INSERT OR IGNORE INTO local_operator_operations(idempotency_key, fingerprint, action, project_id, subject_id, result_json, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, operation.IdempotencyKey, operation.Fingerprint, operation.Action, operation.ProjectID, operation.SubjectID, operation.ResultJSON, formatTime(operation.OccurredAt))
	if err != nil {
		return storage.LocalOperatorOperationResult{}, fmt.Errorf("save local operator operation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.LocalOperatorOperationResult{}, err
	}
	if changed == 1 {
		operation.ResultJSON = append([]byte(nil), operation.ResultJSON...)
		return storage.LocalOperatorOperationResult{Operation: operation}, nil
	}
	existing, err := store.GetLocalOperatorOperation(ctx, operation.IdempotencyKey)
	if err != nil {
		return storage.LocalOperatorOperationResult{}, err
	}
	if existing.Fingerprint != operation.Fingerprint || existing.Action != operation.Action || existing.ProjectID != operation.ProjectID || existing.SubjectID != operation.SubjectID {
		return storage.LocalOperatorOperationResult{}, storage.ErrConflict
	}
	return storage.LocalOperatorOperationResult{AlreadyPresent: true, Operation: existing}, nil
}

func (store localProjectOperationsStore) GetLocalOperatorOperation(ctx context.Context, key string) (storage.LocalOperatorOperation, error) {
	if strings.TrimSpace(key) == "" || len(key) > 128 {
		return storage.LocalOperatorOperation{}, errors.New("local operator idempotency key is invalid")
	}
	var operation storage.LocalOperatorOperation
	var occurredAt string
	err := store.db.QueryRowContext(ctx, `
SELECT idempotency_key, fingerprint, action, project_id, subject_id, result_json, occurred_at
FROM local_operator_operations WHERE idempotency_key = ?`, key).Scan(
		&operation.IdempotencyKey, &operation.Fingerprint, &operation.Action, &operation.ProjectID,
		&operation.SubjectID, &operation.ResultJSON, &occurredAt,
	)
	if err != nil {
		return storage.LocalOperatorOperation{}, mapNotFound(err, "local operator operation", key)
	}
	operation.OccurredAt = parseStoredTime(occurredAt)
	operation.ResultJSON = append([]byte(nil), operation.ResultJSON...)
	return operation, nil
}

func (store localProjectOperationsStore) FindLatestLocalOperatorOperation(ctx context.Context, action, projectID, subjectID string) (storage.LocalOperatorOperation, error) {
	if strings.TrimSpace(action) == "" || len(action) > 64 || storage.ValidateProjectProjectionID(projectID) != nil || strings.TrimSpace(subjectID) == "" || len(subjectID) > 128 {
		return storage.LocalOperatorOperation{}, errors.New("local operator operation query is invalid")
	}
	var operation storage.LocalOperatorOperation
	var occurredAt string
	err := store.db.QueryRowContext(ctx, `
SELECT idempotency_key, fingerprint, action, project_id, subject_id, result_json, occurred_at
FROM local_operator_operations
WHERE action = ? AND project_id = ? AND subject_id = ?
ORDER BY occurred_at DESC, idempotency_key DESC
LIMIT 1`, action, projectID, subjectID).Scan(
		&operation.IdempotencyKey, &operation.Fingerprint, &operation.Action, &operation.ProjectID,
		&operation.SubjectID, &operation.ResultJSON, &occurredAt,
	)
	if err != nil {
		return storage.LocalOperatorOperation{}, mapNotFound(err, "local operator operation", action+":"+subjectID)
	}
	operation.OccurredAt = parseStoredTime(occurredAt)
	operation.ResultJSON = append([]byte(nil), operation.ResultJSON...)
	return operation, nil
}

func (store localProjectOperationsStore) SaveLocalIntegrationSummary(ctx context.Context, summary storage.LocalIntegrationSummary) error {
	if err := validateLocalIntegrationSummary(summary); err != nil {
		return err
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO local_integration_summaries(assignment_id, attempt_id, project_id, summary_digest, summary_json, recorded_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(assignment_id) DO UPDATE SET
    attempt_id = excluded.attempt_id,
    project_id = excluded.project_id,
    summary_digest = excluded.summary_digest,
    summary_json = excluded.summary_json,
    recorded_at = excluded.recorded_at`,
		summary.AssignmentID, summary.AttemptID, summary.ProjectID, summary.SummaryDigest, summary.SummaryJSON, formatTime(summary.RecordedAt))
	if err != nil {
		return fmt.Errorf("save local integration summary: %w", err)
	}
	return nil
}

func (store localProjectOperationsStore) GetLocalIntegrationSummary(ctx context.Context, assignmentID string) (storage.LocalIntegrationSummary, error) {
	if strings.TrimSpace(assignmentID) == "" || len(assignmentID) > 128 {
		return storage.LocalIntegrationSummary{}, errors.New("local integration assignment ID is invalid")
	}
	var summary storage.LocalIntegrationSummary
	var recordedAt string
	err := store.db.QueryRowContext(ctx, `
SELECT assignment_id, attempt_id, project_id, summary_digest, summary_json, recorded_at
FROM local_integration_summaries WHERE assignment_id = ?`, assignmentID).Scan(
		&summary.AssignmentID, &summary.AttemptID, &summary.ProjectID, &summary.SummaryDigest, &summary.SummaryJSON, &recordedAt)
	if err != nil {
		return storage.LocalIntegrationSummary{}, mapNotFound(err, "local integration summary", assignmentID)
	}
	summary.RecordedAt = parseStoredTime(recordedAt)
	summary.SummaryJSON = append([]byte(nil), summary.SummaryJSON...)
	return summary, nil
}

func validateLocalOperatorOperation(operation storage.LocalOperatorOperation) error {
	if strings.TrimSpace(operation.IdempotencyKey) == "" || len(operation.IdempotencyKey) > 128 ||
		len(operation.Fingerprint) != 64 || strings.Trim(operation.Fingerprint, "0123456789abcdef") != "" ||
		strings.TrimSpace(operation.Action) == "" || len(operation.Action) > 64 ||
		storage.ValidateProjectProjectionID(operation.ProjectID) != nil || strings.TrimSpace(operation.SubjectID) == "" || len(operation.SubjectID) > 128 ||
		len(operation.ResultJSON) == 0 || len(operation.ResultJSON) > storage.MaxLocalOperatorResultBytes || operation.OccurredAt.IsZero() {
		return errors.New("local operator operation is invalid")
	}
	return nil
}

func validateLocalIntegrationSummary(summary storage.LocalIntegrationSummary) error {
	if !strings.HasPrefix(summary.AssignmentID, "assignment:") || len(summary.AssignmentID) > 128 ||
		!strings.HasPrefix(summary.AttemptID, "attempt:") || len(summary.AttemptID) > 128 ||
		storage.ValidateProjectProjectionID(summary.ProjectID) != nil || len(summary.SummaryDigest) != 64 || strings.Trim(summary.SummaryDigest, "0123456789abcdef") != "" ||
		len(summary.SummaryJSON) == 0 || len(summary.SummaryJSON) > storage.MaxLocalOperatorResultBytes || summary.RecordedAt.IsZero() {
		return errors.New("local integration summary is invalid")
	}
	return nil
}

var _ storage.LocalProjectOperationsStore = localProjectOperationsStore{}
