# One-Way Revision Manifest Protocol

Version 1 defines transport-neutral JSON messages for one-way synchronization.
An authenticated session may frame these messages, but TCP, TLS, stream, and
chunk-transfer details are intentionally outside this contract.

## Messages

- `RevisionAdvertisementRequest` advertises one or more authoritative source
  revisions to a target. Each entry has a revision ID, optional parent revision
  ID, canonical relative path, revision type, content metadata where applicable,
  and sequence number.
- `RevisionAdvertisementResponse` accepts or rejects the advertisement. A
  rejection includes a structured error.
- `OneWayChangeRequest` asks the receiver to prepare exactly one advertised
  revision, its expected target base revision, and the requested add, modify, or
  delete action.
- `OneWayChangeResponse` accepts, rejects, or marks the request as duplicate.
  A rejection includes a structured error.

Every message includes protocol version, request ID, device identity, share ID,
and revision IDs where relevant. Request/response correlation uses `request_id`.
The source remains authoritative only after later receiver-side authorization and
policy validation.

## Bounds and validation

- Version is exactly `1`; unknown versions fail closed.
- A revision advertisement supplies `max_entries` and `max_bytes`. Both are
  positive and cannot exceed the local hard limits of 1,024 entries or 1 MiB.
- The encoded message and advertised revision count must remain within those
  limits. Unknown JSON fields and trailing JSON values are rejected.
- Identifiers are trimmed, bounded ASCII protocol tokens. Revision IDs are
  unique within an advertisement.
- Paths must already be canonical, relative, and safe for Windows share roots.
- Version 1 accepts files, directories, and deletion revisions. Symlink and
  other unsupported entry types cannot be advertised for one-way application.
- File entries require a lower-case SHA-256 digest; directory and deletion
  entries cannot carry file-content metadata.
- Rejections must have one recognized structured error code and a short,
  trimmed message. Successful and duplicate responses cannot include an error.

The protocol types do not open files, create transfers, authorize peers, or
apply revisions. Receiver preparation validates the request and advertised
revision, then checks stored `sync` and action capabilities before returning a
non-persistent descriptor that includes the observed target revision. For file
changes, the transfer layer commits verified bytes to a receiver-selected
`.sync-incoming/` path; no peer-supplied staging path is accepted. The apply
executor revalidates content and target state before destination work and uses
the source revision ID for the atomic revision/index commit.
