# Agent Project Ingestion and Recovery

`internal/projector` is the application-layer bridge between canonical Agent
Project files and the rebuildable SQLite projection. It imports
`internal/project` and `internal/storage`; neither the sync engine nor the
project contract package imports it.

## Deterministic candidate set

Every run first validates `.agent-project/manifest.json`, including its
integrity digest and project identity. It then walks `.agent-project/` without
following symlinks, skips `.agent-project/local/`, and considers only these
record shapes:

- `manifest.json`
- `history/events/YYYY/MM/DD/<event-id>.json`
- `work-packages/<work-package-id>/definition.json`
- `executions/<execution-id>/manifest.json`
- `executions/<execution-id>/handoff.json`
- `artifacts/manifests/<artifact-id>.json`

The discovered paths are sorted before validation. A decoded record is accepted
only when its embedded project ID matches the root manifest and
`project.RecordRelativePath` derives its exact current path. Files under the
workspace, execution results, unknown control paths, local quarantine, and
partial-transfer paths cannot enter the projection.

The checkpoint hash is a deterministic digest of every candidate path, content
identity, and validation outcome. This detects a synchronized record that
arrives lexically before a previous path and avoids depending on directory
traversal order. An unchanged digest is a safe no-op.

## Commit and recovery order

An ingestion run performs these durable steps:

1. Idempotently register the validated root manifest.
2. When the candidate-set digest changed, replace all event, artifact, and
   rejection projection rows in one SQLite transaction.
3. Advance the record-set checkpoint in a second transaction.

The checkpoint is never written before the projection transaction commits. A
crash after discovery leaves no projection changes. A crash after projection
commit but before checkpoint advancement causes the next run to replace the
projection with the same deterministic result. Replacing changed candidate sets
also removes stale rows when a canonical record disappears or becomes invalid,
so incremental and explicit clean rebuilds converge. Canonical files are never
changed by database reset or rebuild.

## Out-of-order records

Each run validates work-package, execution, handoff, and artifact records before
projecting events. An event with a missing referenced record is stored as
`pending`, retaining its canonical payload and typed query fields. When a later
run sees the dependency, the identical event hash is reconciled to `accepted`.
No placeholder work package, execution, artifact, or event is invented.

## Rejection and quarantine

One invalid non-manifest candidate does not stop independent valid records. The
projector records a local rejection with a stable reason code. When the invalid
path is a safely opened bounded regular file, it also creates a content-addressed
copy under `.agent-project/local/quarantine/rejected-records/`. It never moves,
deletes, or rewrites the authoritative candidate. Unsafe, oversized, or
unreadable paths receive rejection metadata without a quarantine copy.

Structured diagnostics contain only project-relative paths, stable reason
codes, severity, and fixed messages. Raw record content, secrets, underlying
filesystem errors, and absolute local paths are excluded.

## Lifecycle request boundary

`projector.LifecycleHook` supports requests after a local portable-record write
or a share update. Ordinary shares without a project manifest are a no-op, and
share-triggered ingestion verifies that the manifest authority names the same
share. The daemon exposes an optional `RequestProjectIngestion` callback and
invokes it after committed scans and successful receiver job execution. This is
an application callback; no project semantics are added to `internal/sync`.
