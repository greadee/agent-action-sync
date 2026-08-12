# Phase 0: Project Skeleton Decisions

Status: accepted

## Scope

Phase 0 established the implementation language, local persistence model, and
product boundaries needed before transfer or synchronization work began.

## Use Go for the local service

### Context

The local service must run for long periods on personal computers, coordinate
filesystem and network work concurrently, and ship as a straightforward
cross-platform binary.

### Decision

Use Go as the default implementation language for the daemon, command-line
tools, and core service binaries.

### Consequences

- Cross-platform builds remain straightforward.
- Concurrency and networking use the standard library where practical.
- Early dependencies stay small and explicit.
- A later local interface may be served by the Go daemon without changing the
  core implementation language.

## Use SQLite for durable local state

### Context

The service needs durable local state for trusted devices, shares, revisions,
chunks, transfers, tombstones, conflicts, jobs, and audit events. Transfer and
recovery state must survive process and computer restarts.

### Decision

Use SQLite as the first local persistence engine behind storage interfaces.
The daemon is the single live owner of the database.

### Consequences

- Transfer and synchronization recovery can survive restarts.
- Schema migrations are required from the first persistence implementation.
- Sync and transfer domains depend on storage interfaces rather than SQLite
  details.
- Other local control surfaces must call the daemon instead of opening its live
  database independently.

## Keep remote execution outside synchronization

### Context

Coordinating automation across personal computers creates pressure to add
remote command execution to the file synchronization path. That would combine
two trust boundaries and substantially increase the consequence of a sync
vulnerability.

### Decision

Synchronization coordinates files, task manifests, handoff packages, and
outputs. It does not embed arbitrary remote shell execution.

### Consequences

- Early phases remain focused on safe transfer, permissions, and auditability.
- Shared workspaces can exchange explicit artifacts without granting process
  execution authority.
- Any future remote execution feature must be separate, capability-scoped,
  allowlisted, resource-limited, revocable, and auditable.

## Defer and restrict browser access

### Context

Unmanaged school or lab computers may be monitored, locked down, or cleared
after logout. Native software cannot be assumed to run there.

### Decision

Defer the browser portal until local transfer and authorization are stable.
When implemented, it must use HTTPS, short-lived revocable sessions, and only
explicitly exposed virtual shares. Upload-only shares cannot list existing
files.

### Consequences

- The browser portal is not a remote filesystem browser.
- Browser access requires separate session, quota, and audit controls.
- No browser route is part of the project skeleton, transfer foundation, or
  sync-scanner phases.
