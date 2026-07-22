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
