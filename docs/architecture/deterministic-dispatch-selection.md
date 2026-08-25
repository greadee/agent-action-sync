# Deterministic Dispatch Selection

- Status: implemented for Phase 1 Slice 2
- Scope: fact-only worker/node proposal selection before immutable contract
  binding, workspace allocation, lease claim, or runtime start

## Boundary

`orchestration.Select` is a pure policy reducer. It accepts the trusted local
task/readiness projections, an immutable work-package definition/digest,
registered worker/trade facts, current node observations, explicit runtime
capabilities, and an operator-approved policy snapshot. It returns a selected
candidate or a blocked explanation; it never invokes a provider, acquires a
node lease, creates an assignment, or writes portable history.

Telemetry is deliberately absent from the request and result types. Sparse
observations, evidence scores, provider/model labels, and optional capability
matches cannot influence a Phase 1 choice.

## Ordered policy

Every candidate is considered in this fixed order. A failure at one stage stops
evaluation of later stages for that candidate, producing only the relevant
closed reason code(s).

1. The task projection, ready work-package node, immutable definition record,
   revisions, project identity, and optional active project-trade adaptation
   must match exactly.
2. The active trade reference and active worker's trade reference must match;
   worker tags must satisfy all required trade/adaptation capability tags.
3. The worker's declared runtime ID/version must match the selected runtime,
   the runtime must be available, and the node must be eligible from its
   verified definition plus a current healthy observation.
4. Each requested runtime capability must be allowed by the user policy and
   offered by the runtime.
5. Work risk must not exceed the policy ceiling, and every required gate must
   have exact configured preflight evidence. This confirms gate configuration;
   it does not claim the later result-time gate is satisfied.
6. Requested token, cost, wall-clock, and concurrency bounds must fit the
   policy. Phase 1 rejects a policy above two concurrent workers.
7. Remaining eligible worker/node pairs use the explicit ordered preference
   list. Ties are broken by worker ID/version, node ID/version, then runtime
   ID/version.

An empty eligible set is `blocked`; the selector does not silently choose a
weaker worker, broader runtime, offline node, or over-budget option.

## Identity separation

Trade matching uses only the immutable trade reference and worker trade
ID/version. Provider, model, and model-version stay on the candidate identity
as separate descriptive fields. They are neither aliases for a trade nor a
ranking signal. Slice 3 will bind the selected runtime/provider/model digests
into the immutable execution contract.

## Operator override

An override names an exact eligible worker/node pair and has an actor and
closed reason code. It cannot bypass any safety filter. The selection result
returns `operator_override` as the required assignment audit reason; creating
the subsequent new assignment applies that result through
`ApplySelectionToPlan` and then `ControlService.Plan`, which persists it as an
`assign` audit event under the overriding actor. A selector call has no hidden
mutable preference state.

## Verification

Focused tests prove stable selection when candidate input order reverses,
separate provider/model identity, project-authority mismatch, missing required
tags, unavailable runtime, unhealthy node, denied permissions, missing gate
evidence, budget/concurrency limits, explicit eligible override, rejected
ineligible override, bounded Phase 1 concurrency, and unknown capability
rejection. SQLite coverage also proves the propagated override reason is
retained by the new assignment audit event.
