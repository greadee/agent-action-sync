# Phase 2: Sync Scanner Decisions

Status: accepted

## Scope

Phase 2 added authoritative folder scanning, revision history, one-way planning,
deletion propagation, and guarded receiver apply on top of the transfer
foundation.

## Treat scans as authoritative

### Context

Filesystem watcher events may be duplicated, reordered, coalesced, or lost.
They also cannot safely distinguish an unavailable root from an intentionally
empty directory.

### Decision

Watchers and schedules request scans but never mutate the index directly. A
full scan of the configured share root is the source of truth. Startup,
periodic, manual, watcher-event, overflow, and watcher-recovery triggers all
flow through the same serialized scan scheduler.

### Consequences

- Watcher loss requests recovery instead of implying file deletion.
- Only one scan runs per share; one pending request may be coalesced while it
  runs.
- Platform watcher adapters remain replaceable and subordinate to scan logic.

## Commit revisions atomically

### Context

One-way synchronization needs durable ancestry and deletion history. A process
failure between index and revision writes could otherwise leave an internally
inconsistent source of truth.

### Decision

Plan scan changes first, then atomically commit the approved file-index
snapshot, revisions, and tombstones. Revision sequence allocation advances only
after a successful commit. Internal incoming, history, and partial-transfer
paths are excluded from ordinary scans.

### Consequences

- Restart recovery observes either the old state or the complete new state.
- Revision manifests can describe deterministic ancestry.
- Receiver apply can validate expected base revisions and preserve history.

## Fail closed for unavailable roots and unsafe deletion sets

### Context

An unmounted drive or inaccessible directory can look like mass deletion. A
legitimate but unexpectedly large deletion set can also result from mistakes or
malicious local changes.

### Decision

Preflight the share root before every scan. An absent or inaccessible root is
reported as unavailable and cannot commit revisions, tombstones, or queued
deletion work. For available roots, apply configured count and percentage
deletion limits before generating IDs or changing durable state.

### Consequences

- Root loss preserves the last committed index.
- Guard-blocked snapshots consume no revision or tombstone IDs.
- Operators must investigate and deliberately adjust thresholds rather than
  silently accepting destructive changes.

## Keep one-way apply receiver-authoritative

### Context

An authoritative source still must not be allowed to bypass target policy,
trust changes, path safety, or target-side drift checks.

### Decision

The receiver validates source and target modes, current peer trust, explicit
sync and action capabilities, revision ancestry, observed target revision,
on-disk content, and configured drift policy before filesystem mutation.
Prepared work and durable jobs retain authenticated peer and network scope, and
authorization is rechecked before job execution and apply.

### Consequences

- Revocation blocks new work and queued receive work at the receiver.
- Target drift is rejected or preserved only according to explicit policy.
- Deletion produces immutable tombstone history after authorization and guard
  checks succeed.
- Diagnostics report sanitized operational state without exposing share roots
  or credentials.
