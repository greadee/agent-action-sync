# ADR: Browser result, budget, and incident views

## Status

Accepted for desktop browser control plane Slice 8.

## Decision

The assignment-detail projection may expose only verified decision metadata: integration-summary digests, test gate outcomes and evidence digests, review outcome, closed limitation codes, canonical acceptance audit evidence, latest bounded telemetry summaries, and observed token, cost, and tool-call counters. Missing, locally measured, or unverifiable telemetry remains explicitly `unknown` or `partial` with closed weak-evidence codes; it is never estimated.

Integration summaries are persisted authority-locally after evaluation so an operator can reopen the same bounded approval or rejection evidence after a desktop restart. The persisted record is capped at 64 KiB and is used only to revalidate the existing integration decision. The browser receives a smaller projection and never receives changed-file lists, result content, prompts, logs, runtime sessions, credentials, workspace identifiers, or paths.

Incident panels classify only stale leases, `needs_operator` runtime recovery, and an explicit allowlist of leaked-context, unsafe-output, and runaway-process failure codes. Every panel shows its closed evidence code plus fixed recovery actions. The only executable actions reuse the existing confirmed, idempotent assignment controls: cancel, mark failed, retry, or reassign. An expired lease exposes only `await_reconciliation`; the browser cannot override its fence.

## Consequences

- Operators can decide with durable result, test, review, usage, acceptance, and incident metadata without raw logs or filesystem exploration.
- A missing telemetry record is evidence of missing data, not zero usage.
- A stale lease remains fenced until local scheduler reconciliation; no browser action can revive or mutate it directly.
- New browser access is still confined to the existing assignment-detail GET and existing confirmed control POST routes.
