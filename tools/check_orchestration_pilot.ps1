param(
    [switch]$HostedRuntime
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$env:GOCACHE = Join-Path $repoRoot ".cache\orchestration-pilot-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\orchestration-pilot-go-tmp"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

& go test ./internal/scheduler ./internal/orchestration ./internal/api ./internal/daemon -run "TestDeterministicPilotMatrix|TestScheduler(ReconcilesBeforeDispatchAndDoesNotReplayUncertainRuntime|PauseCancelAndShutdownPreserveWorkspaces|ReportsStableRuntimeAndBudgetBlocks)|Test.*(Retry|Drains|Unavailable)" -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& go test ./internal/integrationgate ./internal/workspace ./internal/workhistory ./internal/project ./internal/insights -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& go test ./tests/integration -run "TestAgentProjectMigrationConvergesAcrossTrustedDaemonsAndCleanRebuild|TestTwoDaemonTrustedSyncRejectsRevocationAndImpersonation" -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

if ($HostedRuntime) {
    if ($env:SYNCGATE_HOSTED_RUNTIME_PILOT -ne "1") {
        throw "Hosted runtime remains opt-in. Set SYNCGATE_HOSTED_RUNTIME_PILOT=1 only on a disposable repository with approved local credentials and a supervised adapter composition."
    }
    throw "No production hosted-runtime composition is shipped in Phase 1; use the documented supervised adapter fixture and record external evidence before enabling one."
}

Write-Host "Deterministic orchestration pilot matrix passed. Hosted runtime was not enabled."
