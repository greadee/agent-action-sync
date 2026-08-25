# Project Context Compiler v1

`internal/contextcompiler` converts an explicit set of canonical project and
workspace sources into byte-deterministic context for one resolved trade and
work package. It performs no semantic retrieval, model call, worker selection,
or runtime execution.

## Inputs and selection

Every request binds the project ID, work-package ID, immutable trade
ID/version/digest, source list, per-source byte limit, total bundle-byte limit,
and estimated-token limit. The compiler always loads the canonical work-package
definition and verifies its project and resolved trade reference.

Source kinds are closed: repository file/document, ADR, project history, task
graph, and test expectation. Workspace sources must be under `workspace/` and
inside the work package's allowed or inspect scope; forbidden scope takes
precedence. Trade filters exclude irrelevant sources before reading them.

Portable sources are decoded with the Agent Project contract. Task sources are
limited to the current task/graph revision. History sources are limited to the
same work package and state, failure, test, review, or acceptance events. An
explicit path alone does not bypass this allowlist.

## Privacy and bounds

Public and project-class sources may be included. Sensitive and secret-class
sources become omissions. The compiler also excludes `.git`, `.env`, secrets,
local/quarantine state, prompt/transcript/terminal paths, private keys,
symlinks, binary content, oversized files, unsafe paths, and missing sources.
Credential assignments, selected environment assignments, and absolute paths
are deterministically redacted and reported as warnings.

Sources are sorted by canonical relative path before rendering. Byte and token
budgets are applied in that order, producing stable `bundle_budget` omissions.
All omissions and warnings have stable codes and ordering.

## Output and identity

The JSON bundle contains the compiler version `context-compiler:v1`, project,
work-package, and resolved trade identities, a structured source index with
digests and redaction flags, a briefing, stable omissions and warnings,
estimated tokens, and a deterministic context digest.

Equal canonical inputs and compiler version produce byte-equal JSON and the
same digest, independent of input enumeration order or process restart. The
digest is passed as `provenance.context_version` when an execution or artifact
records the bundle.

## Cache and optional publication

Bundle bytes are cached by digest under
`.agent-project/local/context-cache/<digest>.json`. Cache publication is
no-replace and validates an existing file before treating replay as
idempotent. This directory is rebuildable local state and is excluded from
sync.

An approved bundle may be published only by calling the existing
authority-owned work-history artifact operation. That operation hashes the
local cache source, creates the immutable content-addressed blob and artifact
manifest, records provenance, and triggers normal projection. The compiler has
no direct canonical publication method.
