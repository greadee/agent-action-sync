# Desktop Node Configuration and Credential Lifecycle

All commands below use the default per-user node. Add the same `--root` value to
every command when operating a disposable node root.

## Inspect and change settings

```powershell
syncgate node-settings-show

$candidate = syncgate node-settings-stage `
  --device-name HOME-DESKTOP `
  --api-host 127.0.0.1 `
  --api-port 47820 | ConvertFrom-Json

syncgate node-settings-apply --confirmation $candidate.confirmation
```

The node must be stopped before apply. Staging never changes active settings.
Use `syncgate node-settings-discard` to remove an unwanted candidate.

Root changes use `--data-dir`, `--log-dir`, `--cache-dir`, and
`--worktree-root`. Populated roots are never relocated automatically. Perform
root selection before the first daemon start, or use a separately reviewed
backup/migration procedure.

## Register a share

```powershell
$candidate = syncgate node-share-stage `
  --id project-one `
  --name "Project One" `
  --share-root C:\Projects\ProjectOne `
  --mode read_only `
  --ignore "*.tmp" | ConvertFrom-Json

syncgate node-settings-apply --confirmation $candidate.confirmation
```

The share root must already exist, be absolute, and remain separate from node
data, log, cache, and worktree roots.

## Inspect identity state

```powershell
syncgate node-identity-status
```

This reads public identity metadata only. Private device identity remains in
Windows Credential Manager.

## Configure a provider credential

Never place a provider secret directly in a command argument:

```powershell
$secret | syncgate node-credential-set --provider codex --from-stdin
syncgate node-credential-status --provider codex
```

Delete it separately when intended:

```powershell
syncgate node-credential-delete `
  --provider codex `
  --confirm "DELETE PROVIDER CREDENTIAL"
```

There is no production file fallback. A headless Windows token without a
credential logon session cannot configure or enable a provider runtime.

## Run the disposable execution preflight

Create a clean disposable Git repository under the configured worktree root.
Then mark and preflight it:

```powershell
syncgate node-disposable-mark `
  --project C:\Path\To\SyncGate\worktrees\disposable-pilot `
  --confirm "MARK PROJECT DISPOSABLE"

$preflight = syncgate node-execution-preflight `
  --provider codex `
  --model gpt-5.6-sol `
  --runtime C:\Path\To\codex.exe `
  --project C:\Path\To\SyncGate\worktrees\disposable-pilot `
  --confirm "I CONFIRM THIS PROJECT IS DISPOSABLE" | ConvertFrom-Json

syncgate node-execution-enable `
  --receipt $preflight.receipt `
  --confirm "ENABLE LOCAL EXECUTION"
```

The receipt expires after 15 minutes. Activation fails if the credential,
runtime binary, Git HEAD, clean status, marker, or containment changes. The
enabled authorization is revalidated again whenever the Slice 3 daemon
composes the runtime. The scheduler still starts paused and requires an
explicit authenticated start command; see
[Desktop runtime operation and recovery](desktop-runtime-recovery.md).

Disable without deleting history, worktrees, or credentials:

```powershell
syncgate node-execution-disable --confirm "DISABLE LOCAL EXECUTION"
syncgate node-execution-status
```

## Export sanitized diagnostics

```powershell
syncgate node-diagnostics-export --file C:\Temp\syncgate-diagnostics.json
```

The export is safe to inspect before sharing, but operators should still apply
their normal local policy. It contains opaque IDs and status codes only—never
credentials, receipts, absolute paths, project/share names, prompts, output, or
artifact bytes.

Validate the complete Slice 2 lifecycle with:

```powershell
tools\check_desktop_node_slice2_release.ps1
```
