param(
    [switch]$SkipFormat
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")
$portableGoRoot = Join-Path $repoRoot ".tools\go1.25.12\go\bin"
$portableGo = Join-Path $portableGoRoot "go.exe"
$portableGofmt = Join-Path $portableGoRoot "gofmt.exe"
$cachedToolchainRoot = Join-Path $repoRoot ".cache\go-mod\golang.org\toolchain@v0.0.1-go1.25.12.windows-amd64\bin"
$cachedGo = Join-Path $cachedToolchainRoot "go.exe"
$cachedGofmt = Join-Path $cachedToolchainRoot "gofmt.exe"

if (Test-Path $portableGo) {
    $go = $portableGo
} elseif (Test-Path $cachedGo) {
    $go = $cachedGo
} else {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        $commonGo = "C:\Program Files\Go\bin\go.exe"
        if (Test-Path $commonGo) {
            $go = $commonGo
        } else {
            Write-Host "Go is not installed or not on PATH. Install Go 1.25+ from https://go.dev/dl/, restart PowerShell, then run tools\test.ps1 again." -ForegroundColor Red
            Write-Host "Optional local portable layout: .tools\go1.25.12\go\bin\go.exe" -ForegroundColor Yellow
            Write-Host "GitHub Actions also runs the same suite on every push and pull request." -ForegroundColor Yellow
            exit 1
        }
    } else {
        $go = $goCommand.Source
    }
}

$env:GOCACHE = Join-Path $repoRoot ".cache\go-build"
$env:GOMODCACHE = Join-Path $repoRoot ".cache\go-mod"

if (-not $SkipFormat) {
    if (Test-Path $portableGofmt) {
        $gofmt = $portableGofmt
    } elseif (Test-Path $cachedGofmt) {
        $gofmt = $cachedGofmt
    } else {
        $gofmtCommand = Get-Command gofmt -ErrorAction SilentlyContinue
        if (-not $gofmtCommand) {
            $commonGofmt = "C:\Program Files\Go\bin\gofmt.exe"
            if (Test-Path $commonGofmt) {
                $gofmt = $commonGofmt
            } else {
                Write-Host "gofmt is not installed or not on PATH." -ForegroundColor Red
                exit 1
            }
        } else {
            $gofmt = $gofmtCommand.Source
        }
    }

    $formatIssues = & $gofmt -l cmd internal
    if ($formatIssues) {
        Write-Host "gofmt is required for:" -ForegroundColor Red
        $formatIssues
        exit 1
    }
}

& $go test ./...
exit $LASTEXITCODE
