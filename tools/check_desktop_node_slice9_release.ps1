param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 9 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice9-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice9-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice8_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "desktop browser evidence and containment gate failed" }

& node --check (Join-Path $repoRoot "internal\api\controlplane\app.js")
if ($LASTEXITCODE -ne 0) { throw "control-plane JavaScript syntax check failed" }

Get-Content -Raw (Join-Path $repoRoot "docs\protocol\local-admin-api-openapi.json") | ConvertFrom-Json | Out-Null
Get-Content -Raw (Join-Path $repoRoot "docs\protocol\node-status-replication-v1.schema.json") | ConvertFrom-Json | Out-Null

& go test ./internal/nodestatus ./internal/pairing ./internal/storage/sqlite ./internal/api -run "Test(TwoNodeStatusReplication|NodeStatusRejects|PairingAcceptanceIsExplicit|FederatedNodeAPI|ControlPlaneScript|ServerBrowserSession)" -count=1
if ($LASTEXITCODE -ne 0) { throw "paired-node federation tests failed" }

$openAPI = Get-Content -Raw (Join-Path $repoRoot "docs\protocol\local-admin-api-openapi.json")
if ($openAPI -match '"/api/v1/federation/nodes"\s*:\s*\{\s*"(post|put|patch|delete)"') {
    throw "federation contract introduced remote mutation authority"
}

Write-Host "desktop node Slice 9 release gate passed"
