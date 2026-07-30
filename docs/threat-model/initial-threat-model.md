# Initial Threat Model

## Assets

- Local files inside configured share roots.
- Files staged for transfer.
- Device private keys.
- Pairing secrets.
- Share permissions.
- Transfer history.
- Audit logs.
- Browser sessions, later.
- Coordinator and relay metadata, later.

## Trust Boundaries

- User operating system account.
- Local agent process.
- Local administration API on loopback.
- Paired device sessions.
- Per-share authorization checks.
- Network transport.
- Coordinator, later.
- Relay, later.
- Browser portal, later.

## Threats And Mitigations

| Threat | Attacker capability | Entry point | Initial mitigation | Remaining risk | Test coverage target |
|---|---|---|---|---|---|
| Path traversal | Paired or browser client sends malicious path | Receive request | Normalize path and verify containment before writes | Platform-specific path edge cases | Fuzz path normalization |
| Malicious paired device | Trusted device is compromised | Peer protocol | Per-share capabilities enforced by receiver | Allowed shares remain exposed | Authorization integration tests |
| Stolen laptop | Attacker obtains paired device | Device identity | Device revocation and share-level permissions | Offline data already synced | Revocation tests once implemented |
| Relay compromise | Relay operator observes traffic | Relay transport | Endpoint encryption, relay forwards opaque streams | Traffic volume metadata visible | Relay metadata review |
| Coordinator compromise | Coordinator operator observes presence | Presence service | Store minimal metadata only | Device online timing visible | Coordinator data-minimization tests |
| School computer compromise | Managed browser may record activity | Browser portal | Short sessions, explicit logout, restricted shares | Downloaded files can remain on lab computer | Browser session expiry tests |
| Replay attack | Network attacker replays old messages | Peer transport | TLS or QUIC session security; transfer IDs | Protocol implementation bugs | Duplicate and replay message tests |
| Chunk corruption | Network or malicious peer sends bad bytes | Chunk data | Per-chunk hash and whole-file hash | Hash implementation bugs | Corruption tests |
| Destination overwrite | Crash or malicious transfer targets existing file | Commit path | Write partial file, verify, atomic rename, version old file | Filesystem-specific rename behavior | Crash-at-commit tests |
| Target-side drift overwrite | Authoritative source change collides with a locally changed target | One-way sync preparation | Receiver validates one-way modes, action capabilities, and revision ancestry; drift follows explicit reject, preserve-copy, or report-only policy | Conflict-copy and apply executors are not yet implemented | One-way policy branch tests |
| Mass deletion | Sync propagates accidental removal | Sync scan | Tombstones and deletion circuit breaker | Not implemented until sync phase | Deletion threshold tests |
| Ransomware propagation | Local malware mutates many files | Sync engine | Version history and modification-rate pause planned | Detection false negatives | Modification-rate tests |
| Resource exhaustion | Peer sends huge manifests or chunks | Protocol decoder | Bounded message sizes and quotas | Limit tuning | Oversized message tests |
| Secret leakage in logs | Bug logs keys or tokens | Logging | Structured redacted audit fields | Developer mistakes | Log redaction tests |

## Security Principles

- The receiving side is the authority for authorization.
- Metadata exposure must be documented, not hidden.
- Sync is not backup.
- Unsupported filesystem features should fail closed.
- Every destructive action should be auditable.
