# Phase 1 Orchestration Operations

This runbook is the operational boundary for the single-authority,
single-node Phase 1 implementation. It does not enable a hosted runtime,
automatic merge, deployment, remote command, multi-writer history, or learned
routing.

## Current enablement state

The shipped daemon is safe by default. Runtime execution, workspace allocation,
task graph publication, scheduler control, and the orchestration operator facade
are unavailable unless an embedding application supplies the authority-owned
services through `daemon.Options`. There is deliberately no CLI or config flag
that turns on `codexruntime`, a scheduler, or an orchestration facade.

An implementation proposing enablement must be reviewed as a separate change.
It must construct an explicitly enabled supervised adapter, a scheduler, and a
durable authority facade; register a local node; and prove the pilot on a
disposable repository. To disable a composed implementation, stop the daemon,
remove those supplied services, restart, and verify that the capability endpoint
reports execution and workspace allocation disabled. Disabling scheduling must
preserve assignment history and inspectable worktrees.

## Definitions, task approval, and dispatch

Register trades, workers, and nodes through the local authority registry only.
Definitions are immutable versioned ID/digest records and must not contain
credentials, API keys, endpoints, provider sessions, prompts, or raw runtime
settings. A worker binds an exact trade, instruction, runtime, provider/model,
and policy reference; it is not a credential container or a performance rank.

Before dispatch, inspect the typed setup/orchestration API or CLI:

1. Publish and validate the versioned task graph through the authority service.
2. Approve the exact task and graph revisions with its digest and an idempotency
   key.
3. Run context/runtime preflight and preview the effective execution contract.
4. Preview dispatch. Treat a blocked reason as an operator action item; do not
   edit control tables or invent a replacement worker.
5. Start the scheduler only through the composed authority service. The Phase 1
   ceiling is two active local workers.

The operator API exposes sanitized IDs, reason codes, budgets, attempts, gates,
and result summaries. It never returns prompts, context bytes, credentials,
absolute paths, PIDs, raw logs, artifact bytes, quarantine locations, or
provider sessions.

## Workspaces and control actions

Each attempt owns an isolated worktree and a pinned branch derived from its
base commit. Inspect a failed or canceled attempt in place using its opaque
workspace/assignment identity and its persisted relative change manifest. Do
not use `git reset`, `git clean`, force checkout, or filesystem deletion to
"fix" it. Cleanup is a separate ownership-checked action after inspection; no
normal scheduler, retry, cancel, disable, or shutdown path deletes a worktree.

Pause prevents new dispatch and requests a safe runtime pause where supported.
Resume only continues a bound paused attempt. Cancel is idempotent and fences
the attempt. Retry creates a new monotonically numbered attempt with a new
idempotency digest; it never reopens a terminal attempt. Reassign requires a
fresh deterministic selection and immutable contract binding. Every action
must retain its audit reason and stable operation result.

Runtime success only reaches `collecting`. The integration gate validates the
fenced result, verifies manifests and exact machine tests, performs a
non-mutating merge preview, requires review when the contract does, and asks
for an explicit approve/reject decision. Approval publishes canonical history;
rejection, test failure, review rejection, stale base, conflict, unexpected
binary, forged output, or scope violation fails local control. Phase 1 never
auto-merges or deploys.

## Budgets, permissions, and recovery

The effective contract is the intersection of project, runtime, task, trade,
user, work-package, and worker policy. Its token, cost, wall-clock, retry,
tool-call, and concurrency budgets are ceilings, not targets. Missing provider
usage remains unknown. Budget exhaustion, denied capability, unavailable
runtime/node, or failed quality gate is a stable block/failure condition; do
not loosen a contract after an attempt starts.

On daemon restart, reconciliation observes durable assignment, lease, runtime,
and workspace state before any dispatch. A `needs_operator` or uncertain
runtime is never replayed automatically. Preserve its workspace and local
control evidence, inspect the closed reason code, then cancel or issue an
audited retry only after confirming the original run cannot complete. An
expired lease with resources likewise needs operator review.

For a context or secret leak, unsafe output, runaway process, or stale lease:

1. Stop the composed scheduler/daemon and preserve opaque IDs and sanitized
   audit evidence.
2. Cancel or fence the affected attempt; revoke its local adapter composition
   and any trusted replica pairing if its access is in doubt.
3. Do not copy raw prompt, output, credential, or worktree material into
   portable history or an issue tracker.
4. Rebuild canonical projections from validated portable records only after
   containment. Local control state and quarantined material are not recreated
   by that rebuild.
5. Re-enable only after a fresh contract/preflight and the deterministic pilot
   evidence are reviewed.

## Compatibility and release evidence

Portable records are versioned, strict, and append-only. An older target may
transport bytes it understands but must not project unsupported semantics into
readiness or execution authority. Downgrade failures are fail-closed: retain
the original portable bytes, upgrade the target, and rebuild. A trusted replica
may validate and project accepted history and insights, but it cannot schedule,
start, cancel, resume, collect, accept, or merge work.

Run the release wrapper before a Phase 1 release:

```powershell
powershell -ExecutionPolicy Bypass -File tools\check_orchestration_phase1_release.ps1
```

It runs formatting, the full suite, vet, architecture/API privacy checks, fuzz
smoke tests, the deterministic pilot, and focused race tests when the local Go
toolchain supports them. A skipped race or hosted-runtime pilot is not green
external evidence; obtain that evidence from CI/a C-enabled host and an
explicitly approved disposable hosted-runtime pilot respectively.
