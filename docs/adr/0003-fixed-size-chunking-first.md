# ADR 0003: Start With Fixed-Size Chunking

Status: accepted

## Context

The system needs resumable transfers before it needs deduplication efficiency or content-defined chunking.

## Decision

Use fixed-size chunks first, with a configurable default of 4 MiB.

## Consequences

- The first transfer implementation is easier to test and reason about.
- Content-defined chunking can be introduced later behind a chunker interface.
- Files modified during transfer still require source metadata re-checks.
