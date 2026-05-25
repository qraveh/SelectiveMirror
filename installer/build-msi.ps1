# build-msi.ps1 -- Build SelectiveMirror MSI installer
# Prerequisites: Go 1.26+, .NET SDK 8.0+, WiX v6 CLI
# Install WiX: dotnet tool install --global wix
#               wix extension add WixToolset.UI.wixext/6.0.2
#
# -Version:           MSI ProductVersion (x.y.z, no -dev suffix). Defaults to
#                     the version from cmd/smirror/main.go with -dev stripped.
#                     CI (release.yml) overrides this with the git tag's version.
# -SkipGoBuild:       PR-R3 (panel review pre-release 2026-04-28). Skip the
#                     `go build` step and consume an existing bin/smirror.exe.
#                     Used by release.yml to feed the GoReleaser-built binary
#                     straight into the MSI, so the binary inside the MSI is
#                     byte-equal to the one inside the published ZIP. The
#                     script still verifies the binary is present and reports
#                     the expected version before building the MSI.
# -WithTelemetryKey:  Opt in to a production-equivalent build that includes
#                     the per-version derived HMAC key for the live telemetry
#                     pipeline. Requires $env:TELEMETRY_MASTER_KEY (or
#                     $env:SMIRROR_TELEMETRY_MASTER_KEY) in the shell — usually
#                     `source ~/.smirror-deploy.env` first.
#
#                     Default (flag absent) produces a buildKey=none binary
#                     that the Cloudflare Worker rejects — the safe posture
#                     for UI iteration and quick local smoke (no production
#                     rollup-table pollution). Use -WithTelemetryKey only
#                     when deliberately exercising the live pipeline (e.g.,
#                     verifying a non-CI build can land a first_seen event
#                     before a tag). Mutually exclusive with -SkipGoBuild
#                     (the consumed binary already carries whatever key it
#                     was linked with — this script cannot re-link a key
#                     into a pre-built binary).
#
#                     CI release.yml / release-dryrun.yml have their own
#                     key plumbing through GoReleaser; do NOT pass this
#                     flag from CI workflows.

param(
    [string]$Version = "",
    [switch]$SkipGoBuild,
    [switch]$WithTelemetryKey
)

if ($WithTelemetryKey -and $SkipGoBuild) {
    Write-Host "ERROR: -SkipGoBuild and -WithTelemetryKey are mutually exclusive." -ForegroundColor Red
    Write-Host "  With -SkipGoBuild, this script consumes a pre-built binary; it cannot re-link" -ForegroundColor Red
    Write-Host "  a telemetry HMAC key into it. If you need a valid-HMAC binary, drop -SkipGoBuild" -ForegroundColor Red
    Write-Host "  and let this script rebuild it. If you have a CI-built binary that already" -ForegroundColor Red
    Write-Host "  carries a real key, drop -WithTelemetryKey." -ForegroundColor Red
    exit 1
}

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

# Step 1: Build smirror.exe (or use a pre-built one if -SkipGoBuild)
$binDir = Join-Path $root "bin"
if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir | Out-Null }
$exe = Join-Path $binDir "smirror.exe"

if ($SkipGoBuild) {
    Write-Host "[1/3] Using pre-built smirror.exe (-SkipGoBuild)..." -NoNewline
    if (-not (Test-Path $exe)) {
        Write-Host " FAILED" -ForegroundColor Red
        Write-Host "  -SkipGoBuild requested but $exe is missing. Place the binary first." -ForegroundColor Red
        exit 1
    }
    # Sanity-check: the binary must report a version that matches $Version
    # (with optional -dev suffix or build metadata appended). This catches
    # the "downloaded the wrong artifact" failure mode at MSI build time.
    #
    # release-dryrun.yml caveat: GoReleaser's --snapshot mode uses
    # `<latest-tag>-SNAPSHOT-<short-sha>` as the version, so the binary
    # passed by -SkipGoBuild reports e.g. "0.9.26-SNAPSHOT-1234567" while
    # $Version is the intended tag (e.g., "1.0.0"). Accept either match
    # to support the dryrun path; the strict version is enforced upstream
    # by PR-R2 (release.yml's tag-source assertion).
    $reported = (& $exe version 2>&1) -join ' '
    $shortSha = if ($env:GITHUB_SHA) { $env:GITHUB_SHA.Substring(0, 7) } else { '' }
    $snapshotPattern = if ($shortSha) { "SNAPSHOT-${shortSha}" } else { 'SNAPSHOT-' }
    if ($reported -notmatch [regex]::Escape($Version) -and
        $reported -notmatch [regex]::Escape($snapshotPattern)) {
        Write-Host " FAILED" -ForegroundColor Red
        Write-Host "  Pre-built binary reports '$reported'; expected to contain '$Version' or '$snapshotPattern'." -ForegroundColor Red
        exit 1
    }
    $size = (Get-Item $exe).Length / 1MB
    if ($reported -match [regex]::Escape($snapshotPattern)) {
        Write-Host (" OK ({0:N1} MB; goreleaser-snapshot version, contains '{1}')" -f $size, $snapshotPattern) -ForegroundColor Green
    } else {
        Write-Host (" OK ({0:N1} MB; version contains '{1}')" -f $size, $Version) -ForegroundColor Green
    }
} else {
    Write-Host "[1/3] Building smirror.exe..." -NoNewline
    $ldflags = "-s -w -X main.version=$Version"

    # -WithTelemetryKey: derive the per-version HMAC key from
    # $env:TELEMETRY_MASTER_KEY and embed it via -ldflags so the resulting
    # binary's BuildKeyFingerprint() reports a real fingerprint (not "none")
    # and the Cloudflare Worker accepts its payloads. Mirrors the derivation
    # in .github/workflows/release.yml lines 201-231.
    if ($WithTelemetryKey) {
        $masterHex = $env:TELEMETRY_MASTER_KEY
        if (-not $masterHex) { $masterHex = $env:SMIRROR_TELEMETRY_MASTER_KEY }
        if (-not $masterHex) {
            Write-Host " FAILED" -ForegroundColor Red
            Write-Host "  -WithTelemetryKey requires `$env:TELEMETRY_MASTER_KEY (or `$env:SMIRROR_TELEMETRY_MASTER_KEY)." -ForegroundColor Red
            Write-Host "  Source ~/.smirror-deploy.env first, or drop -WithTelemetryKey for a no-key dev build." -ForegroundColor Red
            exit 1
        }
        if ($masterHex.Length -ne 64) {
            Write-Host " FAILED" -ForegroundColor Red
            Write-Host "  Master key length is $($masterHex.Length); expected 64 hex chars." -ForegroundColor Red
            exit 1
        }
        try {
            $masterBytes = New-Object byte[] ($masterHex.Length / 2)
            for ($i = 0; $i -lt $masterBytes.Length; $i++) {
                $masterBytes[$i] = [Convert]::ToByte($masterHex.Substring($i * 2, 2), 16)
            }
        } catch {
            Write-Host " FAILED" -ForegroundColor Red
            Write-Host "  Master key is not valid hex: $_" -ForegroundColor Red
            exit 1
        }
        $hmac = [System.Security.Cryptography.HMACSHA256]::new()
        try {
            $hmac.Key = $masterBytes
            $hash = $hmac.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($Version))
        } finally {
            $hmac.Dispose()
        }
        $derived = -join ($hash | ForEach-Object { $_.ToString('x2') })
        if ($derived.Length -ne 64) {
            Write-Host " FAILED" -ForegroundColor Red
            Write-Host "  Derived key has unexpected length: $($derived.Length) (expected 64)." -ForegroundColor Red
            exit 1
        }
        $ldflags += " -X github.com/qraveh/SelectiveMirror/internal/telemetry.buildKey=$derived"
    }

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
        if ($WithTelemetryKey) {
            Write-Host (" OK ({0:N1} MB; valid-HMAC build, telemetry submission enabled)" -f $size) -ForegroundColor Green
        } else {
            Write-Host (" OK ({0:N1} MB)" -f $size) -ForegroundColor Green
        }
    } finally {
        Pop-Location
    }
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
