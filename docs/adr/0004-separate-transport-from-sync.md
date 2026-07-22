# ADR 0004: Separate Transport From Sync Logic

Status: accepted

## Context

The same synchronization and transfer engine must eventually work over manual IP, LAN discovery, QUIC, TCP/TLS, relay, and overlay transports.

## Decision

The sync and transfer domains use a `PeerTransport` interface that returns authenticated peer sessions and streams. Transport implementations do not own folder policy, revision logic, or destination commit decisions.

## Consequences

- Early manual transport can be replaced without rewriting sync logic.
- Tests can use in-memory transports.
- Authorization remains enforced at the receiver, above the transport.
