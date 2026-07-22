package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)
	return &Store{db: db}, nil
}

func (store *Store) Migrate(ctx context.Context) error {
	if err := storage.ValidateMigrations(storage.Migrations); err != nil {
		return err
	}

	for _, migration := range storage.Migrations {
		applied, err := store.migrationApplied(ctx, migration.Version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", migration.Version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)`,
			migration.Version, migration.Name, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
	}
	return nil
}

func (store *Store) Devices() storage.DeviceStore {
	return deviceStore{db: store.db}
}

func (store *Store) Shares() storage.ShareStore {
	return shareStore{db: store.db}
}

func (store *Store) Revisions() storage.RevisionStore {
	return revisionStore{db: store.db}
}

func (store *Store) Transfers() storage.TransferStore {
	return transferStore{db: store.db}
}

func (store *Store) Audit() storage.AuditStore {
	return auditStore{db: store.db}
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) migrationApplied(ctx context.Context, version int) (bool, error) {
	var count int
	err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count)
	if err == nil {
		return count > 0, nil
	}
	if strings.Contains(err.Error(), "no such table") {
		return false, nil
	}
	return false, fmt.Errorf("check migration %d: %w", version, err)
}

type deviceStore struct {
	db *sql.DB
}

func (store deviceStore) TrustDevice(ctx context.Context, device storage.Device) error {
	now := time.Now().UTC()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	if device.UpdatedAt.IsZero() {
		device.UpdatedAt = now
	}
	if device.TrustState == "" {
		device.TrustState = storage.TrustTrusted
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO devices(device_id, display_name, public_key, fingerprint, trust_state, created_at, updated_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
    display_name = excluded.display_name,
    public_key = excluded.public_key,
    fingerprint = excluded.fingerprint,
    trust_state = excluded.trust_state,
    updated_at = excluded.updated_at,
    last_seen_at = excluded.last_seen_at`,
		device.ID, device.DisplayName, device.PublicKey, device.Fingerprint, device.TrustState,
		formatTime(device.CreatedAt), formatTime(device.UpdatedAt), nullableTime(device.LastSeenAt),
	)
	if err != nil {
		return fmt.Errorf("trust device %s: %w", device.ID, err)
	}
	return nil
}

func (store deviceStore) RevokeDevice(ctx context.Context, id core.DeviceID) error {
	result, err := store.db.ExecContext(ctx,
		`UPDATE devices SET trust_state = ?, updated_at = ? WHERE device_id = ?`,
		storage.TrustRevoked, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("revoke device %s: %w", id, err)
	}
	return requireChanged(result, "device", string(id))
}

func (store deviceStore) GetDevice(ctx context.Context, id core.DeviceID) (storage.Device, error) {
	var device storage.Device
	var createdAt, updatedAt string
	var lastSeen sql.NullString
	err := store.db.QueryRowContext(ctx, `
SELECT device_id, display_name, public_key, fingerprint, trust_state, created_at, updated_at, last_seen_at
FROM devices WHERE device_id = ?`, id).Scan(
		&device.ID, &device.DisplayName, &device.PublicKey, &device.Fingerprint,
		&device.TrustState, &createdAt, &updatedAt, &lastSeen,
	)
	if err != nil {
		return storage.Device{}, mapNotFound(err, "device", string(id))
	}
	device.CreatedAt = parseStoredTime(createdAt)
	device.UpdatedAt = parseStoredTime(updatedAt)
	if lastSeen.Valid {
		device.LastSeenAt = parseStoredTime(lastSeen.String)
	}
	return device, nil
}

type shareStore struct {
	db *sql.DB
}

func (store shareStore) SaveShare(ctx context.Context, share storage.Share) error {
	now := time.Now().UTC()
	if share.CreatedAt.IsZero() {
		share.CreatedAt = now
	}
	if share.UpdatedAt.IsZero() {
		share.UpdatedAt = now
	}
	if share.CasePolicy == "" {
		share.CasePolicy = "windows_case_insensitive"
	}
	if share.VersionPolicy == "" {
		share.VersionPolicy = "disabled"
	}
	if share.DeletionLimitCount == 0 {
		share.DeletionLimitCount = 100
	}
	if share.DeletionLimitPercent == 0 {
		share.DeletionLimitPercent = 10
	}

	_, err := store.db.ExecContext(ctx, `
INSERT INTO shares(share_id, name, root_path, mode, case_policy, version_policy, deletion_limit_count, deletion_limit_percent, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(share_id) DO UPDATE SET
    name = excluded.name,
    root_path = excluded.root_path,
    mode = excluded.mode,
    case_policy = excluded.case_policy,
    version_policy = excluded.version_policy,
    deletion_limit_count = excluded.deletion_limit_count,
    deletion_limit_percent = excluded.deletion_limit_percent,
    updated_at = excluded.updated_at`,
		share.ID, share.Name, share.RootPath, share.Mode, share.CasePolicy, share.VersionPolicy,
		share.DeletionLimitCount, share.DeletionLimitPercent, formatTime(share.CreatedAt), formatTime(share.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save share %s: %w", share.ID, err)
	}
	return nil
}

func (store shareStore) SetPermission(ctx context.Context, permission core.SharePermission) error {
	capability := func(cap core.Capability) int {
		if permission.Capabilities[cap] {
			return 1
		}
		return 0
	}
	lanOnly := 0
	if permission.LANOnly {
		lanOnly = 1
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO share_permissions(share_id, device_id, can_list, can_read, can_upload, can_modify, can_rename, can_delete, can_access_history, can_restore_history, can_sync, lan_only)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(share_id, device_id) DO UPDATE SET
    can_list = excluded.can_list,
    can_read = excluded.can_read,
    can_upload = excluded.can_upload,
    can_modify = excluded.can_modify,
    can_rename = excluded.can_rename,
    can_delete = excluded.can_delete,
    can_access_history = excluded.can_access_history,
    can_restore_history = excluded.can_restore_history,
    can_sync = excluded.can_sync,
    lan_only = excluded.lan_only`,
		permission.ShareID, permission.DeviceID,
		capability(core.CapabilityList), capability(core.CapabilityRead), capability(core.CapabilityUpload),
		capability(core.CapabilityModify), capability(core.CapabilityRename), capability(core.CapabilityDelete),
		capability(core.CapabilityAccessHistory), capability(core.CapabilityRestore), capability(core.CapabilitySync), lanOnly,
	)
	if err != nil {
		return fmt.Errorf("set share permission: %w", err)
	}
	return nil
}

func (store shareStore) Authorize(ctx context.Context, deviceID core.DeviceID, shareID core.ShareID, capability core.Capability, remote bool) error {
	column, err := capabilityColumn(capability)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`
SELECT d.trust_state, p.%s, p.lan_only
FROM share_permissions p
JOIN devices d ON d.device_id = p.device_id
WHERE p.device_id = ? AND p.share_id = ?`, column)

	var trustState string
	var allowed int
	var lanOnly int
	err = store.db.QueryRowContext(ctx, query, deviceID, shareID).Scan(&trustState, &allowed, &lanOnly)
	if err != nil {
		return mapNotFound(err, "share permission", string(shareID)+"/"+string(deviceID))
	}
	if trustState != string(storage.TrustTrusted) {
		return fmt.Errorf("device %s is not trusted", deviceID)
	}
	if allowed == 0 {
		return fmt.Errorf("device %s lacks %s permission on share %s", deviceID, capability, shareID)
	}
	if remote && lanOnly == 1 {
		return fmt.Errorf("share %s is LAN-only for device %s", shareID, deviceID)
	}
	return nil
}

type revisionStore struct {
	db *sql.DB
}

func (store revisionStore) RecordRevision(ctx context.Context, revision core.Revision) error {
	_, err := store.db.ExecContext(ctx, `
INSERT INTO revisions(revision_id, share_id, relative_path, entry_type, size, content_hash, hash_algorithm, parent_revision_id, origin_device_id, sequence, is_deleted, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revision.ID, revision.ShareID, revision.RelativePath, revision.EntryType, revision.Size, revision.ContentHash,
		revision.HashAlgorithm, nullableString(string(revision.ParentRevisionID)), revision.OriginDeviceID, revision.Sequence,
		boolInt(revision.IsDeleted), formatTime(revision.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("record revision %s: %w", revision.ID, err)
	}
	return nil
}

func (store revisionStore) GetRevision(ctx context.Context, id core.RevisionID) (core.Revision, error) {
	row := store.db.QueryRowContext(ctx, `
SELECT revision_id, share_id, relative_path, entry_type, size, content_hash, hash_algorithm, parent_revision_id, origin_device_id, sequence, is_deleted, created_at
FROM revisions WHERE revision_id = ?`, id)
	return scanRevision(row, "revision", string(id))
}

func (store revisionStore) GetCurrentRevision(ctx context.Context, shareID core.ShareID, relativePath string) (core.Revision, error) {
	row := store.db.QueryRowContext(ctx, `
SELECT r.revision_id, r.share_id, r.relative_path, r.entry_type, r.size, r.content_hash, r.hash_algorithm, r.parent_revision_id, r.origin_device_id, r.sequence, r.is_deleted, r.created_at
FROM file_index i
JOIN revisions r ON r.revision_id = i.current_revision_id
WHERE i.share_id = ? AND i.relative_path = ?`, shareID, relativePath)
	return scanRevision(row, "current revision", string(shareID)+"/"+relativePath)
}

type transferStore struct {
	db *sql.DB
}

func (store transferStore) SaveTransfer(ctx context.Context, transfer core.Transfer) error {
	_, err := store.db.ExecContext(ctx, `
INSERT INTO transfers(transfer_id, direction, peer_device_id, share_id, relative_path, state, size, chunk_size, content_hash, hash_algorithm, bytes_verified, retry_count, created_at, updated_at, completed_at, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(transfer_id) DO UPDATE SET
    state = excluded.state,
    bytes_verified = excluded.bytes_verified,
    retry_count = excluded.retry_count,
    updated_at = excluded.updated_at,
    completed_at = excluded.completed_at,
    last_error = excluded.last_error`,
		transfer.ID, transfer.Direction, transfer.PeerDeviceID, nullableString(string(transfer.ShareID)), transfer.RelativePath,
		transfer.State, transfer.Size, transfer.ChunkSize, transfer.ContentHash, transfer.HashAlgorithm,
		transfer.BytesVerified, transfer.RetryCount, formatTime(transfer.CreatedAt), formatTime(transfer.UpdatedAt),
		nullableTime(transfer.CompletedAt), nullableString(transfer.LastError),
	)
	if err != nil {
		return fmt.Errorf("save transfer %s: %w", transfer.ID, err)
	}
	return nil
}

func (store transferStore) GetTransfer(ctx context.Context, id core.TransferID) (core.Transfer, error) {
	var transfer core.Transfer
	var shareID sql.NullString
	var createdAt, updatedAt string
	var completedAt, lastError sql.NullString
	err := store.db.QueryRowContext(ctx, `
SELECT transfer_id, direction, peer_device_id, share_id, relative_path, state, size, chunk_size, content_hash, hash_algorithm, bytes_verified, retry_count, created_at, updated_at, completed_at, last_error
FROM transfers WHERE transfer_id = ?`, id).Scan(
		&transfer.ID, &transfer.Direction, &transfer.PeerDeviceID, &shareID, &transfer.RelativePath, &transfer.State,
		&transfer.Size, &transfer.ChunkSize, &transfer.ContentHash, &transfer.HashAlgorithm, &transfer.BytesVerified,
		&transfer.RetryCount, &createdAt, &updatedAt, &completedAt, &lastError,
	)
	if err != nil {
		return core.Transfer{}, mapNotFound(err, "transfer", string(id))
	}
	if shareID.Valid {
		transfer.ShareID = core.ShareID(shareID.String)
	}
	transfer.CreatedAt = parseStoredTime(createdAt)
	transfer.UpdatedAt = parseStoredTime(updatedAt)
	if completedAt.Valid {
		transfer.CompletedAt = parseStoredTime(completedAt.String)
	}
	if lastError.Valid {
		transfer.LastError = lastError.String
	}
	return transfer, nil
}

func (store transferStore) SaveChunk(ctx context.Context, chunk core.TransferChunk) error {
	_, err := store.db.ExecContext(ctx, `
INSERT INTO transfer_chunks(transfer_id, chunk_index, offset, size, chunk_hash, state, verified_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(transfer_id, chunk_index) DO UPDATE SET
    offset = excluded.offset,
    size = excluded.size,
    chunk_hash = excluded.chunk_hash,
    state = excluded.state,
    verified_at = excluded.verified_at`,
		chunk.TransferID, chunk.Index, chunk.Offset, chunk.Size, chunk.Hash, chunk.State, nullableTime(chunk.VerifiedAt),
	)
	if err != nil {
		return fmt.Errorf("save chunk %s/%d: %w", chunk.TransferID, chunk.Index, err)
	}
	return nil
}

func (store transferStore) VerifiedChunks(ctx context.Context, transferID core.TransferID) ([]core.TransferChunk, error) {
	rows, err := store.db.QueryContext(ctx, `
SELECT transfer_id, chunk_index, offset, size, chunk_hash, state, verified_at
FROM transfer_chunks
WHERE transfer_id = ? AND state = ?
ORDER BY chunk_index`, transferID, core.ChunkVerified)
	if err != nil {
		return nil, fmt.Errorf("list verified chunks %s: %w", transferID, err)
	}
	defer rows.Close()

	var chunks []core.TransferChunk
	for rows.Next() {
		var chunk core.TransferChunk
		var verifiedAt sql.NullString
		if err := rows.Scan(&chunk.TransferID, &chunk.Index, &chunk.Offset, &chunk.Size, &chunk.Hash, &chunk.State, &verifiedAt); err != nil {
			return nil, fmt.Errorf("scan verified chunk: %w", err)
		}
		if verifiedAt.Valid {
			chunk.VerifiedAt = parseStoredTime(verifiedAt.String)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verified chunks: %w", err)
	}
	return chunks, nil
}

type auditStore struct {
	db *sql.DB
}

func (store auditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `
INSERT INTO audit_events(audit_id, event_name, device_id, peer_device_id, share_id, transfer_id, revision_id, transport_type, severity, metadata_json, occurred_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.EventName, nullableString(string(event.DeviceID)), nullableString(string(event.PeerDeviceID)),
		nullableString(string(event.ShareID)), nullableString(string(event.TransferID)), nullableString(string(event.RevisionID)),
		nullableString(event.TransportType), event.Severity, string(metadata), formatTime(event.OccurredAt),
	)
	if err != nil {
		return fmt.Errorf("record audit event %s: %w", event.ID, err)
	}
	return nil
}

func scanRevision(row *sql.Row, entity, id string) (core.Revision, error) {
	var revision core.Revision
	var parent sql.NullString
	var deleted int
	var createdAt string
	err := row.Scan(
		&revision.ID, &revision.ShareID, &revision.RelativePath, &revision.EntryType, &revision.Size,
		&revision.ContentHash, &revision.HashAlgorithm, &parent, &revision.OriginDeviceID, &revision.Sequence,
		&deleted, &createdAt,
	)
	if err != nil {
		return core.Revision{}, mapNotFound(err, entity, id)
	}
	if parent.Valid {
		revision.ParentRevisionID = core.RevisionID(parent.String)
	}
	revision.IsDeleted = deleted == 1
	revision.CreatedAt = parseStoredTime(createdAt)
	return revision, nil
}

func capabilityColumn(capability core.Capability) (string, error) {
	switch capability {
	case core.CapabilityList:
		return "can_list", nil
	case core.CapabilityRead:
		return "can_read", nil
	case core.CapabilityUpload:
		return "can_upload", nil
	case core.CapabilityModify:
		return "can_modify", nil
	case core.CapabilityRename:
		return "can_rename", nil
	case core.CapabilityDelete:
		return "can_delete", nil
	case core.CapabilityAccessHistory:
		return "can_access_history", nil
	case core.CapabilityRestore:
		return "can_restore_history", nil
	case core.CapabilitySync:
		return "can_sync", nil
	default:
		return "", fmt.Errorf("unsupported capability %q", capability)
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func parseStoredTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mapNotFound(err error, entity, id string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s %s not found", entity, id)
	}
	return err
}

func requireChanged(result sql.Result, entity, id string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check %s %s update: %w", entity, id, err)
	}
	if changed == 0 {
		return fmt.Errorf("%s %s not found", entity, id)
	}
	return nil
}
