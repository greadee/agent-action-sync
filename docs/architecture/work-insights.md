# Deterministic Work Insights

`internal/insights` derives local, versioned summaries from accepted
`project_events` rows. Insights are a rebuildable SQLite projection; they are
not portable records and never alter canonical work history.

Each snapshot contains the project scope, metric name and definition version,
SHA-256 watermark of the accepted source event set, optional window fields,
structured JSON value, sample count, completeness, evidence strength, and the
calculation timestamp. Definitions are an explicit registry, currently at
version 1. Rebuild replacement deletes prior snapshots for the project and
writes the complete registry atomically.

The initial registry contains only metrics available from stages 1–7:

- work-package active, blocked, failed, accepted, and completed-execution
  counts;
- completion and first-pass acceptance rates;
- execution and creation-to-acceptance durations when both timestamps exist;
- test outcomes, retry/rework counts, artifact/handoff counts;
- activity grouped by worker, model, provider, and device when present; and
- projection freshness, including accepted/pending event counts.

Missing fields remain unknown. Empty samples are `insufficient/none`; small
samples or any pending event are `partial/weak`; otherwise snapshots are
`complete/strong`. The calculator never treats unknown duration, outcome, or
producer metadata as zero or a success.

History projection rebuilds explicitly delete project insights in the same
transaction. This invalidates a derived snapshot when the event set changes;
the calculator then rebuilds a fresh versioned result. Recalculation sorts its
accepted-event watermark by event identity, so tied timestamps and late,
out-of-order arrival converge with a clean rebuild.

No LLM, embeddings, worker ranking, prediction, cost inference, dashboard, or
HTTP endpoint is introduced in this stage. Stage 9 may expose only these
already-derived, bounded snapshots.
