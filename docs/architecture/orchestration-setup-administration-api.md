# Orchestration Setup Administration API

Slice 9 extends the authenticated, loopback-only administration API with
bounded orchestration setup routes. Responses use `Cache-Control: no-store` and
contain opaque IDs, versions, digests, relative record paths already present in
project history, and sanitized summaries only.

## Read-only routes

- `GET /api/v1/projects/{project_id}/tasks` lists projected task graph
  revisions with stable cursor pagination.
- `GET /api/v1/projects/{project_id}/tasks/{task_id}/readiness` lists a
  bounded readiness projection for one task/graph revision.
- `GET /api/v1/orchestration/trades` and `/workers` list versioned registry
  inventory.
- `GET /api/v1/projects/{project_id}/telemetry` lists accepted telemetry
  summary metadata, never raw evidence.
- `GET /api/v1/orchestration/capabilities` reports whether runtime execution
  and workspace allocation are enabled. The shipped daemon reports both false.

## Authority-service routes

The API defines typed routes for idempotent task-graph creation, graph
validation, context preflight, runtime/node/workspace preflight, and execution
contract preview. Each route requires a daemon-owned setup service; the HTTP
layer does not construct a runtime, allocate a workspace, or publish canonical
records. If no authority service is configured, the route returns a sanitized
`503 unavailable` response rather than guessing or performing a partial
operation.

Every mutation includes an idempotency key. Preflights are read-only and safe
to repeat. Project identity in a request must equal the project path identity;
cross-project requests fail with `400` before the service is called.

## Privacy and operational limits

Request and response models reject unknown fields and bound scalar IDs,
digests, arrays, request bodies, response bodies, page limits, and cursors.
They do not contain local root paths, source/context content, artifact bytes,
worktree paths or handles, runtime sessions, credentials, prompts, terminal
output, secret values, or quarantine content.

The CLI exposes bounded read commands: `orchestration-tasks`,
`orchestration-readiness`, `orchestration-trades`, `orchestration-workers`,
`orchestration-telemetry`, and `orchestration-capabilities`. It also exposes
the typed authority-service requests: `orchestration-task-create`,
`orchestration-task-validate`, `orchestration-context-preflight`,
`orchestration-runtime-preflight`, and `orchestration-contract-preview`.
