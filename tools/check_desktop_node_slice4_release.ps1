param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 4 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice4-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice4-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& go -C $repoRoot test ./internal/storage ./internal/storage/sqlite ./internal/api ./internal/desktop ./internal/scheduler -run "Test(LocalProject|ProjectOrchestrationStatus|BuildLocalOrchestration|AuthorizedWorkSource|SchedulerAppliesProjectConcurrency|LocalAdminAPIContract|OrchestrationRoutes)" -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop multi-project authority tests failed" }

& go -C $repoRoot test ./cmd/syncgate ./internal/daemon -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop multi-project command and daemon tests failed" }

Write-Host "desktop node Slice 4 release gate passed"
