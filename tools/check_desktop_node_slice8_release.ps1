param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 8 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice8-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice8-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice7_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "desktop browser operator controls failed" }

& node --check (Join-Path $repoRoot "internal\api\controlplane\app.js")
if ($LASTEXITCODE -ne 0) { throw "desktop browser evidence script is invalid" }

& go -C $repoRoot test ./internal/api ./internal/desktop ./internal/storage/sqlite -run "Test(ControlPlaneScript|ServerBrowserSession|LocalAdministrationRequiresExplicitResolution|LocalIntegrationSummary|ExecutionTelemetry|BuildLocalOrchestration)" -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop browser evidence and containment tests failed" }

Write-Host "desktop node Slice 8 release gate passed"
