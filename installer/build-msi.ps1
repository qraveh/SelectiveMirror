# build-msi.ps1 -- Build SelectiveMirror MSI installer
# Prerequisites: Go 1.26+, .NET SDK 8.0+, WiX v6 CLI
# Install WiX: dotnet tool install --global wix
#               wix extension add WixToolset.UI.wixext/6.0.2
#
# -Version: MSI ProductVersion (x.y.z, no -dev suffix). Defaults to the
#   version from cmd/smirror/main.go with -dev stripped. CI (release.yml)
#   overrides this with the git tag's version.

param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"
$root = Split-Path $PSScriptRoot -Parent
$installerDir = $PSScriptRoot

# Source of truth: extract version from cmd/smirror/main.go, strip -dev.
# MSI ProductVersion must be strictly numeric x.y.z[.w].
function Get-SourceVersion {
    param([string]$RepoRoot)
    $main = Join-Path $RepoRoot "cmd/smirror/main.go"
    if (-not (Test-Path $main)) { throw "Source file not found: $main" }
    $content = Get-Content $main -Raw
    if ($content -match 'var\s+version\s*=\s*"([0-9]+\.[0-9]+\.[0-9]+)(?:-[a-zA-Z0-9]+)?"') {
        return $Matches[1]
    }
    throw "Could not parse version from $main"
}

if (-not $Version) {
    $Version = Get-SourceVersion $root
    Write-Host "Version not specified; using $Version from cmd/smirror/main.go" -ForegroundColor Yellow
}

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
    # CGo required (mattn/go-sqlite3). Ensure a C compiler is on PATH.
    $env:CGO_ENABLED = "1"
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
    (Join-Path $root "README.md"),
    (Join-Path $root "LICENSE"),
    (Join-Path $root "CREDITS.md"),
    (Join-Path $root "THIRD-PARTY-LICENSES.txt"),
    (Join-Path $root "config.example.yaml"),
    (Join-Path $installerDir "install-rclone.ps1"),
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
    # /p:Version flows into the wixproj's DefineConstants as
    # ProductVersion=<x.y.z>, which the WiX preprocessor picks up and
    # overrides the fallback <?define?> in Variables.wxi.
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
