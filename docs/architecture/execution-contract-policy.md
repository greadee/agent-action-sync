# Execution Contract and Permission Policy

## Status and boundary

Slice 5 implements the provider-neutral authority object that must exist before
any runtime can start work. `internal/executioncontract` builds a deterministic,
immutable `syncgate.execution-contract.v1` document and stores each local
version through `storage.ExecutionContractStore`. This slice does not enable a
runtime, shell, network adapter, deployment path, lease, or automatic merge.

The complete contract is authority-local because it contains local policy,
budgets, runtime and node selections. Portable execution history records only
the resolved `contract:` ID, version, and digest. Sync remains unable to create
or interpret execution authority.

## Exact binding

A contract binds one execution to all of the following immutable inputs:

- project ID and project revision digest;
- task ID/revision/record/digest, graph revision/record/digest, and work-package
  ID/definition record/digest;
- execution ID and resolved trade/worker ID, version, and digest;
- instruction, runtime, provider, model, and node ID/version/digest;
- deterministic context digest;
- effective permissions, limits, start time, deadline, deliverables,
  acceptance criteria, and exact required gates.

The builder rejects mismatched project/task/graph/work-package membership,
stale work-package digests, inactive or differently bound workers, malformed
references, missing policy layers, non-UTC times, and unknown risk or
capability values. Canonical sorting and SHA-256 make equal stored inputs
produce byte-equivalent contract JSON and the same digest.

## Permission intersection

The requested authority is intersected across required `project`, `runtime`,
`task`, `trade`, `user`, `work_package`, and `worker` layers. A requested
capability must appear in every layer and in no deny list. Missing or unknown
capabilities deny the build. The initial closed capability vocabulary is:

`inspect`, `write`, `shell`, `test`, `dependency_install`, `network`, `secret`,
`database`, `branch`, `merge`, `deployment`, and `infrastructure`.

Inspect and write roots are the most specific overlap of the work-package
scope and every corresponding layer. A forbidden path from any source takes
precedence, including when it is a child of an otherwise allowed root. Runtime
path checks validate a project-relative path again and require containment in
the effective root. Secret access additionally requires the `secret`
capability and the exact namespaced secret ID in every layer; secret values are
never stored in the contract.

## Budgets and terminal decisions

Token, cost-in-micros, wall-clock, retry, tool-call, and concurrent-worker
limits use the minimum of the requested limit and every policy ceiling. Zero,
negative, missing, or unrepresentable limits fail contract creation. The
deadline cannot precede the start or exceed the effective wall-clock limit.

The pure runtime decision reducer gives cancellation precedence, rejects
negative usage, and returns stable codes for each exhausted limit. Cancellation
and exhausted ceilings are terminal. A failure inside the retry ceiling is a
recoverable `retry_wait`; it does not imply acceptance or publication.

## Risk and quality gates

Required task and work-package gate references are copied by exact ID, version,
and digest. A high or critical task/work-package risk always adds the built-in
human-review gate. Security/secret risk adds security review; data/database
risk adds data review; deployment/infrastructure risk adds operations approval.
An explicit work-package review flag and merge capability require human review;
secret capability requires security review; deployment or infrastructure
capability requires operations approval.

Built-in gate identities have deterministic version-1 digests. A portable gate
that reuses a built-in ID with different evidence is a hard conflict. Worker
success is only a claim: completion is accepted only when evidence satisfies
every exact required gate reference.

## Immutable amendment and persistence

Migration 11 stores `(contract_id, version)` as immutable local authority and
also prevents two contract identities from claiming the same
`(project_id, execution_id, version)`. Same-ID/version/same-digest replay is
idempotent; different content conflicts. Version 2 and later must name the
immediately preceding version and digest. The service loads that predecessor
and refuses an amendment that changes the project revision, task/graph/work
package identity or digest, or execution identity. Policy or budget changes
therefore create a newly hashed version; no row is updated in place.

## Dependency rule

`executioncontract` may depend on portable project contracts and storage
interfaces. `project`, `sync`, transports, and provider/runtime adapters must
not import it. Slice 6 may consume the verified contract through a
provider-neutral runtime interface, but production adapters remain disabled
until their separate security and release gates pass.
