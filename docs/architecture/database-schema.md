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

CREATE TABLE one_way_jobs (
    job_id TEXT PRIMARY KEY,
    transfer_id TEXT NOT NULL REFERENCES transfers(transfer_id) ON DELETE CASCADE,
    peer_device_id TEXT NOT NULL REFERENCES devices(device_id),
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    revision_id TEXT,
    relative_path TEXT NOT NULL,
    required_capability TEXT NOT NULL,
    remote INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL CHECK (state IN ('queued','running','retry_wait','paused','completed','failed')),
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
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

CREATE TABLE pairing_acceptances (
    invite_id TEXT PRIMARY KEY,
    peer_device_id TEXT NOT NULL REFERENCES devices(device_id),
    fingerprint TEXT NOT NULL,
    audit_id TEXT NOT NULL REFERENCES audit_events(audit_id),
    accepted_at TEXT NOT NULL
);

CREATE TABLE agent_projects (
    project_id TEXT PRIMARY KEY,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    name TEXT NOT NULL,
    authority_device_id TEXT NOT NULL,
    manifest_record_id TEXT NOT NULL,
    manifest_record_hash TEXT NOT NULL,
    manifest_path TEXT NOT NULL,
    registered_at TEXT NOT NULL,
    UNIQUE (share_id)
);

CREATE TABLE project_events (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    event_id TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    event_type TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    work_package_id TEXT,
    execution_id TEXT,
    producer_worker_id TEXT,
    producer_device_id TEXT NOT NULL,
    producer_trade TEXT,
    producer_model TEXT,
    status TEXT NOT NULL,
    record_path TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    PRIMARY KEY (project_id, event_id),
    UNIQUE (project_id, record_path)
);

CREATE TABLE project_artifacts (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    blob_path TEXT,
    work_package_id TEXT,
    execution_id TEXT,
    producer_worker_id TEXT,
    producer_device_id TEXT NOT NULL,
    producer_model TEXT,
    created_at TEXT NOT NULL,
    record_path TEXT NOT NULL,
    PRIMARY KEY (project_id, artifact_id)
);

CREATE TABLE project_projection_checkpoints (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    stream TEXT NOT NULL,
    last_record_path TEXT NOT NULL,
    last_record_hash TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, stream)
);

CREATE TABLE project_projection_rejections (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    record_path TEXT NOT NULL,
    observed_hash TEXT,
    reason_code TEXT NOT NULL,
    quarantine_path TEXT,
    rejected_at TEXT NOT NULL,
    PRIMARY KEY (project_id, record_path)
);
```

## Notes

- All timestamps should be stored as UTC RFC3339 strings.
- `relative_path` values are normalized paths, never raw user input.
- Plaintext file contents, private keys, passwords, and session tokens must never be stored in audit metadata.
- The history, one-way incoming, and partial-transfer paths are application-managed and excluded from ordinary synchronization. Durable one-way intent files live under `.sync-incoming/` only until the revision/index transaction succeeds.
- Tombstones are immutable deletion history. `expires_at` is metadata only until an explicit retention policy is implemented; expired rows are not automatically removed.
- One-way jobs contain queue/retry metadata and reference an existing transfer; transfer chunks remain the sole resume source of truth. Receiver jobs repeat the authenticated peer ID, required action capability, and authenticated LAN/remote scope so execution can reject identity/scope drift and reauthorize current trust before work begins or after restart.
- Authenticated receiver transfer and job rows are inserted atomically only after the same transaction verifies trusted device state plus `sync` and action capabilities. Failed or revoked authorization leaves neither row behind.
- Pairing acceptance rows make signed invitations idempotent per local database. Device trust, explicit permissions, acceptance, and audit are committed atomically.
- A reappeared path is restored by advancing its `file_index.current_revision_id` to a non-deleted revision. The prior tombstone remains available as history, while active deletion propagation considers only tombstones still referenced by a deleted index entry.
- Agent Project tables are local, rebuildable projections. Canonical files under `.agent-project/` remain authoritative and are never modified by a projection reset or rebuild.
- Event and artifact hashes make duplicate ingestion idempotent while rejecting an identifier reused for different canonical content. Chronological indexes include the record ID as a stable tie-breaker.
- Event-specific JSON is validated and bounded before insertion. Basic history filters and pagination use typed columns and do not require SQLite JSON extensions.
- Projection checkpoints advance inside the same transaction as rebuild writes. Local rejection metadata can point at `.agent-project/local/quarantine/`, but it is not portable authority.
- `project_insights` is a local, versioned Stage 8 projection over accepted
  `project_events`. Its source-event watermark, definition version, sample
  count, completeness, and evidence state keep each metric auditable. A
  history-projection rebuild explicitly invalidates insight rows; canonical
  records are unchanged.
- Migration 9 adds `project_tasks` and `project_task_nodes`. They contain only
  rebuildable task/DAG readiness, canonical record identities and hashes,
  stable explanation codes, parallel-ready membership, and node dependency
  views. They contain no assignment, lease, runtime session, credential, or
  agent-authored state. Both tables are removed by an Agent Project projection
  reset and reconstructed from portable task/graph/work-package records plus
  accepted canonical events.
- Migration 10 adds local `registry_trade_versions`, `registry_worker_versions`,
  `registry_project_trade_adaptations`, and `registry_audit_events`. These are
  durable local control data, not Agent Project projections and not portable
  records. Their schema has no credential, secret, token, runtime-session, or
  provider-session column. Portable history retains only resolved registry
  ID/version/digest references.
- Migration 11 adds local immutable `execution_contract_versions`. Each row
  binds a contract ID/version and execution identity to a digest, predecessor
  digest, and bounded canonical contract JSON. Same-content replay is
  idempotent, mutation conflicts, and a unique project/execution/version key
  prevents competing contract identities. This is authority control state,
  not a rebuildable Agent Project projection; it contains logical secret IDs
  only, never secret values, credentials, access tokens, or runtime sessions.
- Migration 12 adds `orchestration_result_intake` for immutable untrusted
  result envelopes and sanitized authority decisions. The result ID is the
  immutable key; same-digest replay is idempotent, different-digest reuse
  conflicts, and one project execution cannot reuse an idempotency digest under
  another result ID. Rows bind project/execution, contract, assignment, decision,
  reason, actor, and bounded canonical envelope JSON. The table is local
  control state, never a portable work event, and has no prompt, terminal,
  credential, secret-value, provider-session, or staging-path columns.
- Migration 13 adds `execution_telemetry` as a bounded, allowlisted local
  evidence projection. Rows bind telemetry to the exact execution contract,
  task/graph/work-package revisions, registry and runtime references, and a
  canonical summary digest. It stores nullable measurements and content
  references only; it has no raw prompt, tool, terminal, secret, workspace,
  or provider-session column. Telemetry cannot authorize execution or satisfy
  a work acceptance gate.
- Migration 14 adds the authority-local orchestration control tables for
  assignments, monotonic attempts, fenced leases, opaque runtime/workspace
  bindings, gate status, operator decisions, idempotent operations, and audit
  events. Partial unique indexes enforce one active attempt per project/work
  package and one active lease per attempt. These rows survive canonical
  projection rebuilds but never create portable history. They store comparison
  digests and logical IDs, not bearer tokens, credentials, raw prompts,
  terminal output, provider sessions, or absolute workspace paths. See
  [Orchestration control, fencing, and recovery](orchestration-control-recovery.md).
