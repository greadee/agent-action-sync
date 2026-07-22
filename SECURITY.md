# Security Policy

## Current Security Status

Private Sync Gate is in Phase 0. The repository contains architecture, interfaces, and security planning only. It does not yet contain a production transfer protocol, browser portal, coordinator, relay, or cryptographic session implementation.

Do not use this repository to protect sensitive files until the relevant phase has been implemented, tested, and reviewed.

## Core Security Rules

- Do not expose the home agent directly to the public internet in the initial implementation.
- Bind local administration APIs to `127.0.0.1` by default.
- Do not create custom cryptographic primitives.
- Do not rely on passwords as stable device identity.
- Separate device trust from per-share authorization.
- Verify destination paths remain inside authorized share roots.
- Never overwrite a destination file while receiving bytes.
- Preserve replaced or deleted files according to the share versioning policy.
- Record security-relevant audit events without logging secrets or file contents.

## Reporting

This is a private personal project. Record suspected vulnerabilities in `docs/threat-model/initial-threat-model.md` until a formal reporting process exists.
