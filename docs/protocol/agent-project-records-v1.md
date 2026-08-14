# Agent Project Portable Records v1

## Status and scope

This document defines the on-disk JSON contract for Agent Project records with
schema family `syncgate.agent-project`, major version `1`, and initial minor
version `0`.

These records are the portable project-history source of truth described by the
[Phase 4 architecture decision](../adr/phase-4-agent-project-foundation.md).
This contract does not define filesystem publication, ingestion, SQLite
projection, HTTP routes, or insight calculation.

## Common envelope

Every record is one JSON object containing these fields:

| Field | Type | Required | Meaning |
|---|---|---:|---|
| `schema.family` | string | yes | Must be `syncgate.agent-project`. |
| `schema.major` | integer | yes | Must be `1`. |
| `schema.minor` | integer | yes | Non-negative compatible minor version. |
| `record_kind` | enum | yes | Selects one supported record contract. |
| `record_id` | identifier | yes | Immutable identity of this record. |
| `project_id` | identifier | yes | Project manifest identity. |
| `integrity.algorithm` | string | yes | Must be `sha256`. |
| `integrity.digest` | string | yes | Lowercase 64-character SHA-256 digest. |

Supported `record_kind` values are:

- `project_manifest`
- `work_package_definition`
- `execution_manifest`
- `work_event`
- `handoff`
- `artifact_manifest`

An unknown record kind is not accepted under v1 even if its envelope is valid.

## Canonical JSON and integrity

Portable records use the following canonical representation:

- UTF-8 JSON with one top-level object and no trailing value.
- No insignificant whitespace.
- Object keys sorted lexicographically by their UTF-8 string value.
- JSON strings escaped using Go `encoding/json` rules.
- Integers use base-10 notation with no leading zero, decimal point, or exponent.
- Integers must fit a signed 64-bit value.
- Duplicate object keys are forbidden at every depth.
- Arrays preserve their declared order.

To calculate `integrity.digest`:

1. Remove the complete top-level `integrity` field.
2. Encode the remaining object using the canonical rules above.
3. Calculate SHA-256 over those exact bytes.
4. Encode the digest as lowercase hexadecimal.
5. Add `integrity` and canonically encode the complete record.

The integrity field detects corruption and same-ID/different-content collisions.
It is not a signature and does not prove that a producing device is honest.

## Compatibility rules

A reader:

- Rejects an unknown schema family or unsupported major version.
- Accepts a non-negative newer minor version when all v1 invariants still hold.
- Accepts bounded unknown additive fields outside event payloads and includes
  them in the integrity calculation.
- Preserves the complete canonical source bytes alongside the typed supported
  view so additive fields are not discarded by ingestion.
- Rejects unknown record kinds and event types until their state and privacy
  semantics are implemented.
- Rejects unknown event-payload fields. Event payloads are closed and typed to
  prevent arbitrary prompt, command, environment, or tool transcript capture.

A minor version cannot remove, rename, or reinterpret an existing field. Such a
change requires a new major version.

## Global bounds and scalar rules

| Item | v1 limit or rule |
|---|---|
| Complete record | 1 MiB |
| Identifier | 1-128 bytes; `A-Z`, `a-z`, `0-9`, `.`, `_`, `:`, `-`; begins alphanumeric |
| Name | 1-256 valid UTF-8 bytes |
| Short version text | at most 1,024 valid UTF-8 bytes |
| General text | at most 8,192 valid UTF-8 bytes |
| Relative path | at most 4,096 bytes; canonical forward slashes; no absolute, traversal, reserved Windows, trailing-dot, or trailing-space segment |
| Array or object | at most 256 entries |
| JSON nesting | at most 32 levels |
| Timestamp | RFC 3339 JSON timestamp with UTC offset zero; writers emit `Z` and RFC 3339 nanosecond precision |
| JSON strings | valid UTF-8 without NUL |

Unless stated otherwise, list order is meaningful. Identifier and relative-path
reference lists must not contain duplicates.

Identifiers used as directory or file names are converted to an ASCII lowercase
path key. For example, work-package IDs `WP-001` and `wp-001` both address
`.agent-project/work-packages/wp-001/definition.json`. Their record content is
still case-sensitive, so attempting to publish both is a same-path/different-
content conflict on every supported filesystem. This prevents Windows and Unix
replicas from producing different histories because of filesystem case rules.

Portable writers derive paths from typed records; callers do not supply final
control paths. Writers publish a synced same-directory temporary file ending in
`.sync-part` through an atomic no-replace operation. Temporary files are never
portable records and are ignored by canonical discovery.

## Shared objects

### Authority

```json
{
  "device_id": "device-one",
  "share_id": "share-one"
}
```

Both fields are required identifiers. The authority names the initial one-way
source device and share. It does not bypass receiver authorization.

### Producer

| Field | Required | Rule |
|---|---:|---|
| `device_id` | yes | Identifier of the producing device. |
| `worker_id` | no | Stable worker identifier when known. |
| `trade` | no | Name text. |
| `specialization` | no | Name text. |
| `provider` | no | Name text. |
| `model` | no | Name text. |
| `model_version` | no | Name text. |

Absence means unknown. It does not mean an empty or zero-valued producer.

### Provenance

| Field | Required | Rule |
|---|---:|---|
| `producer` | yes | Valid Producer object. |
| `work_package_id` | no | Work-package identifier. Required where the containing record requires it. |
| `execution_id` | no | Execution identifier. Required where the containing record requires it. |
| `source_artifact_ids` | no | Unique artifact identifiers. |
| `project_version` | no | Bounded project version or revision reference. |
| `context_version` | no | Bounded compiled-context version reference. |
| `instruction_version` | no | Bounded instruction version reference. |
| `created_at` | yes | UTC timestamp. |

Provenance references are claims within project history. This contract validates
their shape and containing scope, not external truth.

## Project manifest

`record_kind` is `project_manifest`.

| Field | Required | Rule |
|---|---:|---|
| `name` | yes | Project name. |
| `authority` | yes | Valid Authority. |
| `created_at` | yes | UTC timestamp. |

The `project_id` in the common envelope is the project identity to which all
other records bind. A caller that selected a manifest must reject a record with
a different `project_id`.

## Work-package definition

`record_kind` is `work_package_definition`.

| Field | Required | Rule |
|---|---:|---|
| `work_package_id` | yes | Stable work-package identifier. |
| `objective` | yes | General text. |
| `trade` | yes | Name text. |
| `specialization` | no | Name text. |
| `scope.allowed` | no | Unique canonical relative paths. |
| `scope.inspect` | no | Unique canonical relative paths. |
| `scope.forbidden` | no | Unique canonical relative paths. |
| `dependencies` | no | Unique work-package identifiers; cannot include itself. |
| `deliverables` | yes | One or more general-text items. |
| `acceptance_criteria` | yes | One or more general-text items. |
| `review_required` | yes | Boolean. |
| `created_at` | yes | UTC timestamp. |
| `provenance` | yes | If it contains `work_package_id`, it must match this record. |

## Execution manifest

`record_kind` is `execution_manifest`.

| Field | Required | Rule |
|---|---:|---|
| `execution_id` | yes | Stable execution identifier. |
| `work_package_id` | yes | Parent work-package identifier. |
| `state` | yes | `pending`, `running`, `paused`, `failed`, or `completed`. |
| `producer` | yes | Valid Producer. |
| `created_at` | yes | UTC timestamp. |
| `provenance` | yes | Work-package and execution IDs must exactly match this record. |

State-transition legality is enforced by the later work-history service. This
contract validates only the portable manifest state vocabulary.

## Work event

`record_kind` is `work_event`. The common `record_id` is the event identity.

| Field | Required | Rule |
|---|---:|---|
| `event_type` | yes | One initial event type listed below. |
| `occurred_at` | yes | UTC timestamp. |
| `work_package_id` | event-dependent | Work-package identifier. |
| `execution_id` | event-dependent | Execution identifier. |
| `producer` | yes | Valid Producer. |
| `causation_id` | no | A different work-event record ID. |
| `correlation.audit_id` | no | Local operational audit correlation. |
| `correlation.revision_id` | no | Sync revision correlation. |
| `payload` | yes | Closed typed object selected by `event_type`. |

An execution-scoped event always requires a work package. A correlation object
must contain at least one correlation ID. Correlation does not transfer authority
between audit, sync revision, and work history.

### Initial event payloads

| Event type | Required scope | Payload fields |
|---|---|---|
| `PROJECT_REGISTERED` | neither work package nor execution | `manifest_record_id` identifier; `authority` object |
| `WORK_PACKAGE_CREATED` | work package | `definition_record_id` identifier |
| `WORK_PACKAGE_STATE_CHANGED` | work package | distinct `from` and `to` states; optional `reason_code` identifier |
| `EXECUTION_STARTED` | work package and execution | `manifest_record_id` identifier |
| `EXECUTION_PAUSED` | work package and execution | `reason_code` identifier |
| `EXECUTION_FAILED` | work package and execution | `failure_code` identifier; optional `summary` text |
| `EXECUTION_COMPLETED` | work package and execution | optional `summary` text |
| `TEST_RECORDED` | work package and execution | `name`; `outcome`; optional non-negative `duration_milliseconds` |
| `HANDOFF_CREATED` | work package and execution | `handoff_record_id` identifier |
| `REVIEW_RECORDED` | work package; execution optional | `outcome`; `reviewer_id`; optional `summary` |
| `ARTIFACT_RECORDED` | work package; execution optional | `artifact_record_id`; `artifact_id` |
| `WORK_ACCEPTED` | work package; execution optional | `accepted_by`; optional `summary` |

Work-package states are `planned`, `ready`, `in_progress`, `blocked`, `review`,
`accepted`, `failed`, and `canceled`.

Test outcomes are `passed`, `failed`, and `skipped`. Review outcomes are
`approved`, `rejected`, and `changes_requested`.

Payloads intentionally have no arbitrary metadata, prompt, command, environment,
terminal-output, or tool-transcript field.

## Handoff

`record_kind` is `handoff`.

Required fields:

- `handoff_id`, `work_package_id`, and `execution_id` identifiers.
- `completed_work` with at least one general-text item.
- `confidence`: `low`, `medium`, or `high`.
- `created_at` UTC timestamp.
- `provenance` whose work-package and execution IDs match the handoff.

Optional bounded lists:

- `changed_files`: canonical relative paths.
- `decisions`.
- `tests`: objects with required `name` and `outcome`.
- `limitations`.
- `unresolved_issues`.
- `assumptions`.
- `follow_up_work`.
- `review_requirements`.
- `integration_considerations`.
- `failure_conditions`.

## Artifact manifest

`record_kind` is `artifact_manifest`.

| Field | Required | Rule |
|---|---:|---|
| `artifact_id` | yes | Stable artifact identifier. |
| `name` | yes | Artifact display name. |
| `media_type` | yes | `type/subtype` without parameters or whitespace. |
| `size` | yes | Non-negative byte count. |
| `hash_algorithm` | yes | Must be `sha256`. |
| `content_hash` | yes | Lowercase SHA-256 digest of artifact bytes. |
| `blob_relative_path` | no | If present, exactly `artifacts/blobs/sha256/<content_hash>`, relative to `.agent-project/`. |
| `created_at` | yes | UTC timestamp. |
| `provenance` | yes | Valid Provenance. |

Artifact bytes are opaque during contract decoding. Readers verify size and hash
when bytes are registered or ingested; they do not execute, render, or otherwise
interpret a blob based on its media type.

## Reference fixtures

Canonical examples live in `internal/project/testdata/`. Contract tests verify
their integrity, typed decoding, canonical form, and deterministic round trip.
