# ADR 0002: Use SQLite For Local State

Status: accepted

## Context

The agent needs durable local state for devices, shares, revisions, chunks, transfers, tombstones, conflicts, and audit events.

## Decision

Use SQLite as the first local persistence engine behind storage interfaces.

## Consequences

- Transfer resume state can survive process restarts.
- Migrations are required from the first persistence implementation.
- The sync and transfer domains must depend on storage interfaces, not SQLite details.
