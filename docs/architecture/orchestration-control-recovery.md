# Orchestration Control, Fencing, and Recovery

- Status: implemented for Phase 1 Slice 1
- Scope: authority-local assignment state, attempts, leases, resource bindings,
  gates, operator decisions, and startup recovery

## Boundary

The orchestration control store is local authority state. It does not write an
Agent Project event, change canonical task readiness, start a runtime, allocate
a workspace, or publish a result. Portable outcomes still pass through result
intake, deterministic gates, and the existing work-history publication path.

The store retains logical runtime-session and workspace IDs plus a digest of a
runtime resume key. It never stores a bearer fencing token, credential, raw
prompt, terminal output, provider response, absolute workspace path, or process
handle.

## Durable model

Migration 14 adds these authority-local tables:

| Table | Purpose |
| --- | --- |
| `orchestration_assignments` | Stable project/work-package assignment and current attempt pointer. |
| `orchestration_attempts` | Monotonic attempt history, execution binding, state, failure code, and recovery disposition. |
| `orchestration_leases` | Owner, generation, comparison digest, heartbeat, expiry, and release state. |
| `orchestration_resource_bindings` | Fenced runtime-session and workspace identities for one attempt. |
| `orchestration_gate_status` | Versioned pending or terminal gate decisions and bounded evidence references. |
| `orchestration_operator_decisions` | Immutable, idempotent local operator decisions. |
| `orchestration_operations` | Operation digest and recorded result state for callback replay. |
| `orchestration_audit_events` | Closed action, state, reason, actor, generation, and timestamp evidence. |

A partial unique index permits only one active attempt for a project/work
package. A second partial unique index permits only one active lease for an
attempt. Retry inserts a new attempt with exactly the prior attempt number plus
one, a distinct idempotency digest, and an explicit `supersedes_attempt_id`; it
cannot reopen a terminal row.

## Reducers and transaction rules

`orchestration.ReduceAssignmentState` is the executable form of the Phase 1
ADR state table. It rejects skipped, self, reverse, and terminal transitions.
`ReduceGateState` permits only exact replay or `pending` to `satisfied`,
`failed`, or `waived`. Neither reducer interprets worker prose.

Planning, claim, resource binding, state transition, retry, gate resolution,
and operator-decision persistence use SQLite transactions. Each control
operation has an ID and digest. An exact duplicate returns the durable result;
reuse with different content conflicts. A late audit conflict rolls back every
earlier write in the same transaction.

Claim reads the current planned attempt and inserts the next lease generation
in the same transaction that changes the attempt to `leased`. Every subsequent
fenced mutation must match assignment, current attempt, expected state, lease
generation, fencing digest, active lease state, and unexpired lease time.
Heartbeat cannot revive an expired lease. Terminal transitions release or
expire the lease atomically.

Audit actions distinguish `assign`, `claim`, `prepare`, `start`, `pause`,
`resume`, `cancel`, `timeout`, `retry`, `recovery`, `gate`, operator decisions,
and `release`. Audit rows contain closed reason codes and identifiers, not raw
runtime material.

## Startup reconciliation

On startup, the control service classifies each nonterminal assignment without
starting an adapter or replaying an operation:

| Durable observation | Disposition | State change |
| --- | --- | --- |
| `planned` | `resume` | None; it may be reconsidered by a later scheduler decision. |
| `leased` with no resource binding | `reconcile` | None while the lease is current. |
| `leased` with resource evidence | `needs_operator` | None. |
| `preparing`, `running`, `paused`, or `collecting` | `needs_operator` | None. |
| `awaiting_gates` | `resume` | None; only deterministic gate evaluation may resume. |
| Expired active lease | `reconcile` if no resources exist; otherwise `needs_operator` | Attempt becomes `expired`; lease becomes `expired`. |

An ambiguous runtime is deliberately left in its observed state with
`needs_operator`; startup never creates a replacement attempt. Expiry records
separate timeout, release, and recovery audit evidence.

## Projection isolation

Agent Project projection reset and rebuild clear only canonical-derived
projection tables. They neither clear nor create orchestration assignments,
attempts, leases, resource bindings, gates, decisions, operations, or audit
events. Conversely, local control transitions do not insert portable project
events. This prevents a clean rebuild from fabricating an active owner.

## Verification

Focused tests exercise concurrent claims, stale generations and digests,
post-expiry mutations, rollback after a late transaction failure, duplicate
operations, immutable gate and operator decisions, lease timeout, monotonic
retry, cancellation/timeout races, close-and-reopen recovery, the one-active-
attempt invariant, audit coverage, and projection isolation. Production
runtime and workspace adapters remain disabled until their later slices.
