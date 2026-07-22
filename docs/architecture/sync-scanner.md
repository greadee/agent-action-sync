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
