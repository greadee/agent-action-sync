# Sync Scanner

The sync scanner is the first source-of-truth component for folder synchronization. Watchers trigger scans, but filesystem events are not authoritative by themselves.

Watcher trigger behavior:

- A platform watcher is supplied behind the `Watcher` interface.
- Windows uses `ReadDirectoryChangesW` with a bounded native buffer and
  reconnects through a per-share supervisor after backend failure.
- Events are debounced into one scan request and never mutate the index directly.
- The trigger bounds events represented by one debounce window.
- An explicit overflow, a bounded-queue overflow error, or another watcher error requests a full recovery scan.
- The scheduler owns the resulting request and performs the authoritative scan.
- A missing share root produces a full recovery request while the supervisor
  retries; the scheduler's root preflight marks the scan unavailable instead of
  treating the root as empty.

Scheduling behavior:

- Startup, periodic, manual, and watcher-triggered requests share one scheduler.
- The daemon composes automatic runtimes for `one_way_source` and
  `upload_only` shares; target and one-shot modes do not start source scanners.
- Each runtime uses its share's configured scan interval and deletion limits at
  the composition boundary, so callers cannot silently bypass guardrails.
- A scan never overlaps another scan; one pending trigger is retained while a scan is running.
- Scan cancellation is passed through to the scan operation.
- Scan failures use bounded exponential periodic backoff and reset after success.
- Root availability is checked before every scan. Missing or unavailable roots skip the scan and cannot be interpreted as propagated deletions.

Per-share configuration:

- `mode` is validated against the supported local modes: `send_once`, `one_way_source`, `one_way_target`, `upload_only`, and `read_only`.
- `ignore_patterns` are trimmed, normalized to slash separators, and must stay relative to the share root.
- `scan_interval_seconds` defaults to 60 and must be positive.
- `deletion_limit_count` defaults to 100 and must not be negative.
- `deletion_limit_percent` defaults to 10 and must be between 0 and 100.
- `target_drift_policy` defaults to `reject` and must be one of `reject`, `preserve_conflict_copy`, or `report_only`.

One-way job behavior:

- Queue rows reference an existing transfer ID; transfer bytes and verified chunks remain the resume source of truth.
- Runnable jobs are claimed transactionally, bounded by configured concurrency, and retried with capped backoff.
- Paused jobs are excluded until resumed. Running jobs are returned to queued state during restart recovery.
- The daemon waits for active job workers before closing SQLite. Without a trusted transport executor, queued peer work fails closed and never opens a remote connection.
- Local CLI controls pause, resume, or retry persisted jobs idempotently; status reads SQLite directly and does not open a listener.

Diagnostics behavior:

- Scan outcomes can be summarized as recent scan diagnostics with trigger, status, counts, deletion-guard reasons, and sanitized errors.
- The daemon retains a bounded recent outcome history in memory and waits for
  active scan runtimes to stop before closing SQLite during shutdown.
- One-way jobs are summarized only when pending or blocked: queued, running, retry-wait, paused, or failed.
- Ignored-path diagnostics report share ID, relative path, and matching pattern without printing share roots.
- The CLI can print a local JSON diagnostics snapshot with `syncgate diagnostics --file <path> --recent 5`.

Current scanner behavior:

- Walks a share root deterministically.
- Emits `core.FileIndexEntry` records.
- Hashes regular files with SHA-256.
- Records directories.
- Records symlinks as unsupported instead of following them.
- Ignores `.sync-history/` and `*.sync-part` by default.
- Supports simple ignore patterns such as `*.log` and `build/**`.

Unsupported non-regular filesystem entries fail the scan closed.

Reconciliation behavior:

- Compares a previous `file_index` snapshot with a fresh scan result.
- Emits deterministic path-ordered changes.
- Classifies paths as `added`, `modified`, `deleted`, or `unchanged`.
- Treats a previously deleted path as unchanged until it reappears.
- Treats a reappeared previously deleted path as added.
- Compares regular files by size, hash algorithm, and content hash.
- Compares directories by entry type only.

Deletion guard behavior:

- Counts pending `deleted` changes before they are eligible for propagation.
- Tracks deletion percentage against previously active paths.
- Blocks when deletes exceed configured count or percentage limits.
- Treats a zero count or zero percentage limit as disabled for that limit.
- Returns deleted paths and reasons so the CLI or agent can explain the block.

Scan planning behavior:

- Loads the previous file-index snapshot, scans the share, and reconciles both states.
- Applies deletion limits before a snapshot is eligible for commit.
- Never persists a bare snapshot; paths that changed require revisions before storage can advance the index.
- Returns the scan, reconciliation, and guard decision so a revision-aware commit service can persist approved changes.

Revision-building behavior:

- Converts approved added, modified, and deleted changes into path-ordered revisions.
- Associates every revision with its source device and a caller-provided sequence range.
- Links updates and deletions to the previously recorded revision for that path.
- Represents deletions as `deleted` revisions, ready for later tombstone propagation.

Revision-aware storage behavior:

- Records accepted revisions and advances the file-index snapshot in one SQLite transaction.
- Requires every added, modified, reappeared, or deleted path to have one matching revision.
- Preserves the current revision pointer for unchanged paths and assigns the accepted revision ID to changed paths.
- Keeps deleted paths in the index, points them at a deletion revision, and records deletion time.
- Rejects unknown or mismatched share IDs, mismatched revision metadata, stale parent revisions, and revisions for unchanged paths.
- Rolls back the complete revision and index update when validation or persistence fails.

Tombstone behavior:

- Records a tombstone only from a deletion revision that is present in `revisions` and is the current deleted pointer in `file_index`.
- Derives the tombstone share, path, source device, deletion time, and base revision from that accepted deletion revision; callers cannot provide conflicting ancestry metadata.
- Treats tombstones as immutable deletion history. Repeating the same logical deletion is idempotent, while conflicting metadata is rejected.
- Stores optional `expires_at` retention metadata but never deletes or hides expired rows automatically; cleanup and retention policy remain deferred.
- `ListActive` returns only tombstones whose deletion revision is still current in a deleted `file_index` entry. When a path reappears, its new revision supersedes the tombstone for propagation while the historical row remains recoverable through `Get` and `List`.

Revision-aware scan commit service:

- Plans the scan before generating revision or tombstone IDs, so a deletion-guard block performs no writes and consumes no IDs.
- Builds deterministic revisions with injected clock and ID sources, then commits the file-index snapshot, revisions, and derived tombstones through one authoritative storage transaction.
- Returns the plan, accepted revisions, tombstones, commit status, blocked status, and commit time for CLI and scheduler consumers.
- Uses optional tombstone retention as metadata only; cleanup remains outside the scan commit service.
