package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"syncgate/internal/storage"
)

func (store resultIntakeStore) SaveResultIntake(ctx context.Context, record storage.ResultIntakeRecord) (storage.RegistryWriteResult, error) {
	if err := validateResultIntakeRecord(ctx, record); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	result, err := store.db.ExecContext(ctx, `
INSERT OR IGNORE INTO orchestration_result_intake(
    result_id, envelope_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version,
    contract_digest, assignment_id, assignment_digest, decision, reason_code,
    envelope_json, decided_at, decided_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ResultID, record.EnvelopeDigest, record.IdempotencyKeyDigest, record.ProjectID, record.ExecutionID, record.ContractID, record.ContractVersion,
		record.ContractDigest, record.AssignmentID, record.AssignmentDigest, record.Decision, record.ReasonCode,
		record.EnvelopeJSON, formatTime(record.DecidedAt), record.DecidedBy,
	)
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save result intake: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if changed == 1 {
		return storage.RegistryWriteResult{}, nil
	}
	var digest string
	if err := store.db.QueryRowContext(ctx, `SELECT envelope_digest FROM orchestration_result_intake WHERE result_id = ?`, record.ResultID).Scan(&digest); errors.Is(err, sql.ErrNoRows) {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: result idempotency key was reused", storage.ErrConflict)
	} else if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if digest != record.EnvelopeDigest {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: result %s", storage.ErrConflict, record.ResultID)
	}
	return storage.RegistryWriteResult{AlreadyPresent: true}, nil
}

func (store resultIntakeStore) GetResultIntake(ctx context.Context, resultID string) (storage.ResultIntakeRecord, error) {
	if err := validateRegistryKey(ctx, resultID, 1); err != nil {
		return storage.ResultIntakeRecord{}, err
	}
	var record storage.ResultIntakeRecord
	var decidedAt string
	err := store.db.QueryRowContext(ctx, `
SELECT result_id, envelope_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version,
       contract_digest, assignment_id, assignment_digest, decision, reason_code,
       envelope_json, decided_at, decided_by
FROM orchestration_result_intake WHERE result_id = ?`, resultID).Scan(
		&record.ResultID, &record.EnvelopeDigest, &record.IdempotencyKeyDigest, &record.ProjectID, &record.ExecutionID, &record.ContractID, &record.ContractVersion,
		&record.ContractDigest, &record.AssignmentID, &record.AssignmentDigest, &record.Decision, &record.ReasonCode,
		&record.EnvelopeJSON, &decidedAt, &record.DecidedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ResultIntakeRecord{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.ResultIntakeRecord{}, err
	}
	record.EnvelopeJSON = append([]byte(nil), record.EnvelopeJSON...)
	record.DecidedAt = parseStoredTime(decidedAt)
	return record, nil
}

func validateResultIntakeRecord(ctx context.Context, record storage.ResultIntakeRecord) error {
	for _, value := range []string{record.ResultID, record.ProjectID, record.ExecutionID, record.ContractID, record.AssignmentID, record.ReasonCode, record.DecidedBy} {
		if err := validateRegistryKey(ctx, value, 1); err != nil {
			return err
		}
	}
	if record.ContractVersion < 1 || !validProjectionHash(record.EnvelopeDigest) || !validProjectionHash(record.IdempotencyKeyDigest) || !validProjectionHash(record.ContractDigest) || !validProjectionHash(record.AssignmentDigest) ||
		(record.Decision != "accepted" && record.Decision != "rejected") || len(record.EnvelopeJSON) == 0 || len(record.EnvelopeJSON) > projectPayloadLimit || record.DecidedAt.IsZero() {
		return errors.New("result intake record is incomplete")
	}
	return nil
}
