# syncgate command

This directory contains the foreground agent command. It loads local configuration, initializes early transports, and exposes local-only diagnostics.

Current commands:

- `check-config --config config.example.json` validates device, local API, transfer, and per-share sync settings.
- `daemon --config config.example.json` starts the foreground local agent, initializes durable storage and identity, scans eligible source shares on startup and their configured intervals, and shuts down on Ctrl+C or SIGTERM.
- `daemon-status --config config.example.json` reads local SQLite job state and prints sanitized pending or blocked work without opening a listener.
- `scan --config config.example.json --share share-id` runs one authoritative local scan for an eligible source share.
- `job-pause`, `job-resume`, and `job-retry` each accept `--config` and `--job` to apply an idempotent local SQLite job control.
- `diagnostics --file diagnostics.json --recent 5` prints a sanitized local diagnostics snapshot with recent scans, pending or blocked work, and ignored paths.
- `receive-once` and `send-once` provide manual TCP/TLS file-transfer smoke paths.
