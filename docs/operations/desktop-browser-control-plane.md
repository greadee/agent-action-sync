# Desktop browser control plane

SyncGate serves a bounded control-plane shell from the desktop node's configured loopback listener. Start the node, then ask the authenticated CLI for a one-time URL:

```text
syncgate node-ui-session --config <node-root>/config/config.json
```

Open the returned URL within two minutes. Its fragment contains a single-use bootstrap token; URL fragments are not sent in HTTP requests. The shell removes the fragment immediately, exchanges the token from the same origin, and receives an opaque `HttpOnly`, `SameSite=Strict` cookie. The long-lived administration bearer is never placed in the URL, DOM, JavaScript-readable storage, configuration, or SQLite.

The initial session document is deliberately narrow. It contains process health, the sanitized node status response, an expiry, a rotating CSRF token, and only these capability identifiers:

- `node.health.read`
- `node.status.read`
- `project.visibility.read`
- `task.readiness.read`
- `assignment.visibility.read`
- `result-budget-incident.read`
- `worker-node.inventory.read`
- `operator.controls.write`

These visibility capabilities disclose bounded, sanitized records and closed reason/evidence identifiers only. They do not expose source, prompt content, credentials, provider sessions, shell output, absolute paths, artifact bytes, or arbitrary discovery routes. Closing the node invalidates all in-memory bootstrap and browser-session state.

The cookie is restricted to an explicit method-and-path allowlist. Reads cover node status, local-project pages, task/readiness pages, assignment pages/details, worker inventory, and node inventory. POST controls cover only task approval, dispatch preview, scheduler start/disable, assignment controls, and integration decisions. Project selection, project policy, unrelated inventory, and every other mutation remain bearer-only.

## Operator controls

State-changing buttons open a native confirmation dialog. Review the selected project authority, scheduler and concurrency state, task/graph digest, dispatch preview, assignment state, budget completeness, gate summary, and result digest before submitting. Reassignment additionally requires a different active worker. Canceling the dialog performs no request.

Each confirmation creates one opaque idempotency key and retains it while the dialog stays open. The node stores the sanitized result in its authority-local replay ledger. A repeated submission of the same command reports a safe replay; key reuse with different inputs reports `409 state_conflict`. The shell does not retry conflicts or unavailable operations automatically and displays the response code plus request ID. Disabling the scheduler pauses new dispatch without deleting assignments, history, worktrees, or audit evidence.

## Decision evidence and incidents

Selecting an assignment shows only its bounded integration summary, test-gate outcomes, review outcome, acceptance audit event, observed token/cost/tool counters, and recent allowlisted telemetry summaries. Missing or weak telemetry is labeled `unknown` or `partial` with closed warning codes; it is never displayed as zero or extrapolated. The browser has no endpoint for result bytes, changed-file lists, shell output, runtime sessions, prompts, credentials, logs, or paths.

Incident panels classify stale leases, uncertain runtime recovery, leaked-context reports, unsafe output, and runaway processes from closed local state and failure codes. Buttons reuse the confirmed cancel, mark-failed, retry, or reassign controls. An expired lease permits only local scheduler reconciliation, because the browser cannot override a stale fence.

## Security boundary

- The HTTP listener accepts only `127.0.0.1`, `localhost`, or `::1` bindings.
- Host validation rejects non-loopback authorities and rebinding-shaped values.
- Browser bootstrap and unsafe cookie-authenticated API calls require an exact HTTP `Origin` match to the request host. Unsafe calls also require the current `X-SyncGate-CSRF` value.
- Bearer-authenticated requests continue to reject every `Origin` header.
- The server emits no CORS allow headers and the shell uses a restrictive Content Security Policy, no caching, frame denial, MIME sniffing denial, and same-origin resource isolation.
- Bootstrap tokens live for two minutes and are single-use. Browser sessions live for eight hours, are bounded in count, and exist only in memory as token digests.

A direct visit to `/ui/` without a current cookie displays a protected-session message. Run `node-ui-session` again to establish a new session. A `401` indicates a missing, expired, or invalid session; a `403` indicates an origin, host, or CSRF boundary failure.
