# ADR 0005: Browser Portal Is Restricted Access

Status: accepted

## Context

Unmanaged school or lab computers may be monitored, locked down, or cleared after logout. Native clients cannot be assumed to run there.

## Decision

The browser portal will be HTTPS-only, short-lived, revocable, and limited to explicitly exposed virtual shares. Upload-only shares cannot list existing files.

## Consequences

- The portal is not a remote filesystem browser.
- Browser access requires separate session, quota, and audit controls.
- Portal implementation is deferred until after local transfer and core authorization are reliable.
