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
| Path traversal | Paired or browser client sends malicious path | Receive request | Normalize path and verify containment before writes; reject symlinks and Windows-reserved names | Platform-specific path edge cases and TOCTOU filesystem changes | Fuzz path normalization and containment tests |
| Malicious paired device | Trusted device is compromised | Peer protocol | Per-share capabilities enforced by receiver | Allowed shares remain exposed | Authorization integration tests |
| Stolen laptop | Attacker obtains paired device | Device identity | Atomic device revocation removes share permissions and authorization rejects revoked trust | Offline data already synced | Pairing revocation and authorization tests |
| Relay compromise | Relay operator observes traffic | Relay transport | Endpoint encryption, relay forwards opaque streams | Traffic volume metadata visible | Relay metadata review |
| Coordinator compromise | Coordinator operator observes presence | Presence service | Store minimal metadata only | Device online timing visible | Coordinator data-minimization tests |
| School computer compromise | Managed browser may record activity | Browser portal | Short sessions, explicit logout, restricted shares | Downloaded files can remain on lab computer | Browser session expiry tests |
| Replay attack | Network attacker replays old messages | Peer transport | TLS 1.3 direct sessions prove paired key possession; transfer IDs provide application idempotency | Application messages still need authenticated-session binding and replay handling | Duplicate and replay message tests |
| Chunk corruption | Network or malicious peer sends bad bytes | Chunk data | Per-chunk hash and whole-file hash | Hash implementation bugs | Corruption tests |
| Destination overwrite | Crash or malicious transfer targets existing file | Commit path | Commit verified bytes to ignored same-volume staging; durable apply intent; reject symlink parents; preserve old destination before atomic rename | Filesystem-specific rename, locked-file, and reparse-point behavior | Crash-at-commit, fault-injection, and symlink tests |
| Target-side drift overwrite | Authoritative source change collides with a locally changed target | One-way sync apply | Receiver validates one-way modes, action capabilities, revision ancestry, observed target revision, and on-disk content; preserve-copy policy moves the target to deterministic history | Apply must remain serialized per path when schedulers are introduced | One-way policy and drifted-apply integration tests |
| Unauthorized or malformed change | Peer submits an invalid revision or action to create receiver work | One-way preparation | Validate request, normalized path, source revision identity/ancestry, policy, and stored capabilities before returning preparation; preparation has no filesystem or state writes | Authenticated transport remains a later boundary | Preparation rejection tests |
| Mass deletion | Sync propagates accidental removal | Sync scan | Tombstones and deletion circuit breaker block unsafe snapshots before persistence | Guard thresholds require per-share tuning | Deletion threshold and unavailable-root tests |
| Ransomware propagation | Local malware mutates many files | Sync engine | Version history and modification-rate pause planned | Detection false negatives | Modification-rate tests |
| Resource exhaustion | Peer sends huge manifests or chunks | Protocol decoder | Bounded message sizes and quotas | Limit tuning | Oversized message tests |
| Secret leakage in logs | Bug logs keys or tokens | Logging and diagnostics | Redact absolute paths plus common authorization, token, secret, password, and API-key values | New sensitive field names can bypass generic redaction | Log-redaction tests and structured logging review |

## Security Principles

- The receiving side is the authority for authorization.
- Metadata exposure must be documented, not hidden.
- Sync is not backup.
- Unsupported filesystem features should fail closed.
- Every destructive action should be auditable.

## Known Limitations

- Filesystem checks reduce but cannot eliminate time-of-check/time-of-use races when another local process changes a share concurrently. Per-path apply locking and terminal-state verification limit the effect; local share roots must remain trusted.
- Portable automated tests cannot reliably reproduce Windows sharing violations or a truly full filesystem. CI injects write and flush failures as disk-full equivalents; locked-file behavior needs platform-level manual coverage.
- Diagnostics redact common secret-bearing field names and absolute paths, but callers should provide structured error codes rather than embed sensitive values in free-form error text.
