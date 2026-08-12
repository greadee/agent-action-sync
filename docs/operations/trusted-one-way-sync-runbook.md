# Trusted One-Way Sync Runbook

This runbook covers the manual trust setup and safe recovery procedure for a
`one_way_source` device and a `one_way_target` device. The receiver is always
the authority: pairing alone grants no share access, and every accepted change
requires current trust plus explicit `sync` and action capabilities.

## Current milestone boundary

The pre-API daemon contains the persistent scanner, authenticated work store,
restart-safe job supervisor, authorization gates, and one-way apply executor.
The foreground `syncgate daemon` command does not yet install a peer message
loop or automatic transfer executor. It therefore fails queued peer work closed
with `automatic peer job execution is disabled until trusted transport is
configured`. The two-daemon acceptance suite supplies only that missing wire
adapter through the daemon's production executor seam; it does not bypass
pairing, mutual TLS, receiver authorization, storage, or filesystem guardrails.

Do not treat starting the current CLI daemon by itself as enabling unattended
network synchronization. Protocol wiring belongs after this pre-API milestone.

## Prepare both devices

Use the same share ID on both devices. Give each device its own data directory
and identity store. Configure the authoritative device with
`mode: "one_way_source"` and the destination with `mode: "one_way_target"`.
The two root paths may differ.

On each device, validate its local configuration and confirm its share root is
mounted and writable:

```powershell
syncgate check-config --config C:\SyncGate\source.json
syncgate check-config --config C:\SyncGate\target.json
```

Do not initialize a source while its intended root is unavailable. An empty
directory at the configured path is a real, available root and may represent a
deletion; an absent or inaccessible directory is unavailable and must be fixed
before scanning.

## Pair in both directions

The target must trust and authorize the source. The source must also trust the
target so its outbound mutual-TLS connection can pin the expected receiver.

On the source, create an invitation:

```powershell
syncgate pair-create --config C:\SyncGate\source.json --ttl 10m `
  --request sync,upload,modify,delete
```

Transfer the encoded invitation to the target. Compare the fingerprint and
one-time code over a separate channel or directly on both screens. Inspect it
without changing state:

```powershell
syncgate pair-inspect --invite SOURCE_INVITE
```

On the target, accept only the capabilities required for authoritative
one-way synchronization:

```powershell
syncgate pair-accept --config C:\SyncGate\target.json `
  --invite SOURCE_INVITE --fingerprint SOURCE_FINGERPRINT --code SOURCE_CODE `
  --grant shared-docs=sync,upload,modify,delete
```

Grants are LAN-only by default. Keep that default for direct LAN sync. Use
`--lan-only=false` only when the transport has positively classified the
session as remote and remote synchronization is an intentional policy choice.
The authenticated LAN/remote classification is persisted with each job so a
restart cannot silently broaden it.

Next, create an invitation on the target and accept it on the source without a
share grant:

```powershell
syncgate pair-create --config C:\SyncGate\target.json --ttl 10m

syncgate pair-accept --config C:\SyncGate\source.json `
  --invite TARGET_INVITE --fingerprint TARGET_FINGERPRINT --code TARGET_CODE
```

No grant is needed in this direction for one-way source-to-target changes. The
trust record is needed to authenticate the target's pinned transport identity.

## Restart and recovery

Use this order after a crash, power loss, network outage, or storage outage:

1. Stop the affected process if it is still running.
2. Restore the configured data directory and share root at their original
   paths. Never substitute a new empty directory for an unavailable source.
3. Check persisted work before changing it:

   ```powershell
   syncgate daemon-status --config C:\SyncGate\target.json
   ```

4. Restart the process with the same configuration and identity store. Running
   jobs are recovered to `queued`; transfer IDs and verified chunk state remain
   stable for the executor to resume.
5. Recheck status. A `retry_wait` or `failed` job must retain the same peer,
   share, path, revision, action capability, and LAN/remote scope before retry.
6. Fix the reported cause before an explicit retry:

   ```powershell
   syncgate job-retry --config C:\SyncGate\target.json --job JOB_ID
   ```

Do not delete the SQLite database, incoming artifacts, or history directory to
clear a job. Those records are the recovery and audit state.

## Unavailable roots and deletion protection

If a source root is absent or inaccessible, its scan is marked unavailable and
the last committed index remains authoritative. No deletion snapshot is
created. Once the root returns, run a deliberate scan while the foreground
daemon is stopped:

```powershell
syncgate scan --config C:\SyncGate\source.json --share shared-docs
```

If the restored root is unexpectedly empty or has lost many entries, the
deletion count/percentage guard blocks the snapshot. Investigate the mount,
path, permissions, and source contents. Do not raise deletion limits merely to
make the warning disappear. Change thresholds only after independently
confirming that the deletions are intended and recoverable.

## Revocation

To stop the source immediately at the receiver authorization boundary, revoke
it on the target:

```powershell
syncgate pair-revoke --config C:\SyncGate\target.json --device SOURCE_DEVICE_ID
```

Revocation removes its share grants atomically. New mutual-TLS sessions fail,
new work is not created, queued receive work is reauthorized before execution,
and apply checks trust again before a filesystem mutation. A stream that was
already authenticated is not forcibly disconnected, so these receiver-side
rechecks are required. Revoke the target on the source as well when the two
devices should no longer communicate in either direction.

## Diagnostics and acceptance check

Diagnostics intentionally redact absolute paths and common secret-bearing
values. Still treat reports as local operational data and inspect them before
sharing. A sanitized JSON snapshot can be rendered with:

```powershell
syncgate diagnostics --file C:\SyncGate\diagnostics.json --recent 5
```

Before shipping changes to the trusted-sync path, run the deterministic
acceptance scenarios:

```powershell
go test ./tests/integration -run TwoDaemonTrustedSync -count=1
```

The suite proves signed bidirectional pairing, pinned mutual TLS, LAN-scoped
authorization across restart, one-way add/modify/delete, revocation, rejected
impersonation, unavailable-root retention, deletion blocking, and sanitized
diagnostics with two independent daemon stores and identities.
