# Phase 6: Supervised Single-Node Pilot State Machine

- Status: accepted for Phase 1 Slice 0
- Date: 2026-08-21
- Scope: authority-local assignment lifecycle, failure taxonomy, recovery, and
  operator confirmation points for the first single-node pilot

## Context

Phase 5 established portable work intent, authority-local control state, and
provider-neutral setup contracts. Phase 1 will connect those contracts to one
opt-in local runtime adapter and one local compute node. The connection must
not turn a worker claim, a lease expiry, or a runtime callback into canonical
project history.

This ADR freezes the control-plane state machine before durable tables,
worktree provisioning, or a production adapter are implemented. It does not
enable a runtime, process, remote dispatch, Git mutation, or a new portable
record kind.

## Decision

### Authority and state ownership

Assignments, attempts, leases, runtime sessions, workspace IDs, gate progress,
and operator decisions are authority-local durable state. Process handles,
provider session IDs, temporary tokens, absolute paths, and cancellation
functions are ephemeral local state. The only canonical outcomes remain the
existing `workhistory.Service` publications: typed execution, test, handoff,
artifact, review, and acceptance records.

No assignment state or runtime callback changes a work package's canonical
state by itself. A result is an untrusted input to result intake. A worker
claim of success is never `WORK_ACCEPTED`.

### Assignment states

Every assignment has one immutable execution-contract reference, a monotonic
attempt number, an idempotency-key digest, and one current state:

| State | Meaning | Permitted next state |
| --- | --- | --- |
| `planned` | Validated ready work awaiting a durable claim. | `leased`, `canceled` |
| `leased` | A current lease generation reserves the selected local node. | `preparing`, `expired`, `canceled` |
| `preparing` | The authority is provisioning only the already-authorized workspace/runtime session. | `running`, `paused`, `collecting`, `failed`, `canceled`, `expired` |
| `running` | A current fenced attempt is active. | `paused`, `collecting`, `failed`, `canceled`, `expired` |
| `paused` | The authority has requested and observed a resumable pause. | `running`, `collecting`, `failed`, `canceled`, `expired` |
| `collecting` | Runtime execution has stopped; only bounded result collection is in progress. | `awaiting_gates`, `failed`, `canceled` |
| `awaiting_gates` | Result intake accepted a candidate; deterministic tests/review/approval remain. | `accepted`, `failed`, `canceled` |
| `accepted` | The authority completed every required gate and published the allowed canonical records. | terminal |
| `failed` | The authority recorded a terminal local failure or a retry superseded this attempt. | terminal |
| `canceled` | An authorized cancellation completed or the attempt was invalidated before start. | terminal |
| `expired` | Lease or deadline elapsed without safe evidence that the runtime stopped. | terminal |

An explicit retry does not reactivate a terminal attempt. It creates a new
assignment attempt with a higher number, a new idempotency digest, and a new
lease generation. A contract amendment follows the immutable predecessor rules
and is not an in-place permission, budget, or deadline update.

### Fencing, idempotency, and callbacks

A claim succeeds only when the readiness watermark, contract digest, selected
worker/node, and expected assignment state still match in one transaction. The
authority increments a lease generation and stores only a comparison/fencing
value; the bearer token itself is ephemeral and never portable or returned by
the API.

Every runtime operation and callback binds assignment ID, attempt number,
lease generation, contract digest, and an operation idempotency digest. A
duplicate with the same binding returns the recorded result. A stale, changed,
or unknown binding is rejected and cannot advance state. No transition depends
on parsing free-form worker prose: state decisions consume closed runtime
status/error codes, contract limits, exact result envelopes, deterministic gate
evidence, and explicit operator decisions.

### Failure taxonomy

| Class | Examples | Authority outcome |
| --- | --- | --- |
| Policy rejection | invalid readiness, unavailable capability, scope mismatch, denied tool/secret/network request | Do not lease or start; retain a sanitized local reason code. |
| Pre-start failure | workspace preflight failure, dirty repository, adapter negotiation failure | Release reservation; terminal failure or explicit operator retry. |
| Runtime failure | normalized adapter error, lost local child, timeout, cancellation failure | Fence the attempt, collect only safe evidence, and mark `failed`, `canceled`, or `expired`; never infer canonical work failure. |
| Result rejection | bad envelope, stale lease, forged binding, unsafe patch, duplicate conflict | Preserve a sanitized intake decision; do not publish canonical output. |
| Gate failure | deterministic test failure, required review rejection, missing evidence | Keep the candidate non-canonical; fail or return for an explicit retry. |
| Recovery ambiguity | daemon restart, missing process evidence, lost workspace handle, clock uncertainty | Fence the old attempt and set recovery disposition `needs_operator`; never automatically replay it. |

Failure codes are closed, versioned identifiers. Sanitized explanations may be
shown to the local operator but contain no credential, absolute path, raw
prompt, terminal output, provider response, or session identifier.

### Cancellation, timeout, shutdown, and restart

- Cancellation is authorized only by the local authority/operator. The
  authority fences new callbacks before requesting adapter cancellation.
- Deadline exhaustion fences the attempt and yields `expired`; it does not
  publish a canonical failure or start a replacement automatically.
- Daemon shutdown stops new claims, persists a recovery checkpoint, requests
  bounded cancellation where an adapter exists, and leaves ambiguous active
  attempts for reconciliation rather than replay.
- On restart, `planned` work may be reconsidered. A `leased` attempt with no
  runtime/workspace evidence may be reconciled. Any `preparing`, `running`,
  `paused`, or `collecting` attempt without exact current fencing and adapter
  evidence becomes `needs_operator` recovery disposition. `awaiting_gates`
  resumes only deterministic gate evaluation against immutable retained input.
- A clean project projection rebuild never recreates an assignment, lease,
  session, or active attempt. It can only reconstruct portable readiness.

### Operator and irreversible-action boundary

The operator must explicitly approve the task graph before dispatch, the
project/runtime opt-in before a first start, each retry after an ambiguous or
terminal attempt, any gate waiver, and final integration/acceptance. Phase 1
does not automatically merge, deploy, install dependencies, access secrets,
enable network access, or publish a result as canonical acceptance.

The adapter may receive only the immutable execution contract and opaque
workspace ID. It cannot broaden sync permissions, change share capabilities,
publish project records directly, or request a remote shell. Sync and transport
remain semantic-blind byte movement.

## Consequences

Slice 1 must implement this state machine in authority-local tables with
transactional fencing, audit records, and explicit recovery dispositions. Any
additional state, automatic recovery path, or production adapter capability
requires an ADR amendment and threat-model review.

## Alternatives rejected

- Treat lease expiry as a canonical work failure: rejected because no runtime
  stop or work outcome has been established.
- Automatically resume every active attempt after restart: rejected because a
  surviving provider or child process could create duplicate execution.
- Use worker prose to infer status, tests, or approval: rejected because it is
  ambiguous and may be malicious.
- Reuse one attempt after retry: rejected because it loses fencing and audit
  history.
