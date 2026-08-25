# Orchestration Pilot and Recovery Matrix

Slice 10 provides a deterministic, disposable-project pilot gate. It proves
the Phase 1 boundaries without enabling the shipped daemon's runtime adapter,
workspace allocation, or remote execution.

Run from the repository root:

```powershell
powershell -ExecutionPolicy Bypass -File tools\check_orchestration_pilot.ps1
```

The deterministic matrix proves two independent work packages can overlap at
the two-worker ceiling, pause and resume safely, and leave a dependent
integration package waiting until canonical acceptance exists. Cancellation
preserves the allocated workspace for inspection. The focused suite also covers
runtime unavailable, rate limit/network-loss normalization, stale leases,
uncertain restart recovery, budget exhaustion, failure/retry control,
malicious-result rejection, changed base, out-of-scope change, test failure,
review rejection, and API draining.

The two-daemon integration portion copies portable project history to a trusted
replica, compares projected accepted history and deterministic insights at the
same watermark, rebuilds the replica cleanly, and verifies that pairing
revocation blocks future sync work. The transport remains byte transfer only:
it has no start, resume, cancel, result-acceptance, or remote command surface.

## Hosted runtime pilot

Hosted execution is deliberately not part of the normal gate. A future operator
may invoke `tools\check_orchestration_pilot.ps1 -HostedRuntime` only after
setting `SYNCGATE_HOSTED_RUNTIME_PILOT=1`, using a disposable fixture repository
and explicitly approved local credentials. The command then fails closed because
Phase 1 ships no production hosted-runtime composition. Record adapter output,
operator approvals, and recovery observations outside portable history before
proposing a separate enablement change.

No pilot path auto-merges, deploys, deletes a worktree, or grants the replica
execution authority.
