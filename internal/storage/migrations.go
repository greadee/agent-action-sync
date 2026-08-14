package storage

import (
	"errors"
	"fmt"
	"strings"
)

type Migration struct {
	Version int
	Name    string
	SQL     string
}

var Migrations = []Migration{
	{
		Version: 1,
		Name:    "initial local agent schema",
		SQL: strings.TrimSpace(`
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    device_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    public_key BLOB NOT NULL,
    fingerprint TEXT NOT NULL UNIQUE,
    trust_state TEXT NOT NULL CHECK (trust_state IN ('pending','trusted','revoked')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_seen_at TEXT
);

CREATE TABLE IF NOT EXISTS shares (
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

CREATE TABLE IF NOT EXISTS share_permissions (
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

CREATE TABLE IF NOT EXISTS revisions (
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

CREATE TABLE IF NOT EXISTS file_index (
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

CREATE TABLE IF NOT EXISTS transfers (
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

CREATE TABLE IF NOT EXISTS transfer_chunks (
    transfer_id TEXT NOT NULL REFERENCES transfers(transfer_id) ON DELETE CASCADE,
    chunk_index INTEGER NOT NULL,
    offset INTEGER NOT NULL,
    size INTEGER NOT NULL,
    chunk_hash TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending','received','verified','rejected')),
    verified_at TEXT,
    PRIMARY KEY (transfer_id, chunk_index)
);

CREATE TABLE IF NOT EXISTS tombstones (
    tombstone_id TEXT PRIMARY KEY,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    deleted_by_device_id TEXT NOT NULL,
    base_revision_id TEXT REFERENCES revisions(revision_id),
    tombstone_revision_id TEXT NOT NULL REFERENCES revisions(revision_id),
    deleted_at TEXT NOT NULL,
    expires_at TEXT
);

CREATE TABLE IF NOT EXISTS conflicts (
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

CREATE TABLE IF NOT EXISTS audit_events (
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
`),
	},
	{
		Version: 2,
		Name:    "persistent one-way sync jobs",
		SQL: strings.TrimSpace(`
CREATE TABLE IF NOT EXISTS one_way_jobs (
    job_id TEXT PRIMARY KEY,
    transfer_id TEXT NOT NULL REFERENCES transfers(transfer_id) ON DELETE CASCADE,
    share_id TEXT NOT NULL REFERENCES shares(share_id) ON DELETE CASCADE,
    revision_id TEXT,
    relative_path TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','running','retry_wait','paused','completed','failed')),
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT,
    last_error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS one_way_jobs_runnable_idx
    ON one_way_jobs(state, next_attempt_at, created_at);
`),
	},
	{
		Version: 3,
		Name:    "pairing acceptance records",
		SQL: strings.TrimSpace(`
CREATE TABLE IF NOT EXISTS pairing_acceptances (
    invite_id TEXT PRIMARY KEY,
    peer_device_id TEXT NOT NULL REFERENCES devices(device_id),
    fingerprint TEXT NOT NULL,
    audit_id TEXT NOT NULL REFERENCES audit_events(audit_id),
    accepted_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pairing_acceptances_peer_idx
    ON pairing_acceptances(peer_device_id, accepted_at);
`),
	},
	{
		Version: 4,
		Name:    "authenticated one-way job identity",
		SQL: strings.TrimSpace(`
ALTER TABLE one_way_jobs ADD COLUMN peer_device_id TEXT REFERENCES devices(device_id);
ALTER TABLE one_way_jobs ADD COLUMN required_capability TEXT;
CREATE INDEX IF NOT EXISTS one_way_jobs_peer_idx
    ON one_way_jobs(peer_device_id, state, created_at);
`),
	},
	{
		Version: 5,
		Name:    "one-way job network scope",
		SQL: strings.TrimSpace(`
ALTER TABLE one_way_jobs ADD COLUMN remote INTEGER NOT NULL DEFAULT 1;
`),
	},
	{
		Version: 6,
		Name:    "bounded administration query indexes",
		SQL: strings.TrimSpace(`
CREATE INDEX IF NOT EXISTS admin_permissions_device_idx
    ON share_permissions(device_id, share_id);
CREATE INDEX IF NOT EXISTS admin_jobs_page_idx
    ON one_way_jobs(created_at DESC, job_id DESC);
CREATE INDEX IF NOT EXISTS admin_jobs_share_state_page_idx
    ON one_way_jobs(share_id, state, created_at DESC, job_id DESC);
CREATE INDEX IF NOT EXISTS admin_audit_page_idx
    ON audit_events(occurred_at DESC, audit_id DESC);
CREATE INDEX IF NOT EXISTS admin_audit_scope_page_idx
    ON audit_events(share_id, peer_device_id, occurred_at DESC, audit_id DESC);
`),
	},
	{
		Version: 7,
		Name:    "agent project local projections",
		SQL: strings.TrimSpace(`
CREATE TABLE IF NOT EXISTS agent_projects (
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

CREATE TABLE IF NOT EXISTS project_events (
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
    status TEXT NOT NULL CHECK (status IN ('accepted','pending')),
    record_path TEXT NOT NULL,
    payload_json TEXT NOT NULL CHECK (length(payload_json) <= 1048576),
    PRIMARY KEY (project_id, event_id),
    UNIQUE (project_id, record_path)
);
CREATE INDEX IF NOT EXISTS project_events_chronology_idx
    ON project_events(project_id, occurred_at, event_id);
CREATE INDEX IF NOT EXISTS project_events_work_package_idx
    ON project_events(project_id, work_package_id, occurred_at, event_id);
CREATE INDEX IF NOT EXISTS project_events_execution_idx
    ON project_events(project_id, execution_id, occurred_at, event_id);
CREATE INDEX IF NOT EXISTS project_events_type_idx
    ON project_events(project_id, event_type, occurred_at, event_id);

CREATE TABLE IF NOT EXISTS project_artifacts (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    artifact_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    record_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    content_hash TEXT NOT NULL,
    blob_path TEXT,
    work_package_id TEXT,
    execution_id TEXT,
    producer_worker_id TEXT,
    producer_device_id TEXT NOT NULL,
    producer_model TEXT,
    created_at TEXT NOT NULL,
    record_path TEXT NOT NULL,
    PRIMARY KEY (project_id, artifact_id),
    UNIQUE (project_id, record_id),
    UNIQUE (project_id, record_path)
);
CREATE INDEX IF NOT EXISTS project_artifacts_chronology_idx
    ON project_artifacts(project_id, created_at, artifact_id);
CREATE INDEX IF NOT EXISTS project_artifacts_work_package_idx
    ON project_artifacts(project_id, work_package_id, created_at, artifact_id);
CREATE INDEX IF NOT EXISTS project_artifacts_execution_idx
    ON project_artifacts(project_id, execution_id, created_at, artifact_id);

CREATE TABLE IF NOT EXISTS project_projection_checkpoints (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    stream TEXT NOT NULL,
    last_record_path TEXT NOT NULL,
    last_record_hash TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, stream)
);

CREATE TABLE IF NOT EXISTS project_projection_rejections (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    record_path TEXT NOT NULL,
    observed_hash TEXT,
    reason_code TEXT NOT NULL,
    quarantine_path TEXT,
    rejected_at TEXT NOT NULL,
    PRIMARY KEY (project_id, record_path)
);
CREATE INDEX IF NOT EXISTS project_projection_rejections_time_idx
    ON project_projection_rejections(project_id, rejected_at, record_path);
`),
	},
	{
		Version: 8,
		Name:    "agent project deterministic work insights",
		SQL: strings.TrimSpace(`
ALTER TABLE project_events ADD COLUMN producer_provider TEXT;

CREATE TABLE IF NOT EXISTS project_insights (
    project_id TEXT NOT NULL REFERENCES agent_projects(project_id) ON DELETE CASCADE,
    scope TEXT NOT NULL,
    metric_name TEXT NOT NULL,
    definition_version INTEGER NOT NULL CHECK (definition_version > 0),
    source_event_watermark TEXT NOT NULL,
    window_start TEXT,
    window_end TEXT,
    value_json TEXT NOT NULL CHECK (length(value_json) <= 1048576),
    sample_count INTEGER NOT NULL CHECK (sample_count >= 0),
    completeness TEXT NOT NULL CHECK (completeness IN ('complete','partial','insufficient')),
    evidence TEXT NOT NULL CHECK (evidence IN ('strong','weak','none')),
    calculated_at TEXT NOT NULL,
    PRIMARY KEY (project_id, scope, metric_name, definition_version)
);
CREATE INDEX IF NOT EXISTS project_insights_lookup_idx
    ON project_insights(project_id, scope, metric_name, definition_version);
`),
	},
}

func ValidateMigrations(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("at least one migration is required")
	}
	for i, migration := range migrations {
		expected := i + 1
		if migration.Version != expected {
			return fmt.Errorf("migration %d has version %d, want %d", i, migration.Version, expected)
		}
		if strings.TrimSpace(migration.Name) == "" {
			return fmt.Errorf("migration %d has empty name", migration.Version)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("migration %d has empty SQL", migration.Version)
		}
	}
	return nil
}
