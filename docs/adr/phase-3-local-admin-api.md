# Phase 3: Local administration API

- Status: accepted
- Date: 2026-08-11
- Scope: the loopback administration surface for the local daemon

## Decision

The local administration API is an in-process HTTP boundary owned by the daemon. It
serves a versioned, JSON-only v1 contract and calls the daemon's application services;
handlers do not open SQLite databases, inspect share files, or own lifecycle state.

The server binds to loopback only. Every operation under `/api/v1` requires a bearer
credential issued for local administration. `/healthz` is the sole unauthenticated
endpoint and returns minimal liveness information. CORS is not part of v1, and requests
from non-loopback origins are not accepted.

The v1 surface is intentionally administrative and bounded:

- status and diagnostics are read-only views;
- shares, devices, jobs, and audit events are paginated DTOs;
- scan requests and job actions are explicit commands with bounded input;
- pairing invitation, inspection, acceptance, and revocation are explicit commands;
- no endpoint reads or writes file contents, edits share configuration, exports secrets,
  changes the bind address, or administers a remote daemon.

The hand-maintained OpenAPI 3.1 document at
`docs/protocol/local-admin-api-openapi.json` is the contract source for this boundary.
Runtime code generation is deliberately not used. Contract tests validate route shape,
security declarations, bounded pagination, and forbidden surface area.

## Consequences

This keeps SQLite ownership and authorization decisions inside the daemon while allowing
future HTTP handlers, CLI commands, and a local web UI to share the same service layer.
The contract can evolve through `/api/v2` rather than silently changing v1 DTOs. A
credential store, middleware, and concrete service adapters are implementation slices
that follow this boundary; this decision does not claim that they are complete.

## Rejected alternatives

- A separate local API process would duplicate lifecycle and storage ownership.
- An unauthenticated loopback API would make browser and local-process attacks too easy.
- A file-oriented API would expand the administrative boundary into a second transfer
  protocol and increase the consequences of an authorization mistake.
