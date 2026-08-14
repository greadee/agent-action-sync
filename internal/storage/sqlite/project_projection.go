package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type projectSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type projectRegistrationStore struct{ sql projectSQL }
type projectEventStore struct{ sql projectSQL }
type projectArtifactStore struct{ sql projectSQL }
type projectCheckpointStore struct{ sql projectSQL }
type projectRejectionStore struct{ sql projectSQL }
type projectInsightStore struct{ sql projectSQL }

type projectProjectionStore struct{ db *sql.DB }
type projectInsightProjectionStore struct{ db *sql.DB }

func (store *Store) ProjectRegistrations() storage.ProjectRegistrationStore {
	return projectRegistrationStore{sql: store.db}
}

func (store *Store) ProjectEvents() storage.ProjectEventStore {
	return projectEventStore{sql: store.db}
}

func (store *Store) ProjectArtifacts() storage.ProjectArtifactStore {
	return projectArtifactStore{sql: store.db}
}

func (store *Store) ProjectCheckpoints() storage.ProjectCheckpointStore {
	return projectCheckpointStore{sql: store.db}
}

func (store *Store) ProjectRejections() storage.ProjectRejectionStore {
	return projectRejectionStore{sql: store.db}
}

func (store *Store) ProjectProjections() storage.ProjectProjectionStore {
	return projectProjectionStore{db: store.db}
}

func (store *Store) ProjectInsights() storage.ProjectInsightStore {
	return projectInsightStore{sql: store.db}
}

func (store *Store) ProjectInsightProjections() storage.ProjectInsightProjectionStore {
	return projectInsightProjectionStore{db: store.db}
}

func (store projectRegistrationStore) RegisterProject(ctx context.Context, registration storage.ProjectRegistration) (storage.ProjectRegistrationResult, error) {
	if err := validateProjectRegistration(ctx, registration); err != nil {
		return storage.ProjectRegistrationResult{}, err
	}
	result, err := store.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO agent_projects(
    project_id, share_id, root_path, name, authority_device_id,
    manifest_record_id, manifest_record_hash, manifest_path, registered_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		registration.ProjectID, registration.ShareID, registration.RootPath, registration.Name,
		registration.AuthorityDeviceID, registration.ManifestRecordID, registration.ManifestRecordHash,
		registration.ManifestPath, formatTime(registration.RegisteredAt),
	)
	if err != nil {
		return storage.ProjectRegistrationResult{}, fmt.Errorf("register project: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.ProjectRegistrationResult{}, fmt.Errorf("check project registration: %w", err)
	}
	if changed == 1 {
		return storage.ProjectRegistrationResult{}, nil
	}
	var existing storage.ProjectRegistration
	var existingShareID, existingAuthorityID, registeredAt string
	err = store.sql.QueryRowContext(ctx, `
SELECT project_id, share_id, root_path, name, authority_device_id,
       manifest_record_id, manifest_record_hash, manifest_path, registered_at
FROM agent_projects WHERE project_id = ?`, registration.ProjectID).Scan(
		&existing.ProjectID, &existingShareID, &existing.RootPath, &existing.Name, &existingAuthorityID,
		&existing.ManifestRecordID, &existing.ManifestRecordHash, &existing.ManifestPath, &registeredAt,
	)
	existing.ShareID = core.ShareID(existingShareID)
	existing.AuthorityDeviceID = core.DeviceID(existingAuthorityID)
	existing.RegisteredAt = parseStoredTime(registeredAt)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !sameProjectRegistration(existing, registration)) {
		return storage.ProjectRegistrationResult{}, fmt.Errorf("%w: project registration %s", storage.ErrConflict, registration.ProjectID)
	}
	if err != nil {
		return storage.ProjectRegistrationResult{}, fmt.Errorf("read existing project registration: %w", err)
	}
	return storage.ProjectRegistrationResult{AlreadyPresent: true}, nil
}

func sameProjectRegistration(left, right storage.ProjectRegistration) bool {
	return left.ProjectID == right.ProjectID && left.ShareID == right.ShareID && left.RootPath == right.RootPath &&
		left.Name == right.Name && left.AuthorityDeviceID == right.AuthorityDeviceID &&
		left.ManifestRecordID == right.ManifestRecordID && left.ManifestRecordHash == right.ManifestRecordHash &&
		left.ManifestPath == right.ManifestPath && left.RegisteredAt.Equal(right.RegisteredAt)
}

func (store projectRegistrationStore) GetProject(ctx context.Context, projectID string) (storage.ProjectRegistration, error) {
	if err := requireProjectID(ctx, projectID); err != nil {
		return storage.ProjectRegistration{}, err
	}
	var registration storage.ProjectRegistration
	var shareID string
	var authorityID string
	var registeredAt string
	err := store.sql.QueryRowContext(ctx, `
SELECT project_id, share_id, root_path, name, authority_device_id,
       manifest_record_id, manifest_record_hash, manifest_path, registered_at
FROM agent_projects WHERE project_id = ?`, projectID).Scan(
		&registration.ProjectID, &shareID, &registration.RootPath, &registration.Name, &authorityID,
		&registration.ManifestRecordID, &registration.ManifestRecordHash, &registration.ManifestPath, &registeredAt,
	)
	if err != nil {
		return storage.ProjectRegistration{}, mapNotFound(err, "project", projectID)
	}
	registration.ShareID = core.ShareID(shareID)
	registration.AuthorityDeviceID = core.DeviceID(authorityID)
	registration.RegisteredAt = parseStoredTime(registeredAt)
	return registration, nil
}

func (store projectEventStore) SaveProjectEvent(ctx context.Context, event storage.ProjectEventProjection) (storage.ProjectEventProjectionResult, error) {
	if err := validateProjectEvent(ctx, event); err != nil {
		return storage.ProjectEventProjectionResult{}, err
	}
	status := event.Status
	if status == "" {
		status = "accepted"
	}
	result, err := store.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO project_events(
    project_id, event_id, record_hash, event_type, occurred_at, work_package_id,
    execution_id, producer_worker_id, producer_device_id, producer_trade,
    producer_provider, producer_model, status, record_path, payload_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ProjectID, event.EventID, event.RecordHash, event.EventType, formatTime(event.OccurredAt),
		nullableString(event.WorkPackageID), nullableString(event.ExecutionID), nullableString(event.ProducerWorkerID),
		event.ProducerDeviceID, nullableString(event.ProducerTrade), nullableString(event.ProducerProvider), nullableString(event.ProducerModel), status,
		event.RecordPath, string(event.PayloadJSON),
	)
	if err != nil {
		return storage.ProjectEventProjectionResult{}, fmt.Errorf("save project event: %w", err)
	}
	return projectEventInsertResult(ctx, store.sql, result, event.ProjectID, event.EventID, event.RecordHash, status)
}

func projectEventInsertResult(ctx context.Context, executor projectSQL, result sql.Result, projectID, eventID, recordHash, status string) (storage.ProjectEventProjectionResult, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.ProjectEventProjectionResult{}, fmt.Errorf("check project event insert: %w", err)
	}
	if changed == 1 {
		return storage.ProjectEventProjectionResult{}, nil
	}
	var existingHash string
	err = executor.QueryRowContext(ctx, `SELECT record_hash FROM project_events WHERE project_id = ? AND event_id = ?`, projectID, eventID).Scan(&existingHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existingHash != recordHash) {
		return storage.ProjectEventProjectionResult{}, fmt.Errorf("%w: project event %s", storage.ErrConflict, eventID)
	}
	if err != nil {
		return storage.ProjectEventProjectionResult{}, fmt.Errorf("read existing project event: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `
UPDATE project_events SET status = ?
WHERE project_id = ? AND event_id = ? AND record_hash = ?`, status, projectID, eventID, recordHash); err != nil {
		return storage.ProjectEventProjectionResult{}, fmt.Errorf("reconcile existing project event: %w", err)
	}
	return storage.ProjectEventProjectionResult{AlreadyPresent: true}, nil
}

func (store projectEventStore) GetProjectEvent(ctx context.Context, projectID, eventID string) (storage.ProjectEventProjection, error) {
	if err := requireProjectID(ctx, projectID); err != nil {
		return storage.ProjectEventProjection{}, err
	}
	if err := storage.ValidateProjectProjectionID(eventID); err != nil {
		return storage.ProjectEventProjection{}, err
	}
	row := store.sql.QueryRowContext(ctx, projectEventSelect+` WHERE project_id = ? AND event_id = ?`, projectID, eventID)
	event, err := scanProjectEvent(row)
	if err != nil {
		return storage.ProjectEventProjection{}, mapNotFound(err, "project event", eventID)
	}
	return event, nil
}

func (store projectEventStore) ListProjectEvents(ctx context.Context, query storage.ProjectEventQuery) (storage.Page[storage.ProjectEventProjection], error) {
	page, err := validateProjectEventQuery(ctx, query)
	if err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, err
	}
	statement := projectEventSelect + ` WHERE project_id = ?`
	arguments := []any{query.ProjectID}
	if query.EventType != "" {
		statement += ` AND event_type = ?`
		arguments = append(arguments, query.EventType)
	}
	if query.WorkPackageID != "" {
		statement += ` AND work_package_id = ?`
		arguments = append(arguments, query.WorkPackageID)
	}
	if query.ExecutionID != "" {
		statement += ` AND execution_id = ?`
		arguments = append(arguments, query.ExecutionID)
	}
	if !page.Cursor.Timestamp.IsZero() {
		statement += ` AND (occurred_at > ? OR (occurred_at = ? AND event_id > ?))`
		cursorTime := formatTime(page.Cursor.Timestamp)
		arguments = append(arguments, cursorTime, cursorTime, page.Cursor.ID)
	}
	statement += ` ORDER BY occurred_at, event_id LIMIT ?`
	arguments = append(arguments, page.Limit+1)
	rows, err := store.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, fmt.Errorf("list project events: %w", err)
	}
	defer rows.Close()
	items := make([]storage.ProjectEventProjection, 0, page.Limit+1)
	for rows.Next() {
		event, scanErr := scanProjectEvent(rows)
		if scanErr != nil {
			return storage.Page[storage.ProjectEventProjection]{}, fmt.Errorf("scan project event: %w", scanErr)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.ProjectEventProjection]{}, fmt.Errorf("list project events: %w", err)
	}
	return pageItems(items, page.Limit, func(event storage.ProjectEventProjection) storage.PageCursor {
		return storage.PageCursor{Timestamp: event.OccurredAt, ID: event.EventID}
	}), nil
}

const projectEventSelect = `SELECT project_id, event_id, record_hash, event_type, occurred_at,
       work_package_id, execution_id, producer_worker_id, producer_device_id,
       producer_trade, producer_provider, producer_model, status, record_path, payload_json
FROM project_events`

func scanProjectEvent(row rowScanner) (storage.ProjectEventProjection, error) {
	var event storage.ProjectEventProjection
	var occurredAt string
	var workPackageID, executionID, workerID, trade, provider, model sql.NullString
	var producerDeviceID string
	var payload string
	err := row.Scan(
		&event.ProjectID, &event.EventID, &event.RecordHash, &event.EventType, &occurredAt,
		&workPackageID, &executionID, &workerID, &producerDeviceID, &trade, &provider, &model,
		&event.Status, &event.RecordPath, &payload,
	)
	if err != nil {
		return storage.ProjectEventProjection{}, err
	}
	event.OccurredAt = parseStoredTime(occurredAt)
	event.WorkPackageID = workPackageID.String
	event.ExecutionID = executionID.String
	event.ProducerWorkerID = workerID.String
	event.ProducerDeviceID = core.DeviceID(producerDeviceID)
	event.ProducerTrade = trade.String
	event.ProducerProvider = provider.String
	event.ProducerModel = model.String
	event.PayloadJSON = []byte(payload)
	return event, nil
}

func (store projectArtifactStore) SaveProjectArtifact(ctx context.Context, artifact storage.ProjectArtifactProjection) (storage.ProjectArtifactProjectionResult, error) {
	if err := validateProjectArtifact(ctx, artifact); err != nil {
		return storage.ProjectArtifactProjectionResult{}, err
	}
	result, err := store.sql.ExecContext(ctx, `
INSERT OR IGNORE INTO project_artifacts(
    project_id, artifact_id, record_id, record_hash, name, media_type, size,
    content_hash, blob_path, work_package_id, execution_id, producer_worker_id,
    producer_device_id, producer_model, created_at, record_path
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifact.ProjectID, artifact.ArtifactID, artifact.RecordID, artifact.RecordHash, artifact.Name,
		artifact.MediaType, artifact.Size, artifact.ContentHash, nullableString(artifact.BlobPath),
		nullableString(artifact.WorkPackageID), nullableString(artifact.ExecutionID), nullableString(artifact.ProducerWorkerID),
		artifact.ProducerDeviceID, nullableString(artifact.ProducerModel), formatTime(artifact.CreatedAt), artifact.RecordPath,
	)
	if err != nil {
		return storage.ProjectArtifactProjectionResult{}, fmt.Errorf("save project artifact: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return storage.ProjectArtifactProjectionResult{}, fmt.Errorf("check project artifact insert: %w", err)
	}
	if changed == 1 {
		return storage.ProjectArtifactProjectionResult{}, nil
	}
	var existingHash string
	err = store.sql.QueryRowContext(ctx, `SELECT record_hash FROM project_artifacts WHERE project_id = ? AND artifact_id = ?`, artifact.ProjectID, artifact.ArtifactID).Scan(&existingHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && existingHash != artifact.RecordHash) {
		return storage.ProjectArtifactProjectionResult{}, fmt.Errorf("%w: project artifact %s", storage.ErrConflict, artifact.ArtifactID)
	}
	if err != nil {
		return storage.ProjectArtifactProjectionResult{}, fmt.Errorf("read existing project artifact: %w", err)
	}
	return storage.ProjectArtifactProjectionResult{AlreadyPresent: true}, nil
}

func (store projectArtifactStore) GetProjectArtifact(ctx context.Context, projectID, artifactID string) (storage.ProjectArtifactProjection, error) {
	if err := requireProjectID(ctx, projectID); err != nil {
		return storage.ProjectArtifactProjection{}, err
	}
	if err := storage.ValidateProjectProjectionID(artifactID); err != nil {
		return storage.ProjectArtifactProjection{}, err
	}
	artifact, err := scanProjectArtifact(store.sql.QueryRowContext(ctx, projectArtifactSelect+` WHERE project_id = ? AND artifact_id = ?`, projectID, artifactID))
	if err != nil {
		return storage.ProjectArtifactProjection{}, mapNotFound(err, "project artifact", artifactID)
	}
	return artifact, nil
}

func (store projectArtifactStore) ListProjectArtifacts(ctx context.Context, query storage.ProjectArtifactQuery) (storage.Page[storage.ProjectArtifactProjection], error) {
	page, err := validateProjectArtifactQuery(ctx, query)
	if err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, err
	}
	statement := projectArtifactSelect + ` WHERE project_id = ?`
	arguments := []any{query.ProjectID}
	for _, filter := range []struct {
		value  string
		column string
	}{{query.WorkPackageID, "work_package_id"}, {query.ExecutionID, "execution_id"}, {query.MediaType, "media_type"}} {
		if filter.value != "" {
			statement += ` AND ` + filter.column + ` = ?`
			arguments = append(arguments, filter.value)
		}
	}
	if !page.Cursor.Timestamp.IsZero() {
		statement += ` AND (created_at > ? OR (created_at = ? AND artifact_id > ?))`
		cursorTime := formatTime(page.Cursor.Timestamp)
		arguments = append(arguments, cursorTime, cursorTime, page.Cursor.ID)
	}
	statement += ` ORDER BY created_at, artifact_id LIMIT ?`
	arguments = append(arguments, page.Limit+1)
	rows, err := store.sql.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, fmt.Errorf("list project artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]storage.ProjectArtifactProjection, 0, page.Limit+1)
	for rows.Next() {
		artifact, scanErr := scanProjectArtifact(rows)
		if scanErr != nil {
			return storage.Page[storage.ProjectArtifactProjection]{}, fmt.Errorf("scan project artifact: %w", scanErr)
		}
		items = append(items, artifact)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.ProjectArtifactProjection]{}, fmt.Errorf("list project artifacts: %w", err)
	}
	return pageItems(items, page.Limit, func(artifact storage.ProjectArtifactProjection) storage.PageCursor {
		return storage.PageCursor{Timestamp: artifact.CreatedAt, ID: artifact.ArtifactID}
	}), nil
}

const projectArtifactSelect = `SELECT project_id, artifact_id, record_id, record_hash, name,
       media_type, size, content_hash, blob_path, work_package_id, execution_id,
       producer_worker_id, producer_device_id, producer_model, created_at, record_path
FROM project_artifacts`

func scanProjectArtifact(row rowScanner) (storage.ProjectArtifactProjection, error) {
	var artifact storage.ProjectArtifactProjection
	var blobPath, workPackageID, executionID, workerID, model sql.NullString
	var producerDeviceID, createdAt string
	err := row.Scan(
		&artifact.ProjectID, &artifact.ArtifactID, &artifact.RecordID, &artifact.RecordHash, &artifact.Name,
		&artifact.MediaType, &artifact.Size, &artifact.ContentHash, &blobPath, &workPackageID,
		&executionID, &workerID, &producerDeviceID, &model, &createdAt, &artifact.RecordPath,
	)
	if err != nil {
		return storage.ProjectArtifactProjection{}, err
	}
	artifact.BlobPath = blobPath.String
	artifact.WorkPackageID = workPackageID.String
	artifact.ExecutionID = executionID.String
	artifact.ProducerWorkerID = workerID.String
	artifact.ProducerDeviceID = core.DeviceID(producerDeviceID)
	artifact.ProducerModel = model.String
	artifact.CreatedAt = parseStoredTime(createdAt)
	return artifact, nil
}

func (store projectCheckpointStore) SetProjectCheckpoint(ctx context.Context, checkpoint storage.ProjectProjectionCheckpoint) error {
	if err := validateProjectCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	_, err := store.sql.ExecContext(ctx, `
INSERT INTO project_projection_checkpoints(project_id, stream, last_record_path, last_record_hash, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(project_id, stream) DO UPDATE SET
    last_record_path = excluded.last_record_path,
    last_record_hash = excluded.last_record_hash,
    updated_at = excluded.updated_at`,
		checkpoint.ProjectID, checkpoint.Stream, checkpoint.LastRecordPath, checkpoint.LastRecordHash, formatTime(checkpoint.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("set project checkpoint: %w", err)
	}
	return nil
}

func (store projectCheckpointStore) GetProjectCheckpoint(ctx context.Context, projectID, stream string) (storage.ProjectProjectionCheckpoint, error) {
	if err := requireProjectID(ctx, projectID); err != nil {
		return storage.ProjectProjectionCheckpoint{}, err
	}
	if err := storage.ValidateProjectProjectionID(stream); err != nil {
		return storage.ProjectProjectionCheckpoint{}, err
	}
	var checkpoint storage.ProjectProjectionCheckpoint
	var updatedAt string
	err := store.sql.QueryRowContext(ctx, `
SELECT project_id, stream, last_record_path, last_record_hash, updated_at
FROM project_projection_checkpoints WHERE project_id = ? AND stream = ?`, projectID, stream).Scan(
		&checkpoint.ProjectID, &checkpoint.Stream, &checkpoint.LastRecordPath, &checkpoint.LastRecordHash, &updatedAt,
	)
	if err != nil {
		return storage.ProjectProjectionCheckpoint{}, mapNotFound(err, "project checkpoint", stream)
	}
	checkpoint.UpdatedAt = parseStoredTime(updatedAt)
	return checkpoint, nil
}

func (store projectRejectionStore) RecordProjectRejection(ctx context.Context, rejection storage.ProjectProjectionRejection) error {
	if err := validateProjectRejection(ctx, rejection); err != nil {
		return err
	}
	_, err := store.sql.ExecContext(ctx, `
INSERT INTO project_projection_rejections(project_id, record_path, observed_hash, reason_code, quarantine_path, rejected_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, record_path) DO UPDATE SET
    observed_hash = excluded.observed_hash,
    reason_code = excluded.reason_code,
    quarantine_path = excluded.quarantine_path,
    rejected_at = excluded.rejected_at`,
		rejection.ProjectID, rejection.RecordPath, nullableString(rejection.ObservedHash), rejection.ReasonCode,
		nullableString(rejection.QuarantinePath), formatTime(rejection.RejectedAt),
	)
	if err != nil {
		return fmt.Errorf("record project rejection: %w", err)
	}
	return nil
}

func (store projectProjectionStore) ClearProjectProjection(ctx context.Context, projectID string) error {
	return store.rebuild(ctx, projectID, nil)
}

func (store projectProjectionStore) ApplyProjectProjection(ctx context.Context, projectID string, apply func(storage.ProjectProjectionWriter) error) error {
	if apply == nil {
		return errors.New("project projection apply callback is required")
	}
	return store.transact(ctx, projectID, false, apply)
}

func (store projectProjectionStore) RebuildProjectProjection(ctx context.Context, projectID string, rebuild func(storage.ProjectProjectionWriter) error) error {
	if rebuild == nil {
		return errors.New("project projection rebuild callback is required")
	}
	return store.rebuild(ctx, projectID, rebuild)
}

func (store projectProjectionStore) rebuild(ctx context.Context, projectID string, rebuild func(storage.ProjectProjectionWriter) error) error {
	return store.transact(ctx, projectID, true, rebuild)
}

func (store projectProjectionStore) transact(ctx context.Context, projectID string, clear bool, apply func(storage.ProjectProjectionWriter) error) error {
	if err := requireProjectID(ctx, projectID); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin project projection rebuild: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_projects WHERE project_id = ?`, projectID).Scan(&exists); err != nil {
		return fmt.Errorf("check project projection registration: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: project %s", storage.ErrNotFound, projectID)
	}
	if clear {
		// Insights are derived from event rows, so every canonical-history rebuild
		// explicitly invalidates them instead of silently reinterpreting a stale
		// snapshot under a new event set or definition.
		for _, table := range []string{"project_insights", "project_projection_checkpoints", "project_projection_rejections", "project_artifacts", "project_events"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE project_id = ?`, projectID); err != nil {
				return fmt.Errorf("clear %s: %w", table, err)
			}
		}
	}
	if apply != nil {
		writer := projectProjectionWriter{sql: tx}
		if err := apply(writer); err != nil {
			return fmt.Errorf("apply project projection: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit project projection rebuild: %w", err)
	}
	return nil
}

type projectProjectionWriter struct{ sql projectSQL }

func (writer projectProjectionWriter) SaveProjectEvent(ctx context.Context, event storage.ProjectEventProjection) (storage.ProjectEventProjectionResult, error) {
	return (projectEventStore{sql: writer.sql}).SaveProjectEvent(ctx, event)
}
func (writer projectProjectionWriter) GetProjectEvent(ctx context.Context, projectID, eventID string) (storage.ProjectEventProjection, error) {
	return (projectEventStore{sql: writer.sql}).GetProjectEvent(ctx, projectID, eventID)
}
func (writer projectProjectionWriter) ListProjectEvents(ctx context.Context, query storage.ProjectEventQuery) (storage.Page[storage.ProjectEventProjection], error) {
	return (projectEventStore{sql: writer.sql}).ListProjectEvents(ctx, query)
}
func (writer projectProjectionWriter) SaveProjectArtifact(ctx context.Context, artifact storage.ProjectArtifactProjection) (storage.ProjectArtifactProjectionResult, error) {
	return (projectArtifactStore{sql: writer.sql}).SaveProjectArtifact(ctx, artifact)
}
func (writer projectProjectionWriter) GetProjectArtifact(ctx context.Context, projectID, artifactID string) (storage.ProjectArtifactProjection, error) {
	return (projectArtifactStore{sql: writer.sql}).GetProjectArtifact(ctx, projectID, artifactID)
}
func (writer projectProjectionWriter) ListProjectArtifacts(ctx context.Context, query storage.ProjectArtifactQuery) (storage.Page[storage.ProjectArtifactProjection], error) {
	return (projectArtifactStore{sql: writer.sql}).ListProjectArtifacts(ctx, query)
}
func (writer projectProjectionWriter) SetProjectCheckpoint(ctx context.Context, checkpoint storage.ProjectProjectionCheckpoint) error {
	return (projectCheckpointStore{sql: writer.sql}).SetProjectCheckpoint(ctx, checkpoint)
}
func (writer projectProjectionWriter) GetProjectCheckpoint(ctx context.Context, projectID, stream string) (storage.ProjectProjectionCheckpoint, error) {
	return (projectCheckpointStore{sql: writer.sql}).GetProjectCheckpoint(ctx, projectID, stream)
}
func (writer projectProjectionWriter) RecordProjectRejection(ctx context.Context, rejection storage.ProjectProjectionRejection) error {
	return (projectRejectionStore{sql: writer.sql}).RecordProjectRejection(ctx, rejection)
}

func validateProjectRegistration(ctx context.Context, registration storage.ProjectRegistration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, value := range []string{registration.ProjectID, string(registration.ShareID), string(registration.AuthorityDeviceID), registration.ManifestRecordID} {
		if err := storage.ValidateProjectProjectionID(value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(registration.RootPath) == "" || strings.TrimSpace(registration.Name) == "" || registration.RegisteredAt.IsZero() || !validProjectionHash(registration.ManifestRecordHash) {
		return errors.New("project registration is incomplete")
	}
	return storage.ValidateProjectProjectionPath(registration.ManifestPath, true)
}

func validateProjectEvent(ctx context.Context, event storage.ProjectEventProjection) error {
	if err := requireProjectID(ctx, event.ProjectID); err != nil {
		return err
	}
	for _, value := range []string{event.EventID, event.EventType, string(event.ProducerDeviceID)} {
		if err := storage.ValidateProjectProjectionID(value); err != nil {
			return err
		}
	}
	for _, value := range []string{event.WorkPackageID, event.ExecutionID, event.ProducerWorkerID, event.ProducerTrade, event.ProducerProvider, event.ProducerModel} {
		if err := storage.ValidateProjectQueryFilter(value); err != nil {
			return err
		}
	}
	if event.OccurredAt.IsZero() || !validProjectionHash(event.RecordHash) {
		return errors.New("project event timestamp and record hash are required")
	}
	if event.Status != "" && event.Status != "accepted" && event.Status != "pending" {
		return errors.New("project event status must be accepted or pending")
	}
	if err := storage.ValidateProjectProjectionPath(event.RecordPath, true); err != nil {
		return err
	}
	return storage.ValidateProjectProjectionPayload(event.PayloadJSON)
}

func validateProjectArtifact(ctx context.Context, artifact storage.ProjectArtifactProjection) error {
	if err := requireProjectID(ctx, artifact.ProjectID); err != nil {
		return err
	}
	for _, value := range []string{artifact.ArtifactID, artifact.RecordID, string(artifact.ProducerDeviceID)} {
		if err := storage.ValidateProjectProjectionID(value); err != nil {
			return err
		}
	}
	for _, value := range []string{artifact.Name, artifact.MediaType, artifact.WorkPackageID, artifact.ExecutionID, artifact.ProducerWorkerID, artifact.ProducerModel} {
		if err := storage.ValidateProjectQueryFilter(value); err != nil {
			return err
		}
	}
	if artifact.Name == "" || artifact.MediaType == "" || artifact.Size < 0 || artifact.CreatedAt.IsZero() || !validProjectionHash(artifact.RecordHash) || !validProjectionHash(artifact.ContentHash) {
		return errors.New("project artifact is incomplete")
	}
	if err := storage.ValidateProjectProjectionPath(artifact.RecordPath, true); err != nil {
		return err
	}
	return storage.ValidateProjectProjectionPath(artifact.BlobPath, false)
}

func validateProjectCheckpoint(ctx context.Context, checkpoint storage.ProjectProjectionCheckpoint) error {
	if err := requireProjectID(ctx, checkpoint.ProjectID); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionID(checkpoint.Stream); err != nil {
		return err
	}
	if checkpoint.UpdatedAt.IsZero() || !validProjectionHash(checkpoint.LastRecordHash) {
		return errors.New("project checkpoint is incomplete")
	}
	return storage.ValidateProjectProjectionPath(checkpoint.LastRecordPath, true)
}

func validateProjectRejection(ctx context.Context, rejection storage.ProjectProjectionRejection) error {
	if err := requireProjectID(ctx, rejection.ProjectID); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionID(rejection.ReasonCode); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionPath(rejection.RecordPath, true); err != nil {
		return err
	}
	if err := storage.ValidateProjectProjectionPath(rejection.QuarantinePath, false); err != nil {
		return err
	}
	if rejection.ObservedHash != "" && !validProjectionHash(rejection.ObservedHash) {
		return errors.New("project rejection observed hash is invalid")
	}
	if rejection.RejectedAt.IsZero() {
		return errors.New("project rejection timestamp is required")
	}
	return nil
}

func validateProjectEventQuery(ctx context.Context, query storage.ProjectEventQuery) (storage.PageRequest, error) {
	if err := requireProjectID(ctx, query.ProjectID); err != nil {
		return storage.PageRequest{}, err
	}
	for _, value := range []string{query.EventType, query.WorkPackageID, query.ExecutionID} {
		if err := storage.ValidateProjectQueryFilter(value); err != nil {
			return storage.PageRequest{}, err
		}
	}
	return validateProjectPage(query.Page)
}

func validateProjectArtifactQuery(ctx context.Context, query storage.ProjectArtifactQuery) (storage.PageRequest, error) {
	if err := requireProjectID(ctx, query.ProjectID); err != nil {
		return storage.PageRequest{}, err
	}
	for _, value := range []string{query.WorkPackageID, query.ExecutionID, query.MediaType} {
		if err := storage.ValidateProjectQueryFilter(value); err != nil {
			return storage.PageRequest{}, err
		}
	}
	return validateProjectPage(query.Page)
}

func validateProjectPage(page storage.PageRequest) (storage.PageRequest, error) {
	page, err := storage.NormalizePageRequest(page)
	if err != nil {
		return storage.PageRequest{}, err
	}
	if page.Cursor.Timestamp.IsZero() != (page.Cursor.ID == "") {
		return storage.PageRequest{}, errors.New("project page cursor requires timestamp and id")
	}
	return page, nil
}

func requireProjectID(ctx context.Context, projectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return storage.ValidateProjectProjectionID(projectID)
}

func validProjectionHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

var _ storage.ProjectProjectionWriter = projectProjectionWriter{}
var _ storage.ProjectRegistrationStore = projectRegistrationStore{}
var _ storage.ProjectEventStore = projectEventStore{}
var _ storage.ProjectArtifactStore = projectArtifactStore{}
var _ storage.ProjectCheckpointStore = projectCheckpointStore{}
var _ storage.ProjectRejectionStore = projectRejectionStore{}
var _ storage.Store = (*Store)(nil)
