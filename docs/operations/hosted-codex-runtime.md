# Hosted Codex Runtime Operations

The home node uses a dedicated Codex home below its configured runtime-cache
root. It does not reuse the interactive desktop or CLI login from another
`CODEX_HOME`.

## Authenticate the isolated home

First store the provider API key in the existing OS credential boundary. The
key is accepted only over bounded stdin:

```powershell
$env:OPENAI_API_KEY | .\syncgate.exe node-credential-set --provider codex --from-stdin
```

Then import that credential through the supported Codex login stdin flow:

```powershell
.\syncgate.exe node-codex-auth-bootstrap
.\syncgate.exe node-codex-auth-status
```

The status response contains only `configured` and the closed storage label
`isolated_codex_home`. Runtime startup fails closed until it reports
`configured: true`. The upstream commands and storage choices are documented
in the official [Codex authentication guidance](https://learn.chatgpt.com/docs/auth).

## Runtime and result flow

1. Enable execution only after the existing disposable-project preflight.
2. Start the node; the scheduler remains paused.
3. Approve the immutable task graph and explicitly enable its project policy.
4. Resume the scheduler.
5. The supervised runtime uses `codex exec --json --ephemeral`, a strict output
   schema, no network, no approvals, and the contract sandbox/budgets. See the
   official [non-interactive mode reference](https://learn.chatgpt.com/docs/non-interactive-mode).
6. On success, the authority builds, stores, and intakes the result envelope.
7. Evaluate the collecting assignment. Exact deterministic gates run, then the
   assignment stops at `awaiting_gates` for an explicit approve/reject action.

Restarted in-flight sessions become `uncertain_termination` and are never
replayed automatically. Refusal, cancellation, timeout, malformed output, and
token/tool budget exhaustion fail the attempt with closed reason codes.
