# Phase 8: Desktop Settings, Credentials, and Execution Opt-In

Status: accepted

## Context

The packaged desktop node needs a bounded operator surface before the existing
runtime, workspace, scheduler, and result-gate components can be composed. Raw
configuration editing cannot provide validation-before-activation, and provider
credentials cannot share the JSON/SQLite lifecycle without violating the local
credential boundary.

## Decision

### Use a bounded CLI settings surface

Slice 2 uses explicit `node-settings-*` and `node-share-stage` commands instead
of a native GUI. A change is written to `config.pending.json` only after the
complete candidate passes normal configuration and root-separation validation.
Staging returns a SHA-256 confirmation digest. `node-settings-apply` requires
that exact digest, requires the local listener to be stopped, writes a previous
configuration backup, and atomically activates the candidate.

The settings view covers:

- device display name;
- config, data, log, runtime-cache, and worktree roots;
- loopback API host and port;
- registered share ID, name, root, and bounded mode;
- public identity initialization state and configured store type;
- execution enabled/disabled state without exposing a preflight receipt.

Mutable roots can be relocated only before SQLite/control content, log/cache
content, or worktrees exist. The desktop control-state file is copied to the
new data root, the old root is retained, and no populated root is moved or
deleted implicitly.

### Store provider credentials only in the OS credential store

`node-credential-set --from-stdin` is the only provider credential input path.
Secrets are bounded to 2 KiB, are never accepted as command-line arguments,
and are cleared from process buffers after use. The production store has no
file fallback and uses a deterministic Windows Credential Manager target scoped
to the stable node configuration root and provider ID. Relocating mutable data
roots therefore cannot orphan an otherwise configured credential.

With respect to provider credentials, configuration and SQLite contain only the
provider ID. Status returns a configured boolean, the storage class
`os_credential_store`, and an opaque target ID. It never returns credential
bytes or the actual credential target. Credential deletion is explicit and
independent of execution disablement.

### Require a disposable-project receipt before execution opt-in

Execution remains disabled in a new configuration. Enabling it requires all of
the following while the node is stopped:

1. a configured provider credential in the OS store;
2. an absolute, regular, non-symlink runtime executable with a recorded digest;
3. a clean Git project contained below the node worktree root;
4. an explicit disposable marker stored in node-owned local control state;
5. the exact disposable-project confirmation phrase;
6. a short-lived, random preflight receipt; and
7. the exact execution-enablement confirmation phrase.

Enablement rechecks the credential, runtime digest, project containment,
disposable marker, clean status, and Git HEAD to close the preflight/activation
gap. The enabled record is inspectable configuration, but Slice 2 does not wire
it into a runtime or scheduler. Runtime composition remains Slice 3 work.

Disabling execution changes only the enabled boolean. It preserves provider
metadata, the preflight record, SQLite/control state, runtime observations,
registered projects, and worktrees. It does not delete the OS credential.

### Export a privacy-bounded diagnostic document

`node-diagnostics-export` writes `syncgate.desktop-diagnostics.v1`. The export
contains build identity, health class, identity state, configuration digest,
counts, closed check/warning codes, execution state, and opaque hashed node/share
IDs. It excludes:

- credentials and credential targets;
- preflight receipts;
- device IDs and fingerprints;
- share IDs and names;
- every absolute path;
- runtime executable names/paths;
- prompts, source content, command output, and artifact bytes.

Tests seed every excluded value and assert that none occur in serialized
settings or diagnostics.

## Consequences

- Invalid or unconfirmed settings never replace the active configuration.
- Provider secrets have a single production owner: the OS credential store.
- Execution cannot be enabled accidentally or from a stale/changed disposable
  project preflight.
- Disabling execution is non-destructive and leaves evidence available for
  inspection and recovery.
- Headless Windows sessions without a credential logon session fail closed;
  release validation reports that host limitation instead of using a file.
- A later native settings UI can call the same bounded services without gaining
  direct configuration or credential authority.
