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

## Desktop settings, credential, and execution opt-in gate

The Windows Slice 2 gate proves that settings are staged, validated, and
digest-confirmed before activation; the local API remains loopback-only; share
registration is bounded; execution is disabled by default; and exported
diagnostics contain no credentials, absolute paths, raw identity values, or
preflight receipts. When Windows Credential Manager is available, the gate also
uses a disposable Git project to exercise credential creation, preflight,
enablement, disablement, and deletion. Unit tests inject the credential boundary
so the same lifecycle remains covered on headless Windows sessions where the OS
credential API has no usable logon session.

```powershell
powershell -ExecutionPolicy Bypass -File tools/check_desktop_node_slice2_release.ps1
```

Disabling execution must retain configuration, control state, registered
shares, worktrees, and the provider credential. Slice 2 records authorization
only; no scheduler or provider runtime is composed or started.

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

## Safe Git worktree provisioning gate

Phase 1 Slice 4 uses disposable real Git repositories to prove pinned-base
allocation, deterministic branch ownership, idempotent allocation, primary
worktree preservation, restart inspection, committed change manifests,
contract write-scope enforcement, branch collision, dirty-repository denial,
pre-provision cancellation, dirty-worktree quarantine, clean explicit release,
and stale-registry quarantine. Existing preflight tests cover nested roots,
target collisions, unsafe branch names, low disk, and symlink roots. The adapter has no merge,
reset, clean, force-delete, or primary checkout operation.

## Supervised runtime adapter gate

Phase 1 Slice 5 uses a deterministic executor rather than the hosted provider
to prove bounded Codex CLI invocation, strict structured output, exact
contract/session/attempt/lease/fence matching, normalized progress, nullable
usage provenance, token/tool/wall/concurrency controls, and action replay.
Fixtures cover success, duplicate completion, malformed and forged output,
refusal, rate limit, timeout, disconnect, budget exhaustion, cancellation, and
restart uncertainty. Tests inspect durable state, arguments, and environment to
prove prompts, context, credentials, provider sessions, and local paths are not
retained.

The laptop operations Slice 3 production-composes that adapter only after the
isolated Codex home passes `codex login status`. Its deterministic disposable
pilot proves authority-authored envelope storage and automatic intake rather
than trusting a model-provided result identity or digest. Separate fixtures
prove the credential reaches `codex login --with-api-key` only over stdin, the
runtime child receives no provider key variable, exact built-in gate digests
select the sole bounded workspace-check command, and human review can reach the
decision stop without an automated reviewer. Run the layered gate with:

```powershell
powershell -ExecutionPolicy Bypass -File tools/check_desktop_node_slice13_release.ps1
```

## DAG scheduler and bounded-execution gate

Phase 1 Slice 6 composes only deterministic fakes. Tests prove two independent
DAG nodes run concurrently while a dependent node waits through runtime
success and `collecting`; only canonical approved review and acceptance events
unlock it. Coverage fixes priority/stable-ID order, lower-priority progress,
the two-worker and provider-lease ceilings, current node observations, lease
renewal, concurrent duplicate-cycle serialization, runtime-unavailable and
budget block codes, pause/resume, idempotent cancellation, restart inspection
without uncertain replay, compute-lease release, and workspace preservation.
Daemon coverage proves orchestration shutdown completes before storage close.
Architecture checks prevent the scheduler from importing canonical history,
result intake, projection, sync, or transport packages. Run the focused suite
with the race detector on CI or a C-enabled host.

## Result, review, and human-integration gate

Phase 1 Slice 7 uses real portable work-history publication and SQLite
projection with deterministic runtime, content, workspace, test-runner, and
reviewer seams. Coverage proves exact command/evidence recording, verified
artifact and handoff references, sanitized summaries, human approval and
rejection, accepted replay, and clean projection rebuild. Negative cases cover
failed tests, requested changes, stale bases, merge conflicts, unexpected
binaries, forged envelopes, and stale attempt tokens. Disposable Git tests
also prove binary classification and that integration preview neither changes
the primary head nor hides base drift.

Run the focused gate with:

```powershell
go test ./internal/integrationgate ./internal/workspace ./internal/workhistory ./internal/project
```

## Descriptive orchestration insight gate

The insight suite rebuilds from accepted history and asserts that a rerun at the
same watermark is identical. It treats absent provider usage as unknown,
suppresses versioned telemetry groups below three accepted samples, and excludes
rejected telemetry from accepted-outcome metrics.

```powershell
go test ./internal/insights
```

## Desktop browser control-plane shell gate

Desktop Slice 5 tests one-use bootstrap expiry and replay denial, bounded opaque
sessions, rotating CSRF validation, hardened static assets, exact loopback
origin and host enforcement, browser-cookie authentication, bearer-origin
rejection, sanitized capability discovery, and the OpenAPI contract. The
Windows release gate builds a real executable, starts a disposable local node,
mints a session through the credential-backed CLI, exchanges it over HTTP, and
proves missing credentials and a foreign loopback origin are rejected.

```powershell
tools/check_desktop_node_slice5_release.ps1
```

## Browser project and assignment visibility gate

Desktop Slice 6 tests that the browser shell contains only the established
read-only visibility paths, supplies accessible project/task pickers and
assignment details, and leaves all control methods absent except the session
bootstrap. The existing API and desktop suites exercise bounded no-store
project, readiness, assignment, worker, and node projections. The release gate
reuses the live loopback session foundation before running the focused
visibility suite.

```powershell
tools/check_desktop_node_slice6_release.ps1
```

## Browser explicit operator-control gate

Desktop Slice 7 tests the exact browser POST allowlist, same-origin CSRF denial,
native confirmation markers, stable API conflict rendering, and absence of PUT,
DELETE, arbitrary routes, or silent retries. SQLite coverage proves durable
sanitized result replay and conflicting idempotency-key rejection. Desktop and
orchestration suites cover task approval, bounded dispatch preview, scheduler
replay, retry/reassignment attempt creation, worker replacement, and the local
assignment audit timeline. The Windows gate reuses the live Slice 5 session and
Slice 6 visibility gates before checking the browser script and focused control
suites.

```powershell
tools/check_desktop_node_slice7_release.ps1
```

## Browser result, budget, and incident gate

Desktop Slice 8 proves durable local integration-summary evidence, bounded latest-telemetry reads, explicit unknown and weak-evidence states, sanitized result/test/review projections, acceptance audit history, and incident classification for uncertain runtime recovery. Browser checks require the evidence and incident panels while preserving the existing exact route allowlist, no raw local metadata, and confirmed recovery controls. The Windows release gate chains the prior browser gates before exercising the focused storage, desktop, API, and control-plane suites.

```powershell
tools/check_desktop_node_slice8_release.ps1
```

## Paired-node status federation gate

Desktop Slice 9 uses two generated paired identities and a real migrated SQLite store to prove signed status receipt, an explicit expiring `read_status` grant, monotonic revisions, changing watermarks, bounded snapshot expiry, offline display, and transactional visibility removal on pairing revocation. API and static-shell tests prove the browser receives only sanitized health and project summaries through one GET route; the release gate rejects a federation mutation method and re-runs the earlier browser gates.

```powershell
tools/check_desktop_node_slice9_release.ps1
```

## Cross-node project observability gate

Desktop Slice 10 uses independently signed current and legacy status replicas to prove the cross-node project API names only the local control store as scheduler authority. It deterministically distinguishes replica, stale, offline, and legacy-incompatible observations, compares accepted-history watermarks without merging stores, and rejects every federation mutation path. Browser-session tests verify the project comparison endpoint is cookie-readable but not cookie-writable.

```powershell
tools/check_desktop_node_slice10_release.ps1
```
