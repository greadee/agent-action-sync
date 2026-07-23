# One-Way Sync Master Prompt

Use this prompt to complete the current one-way synchronization milestone for Private Sync Gate. This milestone starts from branch `codex/add-sync-scanner`, where scanning, indexing, reconciliation, deletion limits, scan planning, and pure revision generation already exist.

## Operating Contract

You are a senior Go and security engineer working in `C:\Users\prool\Documents\file transfer app`.

The goal is a reliable, one-way synchronization flow for trusted computers. The source share is authoritative. Do not begin two-way synchronization, browser access, coordinator work, relay work, mDNS discovery, or NAT traversal during this milestone.

Before every slice:

1. Inspect `git status --short --branch`, recent commits, related code, tests, and architecture docs.
2. Preserve all existing user changes. Do not reset, checkout, or revert unrelated work.
3. Confirm the current branch is `codex/add-sync-scanner` unless the owner explicitly changes it.
4. Implement only the named slice. Do not pre-build later slices.

For every completed slice:

1. Format changed Go files with the portable toolchain when needed:
   `\.cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin\gofmt.exe -w <files>`
2. Run `powershell -NoProfile -ExecutionPolicy Bypass -File tools\test.ps1`.
3. Run `git diff --check` and inspect the staged diff.
4. Update the relevant architecture or security documentation when behavior changes.
5. Stage only the slice files.
6. Commit using a short lower-case action message beginning with `add`, `fix`, `upd`, or `edit`. Never use the word `phase` in a commit message.
7. Push `codex/add-sync-scanner`.
8. Check the GitHub Actions run for that commit and report its result.
9. STOP. Do not start another slice. Ask the owner for explicit go-ahead and the model selection for the next slice.

At the end of every numbered milestone below, STOP again even if every slice passed. Give a concise milestone summary, its acceptance evidence, the recommended model for the next milestone, and wait for explicit approval before continuing.

## Model And Effort Policy

Use the assigned model and effort for the active slice. Treat the assignments as quality gates, not merely cost suggestions.

- `5.4`, low effort: documentation, fixtures, narrow CLI output, and mechanical validation only.
- `5.5`, medium effort: routine Go implementations with contained behavior and tests.
- `5.6 Terra`, medium effort: multi-file implementation, integration coverage, and deterministic test scaffolding.
- `5.6 Luna`, medium-high effort: behavior spanning storage, sync, and transfer boundaries.
- `5.6 Sol`, high effort: security-sensitive state transitions, atomic persistence, filesystem commit semantics, and policy design.

If the assigned model is unavailable, stop and ask the owner to select a substitute. Do not silently downgrade security-critical work.

## Baseline To Preserve

Already implemented on this branch:

- deterministic share scans with default ignores and unsupported-symlink recording;
- persisted `file_index` snapshots;
- scan reconciliation and deletion circuit breaker;
- scan planning that avoids persisting a blocked deletion batch;
- revision construction with ancestry, source device, ordering, and deletion revisions.

Known gap: revisions are not yet atomically recorded with the new file-index snapshot, so `current_revision_id` is not yet reliably maintained. Resolve that before a networked one-way executor exists.

## Milestone 1: Durable Authoritative State

Objective: make an approved source scan create a durable, internally consistent revision history and deletion state.

### Slice 1.1: Atomic revision and index commit

Model: `5.6 Sol`, high effort.

Implement a storage-level operation that records accepted revisions and updates the file-index snapshot in one SQLite transaction. It must:

- reject invalid or mismatched share IDs;
- record every new revision before its `file_index.current_revision_id` is referenced;
- set the new revision ID on added and modified paths;
- mark deleted paths with a deletion revision and preserve their last applicable revision ancestry;
- roll back fully on any error;
- prevent the planner from writing a bare snapshot when revisions are required;
- have SQLite tests that prove commit, rollback, current-revision lookup, and deletion behavior.

Stop after commit, push, and CI. Wait for the owner's go-ahead and model choice.

### Slice 1.2: Tombstone persistence and retention contract

Model: `5.6 Luna`, medium-high effort.

Implement a tombstone storage interface and SQLite implementation using the existing schema. Define and test:

- creation from an accepted deletion revision;
- lookup/listing by share and path;
- idempotent duplicate handling;
- retention/expiry fields without premature cleanup;
- restoration semantics when a path reappears;
- audit-ready source-device and base-revision metadata.

Document what expires, what remains recoverable, and what is deferred. Stop after commit, push, and CI.

### Slice 1.3: Revision-aware scan commit service

Model: `5.6 Luna`, medium-high effort.

Wire scan planning, revision building, atomic revision/index persistence, and tombstone creation into one application service. It must not persist anything when the deletion guard blocks the scan. Return a structured result suitable for CLI and future scheduling. Use injected clock and ID generation in tests. Stop after commit, push, and CI.

Milestone 1 acceptance:

- a safe scan atomically creates revisions and updates index pointers;
- a failed write leaves no partial revisions, indexes, or tombstones;
- an unsafe deletion batch changes nothing;
- a deletion creates a durable tombstone linked to a deletion revision.

STOP and wait for owner approval before Milestone 2.

## Milestone 2: One-Way Change Execution

Objective: turn authoritative source revisions into safe, permission-checked transfer work without two-way behavior.

### Slice 2.1: One-way policy and drift decisions

Model: `5.6 Sol`, high effort.

Define explicit source/target policy types. The source is authoritative. Target-side drift must be handled only by a configured policy: reject, preserve conflict copy, or report without applying. Reject unsupported modes and unauthorised delete/modify actions before filesystem work begins. Add unit tests for every policy branch. Stop after commit, push, and CI.

### Slice 2.2: Revision manifest protocol types

Model: `5.6 Terra`, medium effort.

Add strictly validated, versioned request/response structures for advertising revisions and requesting one-way changes. Include request IDs, device IDs, share ID, revision IDs, limits, and structured errors. Bound manifest size and entry count. Do not couple these types to TCP/TLS implementation details. Add fixtures and malformed-input tests. Stop after commit, push, and CI.

### Slice 2.3: Authorized receiver change preparation

Model: `5.6 Luna`, medium-high effort.

Implement receiver-side authorization and change preparation. Validate share capability, normalized path, source revision, and one-way policy before creating transfer work. Ensure an unauthorized or malformed request cannot create partial files, revisions, or index changes. Stop after commit, push, and CI.

### Slice 2.4: Safe one-way apply executor

Model: `5.6 Sol`, high effort.

Connect verified file transfer commit with revision-aware destination application. Handle files, directories, and deletion/tombstone actions through safe filesystem operations. Preserve replaced/deleted data according to the existing history behavior. Make requests idempotent and interruption-safe. Add focused integration tests. Stop after commit, push, and CI.

### Slice 2.5: Two-agent one-way sync integration tests

Model: `5.6 Terra`, medium effort.

Build deterministic local two-agent tests covering add, modify, delete, offline target reconciliation, duplicate request, restart/resume, guard block, authorization failure, target drift policy, and ignored files. No real LAN discovery is needed. Stop after commit, push, and CI.

Milestone 2 acceptance:

- source additions and modifications arrive safely at a target;
- approved deletions create and apply tombstones;
- target drift follows an explicit configured policy;
- duplicate/retried work does not create duplicate commits;
- no unauthorized request changes the target filesystem or state.

STOP and wait for owner approval before Milestone 3.

## Milestone 3: Local Sync Operations

Objective: make one-way sync durable and usable on a daily Windows desktop/laptop workflow.

### Slice 3.1: Filesystem watcher and reconciliation trigger

Model: `5.6 Luna`, medium-high effort.

Add a watcher behind an interface with debounce and bounded event handling. Watcher events only request scans; scans remain authoritative. Detect overflow/error and schedule a full reconciliation. Stop after commit, push, and CI.

### Slice 3.2: Scan scheduling and unavailable-root safety

Model: `5.6 Luna`, medium-high effort.

Add startup, periodic, manual, and watcher-triggered scan scheduling. Detect missing or unavailable share roots and fail closed without treating them as deletions. Add cancellation, backoff, and no-overlapping-scan behavior. Stop after commit, push, and CI.

### Slice 3.3: Sync job queue and restart recovery

Model: `5.6 Luna`, medium-high effort.

Persist one-way sync work with bounded concurrency, retry/backoff, pause/resume, and restart recovery. Reuse existing transfer state where appropriate; do not invent a competing transfer state machine. Stop after commit, push, and CI.

### Slice 3.4: Per-share sync configuration and diagnostics

Model: `5.5`, medium effort.

Add validated per-share configuration for mode, ignore patterns, scan interval, deletion limits, and destination-drift policy. Provide CLI diagnostics that show recent scan state, pending/blocked changes, and ignored-path reasons without leaking secrets. Stop after commit, push, and CI.

### Slice 3.5: Reliability and security test hardening

Model: `5.6 Terra`, medium effort.

Add race-test guidance, fault-injection coverage, path/ignore fuzzing, locked-file and disk-full tests where practical, and structured log redaction checks. Update threat model and known limitations. Stop after commit, push, and CI.

Milestone 3 acceptance:

- missed watcher events recover through scans;
- unavailable roots cannot trigger mass deletion propagation;
- queued work survives restart and retries safely;
- operators can inspect blocked, pending, and completed one-way changes;
- the documented test suite covers the main destructive and restart paths.

STOP and wait for owner approval before merge preparation.

## Merge Preparation

Model: `5.6 Luna`, medium-high effort.

Do not merge automatically. First:

1. Review the complete branch diff against `main` for security regressions, broken package boundaries, missing docs, and test gaps.
2. Run the complete local suite and any available race/fuzz smoke checks.
3. Confirm all branch GitHub Actions runs are green.
4. Prepare a concise merge summary and known limitations.
5. STOP and wait for the owner's explicit approval and selected model before merging into `main`.

After explicit approval, merge the finished feature branch into `main`, push `main`, verify its CI, and stop. Never use `master`.

## Non-Negotiable Boundaries

- Do not add two-way synchronization or automatic conflict resolution.
- Do not add browser portal, coordinator, relay, mDNS, QUIC, NAT traversal, or public exposure.
- Do not claim end-to-end encryption beyond what the implemented mutual-authenticated transport proves.
- Do not trust remote paths, timestamps alone, watcher events, or sender-side authorization.
- Do not propagate deletes from an incomplete, unavailable-root, or guard-blocked scan.
- Do not stage or commit unrelated user work.
