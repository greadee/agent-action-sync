# Telemetry, Provenance, and Memory Inputs

Slice 7 adds authority-local, restart-safe execution telemetry. It is evidence
for review and descriptive calculations, not execution control, workforce
learning, or a runtime transport protocol.

## Evidence boundary

`internal/telemetry` accepts a versioned envelope only after matching every
identity and provenance reference against the immutable execution contract:
project/task/graph/work package/execution, contract, trade/worker,
instruction/context, runtime/provider/model, and compute node. The envelope is
idempotent by telemetry ID and by project-execution idempotency digest.

Each observation is nullable. An absent token, provider-cost, or duration value
means **unknown**, never zero. Values are bounded counts and carry exactly one
evidence source: `provider_reported`, `locally_measured`, `worker_claimed`, or
`reviewer_verified`.

The supported observations cover duration, tokens, provider cost, tool calls,
files inspected/changed, tests, retries, runtime/tool errors, review findings,
rework, conflicts, intervention, rollback, and context size. Evidence itself is
only a typed ID/digest reference.

## Portable history and privacy

The SQLite `execution_telemetry` table retains the accepted canonical envelope
locally. `workhistory.RecordTelemetry` can publish only
`TELEMETRY_RECORDED`, whose closed payload is the allowlisted summary and its
digests. Raw prompts, terminal output, secrets, provider credentials, absolute
paths, and raw evidence bytes are not fields in either contract. Raw material
must remain in a separately approved local artifact.

The summary event is projection-rebuildable. Descriptive insights expose only
outcome counts and nullable resource aggregates, including known/unknown counts
and a minimum-sample warning for fewer than three accepted telemetry summaries.

## Memory inputs

`telemetry.SummaryCandidate` deterministically derives a success or failure
candidate from an accepted summary and known observations. It performs no LLM
call, promotion, routing update, cross-project aggregation, or global/workforce
learning. Any later promotion policy needs its own authority, privacy, and
evaluation decision.

## Explicitly disabled

Production runtime execution, telemetry upload transports, raw transcript
capture, automatic remediation, automatic promotion, and learned routing remain
disabled.
