package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"syncgate/internal/storage"
)

func (store orchestrationControlStore) PlanAssignment(ctx context.Context, request storage.OrchestrationPlanRequest) (result storage.OrchestrationWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, ok, replayErr := orchestrationOperationReplay(ctx, tx, request.OperationID, request.OperationDigest); replayErr != nil {
		return result, replayErr
	} else if ok {
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: replay}, nil
	}
	a := request.Assignment
	insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO orchestration_assignments(
assignment_id, project_id, task_id, task_revision, graph_revision, work_package_id, execution_id,
contract_id, contract_version, contract_digest, worker_id, node_id, state, current_attempt_id,
current_attempt_number, idempotency_digest, recovery_disposition, failure_code, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.AssignmentID, a.ProjectID, a.TaskID, a.TaskRevision, a.GraphRevision, a.WorkPackageID, a.ExecutionID,
		a.ContractID, a.ContractVersion, a.ContractDigest, a.WorkerID, a.NodeID, a.State, a.CurrentAttemptID,
		a.CurrentAttempt, a.IdempotencyDigest, a.RecoveryDisposition, nullableString(a.FailureCode), formatTime(a.CreatedAt), formatTime(a.UpdatedAt))
	if err != nil {
		return result, fmt.Errorf("plan orchestration assignment: %w", err)
	}
	changed, err := insert.RowsAffected()
	if err != nil {
		return result, err
	}
	if changed == 0 {
		existing, loadErr := orchestrationSnapshotTx(ctx, tx, a.AssignmentID)
		if loadErr != nil || existing.Assignment.IdempotencyDigest != a.IdempotencyDigest || existing.Assignment.ContractDigest != a.ContractDigest || existing.Assignment.CurrentAttemptID != a.CurrentAttemptID {
			return result, fmt.Errorf("%w: orchestration assignment already exists", storage.ErrConflict)
		}
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: existing}, nil
	}
	if err := insertOrchestrationAttempt(ctx, tx, request.Attempt); err != nil {
		return result, err
	}
	if err := recordOrchestrationOperation(ctx, tx, request.OperationID, request.OperationDigest, a.AssignmentID, request.Attempt.AttemptID, a.State, a.CreatedAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	snapshot, err := orchestrationSnapshotTx(ctx, tx, a.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (store orchestrationControlStore) ClaimAssignment(ctx context.Context, request storage.OrchestrationClaimRequest) (result storage.OrchestrationWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, ok, replayErr := orchestrationOperationReplay(ctx, tx, request.OperationID, request.OperationDigest); replayErr != nil {
		return result, replayErr
	} else if ok {
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: replay}, nil
	}
	snapshot, err := orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if snapshot.Attempt.AttemptID != request.AttemptID || snapshot.Attempt.State != request.ExpectedState || snapshot.Assignment.CurrentAttemptID != request.AttemptID {
		return result, fmt.Errorf("%w: assignment is not claimable", storage.ErrConflict)
	}
	lease := request.Lease
	lease.Generation = snapshot.Attempt.LeaseGeneration + 1
	lease.AssignmentID = request.AssignmentID
	lease.AttemptID = request.AttemptID
	if _, err := tx.ExecContext(ctx, `INSERT INTO orchestration_leases(
lease_id, assignment_id, attempt_id, generation, owner_node_id, owner_runtime_id, fencing_digest,
state, acquired_at, heartbeat_at, expires_at, released_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		lease.LeaseID, lease.AssignmentID, lease.AttemptID, lease.Generation, lease.OwnerNodeID, lease.OwnerRuntime,
		lease.FencingDigest, storage.LeaseActive, formatTime(lease.AcquiredAt), formatTime(lease.HeartbeatAt), formatTime(lease.ExpiresAt)); err != nil {
		return result, fmt.Errorf("claim orchestration lease: %w", err)
	}
	if err := updateAttemptState(ctx, tx, request.AssignmentID, request.AttemptID, request.ExpectedState, storage.AssignmentLeased, lease.Generation, "", storage.RecoveryNone, lease.AcquiredAt); err != nil {
		return result, err
	}
	request.Audit.LeaseGeneration = lease.Generation
	if err := recordOrchestrationOperation(ctx, tx, request.OperationID, request.OperationDigest, request.AssignmentID, request.AttemptID, storage.AssignmentLeased, lease.AcquiredAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	snapshot, err = orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (store orchestrationControlStore) BindAttemptResources(ctx context.Context, request storage.OrchestrationResourceRequest) (result storage.OrchestrationWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, ok, replayErr := orchestrationOperationReplay(ctx, tx, request.OperationID, request.OperationDigest); replayErr != nil {
		return result, replayErr
	} else if ok {
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: replay}, nil
	}
	snapshot, err := fencedSnapshot(ctx, tx, request.AssignmentID, request.AttemptID, request.ExpectedState, request.LeaseGeneration, request.FencingDigest, request.Resources.UpdatedAt)
	if err != nil {
		return result, err
	}
	resources := request.Resources
	if _, err := tx.ExecContext(ctx, `INSERT INTO orchestration_resource_bindings(
attempt_id, lease_generation, runtime_session_id, runtime_resume_key_digest, runtime_state,
workspace_id, workspace_generation, workspace_state, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		resources.AttemptID, resources.LeaseGeneration, resources.RuntimeSessionID, resources.RuntimeResumeKey,
		resources.RuntimeState, resources.WorkspaceID, resources.WorkspaceGeneration, resources.WorkspaceState, formatTime(resources.UpdatedAt)); err != nil {
		return result, fmt.Errorf("bind orchestration resources: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orchestration_attempts SET runtime_session_id = ?, workspace_id = ? WHERE attempt_id = ?`, resources.RuntimeSessionID, resources.WorkspaceID, request.AttemptID); err != nil {
		return result, err
	}
	if err := updateAttemptState(ctx, tx, request.AssignmentID, request.AttemptID, request.ExpectedState, storage.AssignmentPreparing, snapshot.Attempt.LeaseGeneration, "", storage.RecoveryNone, resources.UpdatedAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationOperation(ctx, tx, request.OperationID, request.OperationDigest, request.AssignmentID, request.AttemptID, storage.AssignmentPreparing, resources.UpdatedAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	snapshot, err = orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (store orchestrationControlStore) TransitionAssignment(ctx context.Context, request storage.OrchestrationTransitionRequest) (result storage.OrchestrationWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, ok, replayErr := orchestrationOperationReplay(ctx, tx, request.OperationID, request.OperationDigest); replayErr != nil {
		return result, replayErr
	} else if ok {
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: replay}, nil
	}
	snapshot, err := fencedSnapshot(ctx, tx, request.AssignmentID, request.AttemptID, request.ExpectedState, request.LeaseGeneration, request.FencingDigest, request.UpdatedAt)
	if err != nil {
		return result, err
	}
	if err := updateAttemptState(ctx, tx, request.AssignmentID, request.AttemptID, request.ExpectedState, request.TargetState, snapshot.Attempt.LeaseGeneration, request.FailureCode, request.Recovery, request.UpdatedAt); err != nil {
		return result, err
	}
	if terminalAssignmentState(request.TargetState) {
		leaseState := storage.LeaseReleased
		reason := "lease_released"
		if request.TargetState == storage.AssignmentExpired {
			leaseState = storage.LeaseExpired
			reason = "lease_expired"
		}
		released, err := tx.ExecContext(ctx, `UPDATE orchestration_leases SET state = ?, released_at = ? WHERE attempt_id = ? AND generation = ? AND state = ?`, leaseState, formatTime(request.UpdatedAt), request.AttemptID, request.LeaseGeneration, storage.LeaseActive)
		if err != nil {
			return result, err
		}
		changed, err := released.RowsAffected()
		if err != nil || changed != 1 {
			return result, fmt.Errorf("%w: orchestration lease was not active", storage.ErrConflict)
		}
		release := storage.OrchestrationAuditEvent{AuditID: recoveryAuditID("release", request.AssignmentID, request.AttemptID, request.UpdatedAt), AssignmentID: request.AssignmentID, AttemptID: request.AttemptID, Action: "release", FromState: request.TargetState, ToState: request.TargetState, ReasonCode: reason, ActorID: request.Audit.ActorID, LeaseGeneration: request.LeaseGeneration, OccurredAt: request.UpdatedAt}
		if err := recordOrchestrationAudit(ctx, tx, release); err != nil {
			return result, err
		}
	}
	if err := recordOrchestrationOperation(ctx, tx, request.OperationID, request.OperationDigest, request.AssignmentID, request.AttemptID, request.TargetState, request.UpdatedAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	snapshot, err = orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (store orchestrationControlStore) HeartbeatLease(ctx context.Context, request storage.OrchestrationHeartbeatRequest) (result storage.OrchestrationSnapshot, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if snapshot.Attempt.AttemptID != request.AttemptID || snapshot.Lease == nil || snapshot.Lease.LeaseID != request.LeaseID || snapshot.Lease.Generation != request.LeaseGeneration || snapshot.Lease.FencingDigest != request.FencingDigest || snapshot.Lease.State != storage.LeaseActive || !snapshot.Lease.ExpiresAt.After(request.HeartbeatAt) {
		return result, fmt.Errorf("%w: stale or expired orchestration lease", storage.ErrConflict)
	}
	if !request.ExpiresAt.After(request.HeartbeatAt) {
		return result, errors.New("lease expiry must follow heartbeat")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orchestration_leases SET heartbeat_at = ?, expires_at = ? WHERE lease_id = ? AND generation = ? AND state = ?`, formatTime(request.HeartbeatAt), formatTime(request.ExpiresAt), request.LeaseID, request.LeaseGeneration, storage.LeaseActive); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orchestration_assignments SET updated_at = ? WHERE assignment_id = ?`, formatTime(request.HeartbeatAt), request.AssignmentID); err != nil {
		return result, err
	}
	snapshot, err = orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return snapshot, nil
}

func (store orchestrationControlStore) RetryAssignment(ctx context.Context, request storage.OrchestrationRetryRequest) (result storage.OrchestrationWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, ok, replayErr := orchestrationOperationReplay(ctx, tx, request.OperationID, request.OperationDigest); replayErr != nil {
		return result, replayErr
	} else if ok {
		return storage.OrchestrationWriteResult{AlreadyPresent: true, Snapshot: replay}, nil
	}
	snapshot, err := orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if snapshot.Attempt.AttemptID != request.PreviousAttemptID || !retryableTerminalState(snapshot.Attempt.State) || request.Attempt.AttemptNumber != snapshot.Attempt.AttemptNumber+1 || request.Attempt.SupersedesAttemptID != snapshot.Attempt.AttemptID {
		return result, fmt.Errorf("%w: orchestration attempt cannot be retried", storage.ErrConflict)
	}
	if err := insertOrchestrationAttempt(ctx, tx, request.Attempt); err != nil {
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orchestration_assignments SET state = ?, current_attempt_id = ?, current_attempt_number = ?, idempotency_digest = ?, recovery_disposition = ?, failure_code = NULL, updated_at = ? WHERE assignment_id = ? AND current_attempt_id = ?`,
		storage.AssignmentPlanned, request.Attempt.AttemptID, request.Attempt.AttemptNumber, request.Attempt.IdempotencyDigest, storage.RecoveryNone, formatTime(request.Attempt.UpdatedAt), request.AssignmentID, request.PreviousAttemptID); err != nil {
		return result, err
	}
	if err := recordOrchestrationOperation(ctx, tx, request.OperationID, request.OperationDigest, request.AssignmentID, request.Attempt.AttemptID, storage.AssignmentPlanned, request.Attempt.CreatedAt); err != nil {
		return result, err
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	snapshot, err = orchestrationSnapshotTx(ctx, tx, request.AssignmentID)
	if err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return storage.OrchestrationWriteResult{Snapshot: snapshot}, nil
}

func (store orchestrationControlStore) ReconcileAssignments(ctx context.Context, now time.Time, actorID string) (snapshots []storage.OrchestrationSnapshot, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT assignment_id FROM orchestration_assignments WHERE state IN ('planned','leased','preparing','running','paused','collecting','awaiting_gates') ORDER BY assignment_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		snapshot, err := orchestrationSnapshotTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		from := snapshot.Attempt.State
		recovery := recoveryDisposition(from, snapshot.Resources != nil)
		target := from
		reason := "startup_" + string(recovery)
		if snapshot.Lease != nil && snapshot.Lease.State == storage.LeaseActive && !snapshot.Lease.ExpiresAt.After(now) {
			target = storage.AssignmentExpired
			recovery = storage.RecoveryNeedsOperator
			if from == storage.AssignmentLeased && snapshot.Resources == nil {
				recovery = storage.RecoveryReconcile
			}
			reason = "lease_expired"
			if _, err := tx.ExecContext(ctx, `UPDATE orchestration_leases SET state = ?, released_at = ? WHERE lease_id = ? AND state = ?`, storage.LeaseExpired, formatTime(now), snapshot.Lease.LeaseID, storage.LeaseActive); err != nil {
				return nil, err
			}
			release := storage.OrchestrationAuditEvent{AuditID: recoveryAuditID("release", id, snapshot.Attempt.AttemptID, now), AssignmentID: id, AttemptID: snapshot.Attempt.AttemptID, Action: "release", FromState: target, ToState: target, ReasonCode: "lease_expired", ActorID: actorID, LeaseGeneration: snapshot.Attempt.LeaseGeneration, OccurredAt: now}
			if err := recordOrchestrationAudit(ctx, tx, release); err != nil {
				return nil, err
			}
		}
		if target != from {
			if err := updateAttemptState(ctx, tx, id, snapshot.Attempt.AttemptID, from, target, snapshot.Attempt.LeaseGeneration, reason, recovery, now); err != nil {
				return nil, err
			}
			if target == storage.AssignmentExpired {
				timeout := storage.OrchestrationAuditEvent{AuditID: recoveryAuditID("timeout", id, snapshot.Attempt.AttemptID, now), AssignmentID: id, AttemptID: snapshot.Attempt.AttemptID, Action: "timeout", FromState: from, ToState: target, ReasonCode: reason, ActorID: actorID, LeaseGeneration: snapshot.Attempt.LeaseGeneration, OccurredAt: now}
				if err := recordOrchestrationAudit(ctx, tx, timeout); err != nil {
					return nil, err
				}
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE orchestration_attempts SET recovery_disposition = ?, updated_at = ? WHERE attempt_id = ?`, recovery, formatTime(now), snapshot.Attempt.AttemptID); err != nil {
			return nil, err
		} else if _, err := tx.ExecContext(ctx, `UPDATE orchestration_assignments SET recovery_disposition = ?, updated_at = ? WHERE assignment_id = ?`, recovery, formatTime(now), id); err != nil {
			return nil, err
		}
		audit := storage.OrchestrationAuditEvent{AuditID: recoveryAuditID("recovery", id, snapshot.Attempt.AttemptID, now), AssignmentID: id, AttemptID: snapshot.Attempt.AttemptID, Action: "recovery", FromState: target, ToState: target, ReasonCode: reason, ActorID: actorID, LeaseGeneration: snapshot.Attempt.LeaseGeneration, OccurredAt: now}
		if err := recordOrchestrationAudit(ctx, tx, audit); err != nil {
			return nil, err
		}
		updated, err := orchestrationSnapshotTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, updated)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func (store orchestrationControlStore) GetAssignment(ctx context.Context, assignmentID string) (storage.OrchestrationSnapshot, error) {
	return orchestrationSnapshotDB(ctx, store.db, assignmentID)
}

func (store orchestrationControlStore) SaveGateStatus(ctx context.Context, request storage.OrchestrationGateRequest) (result storage.RegistryWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	gate := request.Gate
	var assignmentID, digest string
	var status storage.GateState
	var evidenceID, reasonCode sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT assignment_id, gate_digest, status, evidence_id, reason_code FROM orchestration_gate_status WHERE attempt_id = ? AND gate_id = ? AND gate_version = ?`, gate.AttemptID, gate.GateID, gate.GateVersion).Scan(&assignmentID, &digest, &status, &evidenceID, &reasonCode)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO orchestration_gate_status(assignment_id, attempt_id, gate_id, gate_version, gate_digest, status, evidence_id, reason_code, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			gate.AssignmentID, gate.AttemptID, gate.GateID, gate.GateVersion, gate.GateDigest, gate.Status, nullableString(gate.EvidenceID), nullableString(gate.ReasonCode), formatTime(gate.UpdatedAt)); err != nil {
			return result, err
		}
	case err != nil:
		return result, err
	case assignmentID != gate.AssignmentID || digest != gate.GateDigest:
		return result, storage.ErrConflict
	case status == gate.Status && evidenceID.String == gate.EvidenceID && reasonCode.String == gate.ReasonCode:
		return storage.RegistryWriteResult{AlreadyPresent: true}, nil
	case !validGateTransition(status, gate.Status):
		return result, storage.ErrConflict
	default:
		updated, err := tx.ExecContext(ctx, `UPDATE orchestration_gate_status SET status = ?, evidence_id = ?, reason_code = ?, updated_at = ? WHERE attempt_id = ? AND gate_id = ? AND gate_version = ? AND status = ?`,
			gate.Status, nullableString(gate.EvidenceID), nullableString(gate.ReasonCode), formatTime(gate.UpdatedAt), gate.AttemptID, gate.GateID, gate.GateVersion, status)
		if err != nil {
			return result, err
		}
		changed, err := updated.RowsAffected()
		if err != nil || changed != 1 {
			return result, storage.ErrConflict
		}
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (store orchestrationControlStore) SaveOperatorDecision(ctx context.Context, request storage.OrchestrationDecisionRequest) (result storage.RegistryWriteResult, err error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback() }()
	decision := request.Decision
	insert, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO orchestration_operator_decisions(decision_id, assignment_id, attempt_id, decision, reason_code, actor_id, idempotency_digest, decided_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		decision.DecisionID, decision.AssignmentID, decision.AttemptID, decision.Decision, decision.ReasonCode, decision.ActorID, decision.IdempotencyDigest, formatTime(decision.DecidedAt))
	if err != nil {
		return result, err
	}
	changed, err := insert.RowsAffected()
	if err != nil {
		return result, err
	}
	if changed == 0 {
		var existing storage.OrchestrationOperatorDecision
		err := tx.QueryRowContext(ctx, `SELECT assignment_id, attempt_id, decision, reason_code, actor_id, idempotency_digest FROM orchestration_operator_decisions WHERE decision_id = ?`, decision.DecisionID).Scan(
			&existing.AssignmentID, &existing.AttemptID, &existing.Decision, &existing.ReasonCode, &existing.ActorID, &existing.IdempotencyDigest)
		if err != nil {
			return result, err
		}
		if existing.AssignmentID != decision.AssignmentID || existing.AttemptID != decision.AttemptID || existing.Decision != decision.Decision || existing.ReasonCode != decision.ReasonCode || existing.ActorID != decision.ActorID || existing.IdempotencyDigest != decision.IdempotencyDigest {
			return result, storage.ErrConflict
		}
		return storage.RegistryWriteResult{AlreadyPresent: true}, nil
	}
	if err := recordOrchestrationAudit(ctx, tx, request.Audit); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (store orchestrationControlStore) ListOrchestrationAudit(ctx context.Context, assignmentID string) ([]storage.OrchestrationAuditEvent, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT audit_id, assignment_id, attempt_id, action, from_state, to_state, reason_code, actor_id, lease_generation, occurred_at FROM orchestration_audit_events WHERE assignment_id = ? ORDER BY occurred_at, audit_id`, assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []storage.OrchestrationAuditEvent
	for rows.Next() {
		var event storage.OrchestrationAuditEvent
		var from sql.NullString
		var occurred string
		if err := rows.Scan(&event.AuditID, &event.AssignmentID, &event.AttemptID, &event.Action, &from, &event.ToState, &event.ReasonCode, &event.ActorID, &event.LeaseGeneration, &occurred); err != nil {
			return nil, err
		}
		event.FromState = storage.AssignmentState(from.String)
		event.OccurredAt = parseStoredTime(occurred)
		events = append(events, event)
	}
	return events, rows.Err()
}

func insertOrchestrationAttempt(ctx context.Context, tx *sql.Tx, attempt storage.OrchestrationAttempt) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO orchestration_attempts(
attempt_id, assignment_id, project_id, work_package_id, execution_id, attempt_number, supersedes_attempt_id,
state, lease_generation, runtime_session_id, workspace_id, idempotency_digest, recovery_disposition,
failure_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.AttemptID, attempt.AssignmentID, attempt.ProjectID, attempt.WorkPackageID, attempt.ExecutionID, attempt.AttemptNumber, nullableString(attempt.SupersedesAttemptID),
		attempt.State, attempt.LeaseGeneration, nullableString(attempt.RuntimeSessionID), nullableString(attempt.WorkspaceID), attempt.IdempotencyDigest,
		attempt.RecoveryDisposition, nullableString(attempt.FailureCode), formatTime(attempt.CreatedAt), formatTime(attempt.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save orchestration attempt: %w", err)
	}
	return nil
}

func updateAttemptState(ctx context.Context, tx *sql.Tx, assignmentID, attemptID string, expected, target storage.AssignmentState, generation int64, failure string, recovery storage.RecoveryDisposition, updatedAt time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE orchestration_attempts SET state = ?, lease_generation = ?, failure_code = ?, recovery_disposition = ?, updated_at = ? WHERE attempt_id = ? AND assignment_id = ? AND state = ?`,
		target, generation, nullableString(failure), recovery, formatTime(updatedAt), attemptID, assignmentID, expected)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("%w: stale orchestration attempt state", storage.ErrConflict)
	}
	result, err = tx.ExecContext(ctx, `UPDATE orchestration_assignments SET state = ?, failure_code = ?, recovery_disposition = ?, updated_at = ? WHERE assignment_id = ? AND current_attempt_id = ? AND state = ?`,
		target, nullableString(failure), recovery, formatTime(updatedAt), assignmentID, attemptID, expected)
	if err != nil {
		return err
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("%w: stale orchestration assignment state", storage.ErrConflict)
	}
	return nil
}

func fencedSnapshot(ctx context.Context, tx *sql.Tx, assignmentID, attemptID string, state storage.AssignmentState, generation int64, digest string, at time.Time) (storage.OrchestrationSnapshot, error) {
	snapshot, err := orchestrationSnapshotTx(ctx, tx, assignmentID)
	if err != nil {
		return snapshot, err
	}
	if snapshot.Attempt.AttemptID != attemptID || snapshot.Attempt.State != state || snapshot.Attempt.LeaseGeneration != generation || snapshot.Lease == nil || snapshot.Lease.Generation != generation || snapshot.Lease.FencingDigest != digest || snapshot.Lease.State != storage.LeaseActive || !snapshot.Lease.ExpiresAt.After(at) {
		return storage.OrchestrationSnapshot{}, fmt.Errorf("%w: stale orchestration fence", storage.ErrConflict)
	}
	return snapshot, nil
}

func orchestrationOperationReplay(ctx context.Context, tx *sql.Tx, operationID, digest string) (storage.OrchestrationSnapshot, bool, error) {
	var storedDigest, assignmentID string
	err := tx.QueryRowContext(ctx, `SELECT operation_digest, assignment_id FROM orchestration_operations WHERE operation_id = ?`, operationID).Scan(&storedDigest, &assignmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.OrchestrationSnapshot{}, false, nil
	}
	if err != nil {
		return storage.OrchestrationSnapshot{}, false, err
	}
	if storedDigest != digest {
		return storage.OrchestrationSnapshot{}, false, storage.ErrConflict
	}
	snapshot, err := orchestrationSnapshotTx(ctx, tx, assignmentID)
	return snapshot, true, err
}

func recordOrchestrationOperation(ctx context.Context, tx *sql.Tx, id, digest, assignmentID, attemptID string, state storage.AssignmentState, at time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO orchestration_operations(operation_id, operation_digest, assignment_id, attempt_id, resulting_state, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`, id, digest, assignmentID, attemptID, state, formatTime(at))
	return err
}

func recordOrchestrationAudit(ctx context.Context, tx *sql.Tx, event storage.OrchestrationAuditEvent) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO orchestration_audit_events(audit_id, assignment_id, attempt_id, action, from_state, to_state, reason_code, actor_id, lease_generation, occurred_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.AuditID, event.AssignmentID, event.AttemptID, event.Action, nullableString(string(event.FromState)), event.ToState, event.ReasonCode, event.ActorID, event.LeaseGeneration, formatTime(event.OccurredAt))
	return err
}

func orchestrationSnapshotDB(ctx context.Context, db *sql.DB, assignmentID string) (storage.OrchestrationSnapshot, error) {
	return orchestrationSnapshot(ctx, db, assignmentID)
}

func orchestrationSnapshotTx(ctx context.Context, tx *sql.Tx, assignmentID string) (storage.OrchestrationSnapshot, error) {
	return orchestrationSnapshot(ctx, tx, assignmentID)
}

type orchestrationQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func orchestrationSnapshot(ctx context.Context, query orchestrationQuerier, assignmentID string) (storage.OrchestrationSnapshot, error) {
	var snapshot storage.OrchestrationSnapshot
	var assignmentFailure sql.NullString
	var assignmentCreated, assignmentUpdated string
	err := query.QueryRowContext(ctx, `SELECT assignment_id, project_id, task_id, task_revision, graph_revision, work_package_id, execution_id, contract_id, contract_version, contract_digest, worker_id, node_id, state, current_attempt_id, current_attempt_number, idempotency_digest, recovery_disposition, failure_code, created_at, updated_at FROM orchestration_assignments WHERE assignment_id = ?`, assignmentID).Scan(
		&snapshot.Assignment.AssignmentID, &snapshot.Assignment.ProjectID, &snapshot.Assignment.TaskID, &snapshot.Assignment.TaskRevision,
		&snapshot.Assignment.GraphRevision, &snapshot.Assignment.WorkPackageID, &snapshot.Assignment.ExecutionID, &snapshot.Assignment.ContractID,
		&snapshot.Assignment.ContractVersion, &snapshot.Assignment.ContractDigest, &snapshot.Assignment.WorkerID, &snapshot.Assignment.NodeID,
		&snapshot.Assignment.State, &snapshot.Assignment.CurrentAttemptID, &snapshot.Assignment.CurrentAttempt, &snapshot.Assignment.IdempotencyDigest,
		&snapshot.Assignment.RecoveryDisposition, &assignmentFailure, &assignmentCreated, &assignmentUpdated)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, storage.ErrNotFound
	}
	if err != nil {
		return snapshot, err
	}
	snapshot.Assignment.FailureCode = assignmentFailure.String
	snapshot.Assignment.CreatedAt = parseStoredTime(assignmentCreated)
	snapshot.Assignment.UpdatedAt = parseStoredTime(assignmentUpdated)
	var supersedes, runtimeID, workspaceID, attemptFailure sql.NullString
	var attemptCreated, attemptUpdated string
	err = query.QueryRowContext(ctx, `SELECT attempt_id, assignment_id, project_id, work_package_id, execution_id, attempt_number, supersedes_attempt_id, state, lease_generation, runtime_session_id, workspace_id, idempotency_digest, recovery_disposition, failure_code, created_at, updated_at FROM orchestration_attempts WHERE attempt_id = ?`, snapshot.Assignment.CurrentAttemptID).Scan(
		&snapshot.Attempt.AttemptID, &snapshot.Attempt.AssignmentID, &snapshot.Attempt.ProjectID, &snapshot.Attempt.WorkPackageID,
		&snapshot.Attempt.ExecutionID, &snapshot.Attempt.AttemptNumber, &supersedes, &snapshot.Attempt.State, &snapshot.Attempt.LeaseGeneration,
		&runtimeID, &workspaceID, &snapshot.Attempt.IdempotencyDigest, &snapshot.Attempt.RecoveryDisposition, &attemptFailure, &attemptCreated, &attemptUpdated)
	if err != nil {
		return snapshot, err
	}
	snapshot.Attempt.SupersedesAttemptID = supersedes.String
	snapshot.Attempt.RuntimeSessionID = runtimeID.String
	snapshot.Attempt.WorkspaceID = workspaceID.String
	snapshot.Attempt.FailureCode = attemptFailure.String
	snapshot.Attempt.CreatedAt = parseStoredTime(attemptCreated)
	snapshot.Attempt.UpdatedAt = parseStoredTime(attemptUpdated)
	var lease storage.OrchestrationLease
	var acquired, heartbeat, expires string
	var released sql.NullString
	err = query.QueryRowContext(ctx, `SELECT lease_id, assignment_id, attempt_id, generation, owner_node_id, owner_runtime_id, fencing_digest, state, acquired_at, heartbeat_at, expires_at, released_at FROM orchestration_leases WHERE attempt_id = ? ORDER BY generation DESC LIMIT 1`, snapshot.Attempt.AttemptID).Scan(
		&lease.LeaseID, &lease.AssignmentID, &lease.AttemptID, &lease.Generation, &lease.OwnerNodeID, &lease.OwnerRuntime,
		&lease.FencingDigest, &lease.State, &acquired, &heartbeat, &expires, &released)
	if err == nil {
		lease.AcquiredAt, lease.HeartbeatAt, lease.ExpiresAt = parseStoredTime(acquired), parseStoredTime(heartbeat), parseStoredTime(expires)
		lease.ReleasedAt = parseStoredTime(released.String)
		snapshot.Lease = &lease
	} else if !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	var resources storage.OrchestrationResourceBinding
	var updated string
	err = query.QueryRowContext(ctx, `SELECT attempt_id, lease_generation, runtime_session_id, runtime_resume_key_digest, runtime_state, workspace_id, workspace_generation, workspace_state, updated_at FROM orchestration_resource_bindings WHERE attempt_id = ?`, snapshot.Attempt.AttemptID).Scan(
		&resources.AttemptID, &resources.LeaseGeneration, &resources.RuntimeSessionID, &resources.RuntimeResumeKey, &resources.RuntimeState,
		&resources.WorkspaceID, &resources.WorkspaceGeneration, &resources.WorkspaceState, &updated)
	if err == nil {
		resources.UpdatedAt = parseStoredTime(updated)
		snapshot.Resources = &resources
	} else if !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	gateRows, err := query.QueryContext(ctx, `SELECT assignment_id, attempt_id, gate_id, gate_version, gate_digest, status, evidence_id, reason_code, updated_at FROM orchestration_gate_status WHERE attempt_id = ? ORDER BY gate_id, gate_version`, snapshot.Attempt.AttemptID)
	if err != nil {
		return snapshot, err
	}
	for gateRows.Next() {
		var gate storage.OrchestrationGateStatus
		var evidenceID, reasonCode sql.NullString
		var gateUpdated string
		if err := gateRows.Scan(&gate.AssignmentID, &gate.AttemptID, &gate.GateID, &gate.GateVersion, &gate.GateDigest, &gate.Status, &evidenceID, &reasonCode, &gateUpdated); err != nil {
			_ = gateRows.Close()
			return snapshot, err
		}
		gate.EvidenceID, gate.ReasonCode, gate.UpdatedAt = evidenceID.String, reasonCode.String, parseStoredTime(gateUpdated)
		snapshot.Gates = append(snapshot.Gates, gate)
	}
	if err := gateRows.Err(); err != nil {
		_ = gateRows.Close()
		return snapshot, err
	}
	if err := gateRows.Close(); err != nil {
		return snapshot, err
	}
	decisionRows, err := query.QueryContext(ctx, `SELECT decision_id, assignment_id, attempt_id, decision, reason_code, actor_id, idempotency_digest, decided_at FROM orchestration_operator_decisions WHERE attempt_id = ? ORDER BY decided_at, decision_id`, snapshot.Attempt.AttemptID)
	if err != nil {
		return snapshot, err
	}
	for decisionRows.Next() {
		var decision storage.OrchestrationOperatorDecision
		var decidedAt string
		if err := decisionRows.Scan(&decision.DecisionID, &decision.AssignmentID, &decision.AttemptID, &decision.Decision, &decision.ReasonCode, &decision.ActorID, &decision.IdempotencyDigest, &decidedAt); err != nil {
			_ = decisionRows.Close()
			return snapshot, err
		}
		decision.DecidedAt = parseStoredTime(decidedAt)
		snapshot.Decisions = append(snapshot.Decisions, decision)
	}
	if err := decisionRows.Err(); err != nil {
		_ = decisionRows.Close()
		return snapshot, err
	}
	if err := decisionRows.Close(); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func terminalAssignmentState(state storage.AssignmentState) bool {
	return state == storage.AssignmentAccepted || state == storage.AssignmentFailed || state == storage.AssignmentCanceled || state == storage.AssignmentExpired
}

func retryableTerminalState(state storage.AssignmentState) bool {
	return state == storage.AssignmentFailed || state == storage.AssignmentCanceled || state == storage.AssignmentExpired
}

func validGateTransition(current, target storage.GateState) bool {
	return current == storage.GatePending && (target == storage.GateSatisfied || target == storage.GateFailed || target == storage.GateWaived)
}

func recoveryDisposition(state storage.AssignmentState, hasResources bool) storage.RecoveryDisposition {
	switch state {
	case storage.AssignmentPlanned, storage.AssignmentAwaitingGates:
		return storage.RecoveryResume
	case storage.AssignmentLeased:
		if hasResources {
			return storage.RecoveryNeedsOperator
		}
		return storage.RecoveryReconcile
	case storage.AssignmentPreparing, storage.AssignmentRunning, storage.AssignmentPaused, storage.AssignmentCollecting:
		return storage.RecoveryNeedsOperator
	default:
		return storage.RecoveryNone
	}
}

func recoveryAuditID(action, assignmentID, attemptID string, at time.Time) string {
	sum := sha256.Sum256([]byte(action + "\x00" + assignmentID + "\x00" + attemptID + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	return "audit:" + action + ":" + hex.EncodeToString(sum[:16])
}
