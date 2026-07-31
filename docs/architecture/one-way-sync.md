# One-Way Synchronization Policy

The source share is authoritative, but authority does not bypass receiver-side
policy or authorization. Before any destination path is opened, staged, renamed,
or removed, the receiver evaluates a pure one-way policy decision.

## Policy boundary

- The source must be configured as `one_way_source`.
- The target must be configured as `one_way_target`.
- Both policies must name the same share, and the permission must name that
  share and the authoritative source device.
- Every action requires `sync` plus its action capability: `upload` for an add,
  `modify` for a replacement, and `delete` for a deletion.
- Unknown actions, share modes, and drift policies fail closed.
- A later preparation or apply service may begin filesystem work only when the
  policy decision has `Apply` set.

This layer does not perform filesystem work and does not implement a two-way
mode. Receiver-side validation remains authoritative even when the sending
source is trusted.

## Target drift

Target drift exists when the target's current revision differs from the base
revision expected by the source change. Timestamps alone do not establish or
clear drift. Each target share must select exactly one behavior:

| Policy | Decision when drift exists | Filesystem work allowed |
|---|---|---|
| `reject` | Reject the change as drifted. | No |
| `preserve_conflict_copy` | Require a conflict copy before applying the authoritative source change. | Yes |
| `report_only` | Report the drift without applying the source change. | No |

When the revisions match, an authorized add, modify, or delete is eligible to
apply regardless of the configured drift behavior. Conflict-copy creation,
auditing, and destination application are intentionally deferred to later
one-way execution slices.

## Receiver preparation boundary

The receiver validates a change request and its advertised source revision
before any transfer work is created. It checks the request and policy scope,
canonical relative path, source revision identity/share/origin, parent revision,
entry type, action compatibility, and the target drift decision. Only after
those checks does it call the authoritative share store for `sync` and the
action capability. The preparation result is a descriptor for a later transfer
slice; this service performs no file, revision, or file-index writes.
