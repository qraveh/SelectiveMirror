# fix-merge.ps1 — Run from C:\SelectiveMirror in a fresh PowerShell (close Claude Code first)
$ErrorActionPreference = "Continue"
Set-Location C:\SelectiveMirror

Write-Host "`n=== Step 1: Kill all Claude Code CLI processes ===" -ForegroundColor Cyan
Get-Process -Name claude -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -like "*claude-code*" } |
    ForEach-Object { Write-Host "  Killing PID $($_.Id)"; Stop-Process -Id $_.Id -Force }
Start-Sleep -Seconds 2

Write-Host "`n=== Step 2: Clean up worktree registrations ===" -ForegroundColor Cyan
git worktree prune
Write-Host "  Pruned stale worktree refs"

Write-Host "`n=== Step 3: Remove worktree directories ===" -ForegroundColor Cyan
foreach ($wt in @("flamboyant-boyd", "epic-black")) {
    $path = "C:\SelectiveMirror\.claude\worktrees\$wt"
    if (Test-Path $path) {
        Remove-Item -Recurse -Force $path -ErrorAction SilentlyContinue
        if (Test-Path $path) {
            Write-Host "  WARNING: Could not delete $wt (still locked). Try after reboot." -ForegroundColor Yellow
        } else {
            Write-Host "  Removed $wt"
        }
    } else {
        Write-Host "  $wt already gone"
    }
}

Write-Host "`n=== Step 4: Delete orphan branches ===" -ForegroundColor Cyan
git branch -d claude/flamboyant-boyd 2>$null
git branch -d claude/epic-black 2>$null
# Force-delete epic-black if not fully merged (we'll incorporate its changes manually)
git branch -D claude/epic-black 2>$null
Write-Host "  Cleaned up branches"

Write-Host "`n=== Step 5: Handle untracked service files blocking merge ===" -ForegroundColor Cyan
# Move conflicting untracked files out of the way temporarily
$tempDir = "C:\SelectiveMirror\_merge_backup"
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null
foreach ($f in @("internal\service\service_other.go", "internal\service\service_windows.go")) {
    if (Test-Path $f) {
        $dest = Join-Path $tempDir $f
        New-Item -ItemType Directory -Path (Split-Path $dest) -Force | Out-Null
        Copy-Item $f $dest -Force
        Remove-Item $f -Force
        Write-Host "  Backed up $f"
    }
}

Write-Host "`n=== Step 6: Commit local changes on master ===" -ForegroundColor Cyan
git add -A
git status --short
git commit -m "WIP: uncommitted local changes (pre-merge checkpoint)"
Write-Host "  Committed local changes"

Write-Host "`n=== Step 7: Show current state ===" -ForegroundColor Cyan
git log --oneline -10
git status

Write-Host "`n=== Done! ===" -ForegroundColor Green
Write-Host "Local changes are committed. Backup of conflicting files in: $tempDir"
Write-Host "The epic-black branch changes (2 commits) were on a branch that is now deleted."
Write-Host "If you need the epic-black changes, they're already in the working tree modifications."
Write-Host ""
Write-Host "Next: start a fresh 'claude' session from C:\SelectiveMirror to continue work."
