# Telemetry, Provenance, and Memory Inputs

Slice 4 adds authority-local, restart-safe execution telemetry. It is evidence
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

The supported observations cover total duration, queue, active-runtime, gate,
review, and human-wait durations; input/cached-input/output/reasoning tokens;
provider cost; tool calls; files inspected/changed; tests; retries;
runtime/tool errors; review findings; rework; conflicts; intervention;
rollback; and context size. Evidence itself is only a typed ID/digest
reference.

The local scheduler writes terminal telemetry after its authoritative state
transition using the runtime adapter's `UpdatedAt` time, rather than the later
poll time. It writes `partial` success when gate/review/decision boundaries do
not exist yet. After an explicit operator approval, the integration gate writes
a `succeeded` summary and its portable history event. Queue is
planned-to-running, active runtime is running-to-runtime-terminal, gate time
is collecting-to-awaiting-gates, and review/human wait are
awaiting-gates-to-accepted. If either boundary is missing, the duration is
null. There are no synthetic zero measurements.

## Portable history and privacy

The SQLite `execution_telemetry` table retains the accepted canonical envelope
locally. `workhistory.RecordTelemetry` can publish only
`TELEMETRY_RECORDED`, whose closed payload is the allowlisted summary and its
digests. Raw prompts, terminal output, secrets, provider credentials, absolute
paths, and raw evidence bytes are not fields in either contract. Raw material
must remain in a separately approved local artifact.

The summary event is projection-rebuildable. Descriptive insights expose only
outcome counts, the existing accepted-work/completion rate, and nullable timing
and resource aggregates, including known/unknown counts and a minimum-sample
warning for fewer than three accepted telemetry summaries.

## Descriptive orchestration insights

Slice 9 adds versioned project-scoped descriptive metrics. They calculate queue
(created to ready), execution, review, acceptance, blocked, and parallel-overlap
durations from accepted canonical events. Preparation and gate durations remain
explicitly `known: false` until canonical events can identify their boundaries;
they are never inferred as zero. Attempts, retries, failures, cancellations,
and blocked/uncertain terminations are counts, not predictions or rankings.

`telemetry_versioned_groups` hashes the exact worker, trade, provider, model,
runtime, context, and instruction references into a stable opaque group key.
It publishes aggregates only once a group has at least three accepted samples.
Smaller groups are counted as suppressed and mark the metric weak, preventing a
misleading worker or crew comparison. Every insight carries its accepted-event
watermark, sample count, completeness, and evidence class. Rejected or pending
records are excluded before any outcome or resource calculation.

## Memory inputs

`telemetry.SummaryCandidate` deterministically derives a success or failure
candidate from an accepted summary and known observations. It performs no LLM
call, promotion, routing update, cross-project aggregation, or global/workforce
learning. Any later promotion policy needs its own authority, privacy, and
evaluation decision.

## Explicitly disabled

Telemetry upload transports, raw transcript capture, automatic remediation,
automatic promotion, and learned routing remain disabled. Production runtime
execution is enabled only through the separately authorized local desktop
composition and remains subject to its task, dispatch, and runtime gates.
