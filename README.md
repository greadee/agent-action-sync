# Private Sync Gate

Private Sync Gate is a cross-platform, personal file transfer and one-way synchronization agent for trusted computers. The product goal is to let an owner coordinate shared workspaces and move files between those machines without turning a home computer into an open public file server.

The repository now contains the trusted transfer and one-way synchronization
foundation, a foreground daemon, production pairing and mutual TLS, an
authenticated loopback administration API, and the single-authority Agent
Project history and projection foundation. It also contains the Windows desktop
node packaging and first-run lifecycle foundation. It is still an active
development project. Remote administration, discovery, relay/coordinator
services, browser access, and multi-writer synchronization remain disabled or
deferred.

## Current Status

The current implementation provides:

- Clean package boundaries for sync, transfer, storage, transport, and identity.
- A dependency-free JSON config loader with loopback-only local API validation.
- Device identity helpers for Ed25519 keys, fingerprints, and pairing codes.
- Initial share path normalization and containment helpers.
- Fixed-size chunk planning for resumable transfers.
- Safe receive-side partial file writing with hash verification before commit.
- Folder scan manifests for future synchronization, including default ignores for history and partial files.
- Initial Go interfaces for transport and storage.
- Versioned SQLite migration definitions for local agent state.
- Revision and transfer state models.
- Initial SQLite schema.
- Threat model and trust boundaries.
- Mermaid architecture diagrams.
- ADRs for initial technology decisions.
- Manual send-once and receive-once commands over development TCP/TLS.
- One-way folder scanning, revision manifests, reconciliation, deletion guards, and safe receiver-side apply.
- Persistent synchronization jobs, retries, watcher reconciliation, scheduler safety, and per-share diagnostics.
- A foreground daemon with production Windows credential storage, explicit pairing and revocation, and paired mutual-TLS transport.
- An authenticated, loopback-only administration API with bounded inventory, job control, pairing, project migration, history, artifact, insight, rejection, and rebuild operations.
- Single-authority Agent Project manifests, immutable task DAG/work-package and execution history, deterministic readiness and context compilation, local trade/worker registry, immutable execution contracts, provider-neutral runtime/node/workspace contracts, an opt-in bounded DAG scheduler with deterministic fakes, authority-owned result/test/review/human-integration gates, handoffs, artifacts, SQLite projection/rebuild, and descriptive insights.
- Integration, fuzz, and package-level tests for the transfer and synchronization paths.
- A per-user Windows installer definition, signed-release build gate, embedded
  version manifest, separated config/data/log/cache/worktree roots, idempotent
  first-run initialization, downgrade compatibility gate, and foreground node
  health command.
- Staged and confirmation-bound desktop settings/share registration, OS-only
  provider credential lifecycle, disposable-project execution opt-in, and a
  sanitized diagnostics export containing only status, closed codes, counts,
  and opaque IDs.

## MVP Boundary

The current MVP boundary is local, owner-controlled transfer and one-way synchronization between trusted personal computers:

- Manual device pairing.
- Manual IP connection.
- Send one file or folder.
- Fixed-size resumable chunks.
- Per-chunk and whole-file verification.
- Temporary destination writes and atomic commit.
- Transfer history in SQLite.
- Path traversal protection.
- Receiver-authoritative writes with temporary files, verification, and atomic commit.
- Explicit one-way change permissions and guarded deletions.

Later phases add the remaining supervised-execution controls, LAN discovery,
browser portal access, coordinator/relay services, direct remote connectivity,
and separately designed multi-writer synchronization. File sync does not start
agent runtimes or provide arbitrary remote shell access.

## Development

Validate the current scaffold with:

```powershell
tools\test.ps1
```

Validate the Windows desktop-node release lifecycle with:

```powershell
tools\check_desktop_node_slice1_release.ps1
tools\check_desktop_node_slice2_release.ps1
tools\check_desktop_node_slice3_release.ps1
```

Initialize, run, and check an installed per-user node with:

```powershell
syncgate node-init
syncgate node-run
syncgate node-health
```

Execution remains disabled by default. Inspect the bounded local surfaces with:

```powershell
syncgate node-settings-show
syncgate node-identity-status
syncgate node-execution-status
syncgate node-resources
```

Manual local send-once smoke path:

```powershell
syncgate receive-once --listen 127.0.0.1:47821 --share-root C:\SyncGate\Drop
syncgate send-once --addr 127.0.0.1:47821 --file C:\path\file.bin --relative-path file.bin
```

Useful docs:

- [Architecture overview](docs/architecture/overview.md)
- [Phase 0 project skeleton decisions](docs/adr/phase-0-project-skeleton.md)
- [Phase 1 transfer foundation decisions](docs/adr/phase-1-transfer-foundation.md)
- [Phase 2 sync scanner decisions](docs/adr/phase-2-sync-scanner.md)
- [Phase 3 local administration API decision](docs/adr/phase-3-local-admin-api.md)
- [Phase 4 Agent Project Sync foundation decision](docs/adr/phase-4-agent-project-foundation.md)
- [Local administration API contract](docs/protocol/local-admin-api-openapi.json)
- [Agent Project portable records v1](docs/protocol/agent-project-records-v1.md)
- [Agent Project domain and filesystem safety](docs/architecture/agent-project-domain.md)
- [Agent Project ingestion and recovery](docs/architecture/project-ingestion.md)
- [Work-history recording service](docs/architecture/work-history-recording.md)
- [Deterministic work insights](docs/architecture/work-insights.md)
- [Agent Project administration API](docs/architecture/project-administration-api.md)
- [Agent Project migration and recovery](docs/operations/agent-project-migration.md)
- [Orchestration setup entry gate](docs/architecture/orchestration-setup-entry-gate.md)
- [Orchestration ADR index](docs/adr/orchestration-setup-index.md)
- [Orchestration domain and authority decision](docs/adr/phase-5-orchestration-domain-authority.md)
- [Desktop runtime composition decision](docs/adr/phase-9-desktop-runtime-composition.md)
- [Desktop runtime operation and recovery](docs/operations/desktop-runtime-recovery.md)
- [Task graph validation and readiness](docs/architecture/task-graph-readiness.md)
- [Trade and worker registry](docs/architecture/trade-worker-registry.md)
- [Project Context Compiler v1](docs/architecture/project-context-compiler.md)
- [Execution contract and permission policy](docs/architecture/execution-contract-policy.md)
- [Runtime, compute node, workspace, and result intake](docs/architecture/runtime-node-workspace-result-intake.md)
- [Telemetry, provenance, and memory inputs](docs/architecture/telemetry-provenance-memory-inputs.md)
- [Advanced-feature seams and evidence gates](docs/architecture/advanced-feature-seams.md)
- [Orchestration setup administration API](docs/architecture/orchestration-setup-administration-api.md)
- [Orchestration operator control facade](docs/architecture/orchestration-setup-administration-api.md#operator-control-facade)
- [Orchestration setup recovery and release gate](docs/operations/orchestration-setup-recovery.md)
- [Orchestration pilot and recovery matrix](docs/operations/orchestration-pilot.md)
- [Phase 1 orchestration operations and release gate](docs/operations/orchestration-phase1-operations.md)
- [Desktop node packaging and lifecycle decision](docs/adr/phase-7-desktop-node-foundation.md)
- [Desktop node installation and recovery](docs/operations/desktop-node-installation.md)
- [Desktop version manifest v1](docs/protocol/desktop-version-manifest-v1.schema.json)
- [Desktop settings, credentials, and execution opt-in decision](docs/adr/phase-8-desktop-settings-credentials.md)
- [Desktop configuration and credential lifecycle](docs/operations/desktop-node-configuration.md)
- [Sanitized desktop diagnostics v1](docs/protocol/desktop-diagnostics-v1.schema.json)
- [Orchestration control, fencing, and recovery](docs/architecture/orchestration-control-recovery.md)
- [Deterministic dispatch selection](docs/architecture/deterministic-dispatch-selection.md)
- [Context, contract, and attempt binding](docs/architecture/context-contract-attempt-binding.md)
- [Safe Git worktree provisioning](docs/architecture/safe-git-worktree-provisioning.md)
- [Supervised Codex runtime adapter](docs/architecture/supervised-codex-runtime-adapter.md)
- [DAG scheduler and bounded execution](docs/architecture/dag-scheduler-bounded-execution.md)
- [Result, review, and human integration gate](docs/architecture/result-review-integration-gate.md)
- [Testing](docs/architecture/testing.md)
- [Threat model](docs/threat-model/initial-threat-model.md)
- [Database schema](docs/architecture/database-schema.md)
- [Protocol outline](docs/protocol/transfer-protocol.md)
- [Trusted one-way sync runbook](docs/operations/trusted-one-way-sync-runbook.md)

## Security Position

This project should not be described as end-to-end encrypted until the implemented key exchange, mutual authentication, and transfer protocol have been reviewed and tested. Relay and coordinator designs must not require plaintext file contents or folder inventories.
