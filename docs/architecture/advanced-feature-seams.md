# Advanced-Feature Seams and Evidence Gates

Slice 8 defines read-only, provider-neutral contracts for later
evidence-dependent features. Every shipped implementation is disabled. These
interfaces do not alter the Agent Project schema and cannot write storage,
schedule work, change permissions, satisfy a gate, accept work, or publish
canonical history.

## Disabled seams

| Seam | Input boundary | Disabled result and fallback |
| --- | --- | --- |
| Similarity index | Approved work-package/artifact stable IDs, versions, digests, and approval IDs | No index or hits; deterministic policy |
| Retrospective generator | Approved source digests for one project/work package | No proposed artifact; a future proposal is explicitly non-authoritative |
| Duration/cost/success estimator | Exact contract, trade, worker, runtime, provider, model, node, and telemetry watermark references | Unknown estimates, confidence `none`, sample count `0`, version `1`, and `feature_disabled` |
| Crew recommender | Exact project/work-package/trade references and bounded worker alternatives | No alternatives; explicit user selection |
| Dashboard/timeline reader | Project and accepted projection watermark with bounded page size/cursor | No cards or events; deterministic existing projection/query policy |
| Multi-writer conflict engine | Project and base/left/right projection references plus conflict-set digest | Preview unavailable, manual review, and `automatic_merge=false` |

The conflict interface has only `Preview`; it has no merge, acceptance, or
publication operation. Provider and model values cross these seams only as
stable references. Provider-specific payloads, credentials, session data, and
SDK types belong behind later adapters.

Every unavailable response has an explicit fallback. A caller must use current
deterministic policy, request user selection, perform manual review, or create
no generated artifact. Absence is never converted into a score, estimate,
recommendation, or successful result.

## Evidence and evaluation gates

`advancedfeatures.EvidenceGates` is a versioned prerequisites catalog, not an
enablement switch. Every entry remains `disabled`, requires explicit release
approval, and requires at least the following:

| Feature | Minimum evidence | Required evaluations |
| --- | --- | --- |
| Similarity | 100 approved digest corpus items; 20 deletion/staleness cases | Held-out retrieval quality; privacy, deletion, and stale-index behavior |
| Retrospectives | 50 approved source sets; 50 independent usefulness reviews | Source attribution/factuality; consent/redaction/access/retention/deletion; non-authority |
| Estimator | 100 complete reviewed samples per supported cohort; 30 missingness cases | Held-out calibration/coverage; versioned error bounds and drift; undersampled fallback |
| Crew advice | 100 reviewed outcomes across supported trades; 30 cold-start cases | Selection quality; fairness and confounds; explicit-selection fallback |
| Dashboard/timeline | 50 clean-rebuild fixtures; 30 privacy-boundary cases | Projection parity; bounded pagination, cancellation, load, and staleness |
| Multi-writer/conflicts | Approved protocol ADR; 100 conflict cases; 20 recovery drills | Convergence/replay; no automatic publication; rollback and security isolation |

Meeting a count alone is insufficient. A later release must define quantitative
thresholds, pass the named evaluations, add feature-specific threat-model and
operations coverage, record approval, and replace exactly one disabled adapter.

## Data prohibited from the seams

Inputs and outputs contain only typed scalar summaries and stable references.
Raw prompts, terminal/tool output, transcripts, secret values, credentials,
absolute paths, artifact bytes, worktree handles, runtime sessions, and provider
objects are not representable.

Embeddings, LLM generation, learned scores, predictions, learned routing,
automatic conflict resolution, raw capture, and multi-writer publication remain
disabled.
