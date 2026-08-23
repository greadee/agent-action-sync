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
- Portable task revisions, dependency graphs, gate requirements, and resolved
  orchestration provenance.
- Authority-local trade, worker, instruction, runtime, provider/model, node,
  quality-gate, and policy registries.
- Authority-local execution contracts, assignments, leases, result envelopes,
  intake decisions, and orchestration audit evidence.
- Context compiler inputs, derived context caches, and generated artifact
  candidates.
- Isolated workspaces, staged result payloads, runtime sessions, node health,
  and secret-bearing provider configuration.
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
- Local orchestration application and its durable control-state store.
- Context and instruction compiler input versus the bounded bundle delivered to
  a runtime.
- Execution-contract permission boundary.
- Runtime/provider adapter and compute-node boundary.
- Isolated workspace and Git metadata boundary.
- Worker result-envelope intake and authority publication boundary.
- Quality-gate evaluator and generated-artifact approval boundary.
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
| Unsafe existing-share migration | Stale preflight state, a symlink, path collision, or mistaken root causes registration of the wrong workspace | Project migration | Resolve only configured share roots; require one-way source mode, explicit project identity, and a state-bound confirmation; revalidate immediately before idempotent bootstrap; never move, delete, or rewrite workspace files | A local privileged process can change the root again after validation | Changed-state, symlink, collision, retry, exclusion, audit, and workspace-preservation tests |
| Task or dependency-graph poisoning | Malicious input introduces missing/cross-project dependencies, cycles, stale graph revisions, hidden barriers, or gate bypass | Portable task/work-package records and graph updates | Authority-only immutable revisions; bounded graph validation; same-project membership; predecessor/digest checks; deterministic readiness and stable blocking reasons; no LLM mutation | A compromised authority can publish internally valid harmful project intent | Cycle, diamond, duplicate-edge, cross-project, stale-revision, bound, replay, and clean-rebuild tests |
| Duplicate task state or reducer drift | New orchestration code records local/agent status that disagrees with canonical work events | Scheduler persistence and projection | Existing `WORK_PACKAGE_*` and `EXECUTION_*` events remain canonical; assignments and leases are explicitly local operations; projections rebuild at a watermark | Implementation bugs can still derive inconsistent readiness | Architecture-import tests, reducer property tests, incremental-versus-replay equivalence, and no-duplicate-vocabulary review |
| Forged, replayed, or conflicting worker result | Compromised worker/runtime claims another execution, reports false success/tests, reuses an ID, or changes content under one ID | Result-envelope intake | Treat envelopes as untrusted local input; bind project/execution/contract/assignment/worker/runtime/node identities and digests; idempotent same-ID/same-digest decisions; reject conflicts; authority publishes typed canonical records | Bound identities do not prove claims are true; malicious patches and fabricated test evidence still require gates/review | Identity-drift, digest-conflict, replay, forged-contract, fabricated-success, crash-recovery, and authority-publication tests |
| Permission escalation through execution | Worker, provider, tool, or adapter requests broader filesystem, shell, network, secret, deployment, or merge access | Execution contract and runtime tool calls | Immutable contract contains deny-by-default intersection of project, work package, trade, worker, runtime/node, and user policy; amendments create a new version; unknown capabilities deny | OS account compromise or a faulty adapter may bypass application policy | Scope-overlap, forbidden-precedence, unknown-capability, amendment, secret-denial, and attempted-escalation tests |
| Runtime adapter or provider compromise | Adapter/provider executes unexpected commands, leaks content, lies about status/cost, or retains data | Runtime/provider boundary | Provider-neutral narrow interface; credentials isolated behind approved stores; exact capability negotiation; bounded observations; results remain untrusted; production adapters disabled until separate release gate | External providers and local child processes are not equivalent security sandboxes | Deterministic fake conformance, error normalization, cancellation, credential non-disclosure, capability-drift, and adapter-specific security tests |
| Workspace escape or repository poisoning | Worker uses traversal, symlinks, junctions, `.git` manipulation, hooks, submodules, or path races to affect files outside its grant | Workspace allocator and result collection | Resolve trusted repository/worktree roots; keep worktrees outside synchronized content; reject symlink/reparse escape and unsafe Git state; use opaque workspace IDs; collect only allowlisted scoped output | Same-account malware and repository features can create TOCTOU and tool-execution risk | Root containment, dirty-state, symlink/junction, collision, hook/submodule, scope, cleanup-ownership, and changed-during-read tests |
| Lease split brain or stale completion | Crash, clock drift, delayed worker, or duplicate scheduler lets multiple workers believe they own one execution | Assignment and lease control state | Authority-local leases only; compare-and-swap generations and fencing digests; bounded expiry/renewal; restart reconciliation; stale result binding rejected; lease expiry alone does not publish work failure | External work may continue after cancellation and consume resources | Concurrent-acquire, stale-fence, expiry, renewal, restart, late-result, and cancellation tests |
| Context poisoning or secret inclusion | Repository content, malicious instructions, symlinks, environment data, or oversized files inject hostile content or secrets into a worker bundle | Context compiler | Explicit source manifest; path/privacy allowlists; stable ordering; secret/redaction policy; symlink/binary/size/token bounds; source digests and omissions; exclude raw environment and local paths | Allowlisted source text can still contain prompt injection or undiscovered secrets | Golden determinism, secret fixture, symlink, binary, oversize, missing-source, scope, ordering, and omission tests |
| Registry or instruction tampering | Local attacker changes a trade, worker, gate, runtime, or instruction definition without changing its reference | Authority-local registries | Immutable version plus canonical digest and predecessor; lifecycle/audit; same-ID/version conflicts fail; exact versions bound into contracts and provenance | A process with the user's local privileges can alter both registry storage and application binaries | Import conflict, digest, version-chain, lifecycle, cross-project authorization, contract-binding, and tampered-row tests |
| Budget or resource exhaustion by a worker | Worker loops, forks processes, floods tool calls/results, or exceeds token/cost/time/disk/concurrency limits | Runtime, node, workspace, and result intake | Immutable ceilings, cancellation, lease limits, bounded result/envelope sizes, disk preflight, staged quotas, and deterministic exhaustion outcomes | Provider billing lag, child-process escape, and OS-level enforcement gaps | Token/cost/time/tool/result/disk/concurrency boundary, cancellation, retry, and orphan-process tests |
| Mixed-version partial orchestration | Older daemon transports but cannot understand new records, or unsupported result/runtime versions influence state | Portable ingestion, local storage downgrade, and result intake | Unknown kinds/events/payloads never affect projection/readiness; exact local version support required before scheduling; unsupported envelopes publish no canonical state; upgrade rebuilds original bytes; storage downgrade fails closed | Older replicas may expose only known history and cannot report new readiness until upgraded | Older-target transport, unsupported-kind/event/envelope, downgrade, upgrade-rebuild, and same-ID conflict tests |
| Sync-to-runtime authority confusion | Peer with upload/sync permission causes a process to start or treats uploaded results as accepted history | Sync transport and upload-only result return | Sync remains semantic-blind and cannot import/call runtime packages; upload permission grants byte receipt only; authority-owned intake is separate | Future composition changes could accidentally bridge the boundaries | Architecture-import, capability separation, upload-only, no-process-start, and end-to-end authority tests |
| Generated artifact promoted as truth | LLM or tool output is presented as an authoritative retrospective, recommendation, gate result, or project decision | Generated artifact and dashboard/read model | Generated content is non-authoritative with source/model provenance and explicit approval; disabled generators cannot affect readiness, permissions, gates, or acceptance | Reviewers can still over-trust plausible generated content | Disabled-path, provenance, approval, stale-source, and non-authoritative read-model tests |
| Runtime credential or prompt exfiltration | A provider, worker, repository document, or malicious prompt causes credential disclosure or requests data outside the approved bundle | Adapter input, context bundle, tool invocation | Credentials remain in approved local stores; bundles use explicit source/privacy allowlists and redaction; contract capability and path intersection denies secret/network access by default | Allowed source text can still contain prompt injection; an OS-compromised runtime can bypass application controls | Secret fixture, context omission, capability-denial, adapter non-disclosure, and scoped-tool tests |
| Unsafe tool or repository escape | A worker attempts shell, Git hook/submodule, traversal, merge, deployment, dependency install, or writes outside its scope | Workspace or adapter tool call | Immutable contract reauthorizes each capability and path; workspace preflight and result validation reject unsafe Git state and `.git`; automatic merge/deploy/install remain off | Local process isolation is not a complete sandbox | Scope, hook/submodule, dirty-state, forbidden-capability, and result-path tests |
| Runaway, orphaned, or duplicate local execution | Runtime ignores cancellation, consumes budget, escapes after timeout, or survives daemon restart | Runtime session and lease recovery | Budget/deadline ceilings, fencing, bounded cancellation, shutdown checkpoints, and `needs_operator` recovery disposition; no automatic replay of ambiguous attempts | A surviving external process may continue consuming local/provider resources | Timeout, cancellation, restart, stale-callback, duplicate-claim, and orphan-process tests |
| Malicious patch or premature integration | Worker result carries harmful changes or a plausible completion claim bypasses review | Result intake, test/review gates, human integration | Result is untrusted; allowlisted scoped references, deterministic tests, required review/approval gates, and explicit operator integration decision are required | A human may approve a harmful change; content scanning is not yet a sandbox | Forged-result, unsafe-patch, gate-failure, waiver, and human-approval tests |

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
- A worker, runtime lease, provider session, upload capability, or result
  envelope is never a project-history authority.
- Permissions are immutable, bounded, deny-by-default execution-contract input;
  a result cannot amend the contract that authorized it.
- File sync transports bytes and enforces share permissions. It does not start,
  resume, cancel, or accept agent execution.
- Runtime, workspace, node, credential, lease, and absolute-path fields remain
  local even when their stable identity/version digests appear in provenance.
- Git worktrees are created only outside synchronized content from allowlisted
  base commits. Dirty or missing worktrees are quarantined for the operator;
  cleanup is owner/generation fenced and never recursively forced.

## Orchestration setup release controls

The setup release gate runs deterministic recovery, compatibility, boundary,
and fuzz checks before a daemon composition change. It verifies that a
CGO-disabled host reports the race detector as skipped rather than passed, and
that Linux CI or another C-enabled host supplies the race evidence. The
released daemon must still report runtime execution and workspace allocation
as disabled. A `503 unavailable` setup authority route is a containment result,
not an invitation to fall back to an implicit runtime, workspace, or remote
command path. Recovery preserves original portable bytes and immutable local
control records; only deterministic derived state may be rebuilt.

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
- No production runtime adapter, remote command endpoint, distributed compute
  node, or automatic merge path is enabled. The local Git worktree adapter can
  create an explicitly authorized isolated branch/worktree, but does not run
  worker code or establish sandbox safety.
- Authority-local orchestration registries, contracts, leases, and intake state
  are not reconstructible solely from portable project history. Their backup,
  migration, and corruption recovery require separate local operations.
- Context allowlisting and redaction cannot prove that approved source text is
  free of prompt injection, proprietary content, or every secret pattern.
- Cancellation and lease fencing can reject late results, but they cannot prove
  an external provider or escaped child process stopped consuming resources.
- The single authority can approve a malicious result or publish false gate
  evidence. Multi-party attestation and multi-writer correction remain deferred.
