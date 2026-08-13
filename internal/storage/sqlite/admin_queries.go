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

func (store *Store) ListAdminShares(ctx context.Context, requested storage.PageRequest) (storage.Page[storage.AdminShare], error) {
	page, err := storage.NormalizePageRequest(requested)
	if err != nil {
		return storage.Page[storage.AdminShare]{}, err
	}
	query := `SELECT share_id, name, root_path, mode, case_policy, version_policy, deletion_limit_count, deletion_limit_percent, created_at, updated_at FROM shares`
	args := make([]any, 0, 2)
	if page.Cursor.ID != "" {
		query += ` WHERE share_id > ?`
		args = append(args, page.Cursor.ID)
	}
	query += ` ORDER BY share_id LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.Page[storage.AdminShare]{}, fmt.Errorf("list administration shares: %w", err)
	}
	defer rows.Close()
	items := make([]storage.AdminShare, 0, page.Limit+1)
	for rows.Next() {
		var item storage.AdminShare
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.RootPath, &item.Mode, &item.CasePolicy, &item.VersionPolicy, &item.DeletionLimitCount, &item.DeletionLimitPercent, &createdAt, &updatedAt); err != nil {
			return storage.Page[storage.AdminShare]{}, fmt.Errorf("scan administration share: %w", err)
		}
		item.CreatedAt = parseStoredTime(createdAt)
		item.UpdatedAt = parseStoredTime(updatedAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.AdminShare]{}, fmt.Errorf("iterate administration shares: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.AdminShare) storage.PageCursor {
		return storage.PageCursor{ID: string(item.ID)}
	}), nil
}

func (store *Store) ListAdminDevices(ctx context.Context, requested storage.PageRequest) (storage.Page[storage.AdminDevice], error) {
	page, err := storage.NormalizePageRequest(requested)
	if err != nil {
		return storage.Page[storage.AdminDevice]{}, err
	}
	query := `SELECT device_id, display_name, public_key, fingerprint, trust_state, created_at, updated_at, last_seen_at FROM devices`
	args := make([]any, 0, 2)
	if page.Cursor.ID != "" {
		query += ` WHERE device_id > ?`
		args = append(args, page.Cursor.ID)
	}
	query += ` ORDER BY device_id LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.Page[storage.AdminDevice]{}, fmt.Errorf("list administration devices: %w", err)
	}
	defer rows.Close()
	items := make([]storage.AdminDevice, 0, page.Limit+1)
	for rows.Next() {
		item, err := scanAdminDevice(rows)
		if err != nil {
			return storage.Page[storage.AdminDevice]{}, fmt.Errorf("scan administration device: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.AdminDevice]{}, fmt.Errorf("iterate administration devices: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.AdminDevice) storage.PageCursor {
		return storage.PageCursor{ID: string(item.ID)}
	}), nil
}

func (store *Store) GetAdminDevice(ctx context.Context, id core.DeviceID) (storage.AdminDevice, error) {
	if err := storage.ValidateAdminFilter(string(id)); err != nil || id == "" {
		return storage.AdminDevice{}, errors.New("device id is required and must be bounded")
	}
	item, err := scanAdminDevice(store.db.QueryRowContext(ctx, `SELECT device_id, display_name, public_key, fingerprint, trust_state, created_at, updated_at, last_seen_at FROM devices WHERE device_id = ?`, id))
	if err != nil {
		return storage.AdminDevice{}, mapNotFound(err, "device", string(id))
	}
	return item, nil
}

func (store *Store) ListAdminPermissions(ctx context.Context, requested storage.PermissionQuery) (storage.Page[storage.AdminPermission], error) {
	page, err := storage.NormalizePageRequest(requested.Page)
	if err != nil {
		return storage.Page[storage.AdminPermission]{}, err
	}
	if err := validateAdminFilters(string(requested.ShareID), string(requested.DeviceID)); err != nil {
		return storage.Page[storage.AdminPermission]{}, err
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 8)
	if requested.ShareID != "" {
		conditions = append(conditions, "share_id = ?")
		args = append(args, requested.ShareID)
	}
	if requested.DeviceID != "" {
		conditions = append(conditions, "device_id = ?")
		args = append(args, requested.DeviceID)
	}
	if page.Cursor.ID != "" || page.Cursor.SecondaryID != "" {
		if page.Cursor.ID == "" || page.Cursor.SecondaryID == "" {
			return storage.Page[storage.AdminPermission]{}, errors.New("permission cursor requires share and device ids")
		}
		conditions = append(conditions, "(share_id > ? OR (share_id = ? AND device_id > ?))")
		args = append(args, page.Cursor.ID, page.Cursor.ID, page.Cursor.SecondaryID)
	}
	query := `SELECT share_id, device_id, can_list, can_read, can_upload, can_modify, can_rename, can_delete, can_access_history, can_restore_history, can_sync, lan_only FROM share_permissions`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY share_id, device_id LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.Page[storage.AdminPermission]{}, fmt.Errorf("list administration permissions: %w", err)
	}
	defer rows.Close()
	items := make([]storage.AdminPermission, 0, page.Limit+1)
	for rows.Next() {
		var item storage.AdminPermission
		var list, read, upload, modify, rename, deleteValue, history, restore, syncValue, lanOnly int
		if err := rows.Scan(&item.ShareID, &item.DeviceID, &list, &read, &upload, &modify, &rename, &deleteValue, &history, &restore, &syncValue, &lanOnly); err != nil {
			return storage.Page[storage.AdminPermission]{}, fmt.Errorf("scan administration permission: %w", err)
		}
		item.Capabilities = explicitCapabilities(list, read, upload, modify, rename, deleteValue, history, restore, syncValue)
		item.LANOnly = lanOnly != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.AdminPermission]{}, fmt.Errorf("iterate administration permissions: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.AdminPermission) storage.PageCursor {
		return storage.PageCursor{ID: string(item.ShareID), SecondaryID: string(item.DeviceID)}
	}), nil
}

func (store *Store) ListAdminJobs(ctx context.Context, requested storage.JobQuery) (storage.Page[storage.AdminJob], error) {
	page, err := storage.NormalizePageRequest(requested.Page)
	if err != nil {
		return storage.Page[storage.AdminJob]{}, err
	}
	if err := validateAdminFilters(string(requested.ShareID), string(requested.State)); err != nil {
		return storage.Page[storage.AdminJob]{}, err
	}
	if (page.Cursor.ID == "") != page.Cursor.Timestamp.IsZero() {
		return storage.Page[storage.AdminJob]{}, errors.New("job cursor requires timestamp and id")
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 8)
	if requested.ShareID != "" {
		conditions = append(conditions, "share_id = ?")
		args = append(args, requested.ShareID)
	}
	if requested.State != "" {
		conditions = append(conditions, "state = ?")
		args = append(args, requested.State)
	}
	if page.Cursor.ID != "" {
		conditions = append(conditions, "(created_at < ? OR (created_at = ? AND job_id < ?))")
		formatted := formatTime(page.Cursor.Timestamp)
		args = append(args, formatted, formatted, page.Cursor.ID)
	}
	query := `SELECT job_id, transfer_id, peer_device_id, share_id, state, retry_count, next_attempt_at, created_at, updated_at FROM one_way_jobs`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY created_at DESC, job_id DESC LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.Page[storage.AdminJob]{}, fmt.Errorf("list administration jobs: %w", err)
	}
	defer rows.Close()
	items := make([]storage.AdminJob, 0, page.Limit+1)
	for rows.Next() {
		item, err := scanAdminJob(rows)
		if err != nil {
			return storage.Page[storage.AdminJob]{}, fmt.Errorf("scan administration job: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.AdminJob]{}, fmt.Errorf("iterate administration jobs: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.AdminJob) storage.PageCursor {
		return storage.PageCursor{Timestamp: item.CreatedAt, ID: item.ID}
	}), nil
}

func (store *Store) GetAdminJob(ctx context.Context, id string) (storage.AdminJob, error) {
	if err := storage.ValidateAdminFilter(id); err != nil || id == "" {
		return storage.AdminJob{}, errors.New("job id is required and must be bounded")
	}
	item, err := scanAdminJob(store.db.QueryRowContext(ctx, `SELECT job_id, transfer_id, peer_device_id, share_id, state, retry_count, next_attempt_at, created_at, updated_at FROM one_way_jobs WHERE job_id = ?`, id))
	if err != nil {
		return storage.AdminJob{}, mapNotFound(err, "one-way job", id)
	}
	return item, nil
}

func (store *Store) ListAdminAuditEvents(ctx context.Context, requested storage.AuditEventQuery) (storage.Page[storage.AdminAuditEvent], error) {
	page, err := storage.NormalizePageRequest(requested.Page)
	if err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, err
	}
	if err := validateAdminFilters(requested.EventName, string(requested.DeviceID), string(requested.ShareID)); err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, err
	}
	if (page.Cursor.ID == "") != page.Cursor.Timestamp.IsZero() {
		return storage.Page[storage.AdminAuditEvent]{}, errors.New("audit cursor requires timestamp and id")
	}
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 10)
	if requested.EventName != "" {
		conditions = append(conditions, "event_name = ?")
		args = append(args, requested.EventName)
	}
	if requested.DeviceID != "" {
		conditions = append(conditions, "(device_id = ? OR peer_device_id = ?)")
		args = append(args, requested.DeviceID, requested.DeviceID)
	}
	if requested.ShareID != "" {
		conditions = append(conditions, "share_id = ?")
		args = append(args, requested.ShareID)
	}
	if page.Cursor.ID != "" {
		conditions = append(conditions, "(occurred_at < ? OR (occurred_at = ? AND audit_id < ?))")
		formatted := formatTime(page.Cursor.Timestamp)
		args = append(args, formatted, formatted, page.Cursor.ID)
	}
	query := `SELECT audit_id, event_name, device_id, peer_device_id, share_id, transfer_id, revision_id, transport_type, severity, occurred_at FROM audit_events`
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY occurred_at DESC, audit_id DESC LIMIT ?`
	args = append(args, page.Limit+1)
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, fmt.Errorf("list administration audit events: %w", err)
	}
	defer rows.Close()
	items := make([]storage.AdminAuditEvent, 0, page.Limit+1)
	for rows.Next() {
		item, err := scanAdminAuditEvent(rows)
		if err != nil {
			return storage.Page[storage.AdminAuditEvent]{}, fmt.Errorf("scan administration audit event: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return storage.Page[storage.AdminAuditEvent]{}, fmt.Errorf("iterate administration audit events: %w", err)
	}
	return pageItems(items, page.Limit, func(item storage.AdminAuditEvent) storage.PageCursor {
		return storage.PageCursor{Timestamp: item.OccurredAt, ID: item.ID}
	}), nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanAdminDevice(scanner rowScanner) (storage.AdminDevice, error) {
	var item storage.AdminDevice
	var createdAt, updatedAt string
	var lastSeen sql.NullString
	if err := scanner.Scan(&item.ID, &item.DisplayName, &item.PublicKey, &item.Fingerprint, &item.TrustState, &createdAt, &updatedAt, &lastSeen); err != nil {
		return storage.AdminDevice{}, err
	}
	item.PublicKey = append([]byte(nil), item.PublicKey...)
	item.CreatedAt = parseStoredTime(createdAt)
	item.UpdatedAt = parseStoredTime(updatedAt)
	if lastSeen.Valid {
		item.LastSeenAt = parseStoredTime(lastSeen.String)
	}
	return item, nil
}

func scanAdminJob(scanner rowScanner) (storage.AdminJob, error) {
	var item storage.AdminJob
	var peerDeviceID, nextAttempt sql.NullString
	var createdAt, updatedAt string
	if err := scanner.Scan(&item.ID, &item.TransferID, &peerDeviceID, &item.ShareID, &item.State, &item.RetryCount, &nextAttempt, &createdAt, &updatedAt); err != nil {
		return storage.AdminJob{}, err
	}
	item.PeerDeviceID = core.DeviceID(peerDeviceID.String)
	if nextAttempt.Valid {
		item.NextAttemptAt = parseStoredTime(nextAttempt.String)
	}
	item.CreatedAt = parseStoredTime(createdAt)
	item.UpdatedAt = parseStoredTime(updatedAt)
	return item, nil
}

func scanAdminAuditEvent(scanner rowScanner) (storage.AdminAuditEvent, error) {
	var item storage.AdminAuditEvent
	var deviceID, peerDeviceID, shareID, transferID, revisionID, transportType sql.NullString
	var occurredAt string
	if err := scanner.Scan(&item.ID, &item.EventName, &deviceID, &peerDeviceID, &shareID, &transferID, &revisionID, &transportType, &item.Severity, &occurredAt); err != nil {
		return storage.AdminAuditEvent{}, err
	}
	item.DeviceID = core.DeviceID(deviceID.String)
	item.PeerDeviceID = core.DeviceID(peerDeviceID.String)
	item.ShareID = core.ShareID(shareID.String)
	item.TransferID = core.TransferID(transferID.String)
	item.RevisionID = core.RevisionID(revisionID.String)
	item.TransportType = transportType.String
	item.OccurredAt = parseStoredTime(occurredAt)
	return item, nil
}

func explicitCapabilities(values ...int) map[core.Capability]bool {
	result := make(map[core.Capability]bool, len(core.ShareCapabilities()))
	for index, capability := range core.ShareCapabilities() {
		result[capability] = values[index] != 0
	}
	return result
}

func validateAdminFilters(values ...string) error {
	for _, value := range values {
		if err := storage.ValidateAdminFilter(value); err != nil {
			return err
		}
	}
	return nil
}

func pageItems[T any](items []T, limit int, cursor func(T) storage.PageCursor) storage.Page[T] {
	result := storage.Page[T]{Items: items}
	if len(result.Items) <= limit {
		return result
	}
	result.Items = result.Items[:limit]
	next := cursor(result.Items[len(result.Items)-1])
	result.NextCursor = &next
	return result
}

var _ storage.AdministrationQueryStore = (*Store)(nil)
