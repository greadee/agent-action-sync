# Desktop Runtime Operation and Recovery

Execution is disabled in every new desktop configuration. Complete the
disposable-project and credential procedure in
[Desktop node configuration](desktop-node-configuration.md), then restart the
node. Even when execution is enabled, the scheduler starts paused.

## Inspect and start local execution

```powershell
syncgate node-execution-status
syncgate node-resources
syncgate node-run

syncgate orchestration-scheduler-start `
  --config <node-config.json> `
  --project <project-id> `
  --idempotency-key <unique-key>
```

The local API is loopback-only and authenticated. Scheduler start is explicit
after every node process start. The configured machine ceiling is one or two
concurrent leases; lowering it requires disabling execution and completing a
new validated enablement.

## Inspect recovery state

```powershell
syncgate orchestration-assignments --config <node-config.json> --project <project-id>
syncgate orchestration-assignment --config <node-config.json> --project <project-id> --assignment <assignment-id>
```

Use closed state, failure, recovery, and gate codes. Raw prompts, source,
terminal output, credentials, runtime sessions, and workspace paths are not
available through this surface.

For `needs_operator`, do not resume. First investigate the external provider
or child process, then choose an explicit terminal resolution:

```powershell
syncgate orchestration-assignment-control `
  --config <node-config.json> `
  --project <project-id> --assignment <assignment-id> `
  --action fail --idempotency-key <unique-key>

syncgate orchestration-assignment-control `
  --config <node-config.json> `
  --project <project-id> --assignment <assignment-id> `
  --action retry --idempotency-key <new-unique-key>
```

`retry` is accepted only for failed, canceled, or expired attempts and creates
a new attempt identity. It never resumes the uncertain runtime session.

For `paused`, use `resume` only when the attempt is not marked
`needs_operator`. For `collecting`, import the strict result envelope locally,
then evaluate it:

```powershell
Get-Content .\result-envelope.json -Raw |
  syncgate node-result-import --from-stdin

syncgate orchestration-assignment-control `
  --config <node-config.json> `
  --project <project-id> --assignment <assignment-id> `
  --action evaluate --idempotency-key <unique-key>
```

Required automated gates fail closed unless an authorized exact-command test
runner is configured. Approval or rejection requires the returned sanitized
summary digest. No command in this slice merges a branch or deletes a
worktree.

Validate the shipped boundary with:

```powershell
tools\check_desktop_node_slice3_release.ps1
```
