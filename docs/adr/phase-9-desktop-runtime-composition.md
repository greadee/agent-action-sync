# Phase 9: Desktop Runtime and Scheduler Composition

Status: accepted

## Context

The supervised Codex adapter, fenced control service, Git worktree manager,
compute-node contract, scheduler, result intake, and integration gate already
existed as isolated components. Slice 3 needs one desktop-owned lifecycle
without making configuration opt-in equivalent to permission to dispatch at
process startup.

## Decision

The daemon composes local orchestration only when `node.execution.enabled` is
true. Before composition, the desktop execution manager revalidates the OS
credential, runtime digest, disposable marker, project containment, clean Git
state, pinned HEAD, and project digest. A missing composer or failed recheck
prevents daemon startup; there is no fallback runtime.

The composed scheduler always starts paused. The operator must issue an
authenticated, project-scoped scheduler-start command after every node start.
The machine and runtime share the configured concurrency ceiling, initially
restricted to one or two leases. The local node publishes short-lived CPU and
disk observations without returning paths, process data, credentials, or raw
runtime output.

The runtime receives a worktree path only through an authority-local resolver.
That resolver rechecks assignment, attempt, lease generation, and fencing
digest against SQLite before releasing the path. Paths never enter API DTOs or
portable history. Result envelopes are strict-decoded, canonicalized, bounded,
and stored under node-owned data before the integration gate evaluates them.

## Recovery boundary

Startup reconciliation may classify an attempt as `needs_operator`. Such an
attempt is never automatically replayed. The local authenticated control
surface supports pause, resume, cancel, explicit fail, retry of terminal
attempts, collecting-result evaluation, and explicit approve/reject decisions.
Resume fails with a stable conflict for uncertain work; the operator must
cancel or fail it before a retry creates a new attempt identity.

Runtime success still stops at `collecting`. Tests and review remain separately
authorized, and automatic merge, deployment, arbitrary shell, file browsing,
or workspace cleanup are not introduced.

## Verification

The Slice 3 release gate covers feature-gated daemon composition, safe paused
startup, disposable authorization revalidation, real local component wiring,
machine lease ceilings, bounded resource/result storage, project-scoped
assignment inventory, restart recovery without replay, explicit failure/retry,
the deterministic scheduler pilot, and the existing result integration gate.
