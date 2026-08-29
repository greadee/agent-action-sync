# Desktop browser control plane

SyncGate serves a read-only control-plane shell from the desktop node's configured loopback listener. Start the node, then ask the authenticated CLI for a one-time URL:

```text
syncgate node-ui-session --config <node-root>/config/config.json
```

Open the returned URL within two minutes. Its fragment contains a single-use bootstrap token; URL fragments are not sent in HTTP requests. The shell removes the fragment immediately, exchanges the token from the same origin, and receives an opaque `HttpOnly`, `SameSite=Strict` cookie. The long-lived administration bearer is never placed in the URL, DOM, JavaScript-readable storage, configuration, or SQLite.

The initial session document is deliberately narrow. It contains process health, the sanitized node status response, an expiry, a rotating CSRF token, and only these capability identifiers:

- `node.health.read`
- `node.status.read`
- `project.visibility.read`
- `task.readiness.read`
- `assignment.visibility.read`
- `worker-node.inventory.read`

These visibility capabilities disclose bounded, sanitized records and closed reason/evidence identifiers only. They do not expose source, prompt content, credentials, provider sessions, shell output, absolute paths, artifact bytes, or arbitrary discovery routes. Closing the node invalidates all in-memory bootstrap and browser-session state.

The cookie is restricted to this explicit read allowlist: node status, local-project pages, task and readiness pages, assignment pages/details, worker inventory, and node inventory. Other API reads remain bearer-only. Browser mutations are not enabled in this slice; later controls must extend the allowlist deliberately and retain the exact-origin plus CSRF checks.

## Security boundary

- The HTTP listener accepts only `127.0.0.1`, `localhost`, or `::1` bindings.
- Host validation rejects non-loopback authorities and rebinding-shaped values.
- Browser bootstrap and unsafe cookie-authenticated API calls require an exact HTTP `Origin` match to the request host. Unsafe calls also require the current `X-SyncGate-CSRF` value.
- Bearer-authenticated requests continue to reject every `Origin` header.
- The server emits no CORS allow headers and the shell uses a restrictive Content Security Policy, no caching, frame denial, MIME sniffing denial, and same-origin resource isolation.
- Bootstrap tokens live for two minutes and are single-use. Browser sessions live for eight hours, are bounded in count, and exist only in memory as token digests.

A direct visit to `/ui/` without a current cookie displays a protected-session message. Run `node-ui-session` again to establish a new session. A `401` indicates a missing, expired, or invalid session; a `403` indicates an origin, host, or CSRF boundary failure.
