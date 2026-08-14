# Initial Threat Model

## Assets

- Local files inside configured share roots.
- Files staged for transfer.
- Device private keys.
- Pairing secrets.
- Share permissions.
- Transfer history.
- Audit logs.
- Portable agent-project manifests, work records, handoffs, and provenance.
- Agent-project artifact manifests and content-addressed artifact blobs.
- Local project-history projections and ingestion checkpoints.
- Local-only project quarantine evidence.
- Browser sessions, later.
- Coordinator and relay metadata, later.

## Trust Boundaries

- User operating system account.
- Local agent process.
- Local administration API on loopback.
- Paired device sessions.
- Per-share authorization checks.
- Agent-project authority and portable-record validation.
- Portable project control directory versus excluded local project state.
- Network transport.
- Coordinator, later.
- Relay, later.
- Browser portal, later.

## Threats And Mitigations

| Threat | Attacker capability | Entry point | Initial mitigation | Remaining risk | Test coverage target |
|---|---|---|---|---|---|
| Path traversal | Paired or browser client sends malicious path | Receive request | Normalize path and verify containment before writes; reject symlinks and Windows-reserved names | Platform-specific path edge cases and TOCTOU filesystem changes | Fuzz path normalization and containment tests |
| Malicious paired device | Trusted device is compromised | Peer protocol | Per-share capabilities enforced by receiver | Allowed shares remain exposed | Authorization integration tests |
| Stolen laptop | Attacker obtains paired device | Device identity | Atomic device revocation removes share permissions; one-way work creation, receive-job execution, and apply recheck current trust | Offline data already synced; an action already mutating the filesystem is not forcibly interrupted | Pairing revocation and authorization tests |
| Relay compromise | Relay operator observes traffic | Relay transport | Endpoint encryption, relay forwards opaque streams | Traffic volume metadata visible | Relay metadata review |
| Coordinator compromise | Coordinator operator observes presence | Presence service | Store minimal metadata only | Device online timing visible | Coordinator data-minimization tests |
| School computer compromise | Managed browser may record activity | Browser portal | Short sessions, explicit logout, restricted shares | Downloaded files can remain on lab computer | Browser session expiry tests |
| Replay attack | Network attacker replays old messages | Peer transport | TLS 1.3 direct sessions prove paired key possession; manifest identity is bound to that session; authenticated work creation is atomic and reauthorizes idempotent replays | Request IDs do not yet provide a durable protocol-wide replay window | Duplicate, identity-drift, and revoked-replay tests |
| Chunk corruption | Network or malicious peer sends bad bytes | Chunk data | Per-chunk hash and whole-file hash | Hash implementation bugs | Corruption tests |
| Destination overwrite | Crash or malicious transfer targets existing file | Commit path | Commit verified bytes to ignored same-volume staging; durable apply intent; reject symlink parents; preserve old destination before atomic rename | Filesystem-specific rename, locked-file, and reparse-point behavior | Crash-at-commit, fault-injection, and symlink tests |
| Target-side drift overwrite | Authoritative source change collides with a locally changed target | One-way sync apply | Receiver validates one-way modes, action capabilities, revision ancestry, observed target revision, and on-disk content; preserve-copy policy moves the target to deterministic history | Apply must remain serialized per path when schedulers are introduced | One-way policy and drifted-apply integration tests |
| Unauthorized or malformed change | Peer submits an invalid revision or action to create receiver work | One-way preparation | Bind manifest source to verified session identity; validate path, revision identity/ancestry, policy, and stored capabilities; atomically create peer-bound transfer/job rows only after reauthorization | Authorization is action-scoped rather than a long-lived capability lease | Preparation, atomic rollback, capability, and identity-drift tests |
| Mass deletion | Sync propagates accidental removal | Sync scan | Tombstones and deletion circuit breaker block unsafe snapshots before persistence | Guard thresholds require per-share tuning | Deletion threshold and unavailable-root tests |
| Ransomware propagation | Local malware mutates many files | Sync engine | Version history and modification-rate pause planned | Detection false negatives | Modification-rate tests |
| Resource exhaustion | Peer sends huge manifests or chunks | Protocol decoder | Bounded message sizes and quotas | Limit tuning | Oversized message tests |
| Secret leakage in logs | Bug logs keys or tokens | Logging and diagnostics | Redact absolute paths plus common authorization, token, secret, password, and API-key values | New sensitive field names can bypass generic redaction | Log-redaction tests and structured logging review |
| Malicious or corrupt project record | Compromised authority or corrupted transfer creates malformed, oversized, traversal-bearing, or project-mismatched control data | `.agent-project/` ingestion | Discover only canonical paths; bound record and field sizes; validate schema, project identity, paths, timestamps, hashes, and typed payloads before projection | A validly shaped record from a compromised authority can still make false claims | Parser fuzzing, size-boundary, project-mismatch, path, and corrupt-record tests |
| Project history poisoning | Compromised authority reuses an ID, rewrites accepted history, forges causality, or sends records out of order | Work events and portable manifests | Immutable one-record-per-file format; canonical hashes; reject same-ID/different-content collisions; explicit producer and causal references; deterministic unresolved-reference handling | Single-authority compromise can produce internally valid false history; this phase has no external attestation | Collision, mutation, replay, out-of-order, provenance, and clean-rebuild equivalence tests |
| Sensitive project metadata disclosure | A permitted or compromised peer reads agent identity, model/provider, filenames, failures, or work status | Portable project records and local history queries | Existing per-share receiver authorization; allowlisted payloads; relative rather than absolute paths; no raw prompt, terminal, environment, or tool transcript capture | Authorized replicas receive the portable metadata in their granted project share | Authorization tests, DTO/service field allowlist tests, and sensitive-fixture review |
| Sensitive or executable artifact propagation | Authority records secrets, malware, or active content as an artifact blob | `.agent-project/artifacts/` | Treat artifacts as ordinary sensitive synchronized content; verify size and SHA-256; never execute, render, or deserialize blobs during ingestion | Hash integrity does not establish safety; authorized devices still store harmful or private bytes | Hash/size tests, non-execution tests, and operations guidance |
| Project projection tampering or loss | Local process changes or deletes project SQLite rows or checkpoints | Local SQLite database | Portable directory records remain canonical; transactional idempotent projection; clean rebuild from validated files; checkpoints advance only after commit | A process with the user's local privileges can also tamper with project files | Crash-point, checkpoint-replay, tampered-row, and rebuild-equivalence tests |
| Quarantine re-propagation or escape | Invalid input uses paths or symlinks to escape quarantine, or local rejection data is scanned back to peers | `.agent-project/local/` and rejection handling | Exclude the entire local subtree from project scans; reject symlink components; use generated local names and bounded copies; never automatically move/delete canonical remote candidates | Local malware can alter excluded files; rejected portable candidates remain present until an operator acts | Ignore-diagnostic, symlink, containment, and quarantine non-propagation tests |
| Project-record flood | Compromised authority creates many individually valid small records or artifacts | Project scan and ingestion | Bound record sizes, query pages, ingestion batches, diagnostics, and artifact limits; make ingestion cancellable and resumable | Per-project count, rate, and storage quotas require later policy | Batch-limit, cancellation, restart, and many-record load tests |

## Security Principles

- The receiving side is the authority for authorization.
- Metadata exposure must be documented, not hidden.
- Sync is not backup.
- Unsupported filesystem features should fail closed.
- Every destructive action should be auditable.
- Portable project history is untrusted input until schema, identity, path, and
  integrity validation succeeds.
- Project work history, receiver rollback history, and operational audit history
  have separate owners and must not substitute for one another.
- Redaction does not make arbitrary prompt, tool, environment, or terminal
  capture safe; initial work records use allowlisted structured fields.

## Known Limitations

- Filesystem checks reduce but cannot eliminate time-of-check/time-of-use races when another local process changes a share concurrently. Per-path apply locking and terminal-state verification limit the effect; local share roots must remain trusted.
- Portable automated tests cannot reliably reproduce Windows sharing violations or a truly full filesystem. CI injects write and flush failures as disk-full equivalents; locked-file behavior needs platform-level manual coverage.
- Diagnostics redact common secret-bearing field names and absolute paths, but callers should provide structured error codes rather than embed sensitive values in free-form error text.
- The initial Agent Project model has one authoritative source. A compromised
  authority can create well-formed but false work history, and replicas cannot
  independently attest or correct it.
- Relative project paths, worker identity, model/provider fields, work outcomes,
  and provenance remain sensitive metadata even when credentials and absolute
  paths are excluded.
- Content-addressed artifact storage verifies integrity only. It does not scan
  for malware, classify secrets, sandbox active content, or provide encryption
  at rest.
