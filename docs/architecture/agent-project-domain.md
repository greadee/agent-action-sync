# Agent Project Domain and Filesystem Safety

The `internal/project` package owns portable Agent Project contracts and their
filesystem invariants. It has no SQLite, network, daemon, API, or sync-engine
dependency.

## Layout boundary

`NewLayout` accepts an existing real directory as the project sync root. It does
not bootstrap a project. Stage 4 owns creation of the complete directory layout
and the initial manifest/event pair.

The package recognizes these fixed control locations:

```text
workspace/
.agent-project/
  manifest.json
  history/events/YYYY/MM/DD/<event-id>.json
  work-packages/<work-package-id>/definition.json
  executions/<execution-id>/manifest.json
  executions/<execution-id>/handoff.json
  executions/<execution-id>/results/
  artifacts/manifests/<artifact-id>.json
  artifacts/blobs/sha256/<content-hash>
  local/quarantine/
```

Portable paths must be canonical forward-slash relative paths. The project
package reuses `internal/filesystem` for lexical containment, Windows-reserved
names, and `Lstat`-based symlink rejection. Managed-path inspection rejects a
symlink in the root, any existing parent, or the leaf.

`.agent-project/local/` is managed local state but is not portable. Portable
record reads and writes reject it even though local quarantine services may use
it in a later stage.

## Bootstrap and scan policy

`ProjectBootstrapper.Preflight` is a read-only eligibility check for an
existing real share root. It reports directory collisions, a malformed control
directory, unclaimed root data, or an identity conflict without creating files
or directories. An eligible unclaimed root may contain an existing
`workspace/`, SyncGate recovery directories, and stale `*.sync-part` files;
other root data is deliberately left for the Stage 10 migration flow instead
of being moved or rewritten automatically.

`ProjectBootstrapper.Bootstrap` creates the fixed directories with symlink-safe
single-directory creation, publishes `manifest.json`, then publishes one
deterministic `PROJECT_REGISTERED` event. A repeat with the same project ID,
name, and authority reuses the immutable records. If a crash occurs after the
manifest publication, the next identical bootstrap completes the missing
registration event rather than issuing a second logical registration. A
mismatched identity or conflicting immutable record is rejected.

`NewProjectScanPolicy` keeps the portable `.agent-project/` control records in
the normal share scan. It adds `.agent-project/local/**`, `.git/**`, `.env*`,
and `.secrets/**` as mandatory project exclusions, preserving existing SyncGate
ignores for history, incoming transfers, and partial files. It does not inject
broad language-cache or build-output rules; those remain configured share
exclusions. Pass its
`EffectiveIgnorePatterns` to the scanner, whose existing ignored-path
diagnostics identify the matching rule.

## Derived immutable paths

Every final portable-record path is derived from its validated typed record:

- The project manifest has one fixed path.
- Work-package, execution, handoff, and artifact paths use their scoped IDs.
- Work events use their UTC occurrence date and record ID.
- Artifact blobs use their verified SHA-256 digest.

Path-bearing identifiers are ASCII-lowercased before use. This makes case-only
IDs address one immutable path on both case-sensitive and case-insensitive
filesystems. The original identifier remains unchanged in record content; a
case-only second record is therefore a content conflict.

## Atomic publication

`PublishRecord` validates and canonically encodes the record before filesystem
mutation. It then:

1. Resolves the derived path inside `.agent-project/` and outside `local/`.
2. Rejects symlink components and a non-regular existing leaf.
3. Treats canonical identical existing content as an idempotent replay.
4. Treats different or malformed existing content as an immutable conflict.
5. Creates only missing real parent directories.
6. Writes a restrictive same-directory `*.sync-part` temporary file.
7. Flushes and closes the complete temporary file.
8. Rechecks the destination path.
9. Atomically publishes without replacement.

Windows uses `MoveFileEx` with `MOVEFILE_WRITE_THROUGH` and without
`MOVEFILE_REPLACE_EXISTING`. Other platforms atomically create a hard link for
the final name and remove the temporary name afterward. In both cases a
concurrent publisher cannot replace a completed immutable record.

A failure before the atomic operation exposes no final record. A process crash
can leave a `*.sync-part` file, but canonical record discovery ignores it. A
failure to remove a post-publication temporary hard link does not invalidate the
final record and is reported in the publication result.

These guarantees match the repository's current local-filesystem durability
boundary. They do not eliminate time-of-check/time-of-use attacks by a process
with the same operating-system account; configured project roots remain trusted
local roots.

## Discovery and verification

`DiscoverProject` walks upward from an existing file or directory to the nearest
valid `.agent-project/manifest.json`. Before returning it verifies that the
starting path is contained without symlink traversal. It does not create or
repair state.

`ReadPortableRecord` requires canonical bytes, a regular non-symlink file, a
supported record contract, and a valid integrity digest. `VerifyRecord` also
compares the file with the canonical encoding expected by the caller.

`HashProjectFile` and `VerifyProjectFile` provide opaque SHA-256 content
identity for regular project files. They verify containment and symlink safety
but never render, execute, or deserialize file content.

## Error contract

Filesystem-facing operations return `DomainError` values that work with
`errors.Is` and `errors.As`. Stable categories are:

- `ErrInvalidRoot`
- `ErrUnsafePath`
- `ErrRecordNotFound`
- `ErrRecordConflict`
- `ErrRecordIntegrity`
- `ErrFilesystem`

The displayed error contains the operation and category but not the underlying
absolute path. The wrapped cause remains available for programmatic checks such
as `errors.Is(err, os.ErrNotExist)`.
