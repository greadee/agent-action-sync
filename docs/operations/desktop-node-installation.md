# Desktop Node Installation and Recovery

## Build a Windows release

Use a clean Windows x64 host with Go 1.25.12, Inno Setup 6, `signtool.exe`, and
an accessible code-signing certificate:

```powershell
tools\build_windows_release.ps1 `
  -Version 0.1.0 `
  -Channel stable `
  -CertificateThumbprint <SHA1-thumbprint>
```

The release command produces a signed executable, signed per-user installer,
embedded/external version manifest, and SHA-256 checksums under
`dist\windows-amd64`. It fails when signing or Authenticode verification fails.
`-AllowUnsignedDevelopment` exists only for local installer development.

Before publishing, run:

```powershell
tools\check_desktop_node_slice1_release.ps1
```

Then install, upgrade, and uninstall in a clean Windows VM and verify the
Authenticode signatures from the file properties or with
`Get-AuthenticodeSignature`.

## First run

The installer runs initialization once. It is safe to repeat:

```powershell
syncgate node-init
syncgate node-run
```

In another terminal:

```powershell
syncgate node-health
```

The node is intentionally foreground-owned. Closing its console or sending an
interrupt begins ordered shutdown. The installer does not register a Windows
service or automatic startup task.

For a disposable test root, use the same `--root` value for all node commands:

```powershell
syncgate node-init --root C:\Temp\SyncGatePilot
syncgate node-run --root C:\Temp\SyncGatePilot
syncgate node-health --root C:\Temp\SyncGatePilot
```

## Upgrade

1. Stop the foreground node cleanly.
2. Back up the configuration and data roots according to local policy.
3. Verify the new installer signature and `version-manifest.json`.
4. Run the newer installer. It replaces only application files.
5. Start the node and run `syncgate node-health`.

Initialization validates the existing configuration, preserves SQLite/control
state and worktrees, and advances only compatible layout state. It does not
rewrite an existing configuration.

## Downgrade

Downgrades are blocked by default. `/ALLOWDOWNGRADE=1` bypasses only the
installer semantic-version check. It does not bypass the node's control-layout
compatibility check and it never rolls SQLite back. Restore a backup with a
matching binary when the older binary does not support the current layout.

## Uninstall and reinstall

Normal uninstall preserves:

- `%APPDATA%\SyncGate\config.json`
- `%LOCALAPPDATA%\SyncGate\data`
- `%LOCALAPPDATA%\SyncGate\logs`
- `%LOCALAPPDATA%\SyncGate\cache`
- `%LOCALAPPDATA%\SyncGate\worktrees`
- every registered project root

Reinstalling later reuses compatible preserved state. Removing those roots is
not part of uninstall and must be a separate, explicit operator decision after
backups and project ownership have been verified.
