# Desktop Multi-Project Operations and Recovery

The desktop node may register and inspect multiple Agent Projects, but its
runtime authorization is intentionally single-project. A registration makes a
project visible; selection makes it the current operator context; execution
still requires that project's root to be the root approved by the desktop
execution preflight.

## Register and inspect projects

Register an existing share as an Agent Project with the migration preflight and
apply commands in [Agent Project migration](agent-project-migration.md). Project
discovery validates `.agent-project/manifest.json` and projects the portable
records into local SQLite.

List the bounded, sanitized local operations view:

```powershell
syncgate orchestration-projects --config <node-config.json> --limit 50
```

The response contains opaque project IDs, display names, selection and policy
flags, aggregate assignment/gate counts, and scheduler state. It does not
contain share IDs, project roots, workspace paths, prompts, runtime sessions, or
project content. Use the returned cursor through the authenticated API when the
inventory exceeds one page.

## Select and authorize scheduling

The scheduler starts paused after every node start. While it is paused, select
one project and set its local policy:

Before task approval, import an immutable authority-local task specification
through bounded stdin. Specification content is never accepted in process
arguments:

First inspect the authority-local identities that the running node bootstrapped:

```powershell
$trades = syncgate orchestration-trades --config <node-config.json> | ConvertFrom-Json
$workers = syncgate orchestration-workers --config <node-config.json> | ConvertFrom-Json
$capabilities = syncgate orchestration-capabilities --config <node-config.json> | ConvertFrom-Json
```

Every work package in the specification must contain the exact active
`trade_id`, `version`, and `digest`. The worker is configuration-scoped, so use
the worker returned by the node rather than copying an ID from another machine.

```powershell
$specification = Get-Content -Raw docs\examples\task-specification-v1.json |
  syncgate node-task-spec-import --from-stdin | ConvertFrom-Json

syncgate orchestration-task-validate `
  --config <node-config.json> `
  --project $specification.project_id `
  --specification $specification.specification_id `
  --specification-digest $specification.digest

$created = syncgate orchestration-task-create `
  --config <node-config.json> `
  --project $specification.project_id `
  --task $specification.task_id `
  --task-revision 1 `
  --graph-revision 1 `
  --specification $specification.specification_id `
  --specification-digest $specification.digest `
  --idempotency-key <unique-key> | ConvertFrom-Json
```

The specification is stored only in the node-owned data root. Task creation
publishes its validated task, graph, work-package definitions, and creation
events into canonical portable project history. Reimporting equivalent content
or repeating the same create command is safe; changing content under an
existing specification identity or idempotency key fails as a conflict.

Keep the execution repository clean before workspace preflight. Portable Agent
Project history is synchronized by SyncGate, not by Git; when both share one
root, configure the repository's local ignore policy for portable control data
before execution preflight. If the control directory is intentionally tracked,
commit its immutable additions and refresh execution authorization before the
next node start.

```powershell
syncgate orchestration-project-select `
  --config <node-config.json> `
  --project <project-id> `
  --idempotency-key <unique-key>

syncgate orchestration-project-policy `
  --config <node-config.json> `
  --project <project-id> `
  --scheduling-enabled `
  --max-concurrent 1 `
  --idempotency-key <unique-key>
```

Preflight the context, immutable contract, and runtime before approval. The
first context preflight discovers and returns `source_set_digest`; supplying
that digest on a replay pins the caller to the same authority-selected source
set. Use one new operation key for both the contract preview and the graph
approval so the scheduler reconstructs the exact previewed contract:

```powershell
$context = syncgate orchestration-context-preflight `
  --config <node-config.json> `
  --project <project-id> `
  --work-package <work-package-id> `
  --trade $trades.items[0].trade_id `
  --trade-version $trades.items[0].version `
  --trade-digest $trades.items[0].digest | ConvertFrom-Json

$operationKey = "approve-<unique-value>"
$contract = syncgate orchestration-contract-preview `
  --config <node-config.json> `
  --project <project-id> `
  --task <task-id> `
  --task-revision 1 `
  --graph-revision 1 `
  --work-package <work-package-id> `
  --worker $workers.items[0].worker_id `
  --worker-version $workers.items[0].version `
  --worker-digest $workers.items[0].digest `
  --idempotency-key $operationKey | ConvertFrom-Json

$runtime = $capabilities.runtimes[0]
$node = $capabilities.nodes[0]
syncgate orchestration-runtime-preflight `
  --config <node-config.json> `
  --project <project-id> `
  --contract $contract.contract_id `
  --contract-version $contract.contract_version `
  --contract-digest $contract.contract_digest `
  --runtime $runtime.id `
  --runtime-version $runtime.version `
  --runtime-digest $runtime.digest `
  --node $node.id `
  --node-version $node.version `
  --node-digest $node.digest

syncgate orchestration-task-approve `
  --config <node-config.json> `
  --project <project-id> `
  --task <task-id> `
  --task-revision 1 `
  --graph-revision 1 `
  --approval-digest $created.graph_digest `
  --idempotency-key $operationKey
```

Only after all preflights report the expected identity and runtime readiness,
start the scheduler:

```powershell

syncgate orchestration-scheduler-start `
  --config <node-config.json> `
  --project <project-id> `
  --idempotency-key <unique-key>
```

The project ceiling must be one or two and cannot exceed the ceiling approved
for the node. A selected project whose root was not authorized by execution
preflight remains inventory-only and cannot start or mutate the shared runtime.

To switch projects, disable the current scheduler first:

```powershell
syncgate orchestration-scheduler-disable `
  --config <node-config.json> `
  --project <current-project-id> `
  --idempotency-key <unique-key>
```

If the next project is inventory-only, stop the node, disable the old execution
authorization, complete a fresh disposable-project preflight for the next root,
enable it, and restart. Selection alone never transfers runtime authority.

## Backup and restore boundary

Sync is not backup. Maintain an independent, versioned backup of each project
root. Portable project material includes `workspace/` and `.agent-project/`
except `.agent-project/local/`; treat artifact blobs as potentially sensitive
and untrusted content.

Node configuration, SQLite, result intake, projections, assignments, leases,
selection, policy, worktrees, runtime cache, and `.agent-project/local/` are
authority-local. Windows Credential Manager secrets and private device identity
are outside file backups. Do not copy a node database, credential handle,
runtime cache, or worktree to another machine and treat it as authority.

For a portable restore onto a new or rebuilt node:

1. Restore the project root from the independent backup and validate it before
   enabling synchronization.
2. Initialize the node and configure credentials through Windows Credential
   Manager.
3. Register/discover the restored project and rebuild its projections from the
   validated portable records.
4. Recreate the local policy and explicit selection.
5. Repeat disposable-root execution preflight and enablement. The scheduler
   remains paused until an explicit project-scoped start.

For a same-node cold disaster backup, stop the daemon cleanly before copying
configuration and data directories. After restore, inspect every nonterminal or
`needs_operator` assignment and resolve it explicitly; never assume a restored
lease or runtime handle is live. Revalidate project roots, Git HEAD, credentials,
and execution authorization before scheduler start.

Run the dispatch-authority release boundary with:

```powershell
tools\check_desktop_node_slice12_release.ps1
```
