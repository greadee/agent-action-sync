# Production Identity Storage

SyncGate uses an Ed25519 device identity. The private key is a local secret; the
device ID, public key, and fingerprint are public metadata.

## Storage contract

Production mode selects Windows Credential Manager automatically. The private
key is stored as a generic credential scoped to the current Windows user and
persisted on the local machine. Its target name contains a hash of the absolute,
case-normalized data-directory path, not the path itself.

The data directory contains `identity-public.json`, which holds only the device
ID, public key, and fingerprint. SQLite stores the same public device metadata.
Configuration and diagnostics contain no private-key field or value. Code that
needs the private key receives it in memory from the identity-store interface.

The plaintext `identity.json` store remains available only when both of these
settings are explicit:

```json
{
  "runtime_mode": "development",
  "identity": {
    "store": "development_file",
    "allow_insecure_development_file": true
  }
}
```

Production validation rejects that store and opt-in flag. Omitted settings
default to production mode and Windows Credential Manager.

## Startup and missing-secret behavior

- If neither a credential nor public metadata exists, startup creates a new
  identity and writes the credential before the public metadata.
- If public metadata exists but the credential is missing, startup fails closed.
  It does not silently generate a replacement identity.
- If the credential and metadata disagree, startup fails closed.
- If the credential exists but public metadata is missing, SyncGate derives and
  restores the public metadata from the credential.
- If a legacy `identity.json` exists without a production credential, startup
  reports that explicit migration is required. It does not read the plaintext
  key as a production fallback.

## Migration

Run `syncgate identity-migrate --config <path>` after changing the configuration
to production mode. The command performs this explicit migration operation:

1. loads and validates the legacy identity;
2. verifies that no different production identity would be overwritten;
3. writes the private key to Windows Credential Manager;
4. reloads the production identity and verifies its device ID; and
5. deletes the plaintext legacy file only after verification succeeds.

If verification or plaintext deletion fails after a new credential was written,
the new production entry is removed and the legacy identity remains available.
An existing, different production identity is never overwritten.

## Rotation

Rotation generates a fresh Ed25519 identity and saves it through the same store.
The credential is written before public metadata. A credential-write failure
leaves the old identity unchanged; a metadata-write failure attempts to restore
the old credential. Rotation changes the device ID and therefore requires
explicit re-pairing with peers once pairing is implemented.

## Backup and recovery

The private key is deliberately excluded from data-directory and SQLite backups.
Windows Credential Manager protects it for the current user on the current
machine, so copying the data directory to another machine is not an identity
backup.

- Loss of `identity-public.json` alone is recoverable from the credential.
- Loss of the credential is not recoverable from SQLite, diagnostics,
  configuration, or public metadata.
- Recovery after credential or machine loss is an explicit identity reset and
  peer re-pairing, not silent key regeneration. Until that workflow exists,
  SyncGate fails closed and preserves the public metadata for diagnosis.

No plaintext private-key export or backup format is defined for this sprint.
