param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 3 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice3-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice3-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& go -C $repoRoot test ./internal/desktop ./internal/daemon ./internal/scheduler ./internal/storage/sqlite ./internal/workspace -run "Test(BuildLocalOrchestration|LocalAdministration|LocalNode|LocalResult|DaemonComposesOrchestration|SchedulerStartsPaused|SchedulerReconcilesBeforeDispatch|OrchestrationAssignmentInventory|GitWorktreeProvisioning)" -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop orchestration composition tests failed" }

& go -C $repoRoot test ./internal/integrationgate -count=1
if ($LASTEXITCODE -ne 0) { throw "result integration gate tests failed" }

& (Join-Path $PSScriptRoot "check_orchestration_pilot.ps1")
if ($LASTEXITCODE -ne 0) { throw "deterministic orchestration pilot failed" }

$gateRoot = Join-Path $repoRoot (".cache\desktop-slice3-gate-" + [Guid]::NewGuid().ToString("N"))
$artifactDir = Join-Path $gateRoot "artifacts"
$runtimeRoot = Join-Path $gateRoot "node"
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$exePath = Join-Path $artifactDir "syncgate.exe"
& go -C $repoRoot build -trimpath -o $exePath .\cmd\syncgate
if ($LASTEXITCODE -ne 0) { throw "desktop node build failed" }

& $exePath node-init --root $runtimeRoot | Out-Null
$settings = (& $exePath node-settings-show --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($settings.execution.enabled -or $settings.execution.max_concurrent -ne 1) {
    throw "first-run execution state is not safely disabled and bounded"
}

$resources = (& $exePath node-resources --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($resources.configured_concurrency -ne 1 -or $resources.cpu_millis -lt 1000 -or $resources.disk_available_bytes -lt 0) {
    throw "bounded local resource observation is invalid"
}

$configPath = Join-Path $runtimeRoot "config\config.json"
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$config.runtime_mode = "development"
$config.identity.store = "development_file"
$config.identity.allow_insecure_development_file = $true
$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$listener.Start()
$config.local_api.port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
$listener.Stop()
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 20), (New-Object System.Text.UTF8Encoding($false)))

$stdoutPath = Join-Path $gateRoot "node.stdout.log"
$stderrPath = Join-Path $gateRoot "node.stderr.log"
$node = Start-Process -FilePath $exePath -WindowStyle Hidden -ArgumentList @("node-run", "--root", "`"$runtimeRoot`"") -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru
try {
    $healthy = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $deadline) {
        $previousErrorPreference = $ErrorActionPreference
        $ErrorActionPreference = "SilentlyContinue"
        & $exePath node-health --root $runtimeRoot --timeout 1s 2>$null
        $healthExitCode = $LASTEXITCODE
        $ErrorActionPreference = $previousErrorPreference
        if ($healthExitCode -eq 0) { $healthy = $true; break }
        if ($node.HasExited) { break }
        Start-Sleep -Milliseconds 100
    }
    if (-not $healthy) {
        $stderrText = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw } else { "" }
        throw "default-off desktop node did not become healthy: $stderrText"
    }
} finally {
    if (-not $node.HasExited) {
        Stop-Process -Id $node.Id -Force
        $node.WaitForExit()
    }
}

Write-Host "desktop node Slice 3 release gate passed"
