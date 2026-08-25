# Task Graph Validation and Readiness

Slice 2 adds deterministic orchestration setup without enabling a worker,
runtime, assignment, or lease. `internal/project` owns portable task, graph,
and extended work-package contracts. `internal/orchestration` depends on those
contracts and contains provider-independent graph validation and replay. The
authority-owned `workhistory.CreateTask` operation is the only supported local
publication boundary.

## Immutable aggregate

A task revision selects one dependency-graph revision. The graph contains a
sorted membership list that pins each immutable work-package definition by
record ID and digest. Dependency edges remain declared exactly once, in those
definitions; the graph commits their sorted dependency-set digest and optional
barrier member IDs. This prevents two edge sources from drifting.

Creation is deterministic and idempotent for the project and idempotency key.
The complete task, definitions, graph, and `WORK_PACKAGE_CREATED` events are
validated before the first portable record is published. Reusing an immutable
path with different content is a conflict. No worker is selected or started.

Validation rejects:

- missing members or dependencies, self-dependencies, and duplicate edges;
- cycles, more than 256 nodes, or more than 4,096 edges;
- cross-project, task-revision, graph-revision, record-ID, or digest mismatch;
- duplicate/unsorted members or barriers and a barrier outside membership;
- canceled validation or replay contexts.

## Canonical replay

Readiness is derived only from accepted canonical `WORK_PACKAGE_*`, review,
and acceptance events. Events are ordered by UTC occurrence time, fixed event
type precedence, and record ID. Thus filesystem order and equal timestamps do
not change the result. The event watermark hashes the graph dependency digest
and the ordered event identities/content digests.

Each node has one readiness value and one stable explanation code:

| Readiness | Representative explanation |
|---|---|
| `planned` | `work_package_not_created` |
| `waiting` | `dependency_waiting` or `execution_in_progress` |
| `ready` | `dependencies_satisfied` |
| `blocked` | `dependency_failed` or `canonical_blocked` |
| `review` | `review_required` |
| `accepted` | `work_accepted` |
| `failed` | `work_failed` |
| `canceled` | `work_canceled` |

The task summary uses equally stable `task_*` explanation codes. Every ready
node is returned in a sorted `parallel_ready` set. A barrier is explicit graph
metadata; it does not override dependency readiness or invent an execution
policy.

Failure or cancellation blocks dependents. A valid canonical retry or reopen
transition changes readiness on replay. Acceptance requires the existing
review and acceptance event sequence; the reducer does not make a quality
judgment.

## Projection and recovery

SQLite migration 9 adds only rebuildable `project_tasks` and
`project_task_nodes` rows. Queries are bounded and cursor-paginated. The
projector validates complete aggregates, reduces accepted canonical events,
and replaces all derived rows transactionally with the other Agent Project
projections. Missing late records fail closed; their later arrival causes a
normal candidate-set rebuild. Deleting projections and replaying the same
portable records recreates equal readiness at the same watermark.

Portable schema minor 0 work-package records remain readable and simply have
no task membership. Minor 1 is required before task membership fields or the
new task/graph record kinds may be published.
