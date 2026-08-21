# Orchestration Setup ADR Index

- Status: active index
- Started: 2026-08-16

This index identifies the decisions the orchestration setup sprint extends and
the decisions it must settle before enabling execution. It is an index, not a
substitute for the individual decision records.

## Accepted foundation

| Decision | Relevance to orchestration |
| --- | --- |
| [Phase 0: Project Skeleton](phase-0-project-skeleton.md) | Local-first Go service, SQLite ownership, and package boundaries |
| [Phase 1: Transfer Foundation](phase-1-transfer-foundation.md) | Resumable verified bytes and transport separation |
| [Phase 2: Sync Scanner](phase-2-sync-scanner.md) | One-way authority, revision planning, safe apply, and deletion guards |
| [Phase 3: Local Administration API](phase-3-local-admin-api.md) | Authenticated daemon-owned loopback control surface |
| [Phase 4: Agent Project Foundation](phase-4-agent-project-foundation.md) | Canonical portable work history, single authority, projections, privacy, and sync semantic blindness |

## Setup decisions

| Decision area | Required outcome | Status |
| --- | --- | --- |
| [Orchestration domain and authority](phase-5-orchestration-domain-authority.md) | Aggregate identities, ownership classes, dependency direction, untrusted result intake, and schema/mixed-version rules | Accepted in Slice 1; threat model updated |
| [Task graph and readiness](../architecture/task-graph-readiness.md) | Task/work-package relationship, immutable graph revision, deterministic reducer, barriers, retries, and cancellation | Implemented in Slice 2 |
| [Registry and context](../architecture/project-context-compiler.md) | Versioned trade/worker definitions and deterministic context-bundle provenance/privacy | Registry implemented in Slice 3; compiler implemented in Slice 4 |
| [Execution policy](../architecture/execution-contract-policy.md) | Immutable execution contract, permission intersection, budgets, risk, and quality gates | Implemented in Slice 5 |
| [Runtime, node, workspace, and intake](../architecture/runtime-node-workspace-result-intake.md) | Provider-neutral capability interfaces with production adapters disabled | Implemented in Slice 6 |
| [Telemetry, provenance, and memory inputs](../architecture/telemetry-provenance-memory-inputs.md) | Nullable bounded evidence, allowlisted summaries, deterministic candidates, and explicit disabled-feature gates | Implemented in Slice 7 |
| [Advanced-feature seams and evidence gates](../architecture/advanced-feature-seams.md) | Disabled similarity, retrospective, estimator, crew, dashboard, and conflict contracts with explicit fallbacks | Implemented in Slice 8 |
| [Orchestration setup administration API](../architecture/orchestration-setup-administration-api.md) | Authenticated bounded setup inventory, preflight, and authority-service routes without runtime start/allocation | Implemented in Slice 9 |
| [Setup compatibility, security, and release gate](../operations/orchestration-setup-recovery.md) | Recovery, feature-disable procedure, compatibility outcome, fuzz/race checks, and machine-checkable release gate | Implemented in Slice 10 |

Multi-writer project history, automatic conflict merging, learned routing,
embeddings, generated retrospectives, predictions, raw transcript capture, and
production remote execution are not implicit setup decisions. Each remains
deferred until its own evidence/security gate and, where authority semantics
change, a separate ADR.
