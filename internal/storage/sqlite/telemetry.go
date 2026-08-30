package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"syncgate/internal/storage"
)

func (store executionTelemetryStore) SaveExecutionTelemetry(ctx context.Context, record storage.ExecutionTelemetryRecord) (storage.RegistryWriteResult, error) {
	if err := validateTelemetryRecord(ctx, record); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	result, err := store.db.ExecContext(ctx, `
INSERT OR IGNORE INTO execution_telemetry(
    telemetry_id, telemetry_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version,
    contract_digest, final_outcome, summary_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.TelemetryID, record.TelemetryDigest, record.IdempotencyKeyDigest, record.ProjectID, record.ExecutionID,
		record.ContractID, record.ContractVersion, record.ContractDigest, record.FinalOutcome, record.SummaryJSON, formatTime(record.CreatedAt),
	)
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save execution telemetry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if changed == 1 {
		return storage.RegistryWriteResult{}, nil
	}
	var digest string
	if err := store.db.QueryRowContext(ctx, `SELECT telemetry_digest FROM execution_telemetry WHERE telemetry_id = ?`, record.TelemetryID).Scan(&digest); errors.Is(err, sql.ErrNoRows) {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: telemetry idempotency key was reused", storage.ErrConflict)
	} else if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if digest != record.TelemetryDigest {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: telemetry %s", storage.ErrConflict, record.TelemetryID)
	}
	return storage.RegistryWriteResult{AlreadyPresent: true}, nil
}

func (store executionTelemetryStore) GetExecutionTelemetry(ctx context.Context, telemetryID string) (storage.ExecutionTelemetryRecord, error) {
	if err := validateRegistryKey(ctx, telemetryID, 1); err != nil {
		return storage.ExecutionTelemetryRecord{}, err
	}
	var record storage.ExecutionTelemetryRecord
	var createdAt string
	err := store.db.QueryRowContext(ctx, `SELECT telemetry_id, telemetry_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version, contract_digest, final_outcome, summary_json, created_at FROM execution_telemetry WHERE telemetry_id = ?`, telemetryID).Scan(
		&record.TelemetryID, &record.TelemetryDigest, &record.IdempotencyKeyDigest, &record.ProjectID, &record.ExecutionID, &record.ContractID, &record.ContractVersion, &record.ContractDigest, &record.FinalOutcome, &record.SummaryJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ExecutionTelemetryRecord{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.ExecutionTelemetryRecord{}, err
	}
	record.SummaryJSON = append([]byte(nil), record.SummaryJSON...)
	record.CreatedAt = parseStoredTime(createdAt)
	return record, nil
}

func (store executionTelemetryStore) ListExecutionTelemetry(ctx context.Context, projectID, executionID string) ([]storage.ExecutionTelemetryRecord, error) {
	if err := validateRegistryKey(ctx, projectID, 1); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT telemetry_id, telemetry_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version, contract_digest, final_outcome, summary_json, created_at FROM execution_telemetry WHERE project_id = ? AND (? = '' OR execution_id = ?) ORDER BY created_at, telemetry_id`, projectID, executionID, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []storage.ExecutionTelemetryRecord
	for rows.Next() {
		var record storage.ExecutionTelemetryRecord
		var createdAt string
		if err := rows.Scan(&record.TelemetryID, &record.TelemetryDigest, &record.IdempotencyKeyDigest, &record.ProjectID, &record.ExecutionID, &record.ContractID, &record.ContractVersion, &record.ContractDigest, &record.FinalOutcome, &record.SummaryJSON, &createdAt); err != nil {
			return nil, err
		}
		record.SummaryJSON = append([]byte(nil), record.SummaryJSON...)
		record.CreatedAt = parseStoredTime(createdAt)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (store executionTelemetryStore) ListLatestExecutionTelemetry(ctx context.Context, projectID, executionID string, limit int) ([]storage.ExecutionTelemetryRecord, error) {
	if err := validateRegistryKey(ctx, projectID, 1); err != nil || validateRegistryKey(ctx, executionID, 1) != nil || limit < 1 || limit > storage.MaxAdminPageLimit {
		return nil, errors.New("latest execution telemetry query is invalid")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT telemetry_id, telemetry_digest, idempotency_key_digest, project_id, execution_id, contract_id, contract_version, contract_digest, final_outcome, summary_json, created_at FROM execution_telemetry WHERE project_id = ? AND execution_id = ? ORDER BY created_at DESC, telemetry_id DESC LIMIT ?`, projectID, executionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []storage.ExecutionTelemetryRecord
	for rows.Next() {
		var record storage.ExecutionTelemetryRecord
		var createdAt string
		if err := rows.Scan(&record.TelemetryID, &record.TelemetryDigest, &record.IdempotencyKeyDigest, &record.ProjectID, &record.ExecutionID, &record.ContractID, &record.ContractVersion, &record.ContractDigest, &record.FinalOutcome, &record.SummaryJSON, &createdAt); err != nil {
			return nil, err
		}
		record.SummaryJSON = append([]byte(nil), record.SummaryJSON...)
		record.CreatedAt = parseStoredTime(createdAt)
		records = append(records, record)
	}
	return records, rows.Err()
}

func validateTelemetryRecord(ctx context.Context, record storage.ExecutionTelemetryRecord) error {
	for _, value := range []string{record.TelemetryID, record.ProjectID, record.ExecutionID, record.ContractID} {
		if err := validateRegistryKey(ctx, value, 1); err != nil {
			return err
		}
	}
	if record.ContractVersion < 1 || !validProjectionHash(record.TelemetryDigest) || !validProjectionHash(record.IdempotencyKeyDigest) || !validProjectionHash(record.ContractDigest) || len(record.SummaryJSON) == 0 || len(record.SummaryJSON) > projectPayloadLimit || record.CreatedAt.IsZero() || (record.FinalOutcome != "succeeded" && record.FinalOutcome != "failed" && record.FinalOutcome != "canceled" && record.FinalOutcome != "partial") {
		return errors.New("execution telemetry record is incomplete")
	}
	return nil
}
