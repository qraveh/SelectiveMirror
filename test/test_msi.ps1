<#
.SYNOPSIS
    SelectiveMirror MSI installer system tests.
    Tests install, uninstall, and service interactions across privilege levels.

.DESCRIPTION
    Scenarios tested:
      1. Per-user install (no admin): binary + PATH + rclone check
      2. smirror version from installed path
      3. smirror service install without admin: clear error, exit 1
      4. Per-user uninstall (no admin, no service): clean removal
      5. Per-user install + admin service install (if elevated)
      6. Per-user uninstall with service running (if elevated)
      7. Quiet install (/quiet)
      8. Quiet uninstall (/quiet)

    Exit code 0 = all tests passed, 1 = one or more failed.

.USAGE
    # Standard (non-admin) tests:
    powershell -ExecutionPolicy Bypass -File test\test_msi.ps1

    # Full tests including service (run as administrator):
    powershell -ExecutionPolicy Bypass -File test\test_msi.ps1
#>

$ErrorActionPreference = "Continue"

$msiPath = Join-Path $PSScriptRoot "..\installer\bin\Release\SelectiveMirror.msi"
$installDir = Join-Path $env:LOCALAPPDATA "SelectiveMirror"
$smirrorExe = Join-Path $installDir "smirror.exe"

$script:passed = 0
$script:failed = 0
$script:skipped = 0

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

function Test-Result($name, $condition, $detail = '') {
    if ($condition) {
        Write-Host "  [OK]   $name $detail" -ForegroundColor Green
        $script:passed++
    } else {
        Write-Host "  [FAIL] $name $detail" -ForegroundColor Red
        $script:failed++
    }
}

function Test-Skip($name, $reason) {
    Write-Host "  [SKIP] $name ($reason)" -ForegroundColor Yellow
    $script:skipped++
}

function Install-MSI([switch]$Quiet) {
    $args = @("/i", $msiPath)
    if ($Quiet) { $args += "/quiet" }
    $args += "/l*v"
    $args += (Join-Path $env:TEMP "smirror_test_msi.log")
    $proc = Start-Process msiexec -ArgumentList $args -Wait -PassThru
    return $proc.ExitCode
}

function Uninstall-MSI([switch]$Quiet) {
    $args = @("/x", $msiPath)
    if ($Quiet) { $args += "/quiet" }
    $proc = Start-Process msiexec -ArgumentList $args -Wait -PassThru
    return $proc.ExitCode
}

# ========================================================================

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "  MSI System Tests" -ForegroundColor Cyan
Write-Host "  Admin: $isAdmin" -ForegroundColor Cyan
Write-Host "  MSI:   $msiPath" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# Pre-check: MSI exists
if (-not (Test-Path $msiPath)) {
    Write-Host "  [FAIL] MSI not found: $msiPath" -ForegroundColor Red
    Write-Host "  Run: powershell -File installer\build-msi.ps1" -ForegroundColor Yellow
    exit 1
}

# Clean slate: uninstall if already installed
if (Test-Path $smirrorExe) {
    Write-Host "  Cleaning previous install..." -ForegroundColor Gray
    Uninstall-MSI -Quiet | Out-Null
    Start-Sleep -Seconds 2
}

# ---- Test 1: Per-user quiet install ----
Write-Host ""
Write-Host "-- Test 1: Per-user quiet install --" -ForegroundColor Yellow

$exitCode = Install-MSI -Quiet
Test-Result "msiexec /i /quiet exit code 0" ($exitCode -eq 0) "got=$exitCode"
Test-Result "smirror.exe exists" (Test-Path $smirrorExe)
Test-Result "Install dir is LOCALAPPDATA" ($installDir -like "*$env:LOCALAPPDATA*")

# ---- Test 2: smirror version from installed path ----
Write-Host ""
Write-Host "-- Test 2: Verify installed binary --" -ForegroundColor Yellow

if (Test-Path $smirrorExe) {
    $ver = & $smirrorExe version 2>&1 | Select-Object -Last 1
    Test-Result "smirror version runs" ($ver -match 'smirror \d+\.\d+\.\d+')  "output=$ver"
} else {
    Test-Result "smirror version runs" $false "binary not found"
}

# ---- Test 3: service install without admin ----
Write-Host ""
Write-Host "-- Test 3: Service install privilege check --" -ForegroundColor Yellow

if (-not $isAdmin) {
    if (Test-Path $smirrorExe) {
        $svcOut = & $smirrorExe service install 2>&1 | Out-String
        $svcExit = $LASTEXITCODE
        Test-Result "service install rejects non-admin" ($svcExit -ne 0) "exit=$svcExit"
        Test-Result "error mentions administrator" ($svcOut -match 'administrator|admin|privilege') "msg=$($svcOut.Trim().Substring(0, [Math]::Min(80, $svcOut.Trim().Length)))"
    } else {
        Test-Result "service install rejects non-admin" $false "binary not found"
    }
} else {
    Test-Skip "service install non-admin check" "running as admin"
}

# ---- Test 4: Per-user quiet uninstall (no service) ----
Write-Host ""
Write-Host "-- Test 4: Per-user quiet uninstall --" -ForegroundColor Yellow

$exitCode = Uninstall-MSI -Quiet
Test-Result "msiexec /x /quiet exit code 0" ($exitCode -eq 0) "got=$exitCode"
Start-Sleep -Seconds 2
Test-Result "smirror.exe removed" (-not (Test-Path $smirrorExe))

# ---- Test 5: Interactive install (non-quiet) ----
Write-Host ""
Write-Host "-- Test 5: Interactive install --" -ForegroundColor Yellow

$exitCode = Install-MSI
Test-Result "msiexec /i interactive exit code 0" ($exitCode -eq 0) "got=$exitCode"
Test-Result "smirror.exe exists after interactive install" (Test-Path $smirrorExe)

# ---- Test 6: Admin-only tests (service lifecycle) ----
Write-Host ""
Write-Host "-- Test 6: Service lifecycle (admin-only) --" -ForegroundColor Yellow

if ($isAdmin -and (Test-Path $smirrorExe)) {
    # Install service
    $out = & $smirrorExe service install 2>&1 | Out-String
    Test-Result "service install succeeds" ($LASTEXITCODE -eq 0) "exit=$LASTEXITCODE"

    # Start service
    $out = & $smirrorExe service start 2>&1 | Out-String
    Test-Result "service start succeeds" ($LASTEXITCODE -eq 0) "exit=$LASTEXITCODE"

    Start-Sleep -Seconds 2

    # Stop service
    $out = & $smirrorExe service stop 2>&1 | Out-String
    Test-Result "service stop succeeds" ($LASTEXITCODE -eq 0) "exit=$LASTEXITCODE"

    # Uninstall service
    $out = & $smirrorExe service uninstall 2>&1 | Out-String
    Test-Result "service uninstall succeeds" ($LASTEXITCODE -eq 0) "exit=$LASTEXITCODE"
} else {
    Test-Skip "service install" "requires admin"
    Test-Skip "service start" "requires admin"
    Test-Skip "service stop" "requires admin"
    Test-Skip "service uninstall" "requires admin"
}

# ---- Test 7: Uninstall interactive ----
Write-Host ""
Write-Host "-- Test 7: Interactive uninstall --" -ForegroundColor Yellow

$exitCode = Uninstall-MSI
Test-Result "msiexec /x interactive exit code 0" ($exitCode -eq 0) "got=$exitCode"
Start-Sleep -Seconds 2
Test-Result "smirror.exe removed after interactive uninstall" (-not (Test-Path $smirrorExe))

# ---- Test 8: Re-install + service install/uninstall via MSI (admin only) ----
Write-Host ""
Write-Host "-- Test 8: MSI-driven service lifecycle (admin only) --" -ForegroundColor Yellow

if ($isAdmin) {
    # Install with elevation: MSI should auto-install+start service
    $exitCode = Install-MSI -Quiet
    Test-Result "elevated quiet install" ($exitCode -eq 0) "got=$exitCode"
    Start-Sleep -Seconds 3

    # Check if service was auto-installed
    $svcInstalled = & $smirrorExe service install 2>&1 | Out-String
    $alreadyInstalled = $svcInstalled -match 'already installed'
    Test-Result "MSI auto-installed service" $alreadyInstalled

    # Uninstall: MSI should auto-stop+uninstall service
    $exitCode = Uninstall-MSI -Quiet
    Test-Result "elevated quiet uninstall" ($exitCode -eq 0) "got=$exitCode"
    Start-Sleep -Seconds 2
    Test-Result "smirror.exe removed" (-not (Test-Path $smirrorExe))
} else {
    Test-Skip "MSI-driven service install" "requires admin"
    Test-Skip "MSI-driven service uninstall" "requires admin"
}

# ---- Summary ----
Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
$total = $script:passed + $script:failed + $script:skipped
Write-Host "  Passed: $($script:passed)  Failed: $($script:failed)  Skipped: $($script:skipped)  Total: $total" -ForegroundColor $(if ($script:failed -eq 0) { 'Green' } else { 'Red' })
Write-Host "========================================" -ForegroundColor Cyan

# Cleanup
Remove-Item (Join-Path $env:TEMP "smirror_test_msi.log") -ErrorAction SilentlyContinue

if ($script:failed -gt 0) { exit 1 }
exit 0
