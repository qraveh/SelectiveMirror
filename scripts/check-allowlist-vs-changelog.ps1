# scripts/check-allowlist-vs-changelog.ps1 — linter that enforces the
# "every allowlisted test must be justified in CHANGELOG" invariant.
#
# PR-PRE-M3 (pre-release status panel 2026-05-03). The release pipeline
# tolerates known-RED system-validation tests via an allowlist
# (system-validation/allowlist.txt). Each entry in that allowlist must
# be referenced in CHANGELOG.md with rationale (typically inside a
# `### Bugs known at tag` or `### Known issues` block); otherwise the
# allowlist accumulates stale entries whose justification has been
# silently deleted. Runbook §3 declared this as an invariant but
# enforced it by inspection only — until this script.
#
# Direction of the check: allowlist ⊆ CHANGELOG-mentioned tests.
# We do NOT enforce the inverse (CHANGELOG mentions ⊆ allowlist),
# because:
#   * CHANGELOG entries that describe a CLOSED test still mention the
#     test name as evidence of closure — those are correctly absent
#     from the allowlist.
#   * Some CHANGELOG entries describe behaviors with no associated
#     test (e.g. R4-PF-10 has no test name to allowlist).
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/check-allowlist-vs-changelog.ps1
#   powershell ... -ChangelogPath path/to/CHANGELOG.md -AllowlistPath path/to/allowlist.txt
#
# Exit codes:
#   0 = all allowlist entries appear in CHANGELOG
#   1 = at least one allowlist entry has no CHANGELOG mention
#   2 = file-not-found / parse failure

param(
    [string]$ChangelogPath = "CHANGELOG.md",
    [string]$AllowlistPath = "system-validation/allowlist.txt"
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $ChangelogPath)) {
    Write-Error "CHANGELOG not found at $ChangelogPath"
    exit 2
}
if (-not (Test-Path $AllowlistPath)) {
    Write-Error "Allowlist not found at $AllowlistPath"
    exit 2
}

# Parse allowlist: skip blanks and comments, trim whitespace.
$allowlist = @(Get-Content $AllowlistPath | ForEach-Object {
    $line = $_.Trim()
    if ($line -and -not $line.StartsWith('#')) { $line }
})

if ($allowlist.Count -eq 0) {
    Write-Host "Allowlist is empty - nothing to validate. CHANGELOG known-issues entries are advisory under empty-allowlist."
    exit 0
}

# Parse CHANGELOG: collect every system-validation/TestXxx token.
# The pattern matches both backticked references (`system-validation/TestX`)
# and bare ones, anchored on the literal `system-validation/` prefix
# so we don't catch arbitrary "Test*" mentions in surrounding prose.
$changelogRaw = Get-Content $ChangelogPath -Raw
$rxMatches = [regex]::Matches($changelogRaw, '`?system-validation/(Test[A-Za-z0-9_]+)`?')
$mentioned = @($rxMatches | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique)

# Validate each allowlist entry has at least one CHANGELOG mention.
$missing = @()
foreach ($t in $allowlist) {
    if ($mentioned -notcontains $t) {
        $missing += $t
    }
}

if ($missing.Count -gt 0) {
    Write-Host ""
    Write-Host "===== Allowlist drift =====" -ForegroundColor Red
    Write-Host ("Tests listed in {0} but with NO matching reference in {1}:" -f $AllowlistPath, $ChangelogPath)
    foreach ($t in $missing) {
        Write-Host ("  - {0}" -f $t)
    }
    Write-Host ""
    Write-Host "Either:"
    Write-Host ("  a. Remove the entry from {0} -- the test is no longer tolerated as RED, OR" -f $AllowlistPath)
    Write-Host "  b. Add a CHANGELOG bullet referencing the test name verbatim in a"
    Write-Host "     '### Bugs known at tag' or '### Known issues' section, with rationale."
    Write-Host ""
    $count = $missing.Count
    Write-Error "Allowlist drift detected: $count entry/entries lack CHANGELOG justification."
    exit 1
}

$count = $allowlist.Count
Write-Host "OK: allowlist has $count entry/entries, all referenced in CHANGELOG."
exit 0
