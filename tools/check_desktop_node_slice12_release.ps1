param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 12 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice12-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice12-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& (Join-Path $PSScriptRoot "check_desktop_node_slice11_release.ps1") -SkipTests
if ($LASTEXITCODE -ne 0) { throw "task authority release gate failed" }

Get-Content -Raw (Join-Path $repoRoot "docs\examples\task-specification-v1.json") | ConvertFrom-Json | Out-Null

& go -C $repoRoot test ./internal/contextcompiler ./internal/storage/sqlite ./internal/workspace ./internal/taskspec ./internal/desktop ./internal/api ./cmd/syncgate -run "Test(SourceSetDigest|LocalOperatorOperation|GitWorktreeProvisioning|BootstrapLocalRegistry|LocalDispatchAuthority|LocalSetup|BuildLocalOrchestration)" -count=1
if ($LASTEXITCODE -ne 0) { throw "dispatch authority tests failed" }

$composition = Get-Content -Raw (Join-Path $repoRoot "internal\desktop\orchestration_composition.go")
if ($composition -notmatch "var workSource scheduler.WorkSource = dispatchAuthority") {
    throw "production scheduler is not backed by dispatch authority"
}
if ($composition -notmatch "StartPaused:\s+true") {
    throw "production scheduler does not start paused"
}

$setup = Get-Content -Raw (Join-Path $repoRoot "internal\desktop\orchestration_setup.go")
if ($setup -notmatch "dispatch.ContextPreflight" -or $setup -notmatch "dispatch.RuntimePreflight" -or $setup -notmatch "dispatch.PreviewContract") {
    throw "production setup preflight is not backed by dispatch authority"
}

Write-Host "desktop node Slice 12 release gate passed"
