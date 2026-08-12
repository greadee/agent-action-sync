# Private Sync Gate

Private Sync Gate is a cross-platform, personal file transfer and one-way synchronization agent for trusted computers. The product goal is to let an owner run agents across multiple machines, coordinate shared workspaces, and move files between those machines without turning a home computer into an open public file server.

The repository now contains the local transfer and one-way synchronization foundation. It is still an active development project: daemon lifecycle management, production pairing and mutual authentication, and a stable external API remain on the roadmap.

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
- Integration, fuzz, and package-level tests for the transfer and synchronization paths.

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

Later phases add daemon lifecycle management, production pairing and mutual authentication, a stable API, LAN discovery, browser portal access, coordinator/relay services, and direct remote connectivity.

## Development

Validate the current scaffold with:

```powershell
tools\test.ps1
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
- [Local administration API contract](docs/protocol/local-admin-api-openapi.json)
- [Testing](docs/architecture/testing.md)
- [Threat model](docs/threat-model/initial-threat-model.md)
- [Database schema](docs/architecture/database-schema.md)
- [Protocol outline](docs/protocol/transfer-protocol.md)
- [Trusted one-way sync runbook](docs/operations/trusted-one-way-sync-runbook.md)

## Security Position

This project should not be described as end-to-end encrypted until the implemented key exchange, mutual authentication, and transfer protocol have been reviewed and tested. Relay and coordinator designs must not require plaintext file contents or folder inventories.
