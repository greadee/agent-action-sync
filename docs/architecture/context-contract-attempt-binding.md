# Context, contract, and attempt binding

Phase 1 Slice 3 makes an assignment attempt executable only after authority
creates one immutable execution contract and compiles one bounded project
context bundle. `internal/dispatchbinding` performs this sequence before the
orchestration control service records a planned assignment; it never claims a
lease, allocates a workspace, or invokes a runtime.

The persisted attempt binding contains contract and context digests plus a
bounded canonical JSON envelope. That envelope records the task, graph, work
package, trade, worker, instruction, runtime/provider/model/node, effective
permissions, budgets, deadline/retry limit, deliverables, acceptance criteria,
required gates, output schema, source digests, and compiler omission/warning
metadata. It stores neither compiled source contents nor secrets, credentials,
raw prompts, terminal output, provider sessions, or absolute paths.

Changing a source, policy, worker, instruction, or any other contract input
changes the immutable authority and cannot overwrite an existing attempt.
Identical retries reproduce the same context and contract digests and replay
safely. Result intake already validates a result envelope against the stored
contract and context digest; runtime adapters receive only the compiled bundle
and bound contract, never database/history access.

`orchestration_attempt_bindings` is authority-local control state keyed by
attempt. It survives rebuild of canonical project projections and is not a
portable history record.
