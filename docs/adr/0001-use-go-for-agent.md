# ADR 0001: Use Go For The Agent

Status: accepted

## Context

The agent must run for long periods on personal computers, perform filesystem and network work concurrently, and eventually ship as a simple cross-platform binary.

## Decision

Use Go as the default implementation language for the core agent and service binaries.

## Consequences

- Cross-platform builds are straightforward.
- Concurrency and networking support are strong.
- The repository can keep early dependencies small.
- UI work may still use an embedded web UI served by the Go agent.
