# Runtime, Compute Node, Workspace, and Result Intake Contracts

## Status and release boundary

Setup Slice 6 defined provider-neutral contracts and deterministic fakes.
Phase 1 Slice 4 added an explicitly composed local Git worktree manager. Phase
1 Slice 5 adds an opt-in supervised Codex CLI adapter, but the daemon does not
compose or enable it yet. Remote nodes, automatic merge, and canonical result
publication remain disabled.

## Runtime lifecycle

`internal/runtimecontract.Adapter` defines `Negotiate`, `Prepare`, `Start`,
`Observe`, `Pause`, `Resume`, `Cancel`, `CollectResult`, and `Close`.

- Prepare receives an already verified immutable execution contract and an
  opaque `workspace:` ID.
- Runtime and node ID/version/digest must exactly match the contract.
- Negotiation requires every requested capability to be present in both the
  contract grant and adapter declaration. Missing capability fails closed.
- Prepare, lifecycle actions, and collection use digest-only idempotency and
  resumability keys. Reuse for different authority conflicts.
- Status and errors use a closed provider-independent vocabulary. Sanitized
  errors contain no provider session, credential handle, local path, raw
  terminal output, or provider response.
- Collected results remain only references to an immutable result envelope.
  They are not canonical work state.

The deterministic fake implements the complete lifecycle and a test-only
completion seam. It never invokes a process or transport.

The concrete adapter's enforcement, credential, privacy, cancellation, and
restart boundary is specified in [Supervised Codex Runtime
Adapter](supervised-codex-runtime-adapter.md).

## Compute-node definition and observation

`internal/computenode` separates a stable, hashed node definition from bounded
volatile observations. Definitions contain node identity/version, lifecycle,
OS/architecture, declared capacity, exact tool digests, repository access,
available runtime definitions, and an active-lease ceiling. They contain no
credentials or live handles.

Eligibility intersects the durable definition, current unexpired healthy
observation, and work requirements. Capacity greater than the declaration,
undeclared tools/runtimes, stale or mismatched observations, unavailable
repository access, and lease exhaustion all reject eligibility with stable
reason codes. Unknown health is unavailable, not healthy.

The deterministic node fake enforces the lease ceiling and owner/generation
fencing. Idempotent acquisition and renewal replay the original lease; a stale
owner or generation cannot renew or release it. These leases are scheduler-test
reservations, not portable records or canonical execution state.

## Opaque workspace contract

`internal/workspace` provides read-only local preflight and a deterministic
allocation fake. Preflight rejects:

- relative, missing, non-directory, symlink, or reparse-like roots;
- a repository outside the project sync root;
- a worktree base inside or containing synchronized project/repository data;
- dirty repository state, unsafe Git branch names, missing or non-directory
  `.git` metadata, target collisions, and insufficient observed free space.

The preflight result and workspace object contain only the opaque workspace ID,
owner, branch, generation, state, and stable check codes. Absolute repository
and worktree paths remain authority-local inputs and are never returned.
Cleanup requires the exact assignment owner and generation. The deterministic
fake remains available. The Phase 1 Git manager provisions only an external,
pinned-base worktree and quarantines dirty or missing state; see
[Safe Git worktree provisioning](safe-git-worktree-provisioning.md).

Result-path validation applies the execution contract write scope again and
always rejects `.git` and traversal paths. Portable project scanning continues
to exclude `.git`; workspace code does not weaken that rule.

## Immutable result envelope and authority intake

`internal/resultintake` defines `syncgate.result-envelope.v1`. The strict JSON
decoder rejects unknown fields and envelopes larger than 1 MiB. The canonical
SHA-256 envelope binds:

- result and idempotency identities;
- project, task/revision, graph revision, work package, and execution;
- exact contract and assignment references;
- worker, runtime, node, and opaque workspace claims;
- exact trade, instruction, context, provider, and model provenance;
- bounded patch, artifact, handoff, test-evidence, and telemetry references;
- claimed outcome and UTC creation time.

Only IDs, digests, sizes, closed outcomes, and bounded reference names are
allowed. Patch bytes, artifact bytes, prompts, terminal output, tool
transcripts, environment values, secret values, credentials, local paths, and
provider sessions are not envelope fields.

The authority service loads and verifies the stored execution contract, then
compares every authority and provenance binding plus the expected assignment
and workspace. A structurally valid forged result is retained only as a local
sanitized rejection decision. A valid envelope is `accepted` for intake as an
untrusted candidate; `CanonicalPublished` remains false. This slice does not
call `workhistory` or write any portable event, handoff, artifact, review, or
acceptance record.

Migration 12 stores immutable envelope bytes and the authority decision.
Same-result/same-digest replay returns the first decision; same result ID with a
different digest conflicts and cannot publish anything.

## Upload-only seam

`UploadOnlyReceiver` models a future return share without importing sync or
transport. Its deterministic fake verifies and stores only envelope identity
and digest. Every receipt has `CanonicalAuthority=false`; receiving bytes does
not run intake or grant project-history publication rights.

## Dependency and enablement rules

- Runtime, node, workspace, and result-intake packages may depend on execution
  contracts and storage interfaces where necessary.
- They do not import `sync` or transport implementations.
- `project` and `sync` do not import or invoke these packages.
- The supervised Codex adapter passes its isolated capability, credential,
  cancellation, workspace-containment, restart, privacy, and result-conformance
  tests. The Phase 1 scheduler lifecycle now provides an explicit daemon seam,
  but shipped CLI/config composition remains disabled until the local
  enablement and operator controls land.
