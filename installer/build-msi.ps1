# build-msi.ps1 — Build SelectiveMirror MSI installer
# Prerequisites: Go 1.26+, .NET SDK 8.0+, WiX v6 CLI
# Install WiX: dotnet tool install --global wix
#               wix extension add WixToolset.UI.wixext/6.0.2

param(
    [string]$Version = "0.2.17"
)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$installerDir = $PSScriptRoot

Write-Host "=== SelectiveMirror MSI Build ===" -ForegroundColor Cyan
Write-Host "Version: $Version"
Write-Host ""

# Step 1: Build smirror.exe
Write-Host "[1/3] Building smirror.exe..." -NoNewline
$binDir = Join-Path $root "bin"
if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir | Out-Null }

$exe = Join-Path $binDir "smirror.exe"
$ldflags = "-s -w -X main.version=$Version"

Push-Location $root
try {
    $env:CGO_ENABLED = "0"
    & go build -ldflags $ldflags -o $exe ./cmd/smirror/ 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host " FAILED" -ForegroundColor Red
        exit 1
    }
    $size = (Get-Item $exe).Length / 1MB
    Write-Host (" OK ({0:N1} MB)" -f $size) -ForegroundColor Green
} finally {
    Pop-Location
}

# Step 2: Verify required files exist
Write-Host "[2/3] Checking source files..." -NoNewline
$required = @(
    (Join-Path $root "bin\smirror.exe"),
    (Join-Path $root "README.txt"),
    (Join-Path $root "LICENSE"),
    (Join-Path $root "CREDITS.md"),
    (Join-Path $root "THIRD-PARTY-LICENSES.txt"),
    (Join-Path $root "config.example.yaml"),
    (Join-Path $installerDir "Resources\license.rtf")
)

$missing = @()
foreach ($f in $required) {
    if (-not (Test-Path $f)) { $missing += $f }
}

if ($missing.Count -gt 0) {
    Write-Host " FAILED" -ForegroundColor Red
    Write-Host "Missing required files:" -ForegroundColor Red
    $missing | ForEach-Object { Write-Host "  $_" -ForegroundColor Red }
    exit 1
}

Write-Host " OK" -ForegroundColor Green

# Step 3: Build MSI
Write-Host "[3/3] Building MSI..." -NoNewline

Push-Location $installerDir
try {
    & dotnet build SelectiveMirror.wixproj -c Release -p:Version=$Version 2>&1
    if ($LASTEXITCODE -ne 0) {
        Write-Host " FAILED" -ForegroundColor Red
        exit 1
    }

    $msi = Get-ChildItem -Path "$installerDir\bin\Release" -Filter "*.msi" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($msi) {
        $msiSize = $msi.Length / 1MB
        Write-Host (" OK ({0:N1} MB)" -f $msiSize) -ForegroundColor Green
        Write-Host ""
        Write-Host "Output: $($msi.FullName)" -ForegroundColor Green
    } else {
        Write-Host " FAILED (no MSI output)" -ForegroundColor Red
        exit 1
    }
} finally {
    Pop-Location
}
