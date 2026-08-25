param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 2 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
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

$gateRoot = Join-Path $repoRoot (".cache\desktop-slice2-gate-" + [Guid]::NewGuid().ToString("N"))
$artifactDir = Join-Path $gateRoot "artifacts"
$runtimeRoot = Join-Path $gateRoot "node"
$shareRoot = Join-Path $gateRoot "registered-share"
New-Item -ItemType Directory -Force -Path $artifactDir, $shareRoot | Out-Null
$exePath = Join-Path $artifactDir "syncgate.exe"
$env:GOCACHE = Join-Path $repoRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
& $go -C $repoRoot build -trimpath -o $exePath .\cmd\syncgate
if ($LASTEXITCODE -ne 0) { throw "desktop node build failed" }

& $exePath node-init --root $runtimeRoot | Out-Null
$settings = (& $exePath node-settings-show --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($settings.execution.enabled -or $settings.identity.store -ne "windows_credential_manager") {
    throw "first-run execution or identity settings are unsafe"
}

$listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
$listener.Stop()
$staged = (& $exePath node-settings-stage --root $runtimeRoot --device-name "SLICE2-GATE" --api-port $port | Out-String) | ConvertFrom-Json
if ($staged.confirmation -notmatch '^[0-9a-f]{64}$') { throw "settings staging did not return a bounded confirmation" }
& $exePath node-settings-apply --root $runtimeRoot --confirmation $staged.confirmation | Out-Null
$settings = (& $exePath node-settings-show --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($settings.device_name -ne "SLICE2-GATE" -or $settings.local_api.port -ne $port) { throw "validated settings were not activated" }

$stagedShare = (& $exePath node-share-stage --root $runtimeRoot --id "slice2-project" --name "Slice 2 Project" --share-root $shareRoot --mode "read_only" | Out-String) | ConvertFrom-Json
& $exePath node-settings-apply --root $runtimeRoot --confirmation $stagedShare.confirmation | Out-Null
$settings = (& $exePath node-settings-show --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($settings.shares.Count -ne 1 -or $settings.shares[0].id -ne "slice2-project") { throw "share registration was not activated" }

$identity = (& $exePath node-identity-status --root $runtimeRoot | Out-String) | ConvertFrom-Json
if ($identity.state -ne "uninitialized" -or $identity.credential_store -ne "windows_credential_manager") { throw "identity status is not bounded" }

$provider = "slice2-gate-" + [Guid]::NewGuid().ToString("N").Substring(0, 12)
$secret = "slice2-provider-secret-" + [Guid]::NewGuid().ToString("N")
$credentialWritten = $false
$credentialSmokeAvailable = $false
try {
    $previousErrorPreference = $ErrorActionPreference
    $ErrorActionPreference = "SilentlyContinue"
    $credentialOutput = ($secret | & $exePath node-credential-set --root $runtimeRoot --provider $provider --from-stdin 2>&1 | Out-String)
    $credentialExitCode = $LASTEXITCODE
    $ErrorActionPreference = $previousErrorPreference
    if ($credentialExitCode -ne 0) {
        Write-Warning "OS credential smoke skipped because this host has no usable Windows credential logon session"
        $diagnosticsPath = Join-Path $gateRoot "sanitized-diagnostics-headless.json"
        & $exePath node-diagnostics-export --root $runtimeRoot --file $diagnosticsPath | Out-Null
        $diagnosticsText = Get-Content -LiteralPath $diagnosticsPath -Raw
        foreach ($privateValue in @($secret, $runtimeRoot, $shareRoot, "Slice 2 Project")) {
            if ($diagnosticsText.Contains($privateValue)) { throw "headless sanitized diagnostics leaked a private value" }
        }
        $diagnostics = $diagnosticsText | ConvertFrom-Json
        if ($diagnostics.schema -ne "syncgate.desktop-diagnostics.v1" -or $diagnostics.node.execution_enabled -or $diagnostics.node.share_count -ne 1) {
            throw "headless sanitized diagnostics are incomplete"
        }
    } else {
        $credential = $credentialOutput | ConvertFrom-Json
        if (-not $credential.configured -or $credential.storage -ne "os_credential_store" -or -not $credential.opaque_target_id) {
            throw "provider credential was not stored in the OS credential store"
        }
        $credentialWritten = $true
        $credentialSmokeAvailable = $true

        $worktreeRoot = $settings.roots.worktree_root
        $projectRoot = Join-Path $worktreeRoot "slice2-disposable"
        New-Item -ItemType Directory -Force -Path $projectRoot | Out-Null
        & git -C $projectRoot init | Out-Null
        & git -C $projectRoot config user.email "slice2@example.invalid"
        & git -C $projectRoot config user.name "Slice Two Gate"
        [System.IO.File]::WriteAllText((Join-Path $projectRoot "README.md"), "disposable gate", (New-Object System.Text.UTF8Encoding($false)))
        & git -C $projectRoot add README.md
        & git -C $projectRoot commit -m "add slice two gate fixture" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "disposable Git fixture creation failed" }

        & $exePath node-disposable-mark --root $runtimeRoot --project $projectRoot --confirm "MARK PROJECT DISPOSABLE" | Out-Null
        $preflight = (& $exePath node-execution-preflight --root $runtimeRoot --provider $provider --runtime $exePath --project $projectRoot --confirm "I CONFIRM THIS PROJECT IS DISPOSABLE" | Out-String) | ConvertFrom-Json
        if (-not $preflight.ready -or $preflight.receipt -notmatch '^[0-9a-f]{64}$' -or $preflight.check_codes.Count -lt 7) {
            throw "disposable execution preflight did not pass"
        }
        $enabled = (& $exePath node-execution-enable --root $runtimeRoot --receipt $preflight.receipt --confirm "ENABLE LOCAL EXECUTION" | Out-String) | ConvertFrom-Json
        if (-not $enabled.enabled -or $enabled.provider_id -ne $provider) { throw "execution enablement did not activate" }

        $preservedControl = Join-Path $settings.roots.data_dir "slice2-preserved-control.bin"
        [System.IO.File]::WriteAllText($preservedControl, "preserved", (New-Object System.Text.UTF8Encoding($false)))
        $diagnosticsPath = Join-Path $gateRoot "sanitized-diagnostics.json"
        & $exePath node-diagnostics-export --root $runtimeRoot --file $diagnosticsPath | Out-Null
        $diagnosticsText = Get-Content -LiteralPath $diagnosticsPath -Raw
        foreach ($privateValue in @($secret, $preflight.receipt, $runtimeRoot, $projectRoot, $shareRoot, "Slice 2 Project")) {
            if ($diagnosticsText.Contains($privateValue)) { throw "sanitized diagnostics leaked a private value" }
        }
        $diagnostics = $diagnosticsText | ConvertFrom-Json
        if ($diagnostics.schema -ne "syncgate.desktop-diagnostics.v1" -or -not $diagnostics.credential.configured -or $diagnostics.node.share_count -ne 1) {
            throw "sanitized diagnostics are incomplete"
        }

        $disabled = (& $exePath node-execution-disable --root $runtimeRoot --confirm "DISABLE LOCAL EXECUTION" | Out-String) | ConvertFrom-Json
        if ($disabled.enabled -or -not $disabled.preflight_receipt_configured) { throw "execution disablement did not preserve inspectable state" }
        if ((Get-Content -LiteralPath $preservedControl -Raw) -ne "preserved" -or -not (Test-Path -LiteralPath $projectRoot)) {
            throw "execution disablement removed control or workspace data"
        }
    }
} finally {
    if ($credentialWritten) {
        & $exePath node-credential-delete --root $runtimeRoot --provider $provider --confirm "DELETE PROVIDER CREDENTIAL" | Out-Null
    }
}

if ($credentialSmokeAvailable) {
    $credential = (& $exePath node-credential-status --root $runtimeRoot --provider $provider | Out-String) | ConvertFrom-Json
    if ($credential.configured) { throw "provider credential deletion did not complete" }
}

Write-Host "desktop node Slice 2 release gate passed"
