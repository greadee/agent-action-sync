# Testing

## Required Toolchain

Local development requires Go 1.25 or newer.

Check the local toolchain:

```powershell
go version
gofmt -h
```

Run the suite:

```powershell
tools\test.ps1
```

The script also supports a local portable Go layout:

```text
.tools/go1.25.12/go/bin/go.exe
```

When that exists, the script uses it automatically and stores build caches under `.cache/`.

If formatting needs to be skipped temporarily while diagnosing a toolchain issue:

```powershell
tools\test.ps1 -SkipFormat
```

## CI Guarantee

GitHub Actions runs the same core suite on every push to `main` and `codex/**` branches, and on pull requests into `main`.

The workflow is defined in:

```text
.github/workflows/go-test.yml
```

CI installs Go from `go.mod`, checks `gofmt` for `cmd` and `internal`, and runs:

```powershell
go test ./...
go vet ./...
go test ./internal/project ./internal/api -run "Contract|GoldenPortableRecords|LocalAdminAPIContract" -count=1
```

The Linux job also runs the focused race command documented below. The Windows
job supplies the second supported-platform test and vet result.

This keeps the test suite runnable even when a local workstation does not yet have Go installed.

## Reliability and security checks

Before merging synchronization changes, also run the focused race detector:

```powershell
go test -race ./internal/sync ./internal/transfer ./internal/filesystem
```

The repository includes fuzz targets for relative-path normalization and ignore matching. A short smoke run is useful before a merge:

```powershell
go test -run '^$' -fuzz=FuzzNormalizeRelativePath -fuzztime=5s ./internal/filesystem
go test -run '^$' -fuzz=FuzzMatchIgnorePattern -fuzztime=5s ./internal/sync
```

Fault-injection tests cover write and flush failures that represent disk-full behavior. Locked-file behavior remains filesystem- and sharing-mode-specific, so it is exercised manually on supported platforms rather than asserted in portable CI.

## Agent Project release gate

Stage 10 adds a two-daemon acceptance scenario that migrates an eligible source
share, records the supported work-history lifecycle, transfers every portable
revision through the authenticated one-way transport, and compares canonical
hashes, history projections, and insight values at the same watermark. It then
restarts both daemons, removes only the target's derived projection in the test
store, rebuilds through the supported projector, and proves equivalent results.

The surrounding integration suites cover authorization revocation, unavailable
targets, interrupted apply recovery, corrupt and unsupported records, deletion
circuit breakers, migration retries, API draining, and mandatory exclusion of
`.git`, `.env*`, `.secrets/**`, `.agent-project/local/**`, and partial files.
Run the canonical `tools\test.ps1` suite and the focused race command above
before release. A platform with `CGO_ENABLED=0` or without a C compiler cannot
execute Go's race detector. That local limitation is not a passing result; use
the Linux CI race step or another supported C-enabled development host for that
part of the gate.

## Context compiler release gate

`internal/contextcompiler` pins a golden context digest and compares complete
bundle bytes across reversed source enumeration and a cache replay. Focused
tests cover work-package scope failure, trade filtering, secret and sensitive
omissions, deterministic credential/absolute-path redaction, private keys,
binary files, oversized and missing sources, symlinks where the host permits
their creation, token-budget omissions, local-only caching, and propagation of
the bundle digest into the authority artifact request's `context_version`.

## Execution contract release gate

`internal/executioncontract` tests byte-equivalent contract reproduction,
least-privilege path overlap, forbidden-child precedence, traversal and scope
escape, missing-layer and unknown-capability denial, exact secret allowlists,
attempted privilege escalation, budget minima, task/work-package risk gates,
worker-success gate bypass, deterministic cancellation/exhaustion decisions,
and immutable predecessor-bound amendments. SQLite tests cover idempotent
replay, same-version conflict, competing execution contract IDs, and stable
version pagination. The full suite and architecture-boundary check must pass;
production runtime adapters remain out of scope for this gate.

## Runtime, node, workspace, and result-intake release gate

Slice 6 focused suites cover normalized runtime transitions, action and prepare
idempotency, resumability binding, capability and runtime/node drift,
cancellation, deterministic completion, node definition hashing, stale health,
capacity/tool/runtime intersection, lease ceilings and fencing, workspace root
separation, dirty state, branch safety, collision, disk limits, symlinks where
the host permits them, opaque responses, cleanup ownership, `.git` exclusion,
strict envelope decoding, privacy-field rejection, exact authority/provenance
bindings, forged results, replay/conflict behavior, and upload-only receipts
that never grant canonical authority. The whole suite, vet, formatting, and
import-boundary checks must pass. No test in this gate starts a real runtime or
creates a Git worktree.

## Orchestration setup release gate

Run `tools\check_orchestration_setup_release.ps1` before declaring the setup
sprint ready. It executes the full suite, vet, architecture boundaries, and
short fuzz campaigns for portable record decoding, task-graph readiness,
result envelopes, and scope matching. Recovery coverage proves canceled
context compilation leaves no cache state, a reopened compiler reproduces its
digest, and reopened SQLite result intake preserves only the exact replay
decision. The gate documents a CGO-disabled race result as a skip; it must be
completed on Linux CI or a C-enabled host. No release-gate test starts a real
runtime, allocates a production workspace, or enables remote execution.

## Orchestration control and recovery gate

Phase 1 Slice 1 adds pure reducer tests and SQLite integration coverage for
single-winner concurrent claims, fencing-token/generation mismatch, expired
lease rejection, transaction rollback after a late audit failure, duplicate
operation replay, one-way gate resolution, immutable operator decisions,
timeout and retry, cancellation/timeout races, close-and-reopen recovery, and
canonical projection isolation. The restart matrix must explicitly return
`resume`, `reconcile`, or `needs_operator`; it must not start or replay runtime
work. Run the full suite, vet, formatting, and architecture-boundary checks at
the slice checkpoint. The race detector remains a required CI/C-enabled-host
check when the local environment has `CGO_ENABLED=0`.

## Deterministic dispatch-selection gate

Phase 1 Slice 2 tests the ordered fact-only selector with reversed candidate
input, exact authority/trade checks, tag mismatch, unavailable runtime,
unhealthy node, denied permission, missing preflight gate evidence, risk,
budget, concurrency, stable preference, and operator-override cases. The
selection request intentionally has no telemetry input. SQLite tests also prove
that a subsequent new assignment records `operator_override` as its audit
reason. No selection test invokes a runtime, reserves a compute node, or
allocates a workspace.

## Context-contract attempt-binding gate

Phase 1 Slice 3 tests deterministic context compilation and contract creation
before a planned assignment is persisted, digest-only binding recovery after a
SQLite reopen, same-input replay, and rejection of changed source, policy,
worker, or instruction inputs under the existing immutable authority. The
tests prove secret source contents are absent from both the compiled bundle and
the persisted binding. No test claims a lease or starts a runtime.
