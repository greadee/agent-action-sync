param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 6 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice6-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice6-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice5_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "desktop browser session foundation failed" }

& go -C $repoRoot test ./internal/api ./internal/desktop ./internal/scheduler -run "Test(ControlPlaneVisibility|ServerBrowserSession|SetupRoutes|OrchestrationRoutes|LocalProject|OrchestrationAssignmentInventory)" -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop browser visibility tests failed" }

Write-Host "desktop node Slice 6 release gate passed"
