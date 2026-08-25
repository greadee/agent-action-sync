package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"syncgate/internal/storage"
)

func (store executionContractStore) SaveExecutionContract(ctx context.Context, record storage.ExecutionContractRecord) (storage.RegistryWriteResult, error) {
	if err := validateExecutionContractRecord(ctx, record); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	result, err := store.db.ExecContext(ctx, `
INSERT OR IGNORE INTO execution_contract_versions(
    contract_id, version, project_id, task_id, task_revision, graph_revision,
    work_package_id, execution_id, digest, predecessor_digest, contract_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ContractID, record.Version, record.ProjectID, record.TaskID, record.TaskRevision, record.GraphRevision,
		record.WorkPackageID, record.ExecutionID, record.Digest, nullableString(record.PredecessorDigest), record.ContractJSON, formatTime(record.CreatedAt),
	)
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save execution contract: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if changed == 1 {
		return storage.RegistryWriteResult{}, nil
	}
	var digest string
	err = store.db.QueryRowContext(ctx, `SELECT digest FROM execution_contract_versions WHERE contract_id = ? AND version = ?`, record.ContractID, record.Version).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: execution %s already has contract version %d", storage.ErrConflict, record.ExecutionID, record.Version)
	}
	if err != nil {
		return storage.RegistryWriteResult{}, err
	}
	if digest != record.Digest {
		return storage.RegistryWriteResult{}, fmt.Errorf("%w: execution contract %s version %d", storage.ErrConflict, record.ContractID, record.Version)
	}
	return storage.RegistryWriteResult{AlreadyPresent: true}, nil
}

func (store executionContractStore) GetExecutionContract(ctx context.Context, contractID string, version int64) (storage.ExecutionContractRecord, error) {
	if err := validateRegistryKey(ctx, contractID, version); err != nil {
		return storage.ExecutionContractRecord{}, err
	}
	record, err := scanExecutionContract(store.db.QueryRowContext(ctx, executionContractSelect+` WHERE contract_id = ? AND version = ?`, contractID, version))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ExecutionContractRecord{}, storage.ErrNotFound
	}
	return record, err
}

func (store executionContractStore) ListExecutionContracts(ctx context.Context, query storage.ExecutionContractQuery) (storage.Page[storage.ExecutionContractRecord], error) {
	page, err := storage.NormalizePageRequest(query.Page)
	if err != nil {
		return storage.Page[storage.ExecutionContractRecord]{}, err
	}
	if err := validateRegistryFilter(ctx, query.ProjectID, query.WorkPackageID, query.ExecutionID); err != nil {
		return storage.Page[storage.ExecutionContractRecord]{}, err
	}
	if query.ProjectID == "" {
		return storage.Page[storage.ExecutionContractRecord]{}, errors.New("project id is required")
	}
	statement := executionContractSelect + ` WHERE project_id = ?`
	arguments := []any{query.ProjectID}
	if query.WorkPackageID != "" {
		statement += ` AND work_package_id = ?`
		arguments = append(arguments, query.WorkPackageID)
	}
	if query.ExecutionID != "" {
		statement += ` AND execution_id = ?`
		arguments = append(arguments, query.ExecutionID)
	}
	if page.Cursor.ID != "" {
		statement += ` AND (contract_id > ? OR (contract_id = ? AND version > ?))`
		arguments = append(arguments, page.Cursor.ID, page.Cursor.ID, page.Cursor.Version)
	}
	statement += ` ORDER BY contract_id, version LIMIT ?`
	arguments = append(arguments, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return storage.Page[storage.ExecutionContractRecord]{}, err
	}
	defer rows.Close()
	items := make([]storage.ExecutionContractRecord, 0, page.Limit+1)
	for rows.Next() {
		item, scanErr := scanExecutionContract(rows)
		if scanErr != nil {
			return storage.Page[storage.ExecutionContractRecord]{}, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.ExecutionContractRecord]{}, err
	}
	return pageItems(items, page.Limit, func(item storage.ExecutionContractRecord) storage.PageCursor {
		return storage.PageCursor{ID: item.ContractID, Version: int(item.Version)}
	}), nil
}

const executionContractSelect = `SELECT contract_id, version, project_id, task_id, task_revision, graph_revision,
       work_package_id, execution_id, digest, predecessor_digest, contract_json, created_at
FROM execution_contract_versions`

func scanExecutionContract(row registryScanner) (storage.ExecutionContractRecord, error) {
	var record storage.ExecutionContractRecord
	var predecessor sql.NullString
	var createdAt string
	err := row.Scan(&record.ContractID, &record.Version, &record.ProjectID, &record.TaskID, &record.TaskRevision, &record.GraphRevision,
		&record.WorkPackageID, &record.ExecutionID, &record.Digest, &predecessor, &record.ContractJSON, &createdAt)
	if err != nil {
		return storage.ExecutionContractRecord{}, err
	}
	record.PredecessorDigest = predecessor.String
	record.ContractJSON = append([]byte(nil), record.ContractJSON...)
	record.CreatedAt = parseStoredTime(createdAt)
	return record, nil
}

func validateExecutionContractRecord(ctx context.Context, record storage.ExecutionContractRecord) error {
	for _, value := range []string{record.ContractID, record.ProjectID, record.TaskID, record.WorkPackageID, record.ExecutionID} {
		if err := validateRegistryKey(ctx, value, 1); err != nil {
			return err
		}
	}
	if record.Version < 1 || record.TaskRevision < 1 || record.GraphRevision < 1 || record.CreatedAt.IsZero() || !validProjectionHash(record.Digest) || len(record.ContractJSON) == 0 || len(record.ContractJSON) > projectPayloadLimit {
		return errors.New("execution contract record is incomplete")
	}
	if record.PredecessorDigest != "" && !validProjectionHash(record.PredecessorDigest) {
		return errors.New("execution contract predecessor digest is invalid")
	}
	return nil
}

const projectPayloadLimit = 1 << 20
