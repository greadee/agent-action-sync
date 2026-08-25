param(
    [Parameter(Mandatory = $true)]
    [string]$Version,
    [string]$Channel = "stable",
    [string]$Commit = "",
    [string]$CertificateThumbprint = "",
    [string]$TimestampUrl = "http://timestamp.digicert.com",
    [string]$ISCCPath = "",
    [string]$OutputDir = "",
    [switch]$AllowUnsignedDevelopment
)

$ErrorActionPreference = "Stop"

if ($env:OS -ne "Windows_NT") {
    throw "Windows release artifacts must be built on Windows"
}
if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    throw "Release version must contain three numeric semantic-version components (for example 1.2.3)"
}
if ($Channel -notmatch '^[a-z][a-z0-9-]*$') {
    throw "Channel must be a lowercase release identifier"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not $OutputDir) {
    $OutputDir = Join-Path $repoRoot "dist\windows-amd64"
}
$artifactDir = [System.IO.Path]::GetFullPath($OutputDir)
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null

$portableGo = Join-Path $repoRoot ".tools\go1.25.12\go\bin\go.exe"
$cachedGo = Join-Path $repoRoot ".cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin\go.exe"
if (Test-Path -LiteralPath $portableGo) {
    $go = $portableGo
} elseif (Test-Path -LiteralPath $cachedGo) {
    $go = $cachedGo
} else {
    $goCommand = Get-Command go.exe -ErrorAction Stop
    $go = $goCommand.Source
}

if (-not $Commit) {
    $Commit = (& git -C $repoRoot rev-parse HEAD).Trim()
}
if ($Commit -notmatch '^[0-9a-f]{40}$') {
    throw "Commit must be a full Git object ID"
}
$builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
$ldflags = "-s -w -X syncgate/internal/buildinfo.version=$Version -X syncgate/internal/buildinfo.commit=$Commit -X syncgate/internal/buildinfo.builtAt=$builtAt -X syncgate/internal/buildinfo.channel=$Channel"
$exePath = Join-Path $artifactDir "syncgate.exe"

$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOCACHE = Join-Path $repoRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"
try {
    & $go -C $repoRoot build -trimpath -ldflags $ldflags -o $exePath .\cmd\syncgate
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed"
    }
} finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
    $env:CGO_ENABLED = $previousCGO
}

$manifestPath = Join-Path $artifactDir "version-manifest.json"
$manifestText = (& $exePath version --json | Out-String).Trim() + [Environment]::NewLine
if ($LASTEXITCODE -ne 0) {
    throw "release binary did not emit a valid version manifest"
}
$utf8WithoutBOM = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($manifestPath, $manifestText, $utf8WithoutBOM)
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.version -ne $Version -or $manifest.commit -ne $Commit -or $manifest.control_layout_version -lt 1) {
    throw "embedded version manifest does not match the requested release"
}

$signTool = $null
if ($CertificateThumbprint) {
    $signToolCommand = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if (-not $signToolCommand) {
        throw "signtool.exe is required when a certificate thumbprint is provided"
    }
    $signTool = $signToolCommand.Source
    & $signTool sign /fd SHA256 /sha1 $CertificateThumbprint /tr $TimestampUrl /td SHA256 $exePath
    if ($LASTEXITCODE -ne 0) {
        throw "executable signing failed"
    }
    if ((Get-AuthenticodeSignature -FilePath $exePath).Status -ne "Valid") {
        throw "signed executable did not pass Authenticode verification"
    }
} elseif (-not $AllowUnsignedDevelopment) {
    throw "CertificateThumbprint is required; unsigned output is allowed only with -AllowUnsignedDevelopment"
}

if (-not $ISCCPath) {
    $ISCCPath = Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe"
}
if (-not (Test-Path -LiteralPath $ISCCPath)) {
    throw "Inno Setup compiler was not found at $ISCCPath"
}
$installerSource = Join-Path $repoRoot "installer\windows\SyncGate.iss"
& $ISCCPath "/DAppVersion=$Version" "/DSourceDir=$artifactDir" "/O$artifactDir" $installerSource
if ($LASTEXITCODE -ne 0) {
    throw "installer compilation failed"
}
$setupPath = Join-Path $artifactDir "SyncGate-$Version-windows-amd64-setup.exe"
if (-not (Test-Path -LiteralPath $setupPath)) {
    throw "installer output was not created"
}
if ($signTool) {
    & $signTool sign /fd SHA256 /sha1 $CertificateThumbprint /tr $TimestampUrl /td SHA256 $setupPath
    if ($LASTEXITCODE -ne 0) {
        throw "installer signing failed"
    }
    if ((Get-AuthenticodeSignature -FilePath $setupPath).Status -ne "Valid") {
        throw "signed installer did not pass Authenticode verification"
    }
}

$checksums = @(
    "{0}  syncgate.exe" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $exePath).Hash.ToLowerInvariant()
    "{0}  version-manifest.json" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $manifestPath).Hash.ToLowerInvariant()
    "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $setupPath).Hash.ToLowerInvariant(), [System.IO.Path]::GetFileName($setupPath)
) -join [Environment]::NewLine
[System.IO.File]::WriteAllText((Join-Path $artifactDir "SHA256SUMS.txt"), $checksums + [Environment]::NewLine, $utf8WithoutBOM)

Write-Host "Windows release artifacts created in $artifactDir"
