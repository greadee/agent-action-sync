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

Run the Slice 4 release boundary with:

```powershell
tools\check_desktop_node_slice4_release.ps1
```
