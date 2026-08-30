package sqlite

import (
	"bytes"
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
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
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

func (store *Store) FileIndex() storage.FileIndexStore {
	return fileIndexStore{db: store.db}
}

func (store *Store) Tombstones() storage.TombstoneStore {
	return tombstoneStore{db: store.db}
}

func (store *Store) Transfers() storage.TransferStore {
	return transferStore{db: store.db}
}

func (store *Store) OneWayJobs() storage.OneWayJobStore {
	return oneWayJobStore{db: store.db}
}

func (store *Store) OneWayWork() storage.OneWayWorkStore {
	return oneWayWorkStore{db: store.db}
}

func (store *Store) Pairings() storage.PairingStore {
	return pairingStore{db: store.db}
}

func (store *Store) NodeStatus() storage.NodeStatusStore {
	return nodeStatusStore{db: store.db}
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

func (store revisionStore) MaxSequence(ctx context.Context, originDeviceID core.DeviceID) (int64, error) {
	var sequence int64
	if err := store.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) FROM revisions WHERE origin_device_id = ?`,
		originDeviceID,
	).Scan(&sequence); err != nil {
		return 0, fmt.Errorf("load max revision sequence for %s: %w", originDeviceID, err)
	}
	return sequence, nil
}

type fileIndexStore struct {
	db *sql.DB
}

func (store fileIndexStore) CommitSnapshot(
	ctx context.Context,
	shareID core.ShareID,
	entries []core.FileIndexEntry,
	revisions []core.Revision,
	scannedAt time.Time,
) error {
	_, err := store.CommitSnapshotAndTombstones(ctx, shareID, entries, revisions, nil, scannedAt)
	return err
}

func (store fileIndexStore) CommitSnapshotAndTombstones(
	ctx context.Context,
	shareID core.ShareID,
	entries []core.FileIndexEntry,
	revisions []core.Revision,
	tombstoneRequests []storage.TombstoneRequest,
	scannedAt time.Time,
) ([]storage.Tombstone, error) {
	if shareID == "" {
		return nil, errors.New("share id is required")
	}
	if scannedAt.IsZero() {
		return nil, errors.New("scan time is required")
	}
	scannedAt = scannedAt.UTC()

	currentByPath, err := validateCommitEntries(shareID, entries)
	if err != nil {
		return nil, err
	}
	revisionsByPath, err := validateCommitRevisions(shareID, revisions)
	if err != nil {
		return nil, err
	}
	if err := validateTombstoneRequests(tombstoneRequests); err != nil {
		return nil, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin revision-aware file index snapshot: %w", err)
	}
	defer tx.Rollback()

	if err := requireShare(ctx, tx, shareID); err != nil {
		return nil, err
	}
	previousByPath, err := listFileIndexEntries(ctx, tx, shareID)
	if err != nil {
		return nil, err
	}

	acceptedByPath := make(map[string]core.Revision, len(revisionsByPath))
	for path, entry := range currentByPath {
		previous, existed := previousByPath[path]
		changed := !existed || !fileIndexEntriesEquivalent(previous, entry)
		revision, hasRevision := revisionsByPath[path]
		switch {
		case changed && !hasRevision:
			return nil, fmt.Errorf("file index entry %s changed without a revision", path)
		case !changed && hasRevision:
			return nil, fmt.Errorf("file index entry %s is unchanged but has revision %s", path, revision.ID)
		case hasRevision:
			if err := validateCurrentRevision(revision, entry, previous); err != nil {
				return nil, err
			}
			entry.CurrentRevisionID = revision.ID
			acceptedByPath[path] = revision
		case existed:
			if entry.CurrentRevisionID != "" && entry.CurrentRevisionID != previous.CurrentRevisionID {
				return nil, fmt.Errorf("file index entry %s changes current revision without a new revision", path)
			}
			entry.CurrentRevisionID = previous.CurrentRevisionID
		}
		currentByPath[path] = entry
	}

	for path, previous := range previousByPath {
		if _, present := currentByPath[path]; present || previous.IsDeleted {
			continue
		}
		revision, ok := revisionsByPath[path]
		if !ok {
			return nil, fmt.Errorf("deleted file index entry %s is missing a deletion revision", path)
		}
		if err := validateDeletionRevision(revision, previous); err != nil {
			return nil, err
		}
		acceptedByPath[path] = revision
	}
	if len(acceptedByPath) != len(revisionsByPath) {
		for path, revision := range revisionsByPath {
			if _, accepted := acceptedByPath[path]; !accepted {
				return nil, fmt.Errorf("revision %s does not describe a snapshot change for %s", revision.ID, path)
			}
		}
	}
	tombstones, err := buildAcceptedTombstones(shareID, acceptedByPath, tombstoneRequests)
	if err != nil {
		return nil, err
	}

	for _, revision := range revisions {
		if err := insertRevision(ctx, tx, revision); err != nil {
			return nil, err
		}
	}
	for _, entry := range entries {
		entry = currentByPath[entry.RelativePath]
		if err := upsertFileIndexEntry(ctx, tx, shareID, entry, scannedAt); err != nil {
			return nil, err
		}
	}
	for path, previous := range previousByPath {
		if _, present := currentByPath[path]; present || previous.IsDeleted {
			continue
		}
		revision := acceptedByPath[path]
		if _, err := tx.ExecContext(ctx, `
UPDATE file_index
SET entry_type = ?, current_revision_id = ?, is_deleted = 1, deleted_at = ?, last_scanned_at = ?
WHERE share_id = ? AND relative_path = ?`,
			core.EntryDeleted, revision.ID, formatTime(scannedAt), formatTime(scannedAt), shareID, path,
		); err != nil {
			return nil, fmt.Errorf("mark file index entry %s deleted: %w", path, err)
		}
	}
	for _, tombstone := range tombstones {
		if err := insertTombstone(ctx, tx, tombstone); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit revision-aware file index snapshot: %w", err)
	}
	return tombstones, nil
}

func (store fileIndexStore) CommitOneWayApply(ctx context.Context, commit storage.OneWayApplyCommit) (storage.OneWayApplyCommitResult, error) {
	if err := validateOneWayApplyCommit(commit); err != nil {
		return storage.OneWayApplyCommitResult{}, err
	}
	commit.AppliedAt = commit.AppliedAt.UTC()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("begin one-way apply commit: %w", err)
	}
	defer tx.Rollback()

	if err := requireShare(ctx, tx, commit.ShareID); err != nil {
		return storage.OneWayApplyCommitResult{}, err
	}

	existingRevision, err := scanRevision(tx.QueryRowContext(ctx, `
SELECT revision_id, share_id, relative_path, entry_type, size, content_hash, hash_algorithm, parent_revision_id, origin_device_id, sequence, is_deleted, created_at
FROM revisions WHERE revision_id = ?`, commit.Revision.ID), "revision", string(commit.Revision.ID))
	if err == nil {
		return idempotentOneWayApplyResult(ctx, tx, commit, existingRevision)
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return storage.OneWayApplyCommitResult{}, err
	}

	current, err := scanFileIndexEntry(tx.QueryRowContext(ctx, `
SELECT share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at
FROM file_index WHERE share_id = ? AND relative_path = ?`, commit.ShareID, commit.Revision.RelativePath),
		"file index entry", string(commit.ShareID)+"/"+commit.Revision.RelativePath)
	switch {
	case err == nil:
		if current.CurrentRevisionID == "" || current.CurrentRevisionID != commit.ExpectedCurrentRevisionID {
			return storage.OneWayApplyCommitResult{}, fmt.Errorf("one-way apply current revision is %q, want %q for %s", current.CurrentRevisionID, commit.ExpectedCurrentRevisionID, commit.Revision.RelativePath)
		}
	case errors.Is(err, storage.ErrNotFound):
		if commit.ExpectedCurrentRevisionID != "" {
			return storage.OneWayApplyCommitResult{}, fmt.Errorf("one-way apply expected current revision %s for missing path %s", commit.ExpectedCurrentRevisionID, commit.Revision.RelativePath)
		}
		if commit.Revision.IsDeleted {
			return storage.OneWayApplyCommitResult{}, fmt.Errorf("one-way apply cannot delete missing path %s", commit.Revision.RelativePath)
		}
	default:
		return storage.OneWayApplyCommitResult{}, err
	}

	if err := insertRevision(ctx, tx, commit.Revision); err != nil {
		return storage.OneWayApplyCommitResult{}, err
	}

	result := storage.OneWayApplyCommitResult{}
	if commit.Revision.IsDeleted {
		if _, err := tx.ExecContext(ctx, `
UPDATE file_index
SET entry_type = ?, current_revision_id = ?, is_deleted = 1, deleted_at = ?, last_scanned_at = ?
WHERE share_id = ? AND relative_path = ?`,
			core.EntryDeleted, commit.Revision.ID, formatTime(commit.AppliedAt), formatTime(commit.AppliedAt), commit.ShareID, commit.Revision.RelativePath,
		); err != nil {
			return storage.OneWayApplyCommitResult{}, fmt.Errorf("mark one-way path %s deleted: %w", commit.Revision.RelativePath, err)
		}
		tombstones, err := buildAcceptedTombstones(commit.ShareID, map[string]core.Revision{commit.Revision.RelativePath: commit.Revision}, []storage.TombstoneRequest{*commit.Tombstone})
		if err != nil {
			return storage.OneWayApplyCommitResult{}, err
		}
		if err := insertTombstone(ctx, tx, tombstones[0]); err != nil {
			return storage.OneWayApplyCommitResult{}, err
		}
		result.Tombstone = &tombstones[0]
	} else {
		entry := *commit.Entry
		entry.CurrentRevisionID = commit.Revision.ID
		if err := upsertFileIndexEntry(ctx, tx, commit.ShareID, entry, commit.AppliedAt); err != nil {
			return storage.OneWayApplyCommitResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("commit one-way apply: %w", err)
	}
	return result, nil
}

func validateOneWayApplyCommit(commit storage.OneWayApplyCommit) error {
	if commit.ShareID == "" {
		return errors.New("one-way apply share id is required")
	}
	if commit.AppliedAt.IsZero() {
		return errors.New("one-way apply time is required")
	}
	if _, err := validateCommitRevisions(commit.ShareID, []core.Revision{commit.Revision}); err != nil {
		return err
	}
	if commit.Revision.ParentRevisionID != commit.ExpectedCurrentRevisionID && !commit.AllowTargetDrift {
		return fmt.Errorf("one-way apply revision %s parent is %s, want current revision %s", commit.Revision.ID, commit.Revision.ParentRevisionID, commit.ExpectedCurrentRevisionID)
	}

	switch {
	case commit.Revision.IsDeleted:
		if commit.Revision.EntryType != core.EntryDeleted || commit.Entry != nil || commit.Tombstone == nil {
			return fmt.Errorf("deletion revision %s requires no active entry and exactly one tombstone", commit.Revision.ID)
		}
		if err := validateDeletionRevision(commit.Revision, core.FileIndexEntry{RelativePath: commit.Revision.RelativePath, CurrentRevisionID: commit.Revision.ParentRevisionID}); err != nil {
			return err
		}
		return validateTombstoneRequests([]storage.TombstoneRequest{*commit.Tombstone})
	case commit.Revision.EntryType != core.EntryFile && commit.Revision.EntryType != core.EntryDirectory:
		return fmt.Errorf("one-way apply revision %s has unsupported entry type %q", commit.Revision.ID, commit.Revision.EntryType)
	case commit.Entry == nil:
		return fmt.Errorf("active revision %s requires a file index entry", commit.Revision.ID)
	case commit.Tombstone != nil:
		return fmt.Errorf("active revision %s cannot create a tombstone", commit.Revision.ID)
	}

	entries, err := validateCommitEntries(commit.ShareID, []core.FileIndexEntry{*commit.Entry})
	if err != nil {
		return err
	}
	return validateCurrentRevision(commit.Revision, entries[commit.Revision.RelativePath], core.FileIndexEntry{CurrentRevisionID: commit.Revision.ParentRevisionID})
}

func idempotentOneWayApplyResult(ctx context.Context, tx *sql.Tx, commit storage.OneWayApplyCommit, existing core.Revision) (storage.OneWayApplyCommitResult, error) {
	if !revisionsEqual(existing, commit.Revision) {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("revision %s already exists with different metadata", commit.Revision.ID)
	}
	current, err := scanFileIndexEntry(tx.QueryRowContext(ctx, `
SELECT share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at
FROM file_index WHERE share_id = ? AND relative_path = ?`, commit.ShareID, commit.Revision.RelativePath),
		"file index entry", string(commit.ShareID)+"/"+commit.Revision.RelativePath)
	if err != nil {
		return storage.OneWayApplyCommitResult{}, err
	}
	if current.CurrentRevisionID != commit.Revision.ID || current.IsDeleted != commit.Revision.IsDeleted {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("revision %s exists but is not the accepted current state for %s", commit.Revision.ID, commit.Revision.RelativePath)
	}

	result := storage.OneWayApplyCommitResult{AlreadyApplied: true}
	if !commit.Revision.IsDeleted {
		return result, nil
	}
	var tombstone storage.Tombstone
	err = scanTombstone(tx.QueryRowContext(ctx, `
SELECT tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id,
       tombstone_revision_id, deleted_at, expires_at
FROM tombstones WHERE tombstone_revision_id = ?`, commit.Revision.ID), &tombstone)
	if err != nil {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("look up idempotent tombstone for revision %s: %w", commit.Revision.ID, err)
	}
	expected, err := buildAcceptedTombstones(commit.ShareID, map[string]core.Revision{commit.Revision.RelativePath: commit.Revision}, []storage.TombstoneRequest{*commit.Tombstone})
	if err != nil {
		return storage.OneWayApplyCommitResult{}, err
	}
	if !tombstonesEqual(tombstone, expected[0]) {
		return storage.OneWayApplyCommitResult{}, fmt.Errorf("revision %s already has different tombstone metadata", commit.Revision.ID)
	}
	result.Tombstone = &tombstone
	return result, nil
}

func revisionsEqual(left, right core.Revision) bool {
	return left.ID == right.ID && left.ShareID == right.ShareID && left.RelativePath == right.RelativePath &&
		left.EntryType == right.EntryType && left.Size == right.Size && left.ContentHash == right.ContentHash &&
		left.HashAlgorithm == right.HashAlgorithm && left.ParentRevisionID == right.ParentRevisionID &&
		left.OriginDeviceID == right.OriginDeviceID && left.Sequence == right.Sequence &&
		left.IsDeleted == right.IsDeleted && left.CreatedAt.Equal(right.CreatedAt)
}

func (store fileIndexStore) SaveSnapshot(ctx context.Context, shareID core.ShareID, entries []core.FileIndexEntry, scannedAt time.Time) error {
	if shareID == "" {
		return errors.New("share id is required")
	}
	if scannedAt.IsZero() {
		scannedAt = time.Now().UTC()
	}
	scannedAt = scannedAt.UTC()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin file index snapshot: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS scan_seen_paths(relative_path TEXT PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create scan seen table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_seen_paths`); err != nil {
		return fmt.Errorf("clear scan seen table: %w", err)
	}

	for _, entry := range entries {
		if entry.ShareID != "" && entry.ShareID != shareID {
			return fmt.Errorf("file index entry %s belongs to share %s, want %s", entry.RelativePath, entry.ShareID, shareID)
		}
		if strings.TrimSpace(entry.RelativePath) == "" {
			return errors.New("file index entry relative path is required")
		}
		if entry.EntryType == "" {
			return fmt.Errorf("file index entry %s entry type is required", entry.RelativePath)
		}
		if entry.LastScannedAt.IsZero() {
			entry.LastScannedAt = scannedAt
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO scan_seen_paths(relative_path) VALUES (?)`, entry.RelativePath); err != nil {
			return fmt.Errorf("record seen path %s: %w", entry.RelativePath, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO file_index(share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?)
ON CONFLICT(share_id, relative_path) DO UPDATE SET
    entry_type = excluded.entry_type,
    size = excluded.size,
    modified_time = excluded.modified_time,
    creation_time = excluded.creation_time,
    file_identity = excluded.file_identity,
    content_hash = excluded.content_hash,
    hash_algorithm = excluded.hash_algorithm,
    current_revision_id = excluded.current_revision_id,
    is_deleted = 0,
    deleted_at = NULL,
    last_scanned_at = excluded.last_scanned_at`,
			shareID, entry.RelativePath, entry.EntryType, entry.Size, nullableTime(entry.ModifiedTime),
			nullableTime(entry.CreationTime), nullableString(entry.FileIdentity), nullableString(entry.ContentHash),
			nullableString(entry.HashAlgorithm), nullableString(string(entry.CurrentRevisionID)), formatTime(entry.LastScannedAt),
		); err != nil {
			return fmt.Errorf("save file index entry %s: %w", entry.RelativePath, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE file_index
SET entry_type = ?, is_deleted = 1, deleted_at = ?, last_scanned_at = ?
WHERE share_id = ?
  AND is_deleted = 0
  AND NOT EXISTS (
      SELECT 1 FROM scan_seen_paths seen WHERE seen.relative_path = file_index.relative_path
  )`,
		core.EntryDeleted, formatTime(scannedAt), formatTime(scannedAt), shareID,
	); err != nil {
		return fmt.Errorf("mark missing file index entries deleted: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_seen_paths`); err != nil {
		return fmt.Errorf("clear scan seen table after snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit file index snapshot: %w", err)
	}
	return nil
}

type queryContext interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func requireShare(ctx context.Context, tx *sql.Tx, shareID core.ShareID) error {
	var found core.ShareID
	if err := tx.QueryRowContext(ctx, `SELECT share_id FROM shares WHERE share_id = ?`, shareID).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("share %s not found", shareID)
		}
		return fmt.Errorf("look up share %s: %w", shareID, err)
	}
	return nil
}

func validateCommitEntries(shareID core.ShareID, entries []core.FileIndexEntry) (map[string]core.FileIndexEntry, error) {
	byPath := make(map[string]core.FileIndexEntry, len(entries))
	for _, entry := range entries {
		if entry.ShareID == "" {
			return nil, fmt.Errorf("file index entry %s has empty share id", entry.RelativePath)
		}
		if entry.ShareID != shareID {
			return nil, fmt.Errorf("file index entry %s belongs to share %s, want %s", entry.RelativePath, entry.ShareID, shareID)
		}
		if strings.TrimSpace(entry.RelativePath) == "" {
			return nil, errors.New("file index entry relative path is required")
		}
		if entry.RelativePath != strings.TrimSpace(entry.RelativePath) {
			return nil, fmt.Errorf("file index entry path %q is not normalized", entry.RelativePath)
		}
		if !validActiveEntryType(entry.EntryType) {
			return nil, fmt.Errorf("file index entry %s has invalid entry type %q", entry.RelativePath, entry.EntryType)
		}
		if entry.IsDeleted || !entry.DeletedAt.IsZero() {
			return nil, fmt.Errorf("active snapshot entry %s cannot be marked deleted", entry.RelativePath)
		}
		if _, exists := byPath[entry.RelativePath]; exists {
			return nil, fmt.Errorf("file index snapshot contains duplicate path %s", entry.RelativePath)
		}
		byPath[entry.RelativePath] = entry
	}
	return byPath, nil
}

func validateCommitRevisions(shareID core.ShareID, revisions []core.Revision) (map[string]core.Revision, error) {
	byPath := make(map[string]core.Revision, len(revisions))
	for _, revision := range revisions {
		if revision.ID == "" {
			return nil, errors.New("revision id is required")
		}
		if revision.ShareID == "" {
			return nil, fmt.Errorf("revision %s has empty share id", revision.ID)
		}
		if revision.ShareID != shareID {
			return nil, fmt.Errorf("revision %s belongs to share %s, want %s", revision.ID, revision.ShareID, shareID)
		}
		if strings.TrimSpace(revision.RelativePath) == "" {
			return nil, fmt.Errorf("revision %s relative path is required", revision.ID)
		}
		if revision.RelativePath != strings.TrimSpace(revision.RelativePath) {
			return nil, fmt.Errorf("revision %s path %q is not normalized", revision.ID, revision.RelativePath)
		}
		if revision.OriginDeviceID == "" {
			return nil, fmt.Errorf("revision %s origin device id is required", revision.ID)
		}
		if revision.Sequence <= 0 {
			return nil, fmt.Errorf("revision %s sequence must be positive", revision.ID)
		}
		if revision.CreatedAt.IsZero() {
			return nil, fmt.Errorf("revision %s creation time is required", revision.ID)
		}
		if _, exists := byPath[revision.RelativePath]; exists {
			return nil, fmt.Errorf("multiple revisions supplied for path %s", revision.RelativePath)
		}
		byPath[revision.RelativePath] = revision
	}
	return byPath, nil
}

func validateCurrentRevision(revision core.Revision, entry, previous core.FileIndexEntry) error {
	if revision.IsDeleted || revision.EntryType == core.EntryDeleted {
		return fmt.Errorf("revision %s for active path %s is marked deleted", revision.ID, entry.RelativePath)
	}
	if revision.EntryType != entry.EntryType ||
		revision.Size != entry.Size ||
		revision.ContentHash != entry.ContentHash ||
		revision.HashAlgorithm != entry.HashAlgorithm {
		return fmt.Errorf("revision %s does not match file index entry %s", revision.ID, entry.RelativePath)
	}
	if revision.ParentRevisionID != previous.CurrentRevisionID {
		return fmt.Errorf("revision %s parent is %s, want %s for %s", revision.ID, revision.ParentRevisionID, previous.CurrentRevisionID, entry.RelativePath)
	}
	if entry.CurrentRevisionID != "" && entry.CurrentRevisionID != revision.ID {
		return fmt.Errorf("file index entry %s points to revision %s, want %s", entry.RelativePath, entry.CurrentRevisionID, revision.ID)
	}
	return nil
}

func validateDeletionRevision(revision core.Revision, previous core.FileIndexEntry) error {
	if !revision.IsDeleted || revision.EntryType != core.EntryDeleted {
		return fmt.Errorf("revision %s for deleted path %s is not a deletion revision", revision.ID, previous.RelativePath)
	}
	if revision.ParentRevisionID != previous.CurrentRevisionID {
		return fmt.Errorf("deletion revision %s parent is %s, want %s for %s", revision.ID, revision.ParentRevisionID, previous.CurrentRevisionID, previous.RelativePath)
	}
	if revision.Size != 0 || revision.ContentHash != "" || revision.HashAlgorithm != "" {
		return fmt.Errorf("deletion revision %s contains file content metadata", revision.ID)
	}
	return nil
}

func validActiveEntryType(entryType core.EntryType) bool {
	switch entryType {
	case core.EntryFile, core.EntryDirectory, core.EntrySymlinkUnsupported:
		return true
	default:
		return false
	}
}

func fileIndexEntriesEquivalent(previous, current core.FileIndexEntry) bool {
	if previous.IsDeleted || previous.EntryType != current.EntryType {
		return false
	}
	switch previous.EntryType {
	case core.EntryFile:
		return previous.Size == current.Size &&
			previous.ContentHash == current.ContentHash &&
			previous.HashAlgorithm == current.HashAlgorithm
	case core.EntrySymlinkUnsupported:
		return previous.FileIdentity == current.FileIdentity
	default:
		return true
	}
}

func insertRevision(ctx context.Context, tx *sql.Tx, revision core.Revision) error {
	_, err := tx.ExecContext(ctx, `
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

func validateTombstoneRequests(requests []storage.TombstoneRequest) error {
	seenIDs := make(map[core.TombstoneID]struct{}, len(requests))
	seenRevisions := make(map[core.RevisionID]struct{}, len(requests))
	for _, request := range requests {
		if request.ID == "" {
			return errors.New("tombstone request id is required")
		}
		if request.TombstoneRevisionID == "" {
			return errors.New("tombstone request revision id is required")
		}
		if _, exists := seenIDs[request.ID]; exists {
			return fmt.Errorf("duplicate tombstone request id %s", request.ID)
		}
		if _, exists := seenRevisions[request.TombstoneRevisionID]; exists {
			return fmt.Errorf("duplicate tombstone request revision %s", request.TombstoneRevisionID)
		}
		seenIDs[request.ID] = struct{}{}
		seenRevisions[request.TombstoneRevisionID] = struct{}{}
	}
	return nil
}

func buildAcceptedTombstones(
	shareID core.ShareID,
	acceptedByPath map[string]core.Revision,
	requests []storage.TombstoneRequest,
) ([]storage.Tombstone, error) {
	tombstones := make([]storage.Tombstone, 0, len(requests))
	for _, request := range requests {
		var revision core.Revision
		found := false
		for _, candidate := range acceptedByPath {
			if candidate.ID == request.TombstoneRevisionID {
				revision = candidate
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("tombstone request revision %s is not part of the accepted snapshot", request.TombstoneRevisionID)
		}
		if revision.ShareID != shareID {
			return nil, fmt.Errorf("tombstone request revision %s belongs to share %s, want %s", revision.ID, revision.ShareID, shareID)
		}
		if revision.EntryType != core.EntryDeleted || !revision.IsDeleted {
			return nil, fmt.Errorf("tombstone request revision %s is not a deletion revision", revision.ID)
		}
		if revision.CreatedAt.IsZero() {
			return nil, fmt.Errorf("tombstone request revision %s has no creation time", revision.ID)
		}
		expiresAt := request.ExpiresAt
		if !expiresAt.IsZero() {
			expiresAt = expiresAt.UTC()
			if expiresAt.Before(revision.CreatedAt) {
				return nil, fmt.Errorf("tombstone request %s expires before deletion", request.ID)
			}
		}
		tombstones = append(tombstones, storage.Tombstone{
			ID:                  request.ID,
			ShareID:             shareID,
			RelativePath:        revision.RelativePath,
			DeletedByDeviceID:   revision.OriginDeviceID,
			BaseRevisionID:      revision.ParentRevisionID,
			TombstoneRevisionID: revision.ID,
			DeletedAt:           revision.CreatedAt.UTC(),
			ExpiresAt:           expiresAt,
		})
	}
	return tombstones, nil
}

func insertTombstone(ctx context.Context, tx *sql.Tx, tombstone storage.Tombstone) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO tombstones(tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id, tombstone_revision_id, deleted_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tombstone.ID, tombstone.ShareID, tombstone.RelativePath, tombstone.DeletedByDeviceID,
		nullableString(string(tombstone.BaseRevisionID)), tombstone.TombstoneRevisionID,
		formatTime(tombstone.DeletedAt), nullableTime(tombstone.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("record tombstone %s: %w", tombstone.ID, err)
	}
	return nil
}

func upsertFileIndexEntry(ctx context.Context, tx *sql.Tx, shareID core.ShareID, entry core.FileIndexEntry, scannedAt time.Time) error {
	lastScannedAt := entry.LastScannedAt
	if lastScannedAt.IsZero() {
		lastScannedAt = scannedAt
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO file_index(share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, ?)
ON CONFLICT(share_id, relative_path) DO UPDATE SET
    entry_type = excluded.entry_type,
    size = excluded.size,
    modified_time = excluded.modified_time,
    creation_time = excluded.creation_time,
    file_identity = excluded.file_identity,
    content_hash = excluded.content_hash,
    hash_algorithm = excluded.hash_algorithm,
    current_revision_id = excluded.current_revision_id,
    is_deleted = 0,
    deleted_at = NULL,
    last_scanned_at = excluded.last_scanned_at`,
		shareID, entry.RelativePath, entry.EntryType, entry.Size, nullableTime(entry.ModifiedTime),
		nullableTime(entry.CreationTime), nullableString(entry.FileIdentity), nullableString(entry.ContentHash),
		nullableString(entry.HashAlgorithm), nullableString(string(entry.CurrentRevisionID)), formatTime(lastScannedAt),
	)
	if err != nil {
		return fmt.Errorf("save file index entry %s: %w", entry.RelativePath, err)
	}
	return nil
}

func listFileIndexEntries(ctx context.Context, query queryContext, shareID core.ShareID) (map[string]core.FileIndexEntry, error) {
	rows, err := query.QueryContext(ctx, `
SELECT share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at
FROM file_index
WHERE share_id = ?`, shareID)
	if err != nil {
		return nil, fmt.Errorf("list file index entries for share %s: %w", shareID, err)
	}
	defer rows.Close()

	byPath := make(map[string]core.FileIndexEntry)
	for rows.Next() {
		entry, err := scanFileIndexEntryRows(rows)
		if err != nil {
			return nil, err
		}
		byPath[entry.RelativePath] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file index entries: %w", err)
	}
	return byPath, nil
}

type tombstoneStore struct {
	db *sql.DB
}

func (store tombstoneStore) RecordDeletion(
	ctx context.Context,
	tombstoneID core.TombstoneID,
	tombstoneRevisionID core.RevisionID,
	expiresAt time.Time,
) (storage.Tombstone, error) {
	if tombstoneID == "" {
		return storage.Tombstone{}, errors.New("tombstone id is required")
	}
	if tombstoneRevisionID == "" {
		return storage.Tombstone{}, errors.New("tombstone revision id is required")
	}
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Tombstone{}, fmt.Errorf("begin tombstone record: %w", err)
	}
	defer tx.Rollback()

	var tombstone storage.Tombstone
	var parentRevision, currentRevision sql.NullString
	var deletedAt string
	var entryType string
	var revisionDeleted int
	var indexDeleted sql.NullInt64
	err = tx.QueryRowContext(ctx, `
SELECT r.share_id, r.relative_path, r.entry_type, r.parent_revision_id, r.origin_device_id,
       r.is_deleted, r.created_at, i.current_revision_id, i.is_deleted
FROM revisions r
LEFT JOIN file_index i
  ON i.share_id = r.share_id AND i.relative_path = r.relative_path
WHERE r.revision_id = ?`, tombstoneRevisionID).Scan(
		&tombstone.ShareID, &tombstone.RelativePath, &entryType, &parentRevision,
		&tombstone.DeletedByDeviceID, &revisionDeleted, &deletedAt, &currentRevision, &indexDeleted,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Tombstone{}, fmt.Errorf("deletion revision %s not found", tombstoneRevisionID)
		}
		return storage.Tombstone{}, fmt.Errorf("load deletion revision %s: %w", tombstoneRevisionID, err)
	}
	if entryType != string(core.EntryDeleted) || revisionDeleted != 1 {
		return storage.Tombstone{}, fmt.Errorf("revision %s is not a deletion revision", tombstoneRevisionID)
	}
	tombstone.ID = tombstoneID
	tombstone.TombstoneRevisionID = tombstoneRevisionID
	tombstone.DeletedAt = parseStoredTime(deletedAt)
	if tombstone.DeletedAt.IsZero() {
		return storage.Tombstone{}, fmt.Errorf("deletion revision %s has invalid creation time", tombstoneRevisionID)
	}
	if parentRevision.Valid {
		tombstone.BaseRevisionID = core.RevisionID(parentRevision.String)
	}
	if !expiresAt.IsZero() && expiresAt.Before(tombstone.DeletedAt) {
		return storage.Tombstone{}, fmt.Errorf("tombstone %s expires before deletion", tombstoneID)
	}
	tombstone.ExpiresAt = expiresAt

	var existing storage.Tombstone
	err = scanTombstone(tx.QueryRowContext(ctx, `
SELECT tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id,
       tombstone_revision_id, deleted_at, expires_at
FROM tombstones
WHERE tombstone_id = ?`, tombstoneID), &existing)
	if err == nil {
		if !tombstonesEqual(existing, tombstone) {
			return storage.Tombstone{}, fmt.Errorf("tombstone %s already exists with different metadata", tombstoneID)
		}
		if err := tx.Commit(); err != nil {
			return storage.Tombstone{}, fmt.Errorf("commit idempotent tombstone record %s: %w", tombstoneID, err)
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.Tombstone{}, fmt.Errorf("look up tombstone %s: %w", tombstoneID, err)
	}

	err = scanTombstone(tx.QueryRowContext(ctx, `
SELECT tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id,
       tombstone_revision_id, deleted_at, expires_at
FROM tombstones
WHERE share_id = ? AND relative_path = ? AND tombstone_revision_id = ?`,
		tombstone.ShareID, tombstone.RelativePath, tombstoneRevisionID), &existing)
	if err == nil {
		if !tombstonesEqualIgnoringID(existing, tombstone) {
			return storage.Tombstone{}, fmt.Errorf("deletion revision %s already has conflicting tombstone metadata", tombstoneRevisionID)
		}
		if err := tx.Commit(); err != nil {
			return storage.Tombstone{}, fmt.Errorf("commit duplicate tombstone record %s: %w", tombstoneID, err)
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.Tombstone{}, fmt.Errorf("look up tombstone for revision %s: %w", tombstoneRevisionID, err)
	}
	if !currentRevision.Valid || currentRevision.String != string(tombstoneRevisionID) || !indexDeleted.Valid || indexDeleted.Int64 != 1 {
		return storage.Tombstone{}, fmt.Errorf("deletion revision %s is not the accepted current revision", tombstoneRevisionID)
	}

	if err := insertTombstone(ctx, tx, tombstone); err != nil {
		return storage.Tombstone{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.Tombstone{}, fmt.Errorf("commit tombstone record %s: %w", tombstoneID, err)
	}
	return tombstone, nil
}

func (store tombstoneStore) Get(ctx context.Context, shareID core.ShareID, relativePath string) (storage.Tombstone, error) {
	if shareID == "" {
		return storage.Tombstone{}, errors.New("share id is required")
	}
	if strings.TrimSpace(relativePath) == "" {
		return storage.Tombstone{}, errors.New("relative path is required")
	}
	var tombstone storage.Tombstone
	err := scanTombstone(store.db.QueryRowContext(ctx, `
SELECT tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id,
       tombstone_revision_id, deleted_at, expires_at
FROM tombstones
WHERE share_id = ? AND relative_path = ?
ORDER BY deleted_at DESC, tombstone_id DESC
LIMIT 1`, shareID, relativePath), &tombstone)
	if err != nil {
		return storage.Tombstone{}, mapNotFound(err, "tombstone", string(shareID)+"/"+relativePath)
	}
	return tombstone, nil
}

func (store tombstoneStore) List(ctx context.Context, shareID core.ShareID) ([]storage.Tombstone, error) {
	return store.list(ctx, `
SELECT tombstone_id, share_id, relative_path, deleted_by_device_id, base_revision_id,
       tombstone_revision_id, deleted_at, expires_at
FROM tombstones
WHERE share_id = ?
ORDER BY deleted_at DESC, relative_path, tombstone_id`, shareID)
}

func (store tombstoneStore) ListActive(ctx context.Context, shareID core.ShareID) ([]storage.Tombstone, error) {
	return store.list(ctx, `
SELECT t.tombstone_id, t.share_id, t.relative_path, t.deleted_by_device_id, t.base_revision_id,
       t.tombstone_revision_id, t.deleted_at, t.expires_at
FROM tombstones t
JOIN file_index i
  ON i.share_id = t.share_id
 AND i.relative_path = t.relative_path
 AND i.current_revision_id = t.tombstone_revision_id
WHERE t.share_id = ? AND i.is_deleted = 1
ORDER BY t.deleted_at DESC, t.relative_path, t.tombstone_id`, shareID)
}

func (store tombstoneStore) list(ctx context.Context, query string, shareID core.ShareID) ([]storage.Tombstone, error) {
	if shareID == "" {
		return nil, errors.New("share id is required")
	}
	rows, err := store.db.QueryContext(ctx, query, shareID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones for share %s: %w", shareID, err)
	}
	defer rows.Close()

	var tombstones []storage.Tombstone
	for rows.Next() {
		var tombstone storage.Tombstone
		if err := scanTombstone(rows, &tombstone); err != nil {
			return nil, fmt.Errorf("scan tombstone: %w", err)
		}
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tombstones for share %s: %w", shareID, err)
	}
	return tombstones, nil
}

func (store fileIndexStore) Get(ctx context.Context, shareID core.ShareID, relativePath string) (core.FileIndexEntry, error) {
	row := store.db.QueryRowContext(ctx, `
SELECT share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at
FROM file_index
WHERE share_id = ? AND relative_path = ?`, shareID, relativePath)
	return scanFileIndexEntry(row, "file index entry", string(shareID)+"/"+relativePath)
}

func (store fileIndexStore) List(ctx context.Context, shareID core.ShareID) ([]core.FileIndexEntry, error) {
	rows, err := store.db.QueryContext(ctx, `
SELECT share_id, relative_path, entry_type, size, modified_time, creation_time, file_identity, content_hash, hash_algorithm, current_revision_id, is_deleted, deleted_at, last_scanned_at
FROM file_index
WHERE share_id = ?
ORDER BY relative_path`, shareID)
	if err != nil {
		return nil, fmt.Errorf("list file index entries for share %s: %w", shareID, err)
	}
	defer rows.Close()

	var entries []core.FileIndexEntry
	for rows.Next() {
		entry, err := scanFileIndexEntryRows(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate file index entries: %w", err)
	}
	return entries, nil
}

type transferStore struct {
	db *sql.DB
}

type oneWayJobStore struct {
	db *sql.DB
}

type oneWayWorkStore struct {
	db *sql.DB
}

func (store oneWayWorkStore) CreateAuthenticatedOneWayWork(ctx context.Context, work storage.AuthenticatedOneWayWork) (storage.AuthenticatedOneWayWorkResult, error) {
	if err := validateAuthenticatedOneWayWork(work); err != nil {
		return storage.AuthenticatedOneWayWorkResult{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.AuthenticatedOneWayWorkResult{}, fmt.Errorf("begin authenticated one-way work: %w", err)
	}
	defer tx.Rollback()

	for _, capability := range work.RequiredCapabilities {
		if err := authorizeShareTx(ctx, tx, work.Transfer.PeerDeviceID, work.Transfer.ShareID, capability, work.Remote); err != nil {
			return storage.AuthenticatedOneWayWorkResult{}, fmt.Errorf("authorize authenticated one-way work: %w", err)
		}
	}

	var storedTransferID core.TransferID
	var storedPeerID core.DeviceID
	var storedShareID core.ShareID
	var storedRevisionID core.RevisionID
	var storedRelativePath string
	var storedCapability core.Capability
	var storedRemote bool
	err = tx.QueryRowContext(ctx, `
SELECT transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote
FROM one_way_jobs WHERE job_id = ?`, work.Job.ID,
	).Scan(&storedTransferID, &storedPeerID, &storedShareID, &storedRevisionID, &storedRelativePath, &storedCapability, &storedRemote)
	switch {
	case err == nil:
		if storedTransferID != work.Transfer.ID || storedPeerID != work.Transfer.PeerDeviceID ||
			storedShareID != work.Job.ShareID || storedRevisionID != work.Job.RevisionID ||
			storedRelativePath != work.Job.RelativePath || storedCapability != work.Job.RequiredCapability ||
			storedRemote != work.Job.Remote {
			return storage.AuthenticatedOneWayWorkResult{}, errors.New("one-way job ID already belongs to different authenticated work")
		}
		return storage.AuthenticatedOneWayWorkResult{AlreadyCreated: true}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return storage.AuthenticatedOneWayWorkResult{}, fmt.Errorf("inspect authenticated one-way work: %w", err)
	}

	if err := insertTransferTx(ctx, tx, work.Transfer); err != nil {
		return storage.AuthenticatedOneWayWorkResult{}, err
	}
	if err := insertOneWayJobTx(ctx, tx, work.Job); err != nil {
		return storage.AuthenticatedOneWayWorkResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.AuthenticatedOneWayWorkResult{}, fmt.Errorf("commit authenticated one-way work: %w", err)
	}
	return storage.AuthenticatedOneWayWorkResult{}, nil
}

func validateAuthenticatedOneWayWork(work storage.AuthenticatedOneWayWork) error {
	transfer := work.Transfer
	job := work.Job
	if transfer.ID == "" || transfer.PeerDeviceID == "" || transfer.ShareID == "" || transfer.RelativePath == "" {
		return errors.New("authenticated transfer ID, peer, share, and path are required")
	}
	if transfer.Direction != core.TransferReceive {
		return errors.New("authenticated receiver work requires a receive transfer")
	}
	if err := validateAuthenticatedOneWayJob(job); err != nil {
		return err
	}
	if job.TransferID != transfer.ID || job.PeerDeviceID != transfer.PeerDeviceID ||
		job.ShareID != transfer.ShareID || job.RelativePath != transfer.RelativePath || job.Remote != work.Remote {
		return errors.New("authenticated transfer and job scope do not match")
	}
	if len(work.RequiredCapabilities) != 2 ||
		work.RequiredCapabilities[0] != core.CapabilitySync ||
		work.RequiredCapabilities[1] != job.RequiredCapability {
		return errors.New("authenticated work must require sync and its action capability")
	}
	return nil
}

func validateAuthenticatedOneWayJob(job core.OneWayJob) error {
	if job.ID == "" || job.TransferID == "" || job.PeerDeviceID == "" || job.ShareID == "" ||
		job.RevisionID == "" || job.RelativePath == "" || !core.IsShareCapability(job.RequiredCapability) ||
		job.RequiredCapability == core.CapabilitySync {
		return errors.New("one-way job ID, transfer, peer, share, revision, path, and action capability are required")
	}
	return nil
}

func authorizeShareTx(ctx context.Context, tx *sql.Tx, deviceID core.DeviceID, shareID core.ShareID, capability core.Capability, remote bool) error {
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
	var allowed, lanOnly int
	if err := tx.QueryRowContext(ctx, query, deviceID, shareID).Scan(&trustState, &allowed, &lanOnly); err != nil {
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

func insertTransferTx(ctx context.Context, tx *sql.Tx, transfer core.Transfer) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO transfers(transfer_id, direction, peer_device_id, share_id, relative_path, state, size, chunk_size, content_hash, hash_algorithm, bytes_verified, retry_count, created_at, updated_at, completed_at, last_error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		transfer.ID, transfer.Direction, transfer.PeerDeviceID, nullableString(string(transfer.ShareID)), transfer.RelativePath,
		transfer.State, transfer.Size, transfer.ChunkSize, transfer.ContentHash, transfer.HashAlgorithm,
		transfer.BytesVerified, transfer.RetryCount, formatTime(transfer.CreatedAt), formatTime(transfer.UpdatedAt),
		nullableTime(transfer.CompletedAt), nullableString(transfer.LastError),
	)
	if err != nil {
		return fmt.Errorf("create authenticated transfer %s: %w", transfer.ID, err)
	}
	return nil
}

func insertOneWayJobTx(ctx context.Context, tx *sql.Tx, job core.OneWayJob) error {
	if job.State == "" {
		job.State = core.OneWayJobQueued
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO one_way_jobs(job_id, transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote, state, retry_count, next_attempt_at, last_error, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.TransferID, job.PeerDeviceID, job.ShareID, nullableString(string(job.RevisionID)), job.RelativePath,
		job.RequiredCapability, job.Remote, job.State, job.RetryCount, nullableTime(job.NextAttemptAt), nullableString(job.LastError),
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("create authenticated one-way job %s: %w", job.ID, err)
	}
	return nil
}

func (store oneWayJobStore) SaveOneWayJob(ctx context.Context, job core.OneWayJob) error {
	if err := validateAuthenticatedOneWayJob(job); err != nil {
		return err
	}
	if job.State == "" {
		job.State = core.OneWayJobQueued
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO one_way_jobs(job_id, transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote, state, retry_count, next_attempt_at, last_error, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(job_id) DO UPDATE SET
 transfer_id=excluded.transfer_id, peer_device_id=excluded.peer_device_id, share_id=excluded.share_id, revision_id=excluded.revision_id,
 relative_path=excluded.relative_path, required_capability=excluded.required_capability, remote=excluded.remote, state=excluded.state, retry_count=excluded.retry_count,
 next_attempt_at=excluded.next_attempt_at, last_error=excluded.last_error, updated_at=excluded.updated_at`,
		job.ID, job.TransferID, job.PeerDeviceID, job.ShareID, nullableString(string(job.RevisionID)), job.RelativePath, job.RequiredCapability, job.Remote, job.State,
		job.RetryCount, nullableTime(job.NextAttemptAt), nullableString(job.LastError), formatTime(job.CreatedAt), formatTime(job.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save one-way job %s: %w", job.ID, err)
	}
	return nil
}

func (store oneWayJobStore) GetOneWayJob(ctx context.Context, id string) (core.OneWayJob, error) {
	return scanOneWayJob(store.db.QueryRowContext(ctx, `SELECT job_id, transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote, state, retry_count, next_attempt_at, last_error, created_at, updated_at FROM one_way_jobs WHERE job_id = ?`, id), "one-way job", id)
}

func (store oneWayJobStore) ListOneWayJobs(ctx context.Context) ([]core.OneWayJob, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT job_id, transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote, state, retry_count, next_attempt_at, last_error, created_at, updated_at FROM one_way_jobs ORDER BY created_at, job_id`)
	if err != nil {
		return nil, fmt.Errorf("list one-way jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]core.OneWayJob, 0)
	for rows.Next() {
		job, scanErr := scanOneWayJob(rows, "one-way job", "")
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate one-way jobs: %w", err)
	}
	return jobs, nil
}

func (store oneWayJobStore) ListRunnableOneWayJobs(ctx context.Context, now time.Time) ([]core.OneWayJob, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT job_id, transfer_id, peer_device_id, share_id, revision_id, relative_path, required_capability, remote, state, retry_count, next_attempt_at, last_error, created_at, updated_at FROM one_way_jobs WHERE state IN (?, ?) AND (next_attempt_at IS NULL OR next_attempt_at <= ?) ORDER BY COALESCE(next_attempt_at, created_at), created_at, job_id`, core.OneWayJobQueued, core.OneWayJobRetryWait, formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("list runnable one-way jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]core.OneWayJob, 0)
	for rows.Next() {
		job, scanErr := scanOneWayJob(rows, "one-way job", "")
		if scanErr != nil {
			return nil, scanErr
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runnable one-way jobs: %w", err)
	}
	return jobs, nil
}

func (store oneWayJobStore) ClaimOneWayJob(ctx context.Context, id string, now time.Time) (core.OneWayJob, error) {
	result, err := store.db.ExecContext(ctx, `UPDATE one_way_jobs SET state = ?, updated_at = ? WHERE job_id = ? AND state IN (?, ?) AND (next_attempt_at IS NULL OR next_attempt_at <= ?)`, core.OneWayJobRunning, formatTime(now), id, core.OneWayJobQueued, core.OneWayJobRetryWait, formatTime(now))
	if err != nil {
		return core.OneWayJob{}, fmt.Errorf("claim one-way job %s: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return core.OneWayJob{}, fmt.Errorf("check one-way job claim %s: %w", id, err)
	}
	if changed == 0 {
		return core.OneWayJob{}, fmt.Errorf("%w: one-way job %s", storage.ErrNotFound, id)
	}
	return store.GetOneWayJob(ctx, id)
}

func (store oneWayJobStore) UpdateOneWayJob(ctx context.Context, job core.OneWayJob) error {
	result, err := store.db.ExecContext(ctx, `UPDATE one_way_jobs SET state = ?, retry_count = ?, next_attempt_at = ?, last_error = ?, updated_at = ? WHERE job_id = ?`, job.State, job.RetryCount, nullableTime(job.NextAttemptAt), nullableString(job.LastError), formatTime(job.UpdatedAt), job.ID)
	if err != nil {
		return fmt.Errorf("update one-way job %s: %w", job.ID, err)
	}
	return requireChanged(result, "one-way job", job.ID)
}

func (store oneWayJobStore) RecoverRunningOneWayJobs(ctx context.Context, now time.Time) error {
	_, err := store.db.ExecContext(ctx, `UPDATE one_way_jobs SET state = ?, next_attempt_at = ?, updated_at = ? WHERE state = ?`, core.OneWayJobQueued, formatTime(now), formatTime(now), core.OneWayJobRunning)
	if err != nil {
		return fmt.Errorf("recover running one-way jobs: %w", err)
	}
	return nil
}

func scanOneWayJob(scanner interface{ Scan(...any) error }, entity, id string) (core.OneWayJob, error) {
	var job core.OneWayJob
	var peerDeviceID, revisionID, requiredCapability, nextAttempt, lastError sql.NullString
	var createdAt, updatedAt string
	err := scanner.Scan(&job.ID, &job.TransferID, &peerDeviceID, &job.ShareID, &revisionID, &job.RelativePath, &requiredCapability, &job.Remote, &job.State, &job.RetryCount, &nextAttempt, &lastError, &createdAt, &updatedAt)
	if err != nil {
		return core.OneWayJob{}, mapNotFound(err, entity, id)
	}
	if revisionID.Valid {
		job.RevisionID = core.RevisionID(revisionID.String)
	}
	if peerDeviceID.Valid {
		job.PeerDeviceID = core.DeviceID(peerDeviceID.String)
	}
	if requiredCapability.Valid {
		job.RequiredCapability = core.Capability(requiredCapability.String)
	}
	if nextAttempt.Valid {
		job.NextAttemptAt = parseStoredTime(nextAttempt.String)
	}
	if lastError.Valid {
		job.LastError = lastError.String
	}
	job.CreatedAt = parseStoredTime(createdAt)
	job.UpdatedAt = parseStoredTime(updatedAt)
	return job, nil
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

type pairingStore struct {
	db *sql.DB
}

func (store pairingStore) Accept(ctx context.Context, acceptance storage.PairingAcceptance) (storage.PairingAcceptanceResult, error) {
	if err := validatePairingAcceptance(acceptance); err != nil {
		return storage.PairingAcceptanceResult{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.PairingAcceptanceResult{}, fmt.Errorf("begin pairing acceptance: %w", err)
	}
	defer tx.Rollback()

	var acceptedDeviceID core.DeviceID
	var acceptedFingerprint string
	err = tx.QueryRowContext(ctx,
		`SELECT peer_device_id, fingerprint FROM pairing_acceptances WHERE invite_id = ?`,
		acceptance.InviteID,
	).Scan(&acceptedDeviceID, &acceptedFingerprint)
	switch {
	case err == nil:
		if acceptedDeviceID != acceptance.Device.ID || acceptedFingerprint != acceptance.Device.Fingerprint {
			return storage.PairingAcceptanceResult{}, errors.New("pairing invitation was already accepted for a different identity")
		}
		return storage.PairingAcceptanceResult{AlreadyAccepted: true}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return storage.PairingAcceptanceResult{}, fmt.Errorf("inspect pairing acceptance: %w", err)
	}

	var existingPublicKey []byte
	var existingFingerprint string
	err = tx.QueryRowContext(ctx,
		`SELECT public_key, fingerprint FROM devices WHERE device_id = ?`, acceptance.Device.ID,
	).Scan(&existingPublicKey, &existingFingerprint)
	if err == nil && (!bytes.Equal(existingPublicKey, acceptance.Device.PublicKey) || existingFingerprint != acceptance.Device.Fingerprint) {
		return storage.PairingAcceptanceResult{}, errors.New("refusing to replace existing device identity material")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storage.PairingAcceptanceResult{}, fmt.Errorf("inspect existing paired device: %w", err)
	}

	acceptedAt := acceptance.AcceptedAt.UTC()
	createdAt := acceptance.Device.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = acceptedAt
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO devices(device_id, display_name, public_key, fingerprint, trust_state, created_at, updated_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET
    display_name = excluded.display_name,
    public_key = excluded.public_key,
    fingerprint = excluded.fingerprint,
    trust_state = excluded.trust_state,
    updated_at = excluded.updated_at`,
		acceptance.Device.ID, acceptance.Device.DisplayName, acceptance.Device.PublicKey,
		acceptance.Device.Fingerprint, storage.TrustTrusted, formatTime(createdAt), formatTime(acceptedAt), nil,
	)
	if err != nil {
		return storage.PairingAcceptanceResult{}, fmt.Errorf("persist paired device %s: %w", acceptance.Device.ID, err)
	}

	seenShares := make(map[core.ShareID]bool, len(acceptance.Permissions))
	for _, permission := range acceptance.Permissions {
		if permission.DeviceID != acceptance.Device.ID {
			return storage.PairingAcceptanceResult{}, errors.New("pairing permission device does not match accepted peer")
		}
		if seenShares[permission.ShareID] {
			return storage.PairingAcceptanceResult{}, fmt.Errorf("duplicate pairing permission for share %s", permission.ShareID)
		}
		seenShares[permission.ShareID] = true
		if err := setPermissionTx(ctx, tx, permission); err != nil {
			return storage.PairingAcceptanceResult{}, err
		}
	}
	if acceptance.ControlPlaneGrant != nil {
		grant := *acceptance.ControlPlaneGrant
		if grant.DeviceID != acceptance.Device.ID || !grant.ReadStatus || grant.GrantedAt.IsZero() || !grant.ExpiresAt.After(grant.GrantedAt) || !grant.RevokedAt.IsZero() {
			return storage.PairingAcceptanceResult{}, errors.New("pairing control-plane grant is invalid")
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO control_plane_grants(device_id, can_read_status, granted_at, expires_at, revoked_at)
VALUES (?, 1, ?, ?, NULL)
ON CONFLICT(device_id) DO UPDATE SET
    can_read_status = 1,
    granted_at = excluded.granted_at,
    expires_at = excluded.expires_at,
    revoked_at = NULL`, grant.DeviceID, formatTime(grant.GrantedAt.UTC()), formatTime(grant.ExpiresAt.UTC())); err != nil {
			return storage.PairingAcceptanceResult{}, fmt.Errorf("persist control-plane grant: %w", err)
		}
	}
	if err := recordAuditTx(ctx, tx, acceptance.AuditEvent); err != nil {
		return storage.PairingAcceptanceResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pairing_acceptances(invite_id, peer_device_id, fingerprint, audit_id, accepted_at)
VALUES (?, ?, ?, ?, ?)`,
		acceptance.InviteID, acceptance.Device.ID, acceptance.Device.Fingerprint,
		acceptance.AuditEvent.ID, formatTime(acceptedAt),
	); err != nil {
		return storage.PairingAcceptanceResult{}, fmt.Errorf("record pairing acceptance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.PairingAcceptanceResult{}, fmt.Errorf("commit pairing acceptance: %w", err)
	}
	return storage.PairingAcceptanceResult{}, nil
}

func (store pairingStore) Revoke(ctx context.Context, revocation storage.PairingRevocation) (storage.PairingRevocationResult, error) {
	if revocation.DeviceID == "" {
		return storage.PairingRevocationResult{}, errors.New("revoked device ID is required")
	}
	if revocation.RevokedAt.IsZero() {
		return storage.PairingRevocationResult{}, errors.New("revocation time is required")
	}
	if revocation.AuditEvent.PeerDeviceID != revocation.DeviceID {
		return storage.PairingRevocationResult{}, errors.New("pairing revocation audit peer does not match revoked device")
	}
	if err := validateAuditEvent(revocation.AuditEvent); err != nil {
		return storage.PairingRevocationResult{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("begin pairing revocation: %w", err)
	}
	defer tx.Rollback()

	var trustState storage.TrustState
	if err := tx.QueryRowContext(ctx,
		`SELECT trust_state FROM devices WHERE device_id = ?`, revocation.DeviceID,
	).Scan(&trustState); err != nil {
		return storage.PairingRevocationResult{}, mapNotFound(err, "device", string(revocation.DeviceID))
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM share_permissions WHERE device_id = ?`, revocation.DeviceID); err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("remove revoked device permissions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE control_plane_grants SET can_read_status = 0, revoked_at = ? WHERE device_id = ?`, formatTime(revocation.RevokedAt.UTC()), revocation.DeviceID); err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("revoke control-plane grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM node_status_replicas WHERE device_id = ?`, revocation.DeviceID); err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("remove revoked node status: %w", err)
	}
	if trustState == storage.TrustRevoked {
		if err := tx.Commit(); err != nil {
			return storage.PairingRevocationResult{}, fmt.Errorf("commit repeated pairing revocation: %w", err)
		}
		return storage.PairingRevocationResult{AlreadyRevoked: true}, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE devices SET trust_state = ?, updated_at = ? WHERE device_id = ?`,
		storage.TrustRevoked, formatTime(revocation.RevokedAt.UTC()), revocation.DeviceID,
	); err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("revoke paired device %s: %w", revocation.DeviceID, err)
	}
	if err := recordAuditTx(ctx, tx, revocation.AuditEvent); err != nil {
		return storage.PairingRevocationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return storage.PairingRevocationResult{}, fmt.Errorf("commit pairing revocation: %w", err)
	}
	return storage.PairingRevocationResult{}, nil
}

func validatePairingAcceptance(acceptance storage.PairingAcceptance) error {
	if acceptance.InviteID == "" || acceptance.Device.ID == "" || acceptance.Device.DisplayName == "" ||
		len(acceptance.Device.PublicKey) == 0 || acceptance.Device.Fingerprint == "" {
		return errors.New("pairing acceptance identity fields are required")
	}
	if acceptance.AcceptedAt.IsZero() {
		return errors.New("pairing acceptance time is required")
	}
	if acceptance.AuditEvent.PeerDeviceID != acceptance.Device.ID {
		return errors.New("pairing acceptance audit peer does not match accepted device")
	}
	return validateAuditEvent(acceptance.AuditEvent)
}

func setPermissionTx(ctx context.Context, tx *sql.Tx, permission core.SharePermission) error {
	if permission.ShareID == "" || permission.DeviceID == "" {
		return errors.New("pairing permission share and device are required")
	}
	capability := func(cap core.Capability) int {
		if permission.Capabilities[cap] {
			return 1
		}
		return 0
	}
	for candidate := range permission.Capabilities {
		if !core.IsShareCapability(candidate) {
			return fmt.Errorf("unsupported pairing capability %q", candidate)
		}
	}
	lanOnly := 0
	if permission.LANOnly {
		lanOnly = 1
	}
	_, err := tx.ExecContext(ctx, `
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
		return fmt.Errorf("set pairing permission for share %s: %w", permission.ShareID, err)
	}
	return nil
}

func validateAuditEvent(event storage.AuditEvent) error {
	if event.ID == "" || event.EventName == "" || event.Severity == "" || event.OccurredAt.IsZero() {
		return errors.New("audit event ID, name, severity, and time are required")
	}
	return nil
}

func recordAuditTx(ctx context.Context, tx *sql.Tx, event storage.AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
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

type auditStore struct {
	db *sql.DB
}

func (store auditStore) Get(ctx context.Context, id string) (storage.AuditEvent, error) {
	if err := ctx.Err(); err != nil {
		return storage.AuditEvent{}, err
	}
	if strings.TrimSpace(id) == "" {
		return storage.AuditEvent{}, errors.New("audit event ID is required")
	}
	row := store.db.QueryRowContext(ctx, `
SELECT audit_id, event_name, device_id, peer_device_id, share_id, transfer_id, revision_id, transport_type, severity, metadata_json, occurred_at
FROM audit_events WHERE audit_id = ?`, id)
	var event storage.AuditEvent
	var deviceID, peerDeviceID, shareID, transferID, revisionID, transportType sql.NullString
	var metadataJSON, occurredAt string
	if err := row.Scan(&event.ID, &event.EventName, &deviceID, &peerDeviceID, &shareID, &transferID, &revisionID, &transportType, &event.Severity, &metadataJSON, &occurredAt); err != nil {
		return storage.AuditEvent{}, mapNotFound(err, "audit event", id)
	}
	event.DeviceID = core.DeviceID(deviceID.String)
	event.PeerDeviceID = core.DeviceID(peerDeviceID.String)
	event.ShareID = core.ShareID(shareID.String)
	event.TransferID = core.TransferID(transferID.String)
	event.RevisionID = core.RevisionID(revisionID.String)
	event.TransportType = transportType.String
	event.OccurredAt = parseStoredTime(occurredAt)
	if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
		return storage.AuditEvent{}, fmt.Errorf("parse audit event %s metadata: %w", event.ID, err)
	}
	return event, nil
}

func (store auditStore) ListRecent(ctx context.Context, limit int) ([]storage.AuditEvent, error) {
	if limit < 1 {
		return nil, errors.New("audit event limit must be positive")
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT audit_id, event_name, device_id, peer_device_id, share_id, transfer_id, revision_id, transport_type, severity, metadata_json, occurred_at
FROM audit_events ORDER BY occurred_at DESC, audit_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent audit events: %w", err)
	}
	defer rows.Close()

	events := make([]storage.AuditEvent, 0, limit)
	for rows.Next() {
		var event storage.AuditEvent
		var deviceID, peerDeviceID, shareID, transferID, revisionID, transportType sql.NullString
		var metadataJSON, occurredAt string
		if err := rows.Scan(
			&event.ID, &event.EventName, &deviceID, &peerDeviceID, &shareID, &transferID,
			&revisionID, &transportType, &event.Severity, &metadataJSON, &occurredAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		event.DeviceID = core.DeviceID(deviceID.String)
		event.PeerDeviceID = core.DeviceID(peerDeviceID.String)
		event.ShareID = core.ShareID(shareID.String)
		event.TransferID = core.TransferID(transferID.String)
		event.RevisionID = core.RevisionID(revisionID.String)
		event.TransportType = transportType.String
		event.OccurredAt = parseStoredTime(occurredAt)
		if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
			return nil, fmt.Errorf("parse audit event %s metadata: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return events, nil
}

func (store auditStore) Record(ctx context.Context, event storage.AuditEvent) error {
	if err := validateAuditEvent(event); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit event: %w", err)
	}
	defer tx.Rollback()
	if err := recordAuditTx(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit audit event: %w", err)
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

type tombstoneScanner interface {
	Scan(dest ...any) error
}

func scanTombstone(scanner tombstoneScanner, tombstone *storage.Tombstone) error {
	var id, shareID, relativePath, deletedByDeviceID string
	var baseRevisionID, tombstoneRevisionID sql.NullString
	var deletedAt string
	var expiresAt sql.NullString
	if err := scanner.Scan(
		&id, &shareID, &relativePath, &deletedByDeviceID, &baseRevisionID,
		&tombstoneRevisionID, &deletedAt, &expiresAt,
	); err != nil {
		return err
	}
	if !tombstoneRevisionID.Valid {
		return errors.New("tombstone revision id is null")
	}
	parsedDeletedAt := parseStoredTime(deletedAt)
	if parsedDeletedAt.IsZero() {
		return errors.New("tombstone deleted time is invalid")
	}
	value := storage.Tombstone{
		ID:                  core.TombstoneID(id),
		ShareID:             core.ShareID(shareID),
		RelativePath:        relativePath,
		DeletedByDeviceID:   core.DeviceID(deletedByDeviceID),
		TombstoneRevisionID: core.RevisionID(tombstoneRevisionID.String),
		DeletedAt:           parsedDeletedAt,
	}
	if baseRevisionID.Valid {
		value.BaseRevisionID = core.RevisionID(baseRevisionID.String)
	}
	if expiresAt.Valid {
		value.ExpiresAt = parseStoredTime(expiresAt.String)
		if value.ExpiresAt.IsZero() {
			return errors.New("tombstone expiry time is invalid")
		}
	}
	*tombstone = value
	return nil
}

func tombstonesEqual(left, right storage.Tombstone) bool {
	return left.ID == right.ID && tombstonesEqualIgnoringID(left, right)
}

func tombstonesEqualIgnoringID(left, right storage.Tombstone) bool {
	return left.ShareID == right.ShareID &&
		left.RelativePath == right.RelativePath &&
		left.DeletedByDeviceID == right.DeletedByDeviceID &&
		left.BaseRevisionID == right.BaseRevisionID &&
		left.TombstoneRevisionID == right.TombstoneRevisionID &&
		left.DeletedAt.Equal(right.DeletedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt)
}

type fileIndexScanner interface {
	Scan(dest ...any) error
}

func scanFileIndexEntry(row *sql.Row, entity, id string) (core.FileIndexEntry, error) {
	entry, err := scanFileIndex(row)
	if err != nil {
		return core.FileIndexEntry{}, mapNotFound(err, entity, id)
	}
	return entry, nil
}

func scanFileIndexEntryRows(rows *sql.Rows) (core.FileIndexEntry, error) {
	entry, err := scanFileIndex(rows)
	if err != nil {
		return core.FileIndexEntry{}, fmt.Errorf("scan file index entry: %w", err)
	}
	return entry, nil
}

func scanFileIndex(scanner fileIndexScanner) (core.FileIndexEntry, error) {
	var entry core.FileIndexEntry
	var modifiedAt, createdAt, fileIdentity, contentHash, hashAlgorithm, revisionID, deletedAt sql.NullString
	var lastScannedAt string
	var deleted int
	err := scanner.Scan(
		&entry.ShareID, &entry.RelativePath, &entry.EntryType, &entry.Size, &modifiedAt, &createdAt,
		&fileIdentity, &contentHash, &hashAlgorithm, &revisionID, &deleted, &deletedAt, &lastScannedAt,
	)
	if err != nil {
		return core.FileIndexEntry{}, err
	}
	if modifiedAt.Valid {
		entry.ModifiedTime = parseStoredTime(modifiedAt.String)
	}
	if createdAt.Valid {
		entry.CreationTime = parseStoredTime(createdAt.String)
	}
	if fileIdentity.Valid {
		entry.FileIdentity = fileIdentity.String
	}
	if contentHash.Valid {
		entry.ContentHash = contentHash.String
	}
	if hashAlgorithm.Valid {
		entry.HashAlgorithm = hashAlgorithm.String
	}
	if revisionID.Valid {
		entry.CurrentRevisionID = core.RevisionID(revisionID.String)
	}
	entry.IsDeleted = deleted == 1
	if deletedAt.Valid {
		entry.DeletedAt = parseStoredTime(deletedAt.String)
	}
	entry.LastScannedAt = parseStoredTime(lastScannedAt)
	return entry, nil
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
		return fmt.Errorf("%w: %s %s", storage.ErrNotFound, entity, id)
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
