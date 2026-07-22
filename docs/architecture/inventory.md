# Repository Inventory

## Assessment

The repository was initialized but contained no tracked application source, documentation, build files, or tests. Only `.git` metadata was present.

Classification: empty repository.

Reusable components: none found.

Build system before Phase 0: none found.

Test command before Phase 0: none found.

## Constraints From Product Brief

- Default implementation language: Go.
- Initial platform: Windows desktop and Windows laptop.
- Storage: SQLite once persistence is implemented.
- Sync engine must stay independent from network transports.
- Coordinator and relay must not receive file contents.
- Browser portal must expose only explicit virtual shares.
- Direct public exposure of the home computer is out of scope for early phases.

## Security-Sensitive Areas To Create

- Device identity and private key storage.
- Pairing flow and trusted-device records.
- Share capability checks.
- Path normalization and share-root containment.
- Temporary file receive area and atomic commit.
- SQLite persistence and migration path.
- Audit logging and redaction.
- Browser session storage and revocation, later.

## Initial Validation Commands

```powershell
Get-ChildItem -Force
rg --files
git status --short --branch
git rev-parse --show-toplevel
go test ./...
```
