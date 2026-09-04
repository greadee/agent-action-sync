# Supervised Codex Runtime Adapter

- Status: production-composed for the laptop operations sprint Slice 3
- Release boundary: explicitly enabled disposable-project execution only

## Boundary

`internal/codexruntime` is the first concrete adapter behind
`runtimecontract.Adapter`. It supervises one non-interactive Codex CLI process
per runtime session. Construction requires `Enabled: true`, exact versioned
runtime/provider/model/node bindings, absolute authority-local paths, a bounded
concurrency limit, and a resolver that revalidates the opaque workspace against
the exact attempt, lease generation, and fencing digest. The daemon has an
explicit scheduler lifecycle seam, but the shipped command does not construct
this adapter; deterministic fakes remain the default validation path until
explicit local configuration lands.

The invocation uses the documented [`codex exec` non-interactive
surface](https://learn.chatgpt.com/docs/non-interactive-mode) with JSONL events,
a strict output schema, an ephemeral session, an isolated pre-authenticated
`CODEX_HOME`, and prompt input over stdin. Raw prompts, context bundles, stdout,
stderr, command output, process identifiers, provider session identifiers,
credentials, and absolute paths are not written to the adapter's durable
session records.

## Authority and lifecycle

Prepare accepts only a digest-valid execution contract plus exact attempt ID,
lease generation, fencing digest, opaque workspace ID, instruction bytes, and
verified context-bundle bytes. The adapter stores raw input in memory only and
persists a sanitized contract reference and lifecycle binding. Start consumes
that input once. Action keys make duplicate starts deterministic, and one
runtime process can never be started twice for the same prepared session.

Every structured final response must echo the exact contract digest, runtime
session, attempt, lease generation, and fencing digest. The model supplies only
a closed terminal claim. The desktop authority derives the result identity,
builds the complete envelope from the current contract and fenced assignment,
stores it, and performs untrusted result intake before the adapter can report
success. The adapter rejects an unknown field, changed binding, oversized
response, stale fence, failed publication, or non-terminal collection.

Pause and resume fail closed because `codex exec` has no safe resumable pause.
Cancel fences the result before terminating the child context. A wall-clock or
contract deadline produces `timed_out`; a process interrupted by restart is
recovered as `uncertain_termination`. The adapter performs no internal retry,
so one Start consumes one attempt and retry ownership remains with the future
scheduler.

## Enforced execution policy

The adapter applies the execution contract through CLI and supervisor controls:

- concurrency is capped at one or two local executions;
- sandbox mode is `read-only` unless whole-worktree write was explicitly
  granted;
- inspect and write scopes the Codex sandbox cannot represent are rejected;
- forbidden-path exceptions, secrets, web search, network access, dependency
  installation, unsupported capabilities, and automatic approvals are denied;
- the shell tool is enabled only when the contract grants it;
- wall time uses the earlier of contract deadline and maximum wall seconds;
- the documented rollout-budget control receives the contract token ceiling,
  and streamed usage is checked again by the supervisor;
- tool-start events are counted against the contract tool-call ceiling.

The config keys and their maturity are tracked in the official [Codex
configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).
`--strict-config` makes an unsupported or misspelled enforcement setting a
startup failure rather than silently weakening policy. Cost is not inferred:
provider token fields remain nullable evidence, and cost admission/retry policy
belongs to the authority scheduler.

## Privacy and observations

The child receives a minimal environment allowlist plus `CODEX_HOME`. API-key
variables are deliberately excluded. `node-codex-auth-bootstrap` passes the
already-authorized OS-stored provider key to the supported `codex login
--with-api-key` stdin flow for that isolated home; `node-codex-auth-status`
reduces `codex login status` to a boolean. The runtime composition refuses to
start while that status is false. Credentials never enter arguments, config,
SQLite, portable records, logs, prompts, or runtime environment variables.

## Deterministic gate boundary

The production integration service recognizes only exact version-and-digest
built-in gate definitions. `gate:tests` maps to a fixed, non-hooking `git diff
--check HEAD --` command whose arguments and semantic command digest are
closed in code. Output is bounded, reduced to a digest, and never persisted.
Review gates remain pending for the human operator; an optional automated
reviewer may add evidence but is not required to reach the decision stop.

JSONL is reduced immediately to closed progress codes such as `connected`,
`working`, `tool_active`, and `response_complete`. Raw event content and model
reasoning are discarded. Provider-reported token fields are nullable and carry
exact runtime/provider/model provenance. Durable files contain only normalized
lifecycle state, closed failure codes, usage evidence, and result references.

## Verification

Deterministic executor fixtures cover success, duplicate completion and action
replay, malformed and forged output, refusal, rate limit, timeout, disconnect,
tool-budget exhaustion, cancellation, and restart uncertainty. Privacy tests
inspect both command arguments/environment and durable state. The architecture
gate includes `codexruntime` among execution packages forbidden from importing
sync or transport internals. No test invokes the hosted provider.
