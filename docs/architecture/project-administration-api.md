# Agent Project Administration API

## Boundary

Stages 9 and 10 expose Agent Project projections and explicit migration through
the daemon-owned local administration API. The daemon remains the sole owner
of the live SQLite database, project migration, and rebuild services. Clients
cannot open storage directly or read a project root through this surface.

All project routes inherit the existing `/api/v1` controls: loopback-only
binding, bearer authentication, browser-origin and host rejection, bounded
requests, sanitized errors, and `Cache-Control: no-store` responses.

## Routes and response scope

| Route | Purpose | Deliberately excluded |
| --- | --- | --- |
| `GET /api/v1/projects` | Page through registered projects and projection state | Root and manifest paths |
| `GET /api/v1/projects/{project_id}` | Read one project registration | Local filesystem metadata |
| `GET /api/v1/projects/{project_id}/history` | Page and filter allowlisted history metadata | Raw event payloads and transcripts |
| `GET /api/v1/projects/{project_id}/artifacts` | Page and filter artifact metadata | Blob paths and artifact bytes |
| `GET /api/v1/projects/{project_id}/insights` | Page through versioned deterministic insight snapshots | Unregistered or predictive metrics |
| `GET /api/v1/projects/{project_id}/rejections` | Page through sanitized local rejection evidence | Quarantine paths, payloads, and rejected content |
| `POST /api/v1/projects/{project_id}/projections/rebuild` | Rebuild history and insights under daemon supervision | Project bootstrap or migration |
| `POST /api/v1/project-migrations/preflight` | Inspect an eligible configured share and issue a state-bound confirmation | Absolute roots and configuration secrets |
| `POST /api/v1/project-migrations/apply` | Revalidate and register a project after explicit confirmation | Workspace mutation and silent configuration rewrites |

HTTP response types are separate from storage projection types. This makes
privacy allowlists explicit and prevents newly added storage fields from
silently appearing in the API. User-controlled display strings receive the
same path and secret-pattern sanitization used by administration errors.

## Bounded queries

Every collection defaults to 50 results and accepts at most 200. Cursors are
opaque to clients and encode the stable storage ordering:

- projects use project ID;
- history and artifacts use timestamp plus stable record ID;
- insights use metric name plus scope.

History filters are `event_type`, `work_package_id`, and `execution_id`.
Artifact filters are `work_package_id`, `execution_id`, and `media_type`.
Insight filters are `scope` and `metric_name`. IDs, cursors, and filters are
validated before reaching storage. Request cancellation is propagated into all
SQLite queries and rebuild work.

## Freshness and rebuilds

Project responses report history and insight projection states independently:

- `unavailable` means no checkpoint or insight snapshot exists;
- `available` means a current supported projection is present;
- `stale` means insight definitions or the expected definition set do not
  match the stored snapshots;
- `rebuilding` means the project currently owns the rebuild single-flight.

Insight responses also include the definition version and source event
watermark used for each value. Rebuilds are synchronous, cancellable, and
single-flight per project. During shutdown, the API enters draining state and
waits for bounded in-flight HTTP work before SQLite is closed, so a rebuild
cannot race storage teardown.

Stage 10 adds existing-share migration as two separate operations. Preflight is
read-only, bounded, and returns an opaque root identity plus a confirmation
bound to the observed state. Apply requires that confirmation, revalidates the
root, calls the same bootstrap authority used for new projects, requests
ingestion, rebuilds deterministic insights, and writes an allowlisted audit
event. Repeating a completed request is idempotent. Neither operation returns
absolute roots, moves workspace content, or rewrites configuration.

Rejection queries expose only record path, observed hash, reason code, and
timestamp. Sensitive rejected bytes and local quarantine locations remain
available only to local filesystem diagnostics, never through the HTTP API.
