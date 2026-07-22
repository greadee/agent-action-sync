# ADR 0006: Keep Remote Execution Out Of The Sync Layer

Status: accepted

## Context

The product should help an owner run coding or automation agents across multiple personal computers. That creates pressure to add remote command execution.

## Decision

The sync gate will coordinate files, task manifests, handoff packages, and outputs. It will not embed arbitrary remote shell execution in the file synchronization layer.

## Consequences

- Early phases remain focused on safe transfer, permissions, and auditability.
- Multi-computer agent workflows can use explicit shared workspaces.
- Any future remote execution feature must be separate, capability-scoped, allowlisted, resource-limited, revocable, and auditable.
