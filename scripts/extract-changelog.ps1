# scripts/extract-changelog.ps1 — Extract a single version's section
# from CHANGELOG.md into a release-notes file.
#
# PR-W4 (panel review pre-release 2026-04-28). GoReleaser's auto
# changelog uses commit messages with limited filters; the user-facing
# release body should instead carry the hand-written narrative the
# maintainer drafts in CHANGELOG.md. This script extracts the
# `## [X.Y.Z]` section (and falls back to `## [Unreleased]` when the
# matching section hasn't been promoted yet) and writes it to disk
# for `gh release edit --notes-file` to pick up.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/extract-changelog.ps1 -Version 0.9.27 -Output release-notes.md
#   powershell ... -Version 0.9.27 -Output preview.md -AllowMissing  # don't fail if section missing
#
# Exit codes:
#   0 = section extracted (or AllowMissing + section missing → empty output)
#   1 = section missing and AllowMissing not set

param(
    [Parameter(Mandatory)]
    [string]$Version,
    [Parameter(Mandatory)]
    [string]$Output,
    [switch]$AllowMissing
)

$ErrorActionPreference = 'Stop'

$root = Split-Path $PSScriptRoot -Parent
$changelogPath = Join-Path $root "CHANGELOG.md"
if (-not (Test-Path $changelogPath)) {
    Write-Error "CHANGELOG.md not found at $changelogPath"
    exit 1
}

$content = Get-Content $changelogPath -Raw
$lines = $content -split "`r?`n"

# Match either `## [X.Y.Z]` or `## [X.Y.Z] — date`. The Unreleased
# section is the fallback for dry-runs or when the maintainer forgot
# to promote it. The first regex group is anchored to the version we
# want; the second is the unreleased fallback.
$versionPattern = "^## \[$([regex]::Escape($Version))\]"
$unreleasedPattern = '^## \[Unreleased\]'
$nextSectionPattern = '^## \['

# Locate the section boundaries.
$startIdx = -1
$source = ''
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match $versionPattern) {
        $startIdx = $i
        $source = "[${Version}]"
        break
    }
}

if ($startIdx -lt 0) {
    # Fall back to [Unreleased] for dry-run preview.
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match $unreleasedPattern) {
            $startIdx = $i
            $source = "[Unreleased] (fallback for ${Version})"
            break
        }
    }
}

if ($startIdx -lt 0) {
    if ($AllowMissing) {
        Write-Host "::warning::No CHANGELOG section for version $Version (and no [Unreleased] fallback). Writing empty file."
        Set-Content -Path $Output -Value "" -Encoding UTF8
        exit 0
    }
    Write-Error "No CHANGELOG section for version $Version (and no [Unreleased] fallback)."
    exit 1
}

# Find the end: next `## [` heading after $startIdx, or end-of-file.
$endIdx = $lines.Count
for ($i = $startIdx + 1; $i -lt $lines.Count; $i++) {
    if ($lines[$i] -match $nextSectionPattern) {
        $endIdx = $i
        break
    }
}

# Slice; trim trailing blank lines.
$section = $lines[$startIdx..($endIdx - 1)]
while ($section.Count -gt 0 -and [string]::IsNullOrWhiteSpace($section[-1])) {
    $section = $section[0..($section.Count - 2)]
}

# Compose the release-notes body. Lead with a one-line provenance note
# so readers can tell whether this came from a promoted version section
# or the [Unreleased] fallback.
$header = @(
    ""
    "_Release notes extracted from CHANGELOG.md $source._"
    ""
)

$body = $header + $section -join "`n"
Set-Content -Path $Output -Value $body -Encoding UTF8

Write-Host "Wrote release notes ($($section.Count) lines) from $source to $Output"
exit 0
