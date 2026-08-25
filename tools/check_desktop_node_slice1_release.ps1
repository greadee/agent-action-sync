param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 1 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) {
        throw "repository test suite failed"
    }
}

$portableGo = Join-Path $repoRoot ".tools\go1.25.12\go\bin\go.exe"
$cachedGo = Join-Path $repoRoot ".cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin\go.exe"
if (Test-Path -LiteralPath $portableGo) {
    $go = $portableGo
} elseif (Test-Path -LiteralPath $cachedGo) {
    $go = $cachedGo
} else {
    $go = (Get-Command go.exe -ErrorAction Stop).Source
}

$gateRoot = Join-Path $repoRoot (".cache\desktop-slice1-gate-" + [Guid]::NewGuid().ToString("N"))
$artifactDir = Join-Path $gateRoot "artifacts"
$runtimeRoot = Join-Path $gateRoot "runtime"
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$exePath = Join-Path $artifactDir "syncgate.exe"
$env:GOCACHE = Join-Path $repoRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
$testVersion = "0.1.0"
$testCommit = "1111111111111111111111111111111111111111"
$testBuiltAt = "2026-08-25T00:00:00Z"
$ldflags = "-X syncgate/internal/buildinfo.version=$testVersion -X syncgate/internal/buildinfo.commit=$testCommit -X syncgate/internal/buildinfo.builtAt=$testBuiltAt -X syncgate/internal/buildinfo.channel=release-gate"
& $go -C $repoRoot build -trimpath -ldflags $ldflags -o $exePath .\cmd\syncgate
if ($LASTEXITCODE -ne 0) {
    throw "desktop node build failed"
}

$manifestJSON = (& $exePath version --json | Out-String)
$manifest = $manifestJSON | ConvertFrom-Json
if ($manifest.application -ne "SyncGate Desktop Node" -or
    $manifest.version -ne $testVersion -or
    $manifest.commit -ne $testCommit -or
    -not $manifestJSON.Contains('"built_at":"' + $testBuiltAt + '"') -or
    $manifest.channel -ne "release-gate" -or
    $manifest.control_layout_version -lt 1 -or
    $manifest.minimum_control_layout_version -gt $manifest.control_layout_version) {
    throw "embedded development manifest is invalid"
}

& $exePath node-init --root $runtimeRoot
if ($LASTEXITCODE -ne 0) {
    throw "first-run initialization failed"
}
& $exePath node-init --root $runtimeRoot
if ($LASTEXITCODE -ne 0) {
    throw "idempotent upgrade preparation failed"
}

$configPath = Join-Path $runtimeRoot "config\config.json"
$config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
if ($config.data_dir -ne (Join-Path $runtimeRoot "data") -or
    $config.node.log_dir -ne (Join-Path $runtimeRoot "logs") -or
    $config.node.runtime_cache_dir -ne (Join-Path $runtimeRoot "cache") -or
    $config.node.worktree_root -ne (Join-Path $runtimeRoot "worktrees") -or
    $config.node.lifecycle_mode -ne "foreground") {
    throw "first-run mutable roots are not separated"
}

$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
$listener.Stop()
$config.runtime_mode = "development"
$config.identity.store = "development_file"
$config.identity.allow_insecure_development_file = $true
$config.local_api.port = $port
$utf8WithoutBOM = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 20), $utf8WithoutBOM)

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
        if ($healthExitCode -eq 0) {
            $healthy = $true
            break
        }
        if ($node.HasExited) {
            break
        }
        Start-Sleep -Milliseconds 100
    }
    if (-not $healthy) {
        $stderrText = if (Test-Path -LiteralPath $stderrPath) { Get-Content -LiteralPath $stderrPath -Raw } else { "" }
        throw "foreground desktop node did not become healthy: $stderrText"
    }
} finally {
    if (-not $node.HasExited) {
        Stop-Process -Id $node.Id -Force
        $node.WaitForExit()
    }
}

$installerSource = Get-Content -LiteralPath (Join-Path $repoRoot "installer\windows\SyncGate.iss") -Raw
$uninstallPolicy = Get-Content -LiteralPath (Join-Path $repoRoot "installer\windows\uninstall-policy.txt") -Raw
foreach ($required in @("PrivilegesRequired=lowest", "DefaultDirName={localappdata}\Programs\SyncGate", "node-init", "ALLOWDOWNGRADE", "Deliberately no [UninstallDelete]")) {
    if (-not $installerSource.Contains($required)) {
        throw "installer source is missing required policy: $required"
    }
}
foreach ($preserved in @("config.json", "data", "worktrees", "never removed")) {
    if (-not $uninstallPolicy.Contains($preserved)) {
        throw "uninstall preservation policy is incomplete: $preserved"
    }
}

Write-Host "desktop node Slice 1 release gate passed"
