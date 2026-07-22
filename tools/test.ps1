param(
    [switch]$SkipFormat
)

$ErrorActionPreference = "Stop"

$goCommand = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCommand) {
    $commonGo = "C:\Program Files\Go\bin\go.exe"
    if (Test-Path $commonGo) {
        $go = $commonGo
    } else {
        Write-Host "Go is not installed or not on PATH. Install Go 1.22+ from https://go.dev/dl/, restart PowerShell, then run tools\test.ps1 again." -ForegroundColor Red
        Write-Host "GitHub Actions also runs the same suite on every push and pull request." -ForegroundColor Yellow
        exit 1
    }
} else {
    $go = $goCommand.Source
}

if (-not $SkipFormat) {
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

    $formatIssues = & $gofmt -l .
    if ($formatIssues) {
        Write-Host "gofmt is required for:" -ForegroundColor Red
        $formatIssues
        exit 1
    }
}

& $go test ./...
