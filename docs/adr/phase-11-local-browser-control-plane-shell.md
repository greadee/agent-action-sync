# ADR: Local browser control-plane shell

## Status

Accepted for desktop browser control plane Slice 5.

## Decision

The desktop node embeds and serves a dependency-free HTML/CSS/JavaScript shell on its existing loopback-only administration listener. A bearer-authenticated local CLI operation mints a bounded, two-minute, one-use bootstrap token. The CLI returns that token only in the URL fragment. The browser removes the fragment before exchanging the token from the exact local origin for an opaque in-memory session represented by an `HttpOnly`, `SameSite=Strict` cookie.

The session bootstrap returns a sanitized capability/status document, not an API index. Its fixed capabilities cover only health and node-status reads. The browser session may authenticate the existing local API, but unsafe methods require both exact-origin validation and a rotating CSRF token. Bearer requests remain non-browser requests and reject any `Origin` header. No endpoint emits permissive CORS headers.

## Rationale

Passing the durable administration bearer to JavaScript, a query string, process arguments, or browser storage would widen the credential boundary and create unnecessary disclosure paths. A one-use fragment bootstrap keeps the bearer in its operating-system-backed store while still making local browser startup practical. A server-side opaque session can be revoked by process shutdown, bounded independently, and protected from JavaScript reads.

Serving static assets from the node preserves one exact loopback origin and avoids a second listener, development proxy, package manager, or CORS policy. A strict content policy and a fixed discovery document keep this slice at shell/bootstrap scope; project, DAG, assignment, and command surfaces remain later slices.

## Consequences

- Restarting the desktop node invalidates browser sessions and unused bootstrap tokens.
- Plain HTTP is retained because the listener is loopback-only; therefore the cookie cannot use `Secure`, but it is host-bound, HttpOnly, strict same-site, opaque, short-lived, and protected by exact-origin plus CSRF checks.
- Browser clients must refresh their CSRF token through the authenticated session document before unsafe operations.
- Adding any capability requires explicit contract, authorization, privacy, and UI review.
