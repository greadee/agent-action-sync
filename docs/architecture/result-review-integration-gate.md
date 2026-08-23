# Result, Review, and Human Integration Gate

- Status: implemented for Phase 1 Slice 7
- Release boundary: authority-owned library component; operator API composition is Slice 8

## Authority boundary

`internal/integrationgate` is the only Phase 1 coordinator that turns a
runtime's untrusted result into reviewable canonical evidence. The scheduler
still stops at `collecting` and cannot import this package, result intake, or
work-history writers. The gate resolves the exact contract and current attempt,
collects from the bound runtime session, and passes the signed envelope through
`resultintake` before any worker claim is used.

The result source and content source are reference-only seams. Every envelope,
patch, artifact, and handoff is bounded and checked against its declared SHA-256
digest and size. Artifact bytes stay outside portable history; only verified
name, media type, size, digest, and provenance are recorded.

## Change and integration checks

The Git worktree manager emits a stable manifest containing base/head commits,
relative paths, status, digest, size, and binary classification. It rejects
out-of-contract paths, symlinks/non-regular files, more than 4,096 files, files
over 64 MiB, and aggregate output over 256 MiB. Binary files fail the
integration gate unless trusted application configuration explicitly allows
their path.

`PreviewIntegration` reads the primary and worker heads and invokes
non-mutating `git merge-tree`. A changed primary head marks the result stale;
conflict markers block approval. Preview never changes refs, the index, or a
worktree, and this slice exposes no merge operation.

## Deterministic machine gates

Each non-review contract gate must have an authority-configured `TestCommand`
whose gate ID/version/digest match the immutable contract. A separately
injected `TestRunner` receives that exact command identity. Portable
`TEST_RECORDED` evidence includes command ID/digest, exit code, measured
duration, and evidence ID/digest. Local gate status moves only from pending to
satisfied or failed. A worker-reported test is never substituted for this
run.

Failed tests, malformed or forged references, stale attempts, unexpected
binaries, stale bases, conflicts, and reviewer changes block acceptance. The
authority records the available terminal execution/work-package evidence and
moves local assignment control to `failed`; none publishes `WORK_ACCEPTED`.

## Review and explicit decision

When `review_required` is bound into the execution contract, an injected
reviewer must return an approved review before a summary can be presented.
That reviewer result is evidence, not the human decision. The sanitized
`IntegrationSummary` presents only opaque identities, base/current/head
commits, the relative file manifest, exact test evidence, review result,
artifact metadata, limitations, and unresolved issues. It cannot represent
raw bytes, project/worktree roots, runtime/provider sessions, prompts, or logs.

The summary digest is persisted as a pending integration-preview gate. On an
operator decision, the authority reloads the exact attempt and contract,
re-inspects the worktree, re-runs the merge preview, verifies the summary and
every machine gate, and records the operator decision. Rejection fails local
control without accepting work. Approval publishes the human approved review
and existing `WORK_ACCEPTED` event through `workhistory`, then transitions the
local assignment to `accepted`. Replaying that exact accepted decision is
idempotent. There is no automatic merge.

## Portable history and recovery

Tests, artifacts, handoffs, reviews, and acceptance use the existing portable
record vocabulary and `workhistory` writer. Local intake decisions, assignment
state, gate status, and operator decisions remain authority-local SQLite state.
A clean SQLite projection rebuilt from the portable project root reproduces
the terminal accepted history.
