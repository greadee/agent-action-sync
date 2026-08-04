# Initial SQLite Schema

This schema is the Phase 0 target for the first SQLite implementation. It is intentionally local-agent-owned; the coordinator is not a source of truth for files.

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE devices (
    device_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    public_key BLOB NOT NULL,
    fingerprint TEXT NOT NULL UNIQUE,
    trust_state TEXT NOT NULL CHECK (trust_state IN ('pending','trusted','revoked')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_seen_at TEXT
);

CREATE TABLE shares (
    share_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    root_path TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('send_once','one_way_source','one_way_target','upload_only','read_only','two_way_planned')),
    case_policy TEXT NOT NULL,
    version_policy TEXT NOT NULL,
    deletion_limit_count INTEGER NOT NULL DEFAULT 100,
    deletion_limit_percent INTEGER NOT NULL DEFAULT 10,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE share_permissions (
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    device_id TEXT NOT NULL REFERENCES devices(device_id) ON DELETE CASCADE,
    can_list INTEGER NOT NULL DEFAULT 0,
    can_read INTEGER NOT NULL DEFAULT 0,
    can_upload INTEGER NOT NULL DEFAULT 0,
    can_modify INTEGER NOT NULL DEFAULT 0,
    can_rename INTEGER NOT NULL DEFAULT 0,
    can_delete INTEGER NOT NULL DEFAULT 0,
    can_access_history INTEGER NOT NULL DEFAULT 0,
    can_restore_history INTEGER NOT NULL DEFAULT 0,
    can_sync INTEGER NOT NULL DEFAULT 0,
    lan_only INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (share_id, device_id)
);

CREATE TABLE revisions (
    revision_id TEXT PRIMARY KEY,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    entry_type TEXT NOT NULL CHECK (entry_type IN ('file','directory','symlink_unsupported','deleted')),
    size INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT,
    hash_algorithm TEXT,
    parent_revision_id TEXT REFERENCES revisions(revision_id),
    origin_device_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    is_deleted INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    UNIQUE (share_id, relative_path, revision_id)
);

CREATE TABLE file_index (
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    entry_type TEXT NOT NULL,
    size INTEGER NOT NULL DEFAULT 0,
    modified_time TEXT,
    creation_time TEXT,
    file_identity TEXT,
    content_hash TEXT,
    hash_algorithm TEXT,
    current_revision_id TEXT REFERENCES revisions(revision_id),
    is_deleted INTEGER NOT NULL DEFAULT 0,
    deleted_at TEXT,
    last_scanned_at TEXT NOT NULL,
    PRIMARY KEY (share_id, relative_path)
);

CREATE TABLE transfers (
    transfer_id TEXT PRIMARY KEY,
    direction TEXT NOT NULL CHECK (direction IN ('send','receive')),
    peer_device_id TEXT NOT NULL REFERENCES devices(device_id),
    share_id TEXT REFERENCES shares(share_id),
    relative_path TEXT NOT NULL,
    state TEXT NOT NULL,
    size INTEGER NOT NULL DEFAULT 0,
    chunk_size INTEGER NOT NULL,
    content_hash TEXT,
    hash_algorithm TEXT,
    bytes_verified INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    last_error TEXT
);

CREATE TABLE transfer_chunks (
    transfer_id TEXT NOT NULL REFERENCES transfers(transfer_id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    offset INTEGER NOT NULL,
    size INTEGER NOT NULL,
    chunk_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','received','verified','rejected')),
    verified_at TEXT,
    PRIMARY KEY (transfer_id, chunk_index)
);

CREATE TABLE tombstones (
    tombstone_id TEXT PRIMARY KEY,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    deleted_by_device_id TEXT NOT NULL,
    base_revision_id TEXT REFERENCES revisions(revision_id),
    tombstone_revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
    deleted_at TEXT NOT NULL,
    expires_at TEXT
);

CREATE TABLE conflicts (
    conflict_id TEXT PRIMARY KEY,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    base_revision_id TEXT REFERENCES revisions(revision_id),
    local_revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
    remote_revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
    status TEXT NOT NULL CHECK (status IN ('open','resolved')),
    preserved_paths_json TEXT NOT NULL,
    detected_at TEXT NOT NULL,
    resolved_at TEXT,
    resolution TEXT
);

CREATE TABLE audit_events (
    audit_id TEXT PRIMARY KEY,
    event_name TEXT NOT NULL,
    device_id TEXT,
    peer_device_id TEXT,
    share_id TEXT,
    transfer_id TEXT,
    revision_id TEXT,
    transport_type TEXT,
    severity TEXT NOT NULL,
    metadata_json TEXT NOT NULL DEFAULT '{}',
    occurred_at TEXT NOT NULL
);
```

## Notes

- All timestamps should be stored as UTC RFC3339 strings.
- `relative_path` values are normalized paths, never raw user input.
- Plaintext file contents, private keys, passwords, and session tokens must never be stored in audit metadata.
- The history, one-way incoming, and partial-transfer paths are application-managed and excluded from ordinary synchronization. Durable one-way intent files live under `.sync-incoming/` only until the revision/index transaction succeeds.
- Tombstones are immutable deletion history. `expires_at` is metadata only until an explicit retention policy is implemented; expired rows are not automatically removed.
- A reappeared path is restored by advancing its `file_index.current_revision_id` to a non-deleted revision. The prior tombstone remains available as history, while active deletion propagation considers only tombstones still referenced by a deleted index entry.
