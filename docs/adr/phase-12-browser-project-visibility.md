# ADR: Browser project, DAG, and assignment visibility

## Status

Accepted for desktop browser control plane Slice 6.

## Decision

The embedded browser shell reads the established paginated local administration projections for registered projects, task graphs, readiness nodes, assignments, assignment details, workers, and nodes. The browser’s project picker is local UI state only: it does not call the persisted project-selection endpoint. The shell does not render operator controls, and it performs no API mutation other than the existing session bootstrap.

The browser-session cookie is allowlisted to those exact GET routes plus node status. It cannot read unrelated authenticated inventory or configuration-adjacent routes, and it cannot invoke a command even when an origin and CSRF header are present.

Readiness is rendered as a bounded work-package DAG: every visible node carries its closed state, closed explanation code, definition evidence ID, barrier marker, and opaque dependency IDs. Assignment detail renders the selected assignment’s attempts, recovery disposition, failure code, gates, and gate evidence IDs. The UI uses normal selects and buttons, focus-visible styling, live status text, load-more pagination, and explicit loading, empty, and error states.

## Rationale

The existing daemon-owned projections already enforce project authority, bounded records, pagination, no-store responses, and sanitization. Reusing them avoids a browser-specific data store or a broad discovery API. Closed reason codes and opaque evidence IDs provide explainability without leaking prompts, source content, local filesystem locations, artifact bytes, provider sessions, or raw runtime output.

## Consequences

- The browser can show why work is ready, blocked, assigned, failed, collecting, or awaiting a decision only when the corresponding daemon projection exists.
- Empty states are expected on a newly initialized node; controls and synthetic placeholder work are intentionally absent.
- Assignment actions, scheduler changes, project selection persistence, and approvals remain Slice 7 work and require the existing CSRF-protected mutation pathway plus explicit confirmation design.
