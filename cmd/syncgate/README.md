# syncgate command

This directory contains the foreground agent command. It loads local configuration, initializes early transports, and exposes local-only diagnostics.

Current commands:

- `check-config --config config.example.json` validates device, local API, transfer, and per-share sync settings.
- `diagnostics --file diagnostics.json --recent 5` prints a sanitized local diagnostics snapshot with recent scans, pending or blocked work, and ignored paths.
- `receive-once` and `send-once` provide manual TCP/TLS file-transfer smoke paths.
