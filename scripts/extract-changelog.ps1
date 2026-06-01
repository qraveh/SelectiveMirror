# scripts/extract-changelog.ps1 -- Extract a single version's section
# from CHANGELOG.md into a release-notes file.
#
# PR-W4 (panel review pre-release 2026-04-28). GoReleaser's auto
# changelog uses commit messages with limited filters; the user-facing
# release body should instead carry the hand-written narrative the
# maintainer drafts in CHANGELOG.md. This script extracts the
# `## [X.Y.Z]` section and writes it to disk for `gh release edit
# --notes-file` to pick up.
#
# PR-PRE-F3 (pre-release status panel 2026-05-03): the `[Unreleased]`
# fallback now fires ONLY when -AllowMissing is set. Production
# release.yml runs WITHOUT -AllowMissing -- if the maintainer forgot
# runbook §2 (promote `[Unreleased]` → `[X.Y.Z]`), the release fails
# loudly rather than silently shipping the dev-cycle accumulator under
# the version's name. Dryrun (release-dryrun.yml) still uses
# -AllowMissing for the preview -- `[Unreleased]` is the natural
# preview source pre-promotion.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts/extract-changelog.ps1 -Version 0.9.27 -Output release-notes.md
#   powershell ... -Version 0.9.27 -Output preview.md -AllowMissing  # dryrun preview: fall back to [Unreleased]
#
# Exit codes:
#   0 = section extracted
#   1 = [X.Y.Z] section missing and -AllowMissing not set; OR both [X.Y.Z]
#       and [Unreleased] missing under -AllowMissing

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

# Match either `## [X.Y.Z]` or `## [X.Y.Z] -- date`. The Unreleased
# section is the fallback for dry-runs or when the maintainer forgot
# to promote it. The first regex group is anchored to the version we
# want; the second is the unreleased fallback.
$versionPattern = "^## \[$([regex]::Escape($Version))\]"
$unreleasedPattern = '^## \[Unreleased\]'
$nextSectionPattern = '^## \['

# Locate the section boundaries.
# PR-PRE-F3: the [Unreleased] fallback fires ONLY under -AllowMissing.
# Without that flag, a missing [X.Y.Z] section is a hard error -- runbook
# §2 (promote [Unreleased] → [X.Y.Z]) was skipped, and we will NOT
# silently substitute the dev-cycle accumulator.
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
    if (-not $AllowMissing) {
        Write-Error "No CHANGELOG section [$Version] found in $changelogPath. Did you forget to promote [Unreleased] -> [$Version] (release-runbook.md section 2)? -AllowMissing is reserved for the dryrun preview path."
        exit 1
    }
    # AllowMissing path: fall back to [Unreleased] for the dryrun preview.
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i] -match $unreleasedPattern) {
            $startIdx = $i
            $source = "[Unreleased] (AllowMissing fallback for [${Version}])"
            break
        }
    }
}

if ($startIdx -lt 0) {
    # Reachable only under -AllowMissing AND both sections absent.
    Write-Host "::warning::No CHANGELOG section [$Version] AND no [Unreleased] fallback in $changelogPath. Writing empty file (AllowMissing)."
    Set-Content -Path $Output -Value "" -Encoding UTF8
    exit 0
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

# Compose the release-notes body.
#
# Lead with a Downloads block so the assets are visible-at-load on the
# release page (GitHub's native Assets section sits at the bottom; this
# duplicates the links at the top for one-click access). The URLs use
# the stable per-tag form `releases/download/v$Version/<filename>` --
# the filenames are version-free by `.goreleaser.yaml` design so this
# template is portable across releases. The hardcoded `qraveh/
# SelectiveMirror` owner/repo lives next to `repoOwner`/`repoName` in
# `cmd/smirror/main.go`; update both together on a repo move.
#
# After the Downloads block, a one-line provenance note tells readers
# whether the section came from a promoted version block or the
# [Unreleased] fallback (-AllowMissing path, dryrun preview only).
$tagUrl = "https://github.com/qraveh/SelectiveMirror/releases/download/v$Version"
$downloads = @(
    "## Downloads"
    ""
    "- **[SelectiveMirror.msi]($tagUrl/SelectiveMirror.msi)** -- Windows installer (MSI, recommended)"
    "- **[SelectiveMirror_windows_amd64.zip]($tagUrl/SelectiveMirror_windows_amd64.zip)** -- portable ZIP (no installer)"
    "- **[checksums.txt]($tagUrl/checksums.txt)** -- SHA-256 hashes for all artifacts"
    ""
    "Verify build provenance (independent of Authenticode signing):"
    ""
    '```bash'
    "gh attestation verify SelectiveMirror.msi --owner qraveh"
    '```'
    ""
    "---"
    ""
)

$header = $downloads + @(
    "_Release notes extracted from CHANGELOG.md $source._"
    ""
)

$body = $header + $section -join "`n"
Set-Content -Path $Output -Value $body -Encoding UTF8

Write-Host "Wrote release notes ($($section.Count) lines) from $source to $Output"
exit 0
