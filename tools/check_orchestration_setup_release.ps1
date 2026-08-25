param(
    [switch]$SkipRace
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$portableGo = Join-Path $repoRoot ".tools\go1.25.12\go\bin\go.exe"
$cachedGo = Join-Path $repoRoot ".cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin\go.exe"
$env:GOCACHE = Join-Path $repoRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"

if (Test-Path $portableGo) {
    $go = $portableGo
} elseif (Test-Path $cachedGo) {
    $go = $cachedGo
} else {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        throw "Go is required; install Go 1.25+ or use the repository portable toolchain."
    }
    $go = $goCommand.Source
}

& (Join-Path $PSScriptRoot "test.ps1")
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

& $go test ./internal/advancedfeatures ./internal/runtimecontract -run "Architecture" -count=1
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$fuzzTargets = @(
    @{ Package = "./internal/project"; Target = "FuzzDecodeRecord" },
    @{ Package = "./internal/orchestration"; Target = "FuzzGraphValidationAndReadiness" },
    @{ Package = "./internal/resultintake"; Target = "FuzzDecodeEnvelope" },
    @{ Package = "./internal/executioncontract"; Target = "FuzzAuthorizePathFailsClosed" }
)
foreach ($target in $fuzzTargets) {
    & $go test -run "^$" -fuzz $target.Target -fuzztime 2s $target.Package
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

if (-not $SkipRace) {
    $cgoEnabled = (& $go env CGO_ENABLED).Trim()
    if ($cgoEnabled -eq "1") {
        & $go test -race ./internal/sync ./internal/transfer ./internal/filesystem
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    } else {
        Write-Host "SKIP: race detector requires a C-enabled Go toolchain; use Linux CI or a C-enabled host."
    }
}

Write-Host "Orchestration setup release gate passed."
