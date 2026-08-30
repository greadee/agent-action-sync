package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"syncgate/internal/core"
	"syncgate/internal/storage"
)

type nodeStatusStore struct{ db *sql.DB }

func (store nodeStatusStore) SaveControlPlaneGrant(ctx context.Context, grant storage.ControlPlaneGrant) error {
	if grant.DeviceID == "" || !grant.ReadStatus || grant.GrantedAt.IsZero() || !grant.ExpiresAt.After(grant.GrantedAt) || !grant.RevokedAt.IsZero() {
		return errors.New("valid read-only control-plane grant is required")
	}
	_, err := store.db.ExecContext(ctx, `
INSERT INTO control_plane_grants(device_id, can_read_status, granted_at, expires_at, revoked_at)
VALUES (?, 1, ?, ?, NULL)
ON CONFLICT(device_id) DO UPDATE SET can_read_status = 1, granted_at = excluded.granted_at, expires_at = excluded.expires_at, revoked_at = NULL`,
		grant.DeviceID, formatTime(grant.GrantedAt.UTC()), formatTime(grant.ExpiresAt.UTC()))
	if err != nil {
		return fmt.Errorf("save control-plane grant: %w", err)
	}
	return nil
}

func (store nodeStatusStore) GetControlPlaneGrant(ctx context.Context, deviceID core.DeviceID) (storage.ControlPlaneGrant, error) {
	if deviceID == "" || storage.ValidateAdminFilter(string(deviceID)) != nil {
		return storage.ControlPlaneGrant{}, errors.New("bounded device id is required")
	}
	var grant storage.ControlPlaneGrant
	var readStatus int
	var grantedAt, expiresAt string
	var revokedAt sql.NullString
	err := store.db.QueryRowContext(ctx, `SELECT device_id, can_read_status, granted_at, expires_at, revoked_at FROM control_plane_grants WHERE device_id = ?`, deviceID).
		Scan(&grant.DeviceID, &readStatus, &grantedAt, &expiresAt, &revokedAt)
	if err != nil {
		return storage.ControlPlaneGrant{}, mapNotFound(err, "control-plane grant", string(deviceID))
	}
	grant.ReadStatus = readStatus != 0
	grant.GrantedAt = parseStoredTime(grantedAt)
	grant.ExpiresAt = parseStoredTime(expiresAt)
	if revokedAt.Valid {
		grant.RevokedAt = parseStoredTime(revokedAt.String)
	}
	return grant, nil
}

func (store nodeStatusStore) SaveNodeStatusReplica(ctx context.Context, replica storage.NodeStatusReplica) (storage.RegistryWriteResult, error) {
	if err := validateNodeStatusReplica(replica); err != nil {
		return storage.RegistryWriteResult{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("begin node status replica: %w", err)
	}
	defer tx.Rollback()
	var revision int64
	var digest, watermark string
	err = tx.QueryRowContext(ctx, `SELECT revision, snapshot_digest, watermark FROM node_status_replicas WHERE device_id = ?`, replica.DeviceID).Scan(&revision, &digest, &watermark)
	if err == nil {
		if revision == replica.Revision && digest == replica.Digest {
			return storage.RegistryWriteResult{AlreadyPresent: true}, nil
		}
		if replica.Revision <= revision {
			return storage.RegistryWriteResult{}, fmt.Errorf("%w: stale node status revision", storage.ErrConflict)
		}
		if replica.Watermark == watermark {
			return storage.RegistryWriteResult{}, fmt.Errorf("%w: stale node status watermark", storage.ErrConflict)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return storage.RegistryWriteResult{}, fmt.Errorf("inspect node status replica: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO node_status_replicas(device_id, revision, watermark, protocol, snapshot_json, snapshot_digest, observed_at, expires_at, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(device_id) DO UPDATE SET revision = excluded.revision, watermark = excluded.watermark, protocol = excluded.protocol,
    snapshot_json = excluded.snapshot_json, snapshot_digest = excluded.snapshot_digest, observed_at = excluded.observed_at,
    expires_at = excluded.expires_at, received_at = excluded.received_at`,
		replica.DeviceID, replica.Revision, replica.Watermark, replica.Protocol, replica.SnapshotJSON, replica.Digest,
		formatTime(replica.ObservedAt.UTC()), formatTime(replica.ExpiresAt.UTC()), formatTime(replica.ReceivedAt.UTC()))
	if err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("save node status replica: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.RegistryWriteResult{}, fmt.Errorf("commit node status replica: %w", err)
	}
	return storage.RegistryWriteResult{}, nil
}

func (store nodeStatusStore) ListVisibleNodeStatusReplicas(ctx context.Context, now time.Time, limit int) ([]storage.NodeStatusReplica, error) {
	if now.IsZero() || limit < 1 || limit > storage.MaxAdminPageLimit {
		return nil, errors.New("current time and bounded limit are required")
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT r.device_id, r.revision, r.watermark, r.protocol, r.snapshot_json, r.snapshot_digest,
       r.observed_at, r.expires_at, r.received_at, g.expires_at
FROM node_status_replicas r
JOIN devices d ON d.device_id = r.device_id
JOIN control_plane_grants g ON g.device_id = r.device_id
WHERE d.trust_state = 'trusted' AND g.can_read_status = 1 AND g.revoked_at IS NULL AND g.expires_at > ?
ORDER BY r.device_id LIMIT ?`, formatTime(now.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("list visible node status replicas: %w", err)
	}
	defer rows.Close()
	items := make([]storage.NodeStatusReplica, 0, limit)
	for rows.Next() {
		var item storage.NodeStatusReplica
		var observedAt, expiresAt, receivedAt, grantExpiresAt string
		if err := rows.Scan(&item.DeviceID, &item.Revision, &item.Watermark, &item.Protocol, &item.SnapshotJSON, &item.Digest, &observedAt, &expiresAt, &receivedAt, &grantExpiresAt); err != nil {
			return nil, fmt.Errorf("scan node status replica: %w", err)
		}
		item.SnapshotJSON = append([]byte(nil), item.SnapshotJSON...)
		item.ObservedAt, item.ExpiresAt, item.ReceivedAt, item.GrantExpiresAt = parseStoredTime(observedAt), parseStoredTime(expiresAt), parseStoredTime(receivedAt), parseStoredTime(grantExpiresAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func validateNodeStatusReplica(replica storage.NodeStatusReplica) error {
	if replica.DeviceID == "" || replica.Revision < 1 || len(replica.Watermark) != 64 || len(replica.Digest) != 64 || strings.TrimSpace(replica.Protocol) == "" ||
		len(replica.SnapshotJSON) == 0 || len(replica.SnapshotJSON) > storage.MaxNodeStatusPayloadBytes || !json.Valid(replica.SnapshotJSON) ||
		replica.ObservedAt.IsZero() || !replica.ExpiresAt.After(replica.ObservedAt) || replica.ReceivedAt.IsZero() {
		return errors.New("valid bounded node status replica is required")
	}
	return nil
}

var _ storage.NodeStatusStore = nodeStatusStore{}
