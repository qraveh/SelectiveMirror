# Install repo-tracked git hooks into .git/hooks/.
#
# Background
#   Git hooks live under .git/hooks/ which is per-clone, not under
#   version control. To share the project's pre-commit auto-bump rule
#   (per CLAUDE.md "each commit cycle bumps -dev patch by 1"), the
#   canonical hook scripts are tracked in <repo>/hooks/. This script
#   copies them into the local .git/hooks/ directory of the current
#   clone.
#
# Usage
#   PS> powershell -ExecutionPolicy Bypass -File scripts/install-hooks.ps1
#
# Idempotent: re-running overwrites .git/hooks/<hook> with the canonical
# version. Maintainer-only stale customizations are preserved by
# .git/hooks/<hook>.local (if you have one) — this script does not
# touch *.local files.
#
# Why this script exists rather than core.hooksPath
#   Setting `git config core.hooksPath hooks/` would make git execute
#   hooks directly from the tracked dir. That works but couples every
#   developer's git config to the repo state, and it can't be set
#   automatically on fresh clone (no setup hook fires before the first
#   commit). An explicit copy-on-demand step is more discoverable and
#   keeps the hook contract local-by-default.

$ErrorActionPreference = 'Stop'

$repoRoot = git rev-parse --show-toplevel 2>$null
if (-not $repoRoot) {
    Write-Error "Not inside a git repository. Run this script from within the SelectiveMirror clone."
    exit 1
}

$src = Join-Path $repoRoot 'hooks'
$dst = Join-Path $repoRoot '.git\hooks'

if (-not (Test-Path $src)) {
    Write-Error "Source dir $src does not exist. Run from a clone that contains the tracked hooks/ directory."
    exit 1
}

if (-not (Test-Path $dst)) {
    Write-Error "Target dir $dst does not exist. Are you inside a git working tree?"
    exit 1
}

$installed = 0
Get-ChildItem -Path $src -File | ForEach-Object {
    $name = $_.Name
    # Skip .sample files — those are git's own placeholders.
    if ($name -like '*.sample') { return }
    $target = Join-Path $dst $name
    Copy-Item -Path $_.FullName -Destination $target -Force
    # Mark executable bit on Windows (the FS doesn't have one, but git
    # respects the mode if the source had it; cp doesn't transfer this
    # when invoked from PowerShell — but pre-commit hooks executed by
    # git on Windows don't need an explicit +x bit).
    Write-Host "Installed hook: $name"
    $installed++
}

if ($installed -eq 0) {
    Write-Warning "No hooks installed; check that hooks/ contains files."
} else {
    Write-Host ""
    Write-Host "$installed hook(s) installed into $dst"
    Write-Host "The pre-commit hook auto-bumps cmd/smirror/main.go::version on every commit"
    Write-Host "(unless the version is release-shape X.Y.Z with no -dev suffix, which is the"
    Write-Host "tag-target invariant). See hooks/pre-commit for the full logic."
}
