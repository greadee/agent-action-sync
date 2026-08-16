# Agent Project Migration and Recovery

## Scope and authority

Migration converts one configured `one_way_source` or `upload_only` share into
an Agent Project. The existing share root becomes the project sync root; the
operation never moves, deletes, or rewrites workspace files. Version 1 remains
single-authority: only the configured source device/share creates canonical
project history. A target is a validating replica, not a second writer.

An eligible root keeps ordinary project content below `workspace/`. Existing
content elsewhere is reported as a blocking collision and must be handled by
the operator outside SyncGate. This fail-closed rule prevents migration from
silently reorganizing a repository.

## Preflight

Run preflight against the running daemon:

```powershell
syncgate project-migrate-preflight --config config.json --share SOURCE --project PROJECT --name "Project name"
```

The JSON result reports:

- `status`: `ready`, `already_complete`, or `blocked`;
- the configured share and requested project identity;
- an opaque SHA-256 root identity, never the absolute root;
- bounded relative collision and unsupported-path reasons;
- detected excluded local paths;
- mandatory and missing configured ignore patterns;
- the manifest and registration event expected from apply;
- a confirmation digest tied to the current root state and identity.

Preflight is read-only. A symbolic link, malformed or conflicting control
layout, wrong authority mode, data outside `workspace/`, mismatched manifest,
or different existing share registration blocks apply. It examines at most
10,000 entries and returns at most 64 paths per bounded result.

Project safety ignores are enforced dynamically after registration, so missing
duplicates in `config.json` are informational and do not require a config
rewrite. SyncGate does not edit configuration during migration.

## Apply

Copy the exact confirmation from the immediately preceding preflight:

```powershell
syncgate project-migrate-apply --config config.json --share SOURCE --project PROJECT --name "Project name" --confirmation DIGEST
```

Apply reruns preflight before mutation. A changed root or identity returns a
conflict and requires a new preflight. A successful apply:

1. uses the project bootstrap service to create missing managed directories;
2. atomically publishes the immutable manifest and `PROJECT_REGISTERED` event;
3. registers and ingests the project into daemon-owned SQLite;
4. calculates the initial versioned insights;
5. writes one idempotent `project_migration_applied` audit event;
6. requests an authoritative share scan.

Retrying after timeout or interruption is safe. Identical portable records,
registration, and audit identity are reused, and the result becomes
`already_applied`.

## Portable and local-only paths

Portable project data includes `workspace/` and `.agent-project/` except its
local subtree. These mandatory patterns never synchronize for a registered
project:

```text
.agent-project/local
.agent-project/local/**
.git
.git/**
.env
.env.*
.secrets
.secrets/**
```

The scanner also retains its global exclusions for `.sync-history/`,
`.sync-incoming/`, and `*.sync-part`. Secret-pattern exclusions are a narrow
last line of defense, not a general secret classifier: operators must still
keep credentials and private artifacts out of synchronized workspace content.

## Freshness, rejection evidence, and rebuild

Use the authenticated project endpoints to inspect state:

```text
GET  /api/v1/projects/{project_id}
GET  /api/v1/projects/{project_id}/history
GET  /api/v1/projects/{project_id}/insights
GET  /api/v1/projects/{project_id}/rejections
POST /api/v1/projects/{project_id}/projections/rebuild
```

Rejection results contain only project-relative candidate paths, observed
hashes, fixed reason codes, and timestamps. They never return quarantine paths
or candidate content. `unavailable`, `rebuilding`, and `stale` project states
identify when a clean rebuild or daemon recovery is needed.

An interrupted apply can be retried with a new preflight. A manifest without a
registration event is repaired idempotently. A canonical file published before
SQLite commit is recovered by ingestion. A partial transfer remains a
non-authoritative `*.sync-part` file and is ignored until the supported transfer
recovery path completes it.

To disable project processing without deleting workspace files, stop the
daemon and remove the share from the active configuration, then restart. Do
not delete `.agent-project/` as a disable mechanism: its portable records are
the project system of record. Re-enable with the same share root and identity.

## Compatibility and release boundary

Readers accept supported additive minor schema changes and reject unknown
major versions, record kinds, or event types. Mixed agent versions can transfer
bytes, but the older target may report rejections until upgraded; it never
reinterprets unsupported records.

The release gate covers migration replay, trusted two-daemon transfer,
revocation, unavailable targets, interrupted apply recovery, corruption,
schema rejection, deletion guards, restart, clean rebuild, and exclusion of
Git, local state, partial files, and common secret files. It does not authorize
multi-writer collaboration, automatic conflict merging, remote execution, or
agent orchestration.
