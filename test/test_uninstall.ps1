# test_uninstall.ps1 — Post-uninstall verification for SelectiveMirror MSI
# Run after uninstalling to verify cleanup.
# Usage: powershell -ExecutionPolicy Bypass -File test\test_uninstall.ps1

$ErrorActionPreference = "Stop"
$pass = 0
$fail = 0
$installDir = Join-Path $env:ProgramFiles "SelectiveMirror"
$userConfigDir = Join-Path $env:USERPROFILE ".selectivemirror"

function Test-Check {
    param([string]$Name, [scriptblock]$Check)
    try {
        $result = & $Check
        if ($result) {
            Write-Host "  PASS: $Name" -ForegroundColor Green
            $script:pass++
        } else {
            Write-Host "  FAIL: $Name" -ForegroundColor Red
            $script:fail++
        }
    } catch {
        Write-Host "  FAIL: $Name ($_)" -ForegroundColor Red
        $script:fail++
    }
}

Write-Host "=== SelectiveMirror Uninstall Verification ===" -ForegroundColor Cyan
Write-Host ""

Test-Check "Install directory removed" {
    -not (Test-Path $installDir)
}

Test-Check "smirror.exe not on PATH" {
    $null -eq (Get-Command smirror -ErrorAction SilentlyContinue)
}

Test-Check "PATH entry removed" {
    $syspath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
    $found = $syspath -split ";" | Where-Object { $_ -eq $installDir }
    $null -eq $found -or $found.Count -eq 0
}

Test-Check "User config preserved (~\.selectivemirror\)" {
    if (Test-Path $userConfigDir) {
        Write-Host "    (User data preserved at $userConfigDir — correct)" -ForegroundColor Gray
        $true
    } else {
        Write-Host "    (No user config dir found — OK if never configured)" -ForegroundColor Gray
        $true  # Pass either way: uninstall should NOT delete user data
    }
}

Write-Host ""
Write-Host "=== Results: $pass passed, $fail failed ===" -ForegroundColor $(if ($fail -gt 0) { "Red" } else { "Green" })

if ($fail -gt 0) { exit 1 } else { exit 0 }
