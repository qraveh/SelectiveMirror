# test_install.ps1 — Post-install smoke test for SelectiveMirror MSI
# Run after installing the MSI to verify correctness.
# Usage: powershell -ExecutionPolicy Bypass -File test\test_install.ps1

$ErrorActionPreference = "Stop"
$pass = 0
$fail = 0
$installDir = Join-Path $env:ProgramFiles "SelectiveMirror"

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

Write-Host "=== SelectiveMirror Installation Smoke Test ===" -ForegroundColor Cyan
Write-Host "Install dir: $installDir"
Write-Host ""

# --- Install checks ---
Write-Host "[Install Checks]" -ForegroundColor Yellow

Test-Check "Install directory exists" {
    Test-Path $installDir
}

Test-Check "smirror.exe exists in install dir" {
    Test-Path (Join-Path $installDir "smirror.exe")
}

Test-Check "smirror.exe is on PATH (Get-Command)" {
    $null -ne (Get-Command smirror -ErrorAction SilentlyContinue)
}

Test-Check "smirror version exits cleanly" {
    $out = & smirror version 2>&1
    $LASTEXITCODE -eq 0 -and $out -match "\d+\.\d+\.\d+"
}

Test-Check "smirror doctor runs (exit 0 or 1)" {
    & smirror doctor 2>&1 | Out-Null
    $LASTEXITCODE -le 1  # 0 = all pass, 1 = some checks warn (e.g. no config yet)
}

Test-Check "README.txt installed" {
    Test-Path (Join-Path $installDir "README.txt")
}

Test-Check "LICENSE installed" {
    Test-Path (Join-Path $installDir "LICENSE")
}

Test-Check "config.example.yaml installed" {
    Test-Path (Join-Path $installDir "config.example.yaml")
}

Test-Check "CREDITS.md installed" {
    Test-Path (Join-Path $installDir "CREDITS.md")
}

Test-Check "THIRD-PARTY-LICENSES.txt installed" {
    Test-Path (Join-Path $installDir "THIRD-PARTY-LICENSES.txt")
}

# PDF manuals (in docs subfolder)
$docsDir = Join-Path $installDir "docs"
Test-Check "docs subfolder exists" {
    Test-Path $docsDir
}

foreach ($pdf in @("Installation Manual.pdf", "User Manual.pdf", "Developer Manual.pdf")) {
    Test-Check "PDF: $pdf installed" {
        Test-Path (Join-Path $docsDir $pdf)
    }
}

# PATH check
Test-Check "Install dir is in system PATH" {
    $syspath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
    $syspath -split ";" | Where-Object { $_ -eq $installDir } | Measure-Object | Select-Object -ExpandProperty Count | ForEach-Object { $_ -gt 0 }
}

Write-Host ""
Write-Host "=== Results: $pass passed, $fail failed ===" -ForegroundColor $(if ($fail -gt 0) { "Red" } else { "Green" })

if ($fail -gt 0) { exit 1 } else { exit 0 }
