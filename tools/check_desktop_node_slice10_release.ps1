param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 10 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice10-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice10-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice9_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "paired-node federation gate failed" }

& node --check (Join-Path $repoRoot "internal\api\controlplane\app.js")
if ($LASTEXITCODE -ne 0) { throw "cross-node project browser script is invalid" }

Get-Content -Raw (Join-Path $repoRoot "docs\protocol\local-admin-api-openapi.json") | ConvertFrom-Json | Out-Null
Get-Content -Raw (Join-Path $repoRoot "docs\protocol\node-status-replication-v1.schema.json") | ConvertFrom-Json | Out-Null

& go -C $repoRoot test ./internal/nodestatus ./internal/api -run "Test(CrossNodeProjectAPI|FederatedNodeAPI|ControlPlaneScript|ServerBrowserSession)" -count=1
if ($LASTEXITCODE -ne 0) { throw "cross-node project authority and browser-boundary tests failed" }

$openAPI = Get-Content -Raw (Join-Path $repoRoot "docs\protocol\local-admin-api-openapi.json")
if ($openAPI -match '"/api/v1/federation/(nodes|projects)"\s*:\s*\{\s*"(post|put|patch|delete)"') {
    throw "federation contract introduced remote mutation authority"
}

Write-Host "desktop node Slice 10 release gate passed"
