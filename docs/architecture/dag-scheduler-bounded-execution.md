# DAG Scheduler and Bounded Execution

- Status: implemented for Phase 1 Slice 6
- Release boundary: daemon-owned opt-in composition; shipped CLI/config still
  does not enable runtime execution

## Composition boundary

`internal/scheduler` is the application service that composes the existing
readiness reducer, deterministic selection, context/contract binding,
authority-local control state, compute-node leases, workspaces, and runtime
adapters. The daemon accepts it only through the explicit
`daemon.Options.OrchestrationScheduler` lifecycle seam. A nil scheduler remains
the default, so this slice does not silently enable the Codex adapter or Git
worktree allocation in the shipped command.

The scheduler imports neither project-history writers nor result intake. An
architecture test forbids imports of `workhistory`, `resultintake`, `projector`,
sync, and transport. Runtime output can move an assignment only to
`collecting`; Slice 7 must validate it and publish the existing canonical
events through the authority services.

## Deterministic dispatch

Each cycle first observes active runtimes and renews both the compute-node lease
and authority-control heartbeat. It then reduces every supplied graph from its
canonical events. Only a node whose derived state is `ready` enters the queue.
The queue order is:

1. work-package priority, highest first, falling back to task priority; and
2. stable work-package ID in ascending order.

Selection is re-evaluated against current runtime resolution and node
definition/observation. The active assignment count and daemon ceiling are
injected into the existing budget policy. Phase 1 rejects a scheduler ceiling
above two, while a lower user, node, or runtime constraint can reduce capacity
further. Cycle execution is serialized so polling, API, and retry triggers
cannot race into duplicate dispatch.

Dispatch proceeds in an authority-preserving order: bind immutable context and
contract, acquire a compute lease, claim the fenced control lease, allocate the
deterministic attempt workspace, prepare and start the runtime, then record
`running`. Stable idempotency digests cover every provider and control action.
Failures release compute capacity and transition the local attempt where its
current state permits it. They never create canonical project records.

## Dependency barrier

Runtime success is not acceptance. On a successful runtime observation, the
scheduler transitions the local assignment to `collecting` and releases its
compute lease. A dependent node remains `dependency_waiting` until the
canonical reducer observes the existing approved review and `WORK_ACCEPTED`
events. Canonical failure or cancellation blocks downstream nodes with the
reducer's `dependency_failed` reason.

## Pause, cancellation, restart, and shutdown

- Pause immediately prevents new dispatch and asks each active adapter to
  pause. An adapter that explicitly lacks pause capability may keep running,
  but no replacement work starts.
- Resume restarts sessions that reached `paused` and then reopens dispatch.
- Cancel is idempotent, fences the local assignment, releases compute capacity,
  and never calls workspace cleanup.
- Startup calls durable control reconciliation before dispatch. Existing
  runtime/workspace bindings are inspected and reported, but uncertain work is
  never replayed automatically.
- Daemon shutdown marks the scheduler draining, stops its polling loop,
  cancels active attempts, releases compute leases, and completes before the
  daemon closes SQLite. Worktrees remain available for explicit, ownership-
  checked inspection or cleanup.

## Stable blocked outcomes

Reports preserve deterministic readiness and selection reasons, including
`dependency_waiting`, `dependency_failed`, `runtime_unavailable`,
`budget_tokens_exceeded`, `lease_limit_reached`,
`concurrency_limit_reached`, and `scheduler_paused`. Provider failures after
selection use closed scheduler reasons such as `lease_unavailable`,
`workspace_unavailable`, and `runtime_prepare_failed`; raw provider output is
not part of the report.

## Verification

The deterministic runtime, compute-node, workspace, and control fakes prove
that two independent DAG nodes overlap, a dependent node waits through runtime
completion, canonical acceptance unlocks it, priority ordering does not starve
lower-priority ready work, provider leases cap execution, concurrent cycles do
not duplicate dispatch, cancellation is idempotent, and shutdown preserves
workspaces. Daemon coverage fixes the shutdown order as scheduler drain before
storage close.
