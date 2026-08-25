param(
    [switch]$SkipRace
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$portableGo = Join-Path $repoRoot ".tools\go1.25.12\go\bin\go.exe"
$cachedGo = Join-Path $repoRoot ".cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin\go.exe"
if (Test-Path $portableGo) {
    $go = $portableGo
} elseif (Test-Path $cachedGo) {
    $go = $cachedGo
} else {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCommand) { throw "Go 1.25+ is required for the Phase 1 release gate." }
    $go = $goCommand.Source
}

& powershell -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot "check_orchestration_setup_release.ps1") -SkipRace
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& powershell -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot "check_orchestration_pilot.ps1")
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $go test ./internal/api -run "Test(LocalAdminAPIContract|OrchestrationRoutes)" -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $go test ./internal/runtimecontract -run "Architecture" -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

if ($SkipRace) {
    Write-Host "SKIP: focused race detector was explicitly skipped. Run on a C-enabled host before release."
} elseif ((& $go env CGO_ENABLED).Trim() -eq "1") {
    & $go test -race ./internal/scheduler ./internal/integrationgate ./internal/daemon
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} else {
    Write-Host "SKIP: focused race detector requires a C-enabled Go toolchain; run it in Linux CI or another C-enabled host before release."
}

Write-Host "Phase 1 deterministic release gate passed. Hosted runtime evidence remains separately required."
