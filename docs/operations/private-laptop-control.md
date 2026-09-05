# Private Laptop Control

## Security boundary

The home SyncGate daemon continues to listen only on loopback. This workflow
does not add a LAN listener, public URL, reverse proxy, durable browser token,
or remote shell endpoint. It relies on your existing authenticated SSH host
alias and forwards the laptop's loopback port to the home machine's loopback
administration port.

The SSH identity, host-key verification, and any MFA remain owned by your SSH
client configuration. Use a named host alias with a pinned host key; do not use
`StrictHostKeyChecking=no`, password text in commands, or a public reverse
tunnel for this workflow.

## Start a session

On the laptop, start the private forward and keep this terminal open for the
duration of remote control:

```text
syncgate node-private-tunnel --ssh-target home-syncgate --local-port 47820 --remote-port 47820
```

`node-private-tunnel` invokes SSH with `-N -T`, an explicit
`127.0.0.1:local:127.0.0.1:remote` forward, `ExitOnForwardFailure=yes`, and no
remote command. Closing it immediately revokes the laptop's network path.

In a second laptop terminal, ask the home host to mint a one-use browser
session for the forwarded laptop port:

```text
ssh home-syncgate syncgate node-remote-ui-session --config C:\SyncGate\config\config.json --tunnel-port 47820
```

Open the returned URL on the laptop before its two-minute bootstrap expiry. Its
credential is a URL fragment, so it is not sent in HTTP requests; the browser
exchanges it once for an opaque, HttpOnly, strict same-site session cookie.
The home administration bearer remains in the home OS credential store and is
never copied to the laptop.

The remote browser uses `http://127.0.0.1:47820` because that is the laptop
side of the authenticated tunnel. The home service still validates that the
arriving host and browser origin are loopback; normal browser session CSRF and
route allowlists continue to apply.

## Stop and recover

Close the tunnel terminal, close the browser tab, or stop the home daemon to
end access. The browser session and unused bootstrap tokens are in memory, so a
home daemon restart invalidates them. Start a new tunnel and mint a fresh URL;
never reuse, bookmark, or share a bootstrap URL.

If the tunnel refuses to start, first verify the SSH host alias independently
and that the home daemon is running. If the URL fails, verify that the local
port in both commands matches and mint a fresh one-use URL.
