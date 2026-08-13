# Local Administration API Operations

## Scope and ownership

SyncGate serves its versioned administration API from the daemon process on
the configured loopback address. The daemon remains the only owner of SQLite,
share runtimes, the job queue, pairing state, and shutdown ordering. Operator
commands never fall back to opening the live database.

The API is for local administration only. Version 1 has no remote listener,
browser session, CORS policy, file-content route, configuration mutation,
secret-export route, process-control route, or peer transport endpoint.

## Configuration and startup

Configure `local_api.host` as `127.0.0.1`, `localhost`, or `::1`, and choose an
unused `local_api.port`. Start the daemon normally:

```text
syncgate daemon --config config.json
```

The daemon reserves the listener before starting workers. A bind failure,
credential-store failure, or unexpected HTTP serve failure stops startup or
shuts down the whole daemon. During normal shutdown the API enters `draining`,
rejects new versioned requests, finishes bounded in-flight requests, then the
daemon cancels workers and closes SQLite last.

`GET /healthz` is unauthenticated and returns only process readiness. Every
`/api/v1/*` route requires the local bearer credential. Production stores that
credential in Windows Credential Manager under a target derived from the data
directory. Explicit development mode may use the insecure development file.
The credential is not stored in configuration or SQLite and is never returned
by the API.

## Operator commands

These commands connect to the running daemon through authenticated HTTP:

```text
syncgate daemon-status --config config.json
syncgate scan --config config.json --share SHARE
syncgate job-pause --config config.json --job JOB
syncgate job-resume --config config.json --job JOB
syncgate job-retry --config config.json --job JOB
syncgate pair-create --config config.json --ttl 10m --request read
syncgate pair-inspect --config config.json --invite INVITATION
syncgate pair-accept --config config.json --invite INVITATION --fingerprint FINGERPRINT --code CODE --grant SHARE=read
syncgate pair-revoke --config config.json --device DEVICE
```

Identity migration, daemon startup, configuration validation, diagnostics-file
inspection, and manual one-shot transfers remain local operations. Invitation,
fingerprint, and confirmation-code flags are existing pairing inputs; the
administration bearer credential is loaded from its store and never placed in
process arguments or query strings.

## HTTP behavior and troubleshooting

The canonical contract is
[`docs/protocol/local-admin-api-openapi.json`](../protocol/local-admin-api-openapi.json).
Requests must use the configured loopback host, JSON where a body is required,
and a single bearer `Authorization` header. Browser `Origin` requests and
non-loopback `Host` values are rejected. Responses are JSON with
`Cache-Control: no-store`.

Common failures:

- connection refused: the daemon is stopped, starting, or using another port;
- `401 unauthorized`: the credential is absent, stale, malformed, or from a
  different data directory;
- `403 forbidden`: the request supplied an origin or unsafe host;
- `404 not_found`: the share, device, or job ID is unknown;
- `409`: the requested job or pairing transition conflicts with current state;
- `503 unavailable`: the daemon is starting, draining, or lacks a required
  runtime control.

Errors are deliberately sanitized. Consult local daemon diagnostics when an
HTTP error omits internal filesystem or storage detail.

## Version 1 compatibility policy

The `/api/v1` path is stable for the pre-UI implementation phase.

- Existing methods, paths, required response fields, authentication rules, and
  field meanings remain compatible throughout v1.
- New optional response fields, new error codes for previously generic errors,
  and new routes may be added within v1 when old clients can ignore them.
- New request fields are optional unless a new operation is introduced.
- Removing or renaming a route or field, changing a field type or meaning,
  weakening loopback/authentication policy, or making an optional request field
  required needs a new major API path and an explicit migration plan.
- Deprecations must be documented before removal and remain supported for at
  least one released replacement cycle.

The OpenAPI contract and runtime acceptance suite are release gates. UI,
browser-session, share-reconfiguration, remote-administration, and peer-protocol
work require separate plans and do not extend v1 implicitly.
