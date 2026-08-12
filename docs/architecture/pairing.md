# Pairing and Trusted-Device Management

Pairing is an explicit local CLI workflow. It records a peer's public identity
and only the share capabilities the operator names. It does not open a network
connection; mutually authenticated transport binding is a separate slice.

## Workflow

On the device being introduced, create a short-lived invitation:

```powershell
syncgate pair-create --config config.json --ttl 10m --request read
```

The command prints four values: an encoded invitation, the device fingerprint,
a one-time code, and the expiry. Send the encoded invitation to the accepting
device. Confirm the fingerprint and code through an independent channel or by
comparing the two device screens. Requested capabilities are advisory and are
never granted automatically.

On the accepting device, inspect the signed invitation without changing state:

```powershell
syncgate pair-inspect --invite TOKEN
```

After confirming the peer, accept it and name every grant explicitly:

```powershell
syncgate pair-accept --config config.json --invite TOKEN `
  --fingerprint FINGERPRINT --code CODE `
  --grant school-drop=upload,sync
```

Repeat `--grant` for another share. No `--grant` means the device is trusted but
has no share permissions. Grants default to LAN-only; passing `--lan-only=false`
is an explicit decision to permit the stored capabilities outside a LAN session.

Revoke all access for a peer with:

```powershell
syncgate pair-revoke --config config.json --device DEVICE-ID
```

Revocation marks the device revoked and removes all of its share-permission rows
in one transaction. Repeating acceptance of the same invitation or revocation
of the same device reports the existing state without duplicating audit events
or broadening access.

## Invitation security

The `syncgate-pairing-v2` invitation contains the peer's device ID, public key,
fingerprint, display name, creation time, expiry, requested capabilities, and a
hash of the one-time code. Lifetimes are capped at 24 hours. The code itself is
excluded from the encoded invitation. The device identity signs all invitation
fields with Ed25519.

Acceptance verifies the signature, derives the fingerprint and device ID from
the public key, checks the expiry, and compares both operator confirmations.
Unknown fields, malformed identities, unsupported capabilities, tampering,
expired invitations, and fingerprint or code mismatch are rejected before any
trust or permission write.

An invitation is one-time per accepting SyncGate database. Its signed invite ID
is recorded in `pairing_acceptances`; a duplicate returns the original accepted
state and cannot replace its grants. Offline invitations cannot be globally
consumed across databases without a coordinator, so short expiry and independent
fingerprint/code confirmation remain required.

## Persistence and audit

Acceptance stores only public peer identity material. Device trust, explicit
permissions, the acceptance ledger, and the `pairing.accepted` audit event commit
atomically. A failure rolls back all four. Revocation and its `pairing.revoked`
event are also atomic. Invitation creation records `pairing.invitation_created`.

Audit metadata contains invitation IDs, expiry, advisory capability names, and
explicit grant summaries. It never contains the invitation token, one-time code,
private key, or another reusable secret.

Pairing pins the public identity needed by the next transport slice. Until that
transport verifies possession of the paired private key, a database trust row
alone does not authenticate a network peer.
