# Work-History Recording Service

Stage 7 introduces `internal/workhistory` as the supported local application
boundary for creating portable work history. Callers submit typed requests and
idempotency identities. The service derives record identifiers and paths,
applies field allowlisting and text redaction, publishes canonical files
atomically, and invokes the Stage 6 projector. It adds no HTTP route.

## Durability boundary

Canonical files in `.agent-project/` are authoritative. SQLite is a rebuildable
projection. If canonical publication succeeds and projection fails, the caller
receives `RecoverableProjectionError` together with stable record IDs and
project-relative paths. The service never removes a canonical record to repair
SQLite; later ingestion or rebuild recovers it.

An operation is idempotent only when its idempotency key resolves to the same
deterministic IDs and its complete canonical content is equivalent. Reusing a
key with different content is a conflict. Publication uses no-replace
filesystem operations, and a process-wide per-project gate serializes local
state validation and publication. The portable format remains single-writer;
multi-writer reconciliation is outside this stage.

## State machines

Work-package transitions are:

- `planned -> ready | canceled`
- `ready -> in_progress | canceled`
- `in_progress -> blocked | review | failed | canceled`
- `blocked -> in_progress | failed | canceled`
- `review -> in_progress | failed`
- `failed -> ready | canceled`
- `review -> accepted` only through acceptance after an approved review

`accepted` and `canceled` are terminal. Invalid transitions publish no event.

Execution transitions are:

- absent -> `running` through execution creation
- `running -> paused | failed | completed`
- `paused -> running | failed`

`failed` and `completed` are terminal. Tests are recorded only while an
execution is running. Handoffs are accepted after failure or completion.

## Privacy and artifacts

Request types contain only portable work metadata; there are no request fields
for prompts or command output. Text fields redact common credential assignments
and absolute Windows or UNC paths. Path fields require canonical
project-relative form. Public results contain only project IDs, record or
content IDs, project-relative paths, and created/already-present flags.

Artifact registration always hashes and verifies a regular source file within
the project root. Optional embedded blobs are streamed to
`.agent-project/artifacts/blobs/sha256/<digest>` through an immutable atomic
publisher. Symlink components, source replacement races, size changes, hash
changes, and conflicting content-addressed destinations are rejected.

## Internal reads

The service exposes bounded get/list methods over the Stage 5 event and
artifact projections. List callers provide limits from 1 through the storage
maximum. State reconstruction is capped at 10,000 accepted events.
