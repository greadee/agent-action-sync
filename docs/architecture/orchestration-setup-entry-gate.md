# Orchestration Setup Entry Gate

- Status: Slice 0 verified on 2026-08-16
- Baseline commit: `10addf1a33601282c5ab6de731891ea473038e5c`
- Baseline branch: `main` at `origin/main`, ahead 0 and behind 0
- Scope: inventory and extension rules before orchestration domain changes

## Baseline boundary

Commit `10addf1` is the separately committed and pushed Stage 10 baseline. It
contains Agent Project administration, safe existing-share migration, and the
two-daemon release gate. The worktree was clean before this setup slice began.
No orchestration table, runtime adapter, task scheduler, lease, worker registry,
or workspace allocator exists in that baseline.

The ignored `.local-planning/` material is planning input, not a product
contract or source of truth. Tracked ADRs, protocol documents, Go contracts,
portable project records, and their deterministic projections define the
implemented boundary.

## Implementation inventory and extension map

| Orchestration concept | Implemented foundation | Ownership today | Setup extension rule |
| --- | --- | --- | --- |
| Project identity and authority | `ProjectManifest`, `Authority`, project registration, migration confirmation | Portable canonical record plus rebuildable/local registration | Keep the single configured authority as the only canonical publisher |
| Task and graph | `WorkPackageDefinition.Dependencies`, scope, deliverables, acceptance criteria, review flag | Portable canonical work-package definition | Add a task aggregate, graph revision, membership, and deterministic DAG validation; do not replace work packages |
| Work lifecycle | `WORK_PACKAGE_*`, `REVIEW_RECORDED`, and `WORK_ACCEPTED` events through `workhistory.Service` | Portable canonical events projected into SQLite | Extend these events and their reducer; do not add parallel agent-task state events |
| Execution attempts | `ExecutionManifest` and `EXECUTION_*` events | Portable canonical manifest/events | Bind future assignments and result intake to these execution identities |
| Worker/trade/provider/model | `Producer` and `Provenance` labels on records and projections | Values are portable when attached to accepted records; no registry exists | Introduce versioned registry identities, then resolve them into existing provenance fields |
| Context and instructions | `ContextVersion` and `InstructionVersion` provenance fields | Portable digest/version references only | Compile local deterministic bundles and record their digest; do not publish caches as truth |
| Handoffs and artifacts | Typed `Handoff`, `ArtifactManifest`, immutable optional blobs | Portable canonical records/blobs | Reuse these paths for accepted runtime output and keep result envelopes untrusted until authority intake |
| Projection and recovery | `projector.Projector`, checkpoints, pending references, rejections, clean rebuild | Rebuildable SQLite and local quarantine | Add projections only where they can be reproduced from canonical records or explicitly classified local state |
| Descriptive evidence | Versioned `insights` registry over accepted events | Rebuildable SQLite | Extend only from observed nullable telemetry; no inferred scores or predictions |
| Administration | Authenticated loopback `/api/v1`, daemon-owned services, bounded queries and rebuild/migration | Authority-local control surface | Add setup inspection/preflight operations without starting a runtime or returning local roots |
| Sync and transport | Share capabilities, paired mutual TLS, one-way byte/revision movement | Receiver-authorized transport and sync state | Never call runtime execution from sync/transport and never interpret project semantics there |
| Runtime, node, workspace, lease | Not implemented; automatic peer job execution is explicitly disabled without trusted transport | No orchestration owner yet | Define separate capability-scoped interfaces and authority-local state before enabling an adapter |

## Existing identifiers and canonical vocabulary

Portable records use schema family `syncgate.agent-project`, major 1, minor 0.
Every record has `record_id` and `project_id`; typed identities already include
`work_package_id`, `execution_id`, `handoff_id`, and `artifact_id`. Authority
uses `device_id` and `share_id`. Provenance already carries worker, trade,
specialization, provider, model/model version, source artifact, project,
context, and instruction references. New setup identifiers must be namespaced
and versioned while preserving these references.

The canonical event vocabulary to extend is:

- `PROJECT_REGISTERED`
- `WORK_PACKAGE_CREATED` and `WORK_PACKAGE_STATE_CHANGED`
- `EXECUTION_STARTED`, `EXECUTION_PAUSED`, `EXECUTION_FAILED`, and
  `EXECUTION_COMPLETED`
- `TEST_RECORDED`, `HANDOFF_CREATED`, `REVIEW_RECORDED`,
  `ARTIFACT_RECORDED`, and `WORK_ACCEPTED`

Work-package states are `planned`, `ready`, `in_progress`, `blocked`, `review`,
`accepted`, `failed`, and `canceled`. The supported transitions are:

- `planned -> ready | canceled`
- `ready -> in_progress | canceled`
- `in_progress -> blocked | review | failed | canceled`
- `blocked -> in_progress | failed | canceled`
- `review -> in_progress | failed`
- `failed -> ready | canceled`
- `review -> accepted` only after an approved review

Execution states are `pending`, `running`, `paused`, `failed`, and `completed`.
The recording service creates an execution as running, permits
`running -> paused | failed | completed`, and permits
`paused -> running | failed`. Failed and completed executions are terminal.

Transfer IDs, revision IDs, one-way job states, and share capabilities remain
sync/transport vocabulary. They may be correlated with work history but must
not become orchestration task or execution identities.

## Deferred-feature disposition

| Deferred feature | Setup disposition | Earliest enablement boundary |
| --- | --- | --- |
| Task DAG scheduling | Define, validate, and project readiness without executing | Supervised single-authority scheduler with durable leases |
| Project Context Compiler | Deterministic versioned compiler and local cache | Bind one digest to an execution; learned retrieval waits for evidence |
| Trade registry and worker routing | Versioned registries and exact capability inputs | Explicit deterministic selection before learned recommendations |
| Agent execution and remote commands | Capability-scoped interfaces and disabled adapters | One supervised opt-in local adapter; remote execution needs separate approval |
| Git worktrees and merge orchestration | Safe workspace/integration contracts | Isolated allocation with human integration approval |
| Multi-writer/two-way history | Preserve single authority and define result intake | Separate protocol, authority, causality, and conflict ADR |
| Automatic conflict merging | Result/conflict envelope semantics only | Separate merge engine with review and rollback gates |
| Embeddings/historical similarity | Optional disabled index interface with privacy/version metadata | Representative approved history and evaluated retrieval quality |
| LLM retrospectives | Non-authoritative generated-artifact interface only | Provenance, approval, and usefulness/privacy evaluations |
| Predictive cost/performance/crew advice | Nullable telemetry and disabled estimator contracts | Calibrated minimum samples and error/confidence gates |
| Dashboard/UI | Stable bounded read models and control operations | After control-plane behavior stabilizes |
| Raw prompt/tool/terminal capture | Privacy classes and retention policy; capture remains off | Explicit consent, encryption, redaction, access, and deletion release |
| LAN discovery, relay/coordinator, browser/mobile access | Separate transport/product tracks | Their own threat model and release gates |
| General two-way file sync | Separate sync track | Conflict and recovery design independent of orchestration |

## Entry-gate checklist

| Gate | Command or evidence | Slice 0 result |
| --- | --- | --- |
| Stage 10 isolated and pushed | `git status --porcelain=v2 --branch`; compare `HEAD` and `@{upstream}` | Pass: clean baseline, `+0 -0`, both at `10addf1` before setup edits |
| Formatting and full suite | `powershell -ExecutionPolicy Bypass -File tools/test.ps1` | Pass on Go 1.25.12 repository toolchain |
| Static analysis | `go vet ./...` with repository-local caches | Pass on Go 1.26.3 Windows/amd64 |
| Portable/API contracts | `go test ./internal/project ./internal/api -run 'Contract|Canonical|OpenAPI|Route' -count=1` | Pass |
| Dependency boundary | Inspect direct imports for `internal/project` and `internal/sync` | Pass: project imports only the standard library, its Windows publication primitive, and filesystem safety; sync does not import project, API, daemon, or SQLite |
| Focused race detector | `go test -race ./internal/sync ./internal/transfer ./internal/filesystem` | Local limitation: unavailable because `CGO_ENABLED=0`; command fails before tests with `-race requires cgo` |
| CI replacement for local race gap | Linux CI focused race step | Added to the required workflow; another C-enabled host remains an equivalent local option |

The local race limitation is a toolchain limitation, not a passing race result.
No later slice may describe the race gate as passed without evidence from the
Linux CI job or another C-enabled host.

## Setup sprint release gate

Slice 10 completes the setup-only release gate. Run:

```powershell
powershell -ExecutionPolicy Bypass -File tools\check_orchestration_setup_release.ps1
```

The command is the machine-checkable entry condition for any follow-on
execution sprint. It verifies the complete test/vet/boundary suite, short fuzz
coverage for all untrusted setup input families, recovery after compilation
and intake restart, and the focused race suite where CGO is available. It does
not mark a CGO-disabled host as race-clean. The companion recovery procedure
documents projection rebuild, cache regeneration, immutable intake replay,
mixed-version handling, and the operational feature-disable state.

The following statements are release invariants, not optional configuration:

- the shipped daemon exposes no production runtime or remote execution;
- capability inventory reports runtime execution and workspace allocation as
  false;
- unavailable setup authority operations fail with `503 unavailable` rather
  than a partial side effect or fabricated preflight output;
- generated/advanced seams remain disabled until their evidence gates pass;
- sync and transport remain semantic-blind byte movement.

## Slice 1 entry rule

Architecture and threat-model ownership decisions come next. No schema,
handler, runtime, or scheduler implementation should precede the authority and
data-classification ADR. In particular, portable truth, authority-local durable
control state, rebuildable projections, ephemeral workspace state, and
optional generated artifacts must be classified before a new field ships.
