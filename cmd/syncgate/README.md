# syncgate command

This directory contains the foreground agent command. It loads local configuration, initializes early transports, and exposes local-only diagnostics.

Current commands:

- `node-ui-session --config <node-root>/config/config.json` mints a two-minute, one-use fragment URL for the embedded loopback browser shell without exposing the durable administration bearer.
- `node-private-tunnel --ssh-target HOME` starts an authenticated SSH local forward from a laptop loopback port to the home node's loopback administration port; it never opens a public listener or executes a remote command.
- `node-remote-ui-session --config HOME-CONFIG --tunnel-port PORT` is run on the home host through that authenticated SSH connection and mints a two-minute, one-use browser URL for the laptop-side forwarded port.
- `check-config --config config.example.json` validates device, local API, transfer, and per-share sync settings.
- `daemon --config config.example.json` starts the foreground local agent, initializes durable storage and its Windows Credential Manager identity, scans eligible source shares on startup and their configured intervals, and shuts down on Ctrl+C or SIGTERM.
- `identity-migrate --config config.example.json` explicitly verifies and moves a legacy plaintext development identity into Windows Credential Manager, then removes the plaintext file.
- `pair-create --config config.example.json --ttl 10m --request read` creates a signed, short-lived invitation and prints its separate one-time code.
- `pair-inspect --invite TOKEN` validates an invitation and shows the peer identity and advisory requested capabilities without changing local state.
- `pair-accept --config config.example.json --invite TOKEN --fingerprint FINGERPRINT --code CODE --grant drop=upload,sync --control-plane-read 24h` confirms both out-of-band values and grants only the repeated share capabilities plus an optional, expiring, read-only paired-node status grant.
- `pair-revoke --config config.example.json --device DEVICE-ID` revokes a peer and removes all of its share permissions.
- `daemon-status --config config.example.json` reads local SQLite job state and prints sanitized pending or blocked work without opening a listener.
- `scan --config config.example.json --share share-id` runs one authoritative local scan for an eligible source share.
- `job-pause`, `job-resume`, and `job-retry` each accept `--config` and `--job` to apply an idempotent local SQLite job control.
- `project-migrate-preflight --config config.example.json --share SHARE --project PROJECT --name NAME` performs a bounded, read-only eligibility check and prints a state-bound confirmation value.
- `project-migrate-apply --config config.example.json --share SHARE --project PROJECT --name NAME --confirmation CONFIRMATION` revalidates and registers the configured one-way source share without moving or deleting workspace content.
- `diagnostics --file diagnostics.json --recent 5` prints a sanitized local diagnostics snapshot with recent scans, pending or blocked work, and ignored paths.
- `receive-once --config config.example.json --share-root PATH` accepts one manual file transfer only from a paired, trusted mutual-TLS peer.
- `send-once --config config.example.json --peer DEVICE-ID --file PATH --relative-path PATH` sends one file after the receiver's certificate key matches that explicitly expected paired device.

Production identity storage and legacy migration behavior are defined in
`docs/architecture/identity-storage.md`. Set the config to production mode before
running `identity-migrate`. The plaintext development identity store requires
both development mode and an explicit insecure-storage opt-in.

The signed invitation format, independent confirmation steps, idempotency, and
revocation behavior are defined in `docs/architecture/pairing.md`.

The direct TLS identity binding, rejection behavior, and precise encryption
scope are defined in `docs/architecture/authenticated-transport.md`.

The two-machine SSH workflow and revocation behavior are defined in
`docs/operations/private-laptop-control.md`.
