# Trade and Worker Registry

Slice 3 adds a provider-neutral, local durable registry. It does not start a
worker, select a worker, create a lease, or make the registry portable project
truth.

## Definitions and ownership

`internal/registry` owns deterministic validation, matching, import/export,
and audit orchestration. `internal/storage` owns local SQLite persistence. The
registry contains no runtime adapter implementation, transport integration, or
credential field.

- A global **trade definition** describes reusable capability. It has a stable
  `trade:` ID, immutable positive version, lifecycle, capability tags,
  required/optional capabilities, evidence metadata, and content digest.
- A global **worker profile** has a stable `worker:` ID and immutable version.
  It binds one trade version to explicit instruction, runtime, provider/model,
  and tool-policy references. Different workers can use the same model while
  remaining separately identifiable and measurable.
- A **project trade adaptation** is a local per-project version that references
  a global trade version. Its write API requires the caller's authorized project
  ID to equal the adaptation project ID and verifies that project is registered.
  It cannot be used to mutate another project's adaptation.

Lifecycle changes are new immutable versions. There is no destructive update
or delete path for a definition that may be referenced by history.

## Matching and evidence

Capability matching is explicit set comparison: offered tags are compared with
required and optional tags. The result reports missing required tags and
matched optional tags. It does not rank workers or fabricate a score. An absent
score remains `null`/unknown even when tags match.

Evidence metadata is bounded descriptive input only. It is not an optimizer or
authorization decision.

## Import, export, and audit

Registry bundles use `syncgate.registry.v1`, strict JSON decoding, sorted
definitions, and deterministic content hashes. Re-importing equal content is
idempotent; a reused ID/version with different content fails as an immutable
conflict. Unknown bundle fields are rejected, preventing credentials or opaque
provider payloads from being silently accepted.

Every newly created trade, worker, or adaptation writes a local audit event
with action, actor, subject, optional project, content digest, and time. Audit
rows do not carry arbitrary metadata.

## Portable project references

Schema minor 2 adds optional `trade_reference` on work packages and optional
`trade_reference`/`worker_reference` on execution manifests. Each is only an
ID, version, and SHA-256 digest. These fields preserve the definitions resolved
at creation/start time without copying local registry configuration into the
Agent Project record. Credentials, access tokens, secrets, endpoints, runtime
handles, and provider sessions are neither registry record fields nor portable
reference fields.
