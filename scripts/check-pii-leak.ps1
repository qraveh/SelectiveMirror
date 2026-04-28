# scripts/check-pii-leak.ps1 — Release-time PII smoke test for `report-bug`.
#
# PR-S5 (panel review pre-release 2026-04-28). SM-164 closed the
# placeholder substitution so user-chosen mirror names and accumulated
# counters no longer appear in `smirror report-bug --stdout` output —
# important because `report-bug --open` posts the report to a public
# GitHub issue. This script asserts the redactor still works.
#
# Strategy: build a throwaway smirror, wire up a canary config whose
# values are unmistakable strings ("CANARY_*"), run report-bug, grep
# the output for any canary string, fail if found.
#
# Usage (CI):
#   powershell -ExecutionPolicy Bypass -File scripts/check-pii-leak.ps1
#
# Exit codes:
#   0 = no canary strings observed (redactor working)
#   1 = at least one canary string leaked into report-bug output
#   2 = build / setup failure

$ErrorActionPreference = 'Stop'

$root = Split-Path $PSScriptRoot -Parent
Set-Location $root

# 1. Build a throwaway binary. We don't reuse bin/smirror.exe so the
#    test doesn't perturb the in-tree build state.
Write-Host "[1/4] Building canary smirror.exe..."
$canaryExe = Join-Path $env:TEMP "smirror-pii-test.exe"
$env:CGO_ENABLED = '1'
& go build -o $canaryExe ./cmd/smirror/
if ($LASTEXITCODE -ne 0) {
    Write-Error "go build failed for PII smoke test"
    exit 2
}

# 2. Stage a canary config. Use unmistakable strings so any leak is
#    obvious. Mirror names, local paths, remote names, and usernames
#    all carry a CANARY prefix the redactor must scrub.
Write-Host "[2/4] Staging canary config..."
$cfgRoot = Join-Path $env:TEMP "smirror-pii-test-$(Get-Random)"
New-Item -ItemType Directory -Path $cfgRoot -Force | Out-Null
$cfgPath = Join-Path $cfgRoot "config.yaml"
# Two NON-OVERLAPPING local_paths so config.Validate() does not reject
# (GAP-3 rejects a parent/child overlap and dumps both names in the
# error message, which would foul the canary scan).
$mirrorDirOne = Join-Path $cfgRoot "CANARY_MIRROR_PATH_one"
$mirrorDirTwo = Join-Path $cfgRoot "CANARY_MIRROR_PATH_two"
New-Item -ItemType Directory -Path $mirrorDirOne -Force | Out-Null
New-Item -ItemType Directory -Path $mirrorDirTwo -Force | Out-Null
$mirrorDirOneYaml = $mirrorDirOne -replace '\\','/'
$mirrorDirTwoYaml = $mirrorDirTwo -replace '\\','/'
@"
mirrors:
  - name: CANARY_MIRROR_NAME_ONE
    local_path: $mirrorDirOneYaml
    remote: 'CANARY_REMOTE_ALIAS:CANARY_BUCKET_NAME'
  - name: CANARY_MIRROR_NAME_TWO
    local_path: $mirrorDirTwoYaml
    remote: 'CANARY_REMOTE_ALIAS:CANARY_BUCKET_NAME/sub'
global_excludes:
  - .git/
"@ | Set-Content -Path $cfgPath -Encoding UTF8

# 3. Run report-bug against the canary config and capture stdout.
Write-Host "[3/4] Running report-bug --stdout against canary config..."
$report = & $canaryExe --config $cfgPath report-bug --stdout 2>&1 | Out-String
if (-not $report) {
    Write-Error "report-bug produced no output"
    exit 2
}

# 4. Search for any canary string in the report. Each is a strong
#    signal — none should ever appear in user-facing output. The
#    username is excluded from the canary set because the active
#    user account isn't something the script can swap; if SM-164
#    sanitization regresses on $env:USERNAME, separate inspection
#    is required.
Write-Host "[4/4] Scanning report output for canary strings..."
# The canary set is calibrated to what the SM-164 redactor explicitly
# scrubs. We do NOT include the rclone remote alias (the part of
# `alias:bucket/path` before the colon) because the redactor
# deliberately leaves it visible — it's the remote-config label, not
# user content; the bucket / path on the right side of the colon is
# redacted to `<REDACTED>`. Adding `CANARY_REMOTE_ALIAS` to this set
# would catch a non-leak and break the smoke. If the threat model
# changes (alias names treated as sensitive), add it here AND extend
# the redactor to strip them at the source.
$canaries = @(
    'CANARY_MIRROR_NAME_ONE'
    'CANARY_MIRROR_NAME_TWO'
    'CANARY_MIRROR_PATH_one'
    'CANARY_MIRROR_PATH_two'
    'CANARY_BUCKET_NAME'
)

$leaks = @()
foreach ($needle in $canaries) {
    if ($report -match [regex]::Escape($needle)) {
        $leaks += $needle
    }
}

# Sanity sub-check: the alias-only line `CANARY_REMOTE_ALIAS:<REDACTED>`
# should be present (proves the bucket/path redaction is happening).
# If the redactor regresses such that it stops emitting `<REDACTED>`,
# the alias becomes a real PII vector and we want to know.
$aliasOk = ($report -match 'CANARY_REMOTE_ALIAS:<REDACTED>')

# Cleanup before reporting.
Remove-Item $cfgRoot -Recurse -Force -ErrorAction SilentlyContinue
Remove-Item $canaryExe -Force -ErrorAction SilentlyContinue

if ($leaks.Count -gt 0) {
    Write-Error "PII leak detected in report-bug output. Canary strings present: $($leaks -join ', ')"
    Write-Host ""
    Write-Host "===== report-bug output (truncated) ====="
    ($report -split "`n") | Select-Object -First 200 | ForEach-Object { Write-Host $_ }
    exit 1
}

if (-not $aliasOk) {
    Write-Error "Canary alias substitution missing: expected to see 'CANARY_REMOTE_ALIAS:<REDACTED>' in the report (proves bucket-path redaction). Got an output that lacks the redaction marker - investigate redactor."
    Write-Host ""
    Write-Host "===== report-bug output (truncated) ====="
    ($report -split "`n") | Select-Object -First 200 | ForEach-Object { Write-Host $_ }
    exit 1
}

$count = $canaries.Length
Write-Host "OK: report-bug --stdout output contains zero canary PII strings."
Write-Host "Canaries checked: $count. Bucket-path redaction confirmed."
exit 0
