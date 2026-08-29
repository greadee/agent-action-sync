# ADR: Explicit browser operator controls

## Status

Accepted for desktop browser control plane Slice 7.

## Decision

The embedded loopback browser may invoke only task-graph approval, dispatch preview, scheduler start/disable, assignment pause/resume/cancel/retry/evaluate/reassign, and integration approve/reject. Browser-session authentication remains restricted to an exact method-and-path allowlist. Every allowed POST requires an exact loopback origin and the current session CSRF token; project selection and policy changes remain bearer-only.

The UI uses a native modal confirmation flow for state-changing controls. Before submission it shows only bounded project authority, task/graph revision and digest, scheduler/concurrency policy, assignment state, observed-budget completeness, gate states, result digest, and an opaque idempotency key. Cancel, Escape, focus handling, disabled states, and conflict messages follow native keyboard paths. Dispatch preview is displayed separately before task approval or scheduler start.

Authority-local control results are recorded in a durable SQLite replay ledger keyed by the caller's idempotency key and a command fingerprint. Repeating the same command returns the prior sanitized result with `already_present`; reusing the key for a different command returns a stable `409 state_conflict`. Assignment transitions retain their fenced orchestration operation and audit records. Reassignment creates a new attempt only from a retryable terminal state, requires a different active worker, and records `reassign` plus `operator_reassign` in the assignment audit timeline.

## Rationale

Explicit confirmation is useful only if the backend and browser agree on replay and conflict semantics. Persisting bounded result envelopes prevents a page refresh or node restart from turning a repeated confirmation into an ambiguous second action. Exact route allowlisting keeps the browser from inheriting unrelated bearer-authorized administration powers.

## Consequences

- Disabling scheduling pauses authority without deleting assignments, attempts, worktrees, audit history, or replay evidence.
- The UI never retries a failed control automatically. It retains the same key in the open dialog and displays the closed error code and request ID.
- The ledger stores sanitized API results only and is capped at 64 KiB per operation; it cannot store paths, content, prompts, credentials, runtime sessions, logs, or artifact bytes.
- Result/budget incident analysis beyond the confirmation summary remains Slice 8 work.
