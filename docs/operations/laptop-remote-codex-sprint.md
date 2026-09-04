# Laptop-to-Home Codex Operations Sprint

- Status: active
- Started: 2026-09-01
- Goal: operate supervised Codex work on the home desktop from a laptop, with
  durable work tracking, live action visibility, and trustworthy session
  efficiency measurements.

## Model selection policy

Use the least expensive model that can safely complete each slice. GPT-5.6 Sol
is reserved for authority, security, and runtime-boundary work. GPT-5.6 Terra
is preferred for structured implementation and UI work once the authority
contracts are fixed. GPT-5.6 Luna is suitable for high-volume fixture and
mechanical follow-up work, but it is not the primary model for any slice that
changes execution authority.

| Slice | Outcome | Recommended model | Reasoning effort | Status |
| --- | --- | --- | --- | --- |
| 1. Task authority | Import immutable authority-local task specifications and publish/validate their portable task graphs through the production setup facade | `gpt-5.6-sol` | high | completed |
| 2. Dispatch authority | Build a SQLite/project-backed scheduler work source, registry bootstrap, context compilation, contract preview, and deterministic dispatch binding | `gpt-5.6-sol` | high | completed |
| 3. Hosted runtime loop | Establish isolated Codex authentication, bridge supervised runtime results into intake, configure exact test/review gates, and pass a disposable hosted pilot | `gpt-5.6-sol` | xhigh | pending |
| 4. Work telemetry | Record real queue/runtime/gate/review timestamps and persist nullable usage evidence into telemetry and portable accepted history | `gpt-5.6-terra` | high | pending |
| 5. Private laptop control | Keep the administration listener loopback-only while adding an authenticated private-tunnel bootstrap and narrowly scoped remote operator workflow | `gpt-5.6-sol` | high | pending |
| 6. Live action visualizer | Add a bounded resumable event stream, live DAG/attempt views, and an adapter for the agent-action visualizer event contract | `gpt-5.6-terra` | high | pending |
| 7. Efficiency and operations | Add session KPI views, always-on startup supervision, restart drills, current binaries, installer signing, and laptop/home runbooks | `gpt-5.6-terra` | medium | pending |

## Slice 1: task authority

### Scope

- Define a bounded, strict, versioned task-specification format.
- Store specifications immutably under the node-owned data root.
- Add a local CLI import operation that never accepts specification content in
  process arguments.
- Compose a production setup facade with the desktop orchestration lifecycle.
- Resolve the referenced project locally, publish through `workhistory`, and
  return only opaque record identities and digests.
- Validate specification identity, graph membership, dependencies, barriers,
  replay, conflict, and path/privacy boundaries.

### Acceptance gate

- A specification import is idempotent for equivalent canonical content and
  conflicts for changed content under the same ID.
- Task creation through the existing setup API produces canonical task, graph,
  work-package, and creation-event records and their SQLite projections.
- A repeated create request returns `already_present` without publishing new
  history.
- Cross-project, changed-digest, malformed, cyclic, oversized, duplicate-key,
  and symlink-backed inputs fail closed.
- Context/runtime/contract setup methods remain explicitly unavailable until
  Slice 2; they never return partial authority.
- Focused tests, the full suite, and the desktop release gate pass.

### Completed 2026-09-01

The node now imports strict, immutable task specifications through bounded
stdin, publishes and validates them through the production setup facade, and
projects the resulting portable task history. Replay, conflict, malformed
graph, duplicate-key, oversized, cross-project, and symlink-backed cases fail
closed. `tools\test.ps1` and
`tools\check_desktop_node_slice11_release.ps1 -SkipTests` pass.

## Slice 2: dispatch authority

Create dispatch requests only from accepted task projections and exact portable
records. Register one local Codex worker/node definition, compile an allowlisted
context, build an immutable contract, and expose production context/runtime and
contract preflight. The scheduler must start paused and must never dispatch a
project that is merely visible but not execution-authorized.

### Completed 2026-09-03

The production composition now bootstraps one deterministic local Codex trade
and worker, reconstructs work only from matching portable records and SQLite
projections, compiles the authority-selected context, persists an immutable
preview contract, and emits deterministic scheduler requests only after the
exact graph is approved. Runtime preflight verifies the stored contract, local
node, clean pinned Git workspace, required capabilities, and configured gates
without allocating a branch. The scheduler still starts paused, and changing
selection to a registered but execution-unauthorized project makes the work
source return no requests.

The contract binds a SHA-256 project-revision identity separately from the
exact Git base commit, so SHA-1 repositories remain supported without weakening
the execution-contract digest rules. The focused authority test, full repository
suite, and `tools\check_desktop_node_slice12_release.ps1 -SkipTests` pass.

## Slice 3: hosted runtime loop

Provide a supported authentication bootstrap for the isolated Codex home,
capture the adapter's fenced structured result automatically, run explicitly
authorized tests/review, and stop at human approval. Prove success, refusal,
timeout, restart uncertainty, cancellation, malformed output, and token/tool
budget exhaustion against a disposable repository.

## Slice 4: work telemetry

Write lifecycle events when they occur instead of reconstructing them during
integration evaluation. Persist provider-reported and locally measured usage
with nullable evidence, then derive queue time, active runtime, gate time,
review time, human wait, tokens, tool calls, retries, and accepted-work rates.

## Slice 5: private laptop control

Retain the loopback browser security model. Add a private authenticated tunnel
workflow and remote one-use browser-session bootstrap. Any later native paired
command protocol requires separate expiring grants, exact action allowlists,
replay protection, confirmation, revocation, and audit evidence.

## Slice 6: live action visualizer

Journal sanitized scheduler, runtime-progress, gate, decision, and incident
events with monotonic sequence numbers. Stream them over the existing exact
origin with resume cursors, bounded retention, and backpressure. Never stream
prompts, reasoning, terminal output, credentials, paths, or artifact content.

## Slice 7: efficiency and operations

Render per-session and project KPI views with completeness labels and explicit
unknowns. Add foreground-to-startup supervision, health checks, recovery
drills, current build artifacts, installer signing, and a two-machine operator
runbook.

## Definition of done

From the laptop, an owner can establish a private session, submit an immutable
task specification, approve and start its graph, watch bounded action events in
real time, inspect trustworthy usage/timing evidence, decide integration, and
recover the home node after restart without replaying uncertain work.
