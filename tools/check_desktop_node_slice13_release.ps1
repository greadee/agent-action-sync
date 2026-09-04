param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 13 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice13-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice13-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice12_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "dispatch authority release gate failed" }

& go -C $repoRoot test ./internal/codexruntime ./internal/runtimecontract ./internal/scheduler ./internal/resultintake ./internal/integrationgate ./internal/desktop ./cmd/syncgate -run "Test(Adapter|CodexAuth|LocalResult|LocalOrchestrationRunsDeterministicPilot|LocalTestRunner|ConfiguredGate|HumanReviewStops)" -count=1
if ($LASTEXITCODE -ne 0) { throw "hosted runtime loop tests failed" }

$composition = Get-Content -Raw (Join-Path $repoRoot "internal\desktop\orchestration_composition.go")
if ($composition -notmatch "node-codex-auth-bootstrap" -or $composition -notmatch "Publisher:\s+LocalResultPublisher") {
    throw "production composition is missing authentication or result publication"
}
if ($composition -match "unavailableTestRunner" -or $composition -notmatch "TestPlans:\s+localTestPlans") {
    throw "production composition is missing exact deterministic gates"
}

$adapter = Get-Content -Raw (Join-Path $repoRoot "internal\codexruntime\adapter.go")
if ($adapter -notmatch "--ephemeral" -or $adapter -notmatch "--output-schema" -or $adapter -notmatch "--output-last-message") {
    throw "supervised Codex invocation lost its structured non-interactive boundary"
}
if ($adapter -match 'CODEX_API_KEY=' -or $adapter -match 'OPENAI_API_KEY=') {
    throw "runtime adapter contains a provider credential environment variable"
}

Write-Host "desktop node Slice 13 release gate passed"
