# Phase 1: Transfer Foundation Decisions

Status: accepted

## Scope

Phase 1 established resumable manual transfer between trusted personal
computers. These decisions keep transfer correctness independent from future
discovery and connectivity mechanisms.

## Start with fixed-size chunking

### Context

The first transfer path needs bounded memory use, chunk verification, and
restart resume before it needs deduplication efficiency or content-defined
chunking.

### Decision

Use fixed-size chunks with a configurable default of 4 MiB. Persist verified
chunk state under a stable transfer ID and verify the complete file before
destination commit.

### Consequences

- Chunk planning and resume behavior are deterministic and easy to test.
- Corrupt or incomplete chunks cannot become committed files.
- Source metadata must be rechecked so a file modified during transfer
  invalidates the attempt.
- Content-defined chunking may be introduced later behind the chunker boundary
  without changing transfer state semantics.

## Separate transport from transfer and sync policy

### Context

The same transfer and synchronization rules may eventually operate over manual
IP, LAN discovery, direct TLS, relay, overlay, or other transports. Connectivity
must not decide filesystem policy or receiver authorization.

### Decision

Transfer and synchronization depend on transport interfaces that return
authenticated peer sessions and streams. Transport implementations prove peer
identity and move bytes; they do not own folder policy, revision logic,
authorization grants, or destination commit decisions.

### Consequences

- Connectivity can evolve without rewriting transfer or synchronization logic.
- Tests can use deterministic in-memory or local transports.
- The receiver remains authoritative for share permissions and destination
  mutation.
- Encryption claims are limited to the properties of the selected transport;
  transport abstraction alone does not imply end-to-end encryption.

## Commit verified content safely

### Context

Interrupted writes, path traversal, destination replacement, and source changes
can corrupt or overwrite local data if transfer completion is treated as one
unstructured write.

### Decision

Normalize relative paths, enforce share-root containment, write to a temporary
file on the destination volume, verify chunk and whole-file hashes, preserve an
existing destination when policy requires it, and atomically rename only the
verified artifact.

### Consequences

- Partial files remain distinguishable from committed content.
- Restart recovery uses persisted transfer and chunk state.
- Unsupported path and filesystem behavior fails closed.
- Replacement history and transfer state remain auditable.
