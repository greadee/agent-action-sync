param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "The desktop node Slice 5 release gate requires Windows"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$env:GOCACHE = Join-Path $repoRoot ".cache\desktop-slice5-go-cache"
$env:GOTMPDIR = Join-Path $repoRoot ".cache\desktop-slice5-go-tmp"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOTMPDIR | Out-Null

if (-not $SkipTests) {
    & (Join-Path $PSScriptRoot "test.ps1")
    if ($LASTEXITCODE -ne 0) { throw "repository test suite failed" }
}

& go -C $repoRoot test ./internal/api ./cmd/syncgate -run "Test(BrowserSession|ServerBrowserSession|ServerServesHardened|ClientSendsCredential|LocalAdminAPIContract)" -count=1
if ($LASTEXITCODE -ne 0) { throw "desktop browser session tests failed" }

$gateRoot = Join-Path $repoRoot (".cache\desktop-slice5-gate-" + [Guid]::NewGuid().ToString("N"))
$artifactDir = Join-Path $gateRoot "artifacts"
$runtimeRoot = Join-Path $gateRoot "node"
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$exePath = Join-Path $artifactDir "syncgate.exe"
& go -C $repoRoot build -trimpath -o $exePath .\cmd\syncgate
if ($LASTEXITCODE -ne 0) { throw "desktop node build failed" }

& $exePath node-init --root $runtimeRoot | Out-Null
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

$origin = "http://127.0.0.1:$($config.local_api.port)"
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
        throw "desktop node did not become healthy: $stderrText"
    }

    $ticket = (& $exePath node-ui-session --config $configPath --json | Out-String) | ConvertFrom-Json
    $sessionUri = [Uri]$ticket.url
    if ($sessionUri.Host -ne "127.0.0.1" -or $sessionUri.Fragment -notlike "#bootstrap=*") {
        throw "CLI returned an unsafe browser session URL"
    }
    $bootstrap = [Uri]::UnescapeDataString($sessionUri.Fragment.Substring("#bootstrap=".Length))
    $shell = Invoke-WebRequest -UseBasicParsing -Uri ($origin + "/ui/")
    if ($shell.StatusCode -ne 200 -or $shell.Content -notmatch "SyncGate Control Plane") {
        throw "local control-plane shell was not served"
    }
    if (-not $shell.Headers["Content-Security-Policy"] -or $shell.Headers["Access-Control-Allow-Origin"]) {
        throw "local control-plane security headers are invalid"
    }

    $browser = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $headers = @{ Origin = $origin; "Sec-Fetch-Site" = "same-origin" }
    $document = Invoke-RestMethod -Uri ($origin + "/api/v1/browser-session/bootstrap") -Method Post -Headers $headers -ContentType "application/json" -Body (@{ bootstrap_token = $bootstrap } | ConvertTo-Json) -WebSession $browser
    if ($document.health -ne "ok" -or $document.status.status -ne "running" -or $document.capabilities.Count -ne 2) {
        throw "browser bootstrap did not return sanitized node status"
    }
    $expectedCapabilities = @("node.health.read", "node.status.read")
    if (@($document.capabilities | Where-Object { $_ -notin $expectedCapabilities }).Count -ne 0) {
        throw "browser bootstrap exposed an unexpected capability"
    }

    try {
        Invoke-WebRequest -UseBasicParsing -Uri ($origin + "/api/v1/status") | Out-Null
        throw "missing browser credentials were accepted"
    } catch {
        if ([int]$_.Exception.Response.StatusCode -ne 401) { throw }
    }

    $badTicket = (& $exePath node-ui-session --config $configPath --json | Out-String) | ConvertFrom-Json
    $badUri = [Uri]$badTicket.url
    $badBootstrap = [Uri]::UnescapeDataString($badUri.Fragment.Substring("#bootstrap=".Length))
    try {
        Invoke-WebRequest -UseBasicParsing -Uri ($origin + "/api/v1/browser-session/bootstrap") -Method Post -Headers @{ Origin = "http://127.0.0.1:1" } -ContentType "application/json" -Body (@{ bootstrap_token = $badBootstrap } | ConvertTo-Json) | Out-Null
        throw "non-loopback-origin session bootstrap was accepted"
    } catch {
        if ([int]$_.Exception.Response.StatusCode -ne 403) { throw }
    }
} finally {
    if (-not $node.HasExited) {
        Stop-Process -Id $node.Id -Force
        $node.WaitForExit()
    }
}

Write-Host "desktop node Slice 5 release gate passed"
