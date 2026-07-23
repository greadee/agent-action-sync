# Sync Scanner

The sync scanner is the first source-of-truth component for folder synchronization. Watchers may later trigger scans, but filesystem events are not authoritative by themselves.

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
- Applies deletion limits before writing a new snapshot.
- Persists the snapshot only when the deletion guard allows it.
- Returns the scan, reconciliation, and guard decision so callers can present or propagate the approved changes.

Revision-building behavior:

- Converts approved added, modified, and deleted changes into path-ordered revisions.
- Associates every revision with its source device and a caller-provided sequence range.
- Links updates and deletions to the previously recorded revision for that path.
- Represents deletions as `deleted` revisions, ready for later tombstone propagation.
