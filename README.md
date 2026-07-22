# Private Sync Gate

Private Sync Gate is a planned cross-platform, personal file transfer and synchronization agent for trusted computers. The product goal is to let an owner run agents across multiple machines, coordinate shared workspaces, move files between those machines, and later expose tightly limited browser access for unmanaged computers without turning a home computer into an open public file server.

This repository is currently in Phase 0: architecture and threat model. The first implementation target is a Windows desktop and Windows laptop using manual pairing, explicit share permissions, safe chunked transfers, and local-only administration.

## Current Status

The repository was empty when Phase 0 began. The current contents define:

- Clean package boundaries for sync, transfer, storage, transport, and identity.
- A dependency-free JSON config loader with loopback-only local API validation.
- Device identity helpers for Ed25519 keys, fingerprints, and pairing codes.
- Initial share path normalization and containment helpers.
- Fixed-size chunk planning for resumable transfers.
- Safe receive-side partial file writing with hash verification before commit.
- Initial Go interfaces for transport and storage.
- Versioned SQLite migration definitions for local agent state.
- Revision and transfer state models.
- Initial SQLite schema.
- Threat model and trust boundaries.
- Mermaid architecture diagrams.
- ADRs for initial technology decisions.
- Phase 1 implementation tasks and acceptance tests.

No production file transfer path is implemented yet.

## MVP Boundary

Phase 1 will focus on local manual transfer between trusted personal computers:

- Manual device pairing.
- Manual IP connection.
- Send one file or folder.
- Fixed-size resumable chunks.
- Per-chunk and whole-file verification.
- Temporary destination writes and atomic commit.
- Transfer history in SQLite.
- Path traversal protection.

Later phases add LAN discovery, one-way sync, browser portal access, coordinator/relay services, and direct remote connectivity.

## Development

Validate the current scaffold with:

```powershell
tools\test.ps1
```

Useful docs:

- [Architecture overview](docs/architecture/overview.md)
- [Agent coordination model](docs/architecture/agent-coordination.md)
- [Testing](docs/architecture/testing.md)
- [Threat model](docs/threat-model/initial-threat-model.md)
- [Database schema](docs/architecture/database-schema.md)
- [Protocol outline](docs/protocol/transfer-protocol.md)
- [Phase 1 plan](docs/architecture/phase-1-plan.md)

## Security Position

This project should not be described as end-to-end encrypted until the implemented key exchange, mutual authentication, and transfer protocol have been reviewed and tested. Relay and coordinator designs must not require plaintext file contents or folder inventories.
