# Phase 15: paired-node status federation

Status federation reuses the Ed25519 identity already confirmed by pairing, but trust alone grants no visibility. Pairing acceptance may add one explicit `read_status` control-plane grant with a bounded expiry. It grants only receipt and local display of signed status snapshots; it is not a share capability and cannot authorize execution, shell, filesystem, transfer, scheduler, or assignment actions.

The `syncgate-node-status-v1` envelope is signed by the source device and contains a monotonically increasing revision, a changing digest watermark, a five-minute maximum observation lifetime, closed health/lifecycle values, and at most 200 sanitized project summaries. Project summaries contain identifiers, a safe display name, closed scheduler state, bounded counts, and an optional accepted-history watermark. Paths, prompts, filenames, logs, credentials, runtime sessions, worker/provider details, artifact data, and commands are forbidden.

The receiver loads the paired public key from local trust storage, verifies the signature and digest, checks the active read-only grant, and rejects expired, replayed, conflicting, or stale-watermark observations. SQLite retains only the newest verified snapshot per peer. Expired snapshots remain visible as `offline` while the grant remains active. Pairing revocation disables the grant and deletes the replica in the same transaction, immediately removing browser visibility.

The loopback browser API adds only `GET /api/v1/federation/nodes`. There is no federation POST, remote command DTO, or execution ownership inference. Distributed command authority remains deferred to a separate ADR.
