param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 11 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice11-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice11-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice10_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "paired-node project gate failed" }

Get-Content -Raw (Join-Path $repoRoot "docs\examples\task-specification-v1.json") | ConvertFrom-Json | Out-Null

& go -C $repoRoot test ./internal/taskspec ./internal/desktop ./internal/daemon ./internal/api ./cmd/syncgate -run "Test(FileStore|DecodeRejects|LocalSetup|BuildLocalOrchestration|DaemonComposes|ServerBrowserSession)" -count=1
if ($LASTEXITCODE -ne 0) { throw "task authority composition tests failed" }

$setup = Get-Content -Raw (Join-Path $repoRoot "internal\daemon\admin_api.go")
if ($setup -notmatch "Setup:\s+setup") {
    throw "daemon did not install the composed setup authority"
}

Write-Host "desktop node Slice 11 release gate passed"
