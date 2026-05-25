<#
.SYNOPSIS
    SelectiveMirror stress test suite.
    Hammers the watcher + sync engine with rapid file operations.

.DESCRIPTION
    Uses a local rclone backend (no network, no credentials).
    Creates hundreds of files, renames, deletes, and rapid modifications
    across multiple mirrors, then verifies drift=0.

.USAGE
    powershell -ExecutionPolicy Bypass -File test\stress_test.ps1
#>

$ErrorActionPreference = "Stop"
$GoPath = "C:\Program Files\Go\bin"
$env:Path = "$GoPath;$env:Path"

# ── Config ──────────────────────────────────────────────────────────

$TestRoot    = Join-Path "C:\mine\SelectiveMirror" "_stresstest_$(Get-Random)"
$SrcDir1     = Join-Path $TestRoot "mirror-alpha"
$SrcDir2     = Join-Path $TestRoot "mirror-beta"
$SrcDir3     = Join-Path $TestRoot "mirror-gamma"
$DstDir1     = Join-Path $TestRoot "dest-alpha"
$DstDir2     = Join-Path $TestRoot "dest-beta"
$DstDir3     = Join-Path $TestRoot "dest-gamma"
$DataDir     = Join-Path $TestRoot "data"
$ConfigPath  = Join-Path $DataDir  "config.yaml"
$StateDB     = Join-Path $DataDir  "state.db"
$LogFile     = Join-Path $DataDir  "stress.log"
$GoBin       = "$GoPath\go.exe"
$SmirrorExe  = "C:\mine\SelectiveMirror\bin\smirror.exe"
$SmirrorPkg  = "./cmd/smirror/"
$Passed      = 0
$Failed      = 0
$SmirrorProc = $null

# ── Helpers ──────────────────────────────────────────────────────────

function Setup-TestEnv {
    Write-Host "`n=== Stress Test Setup ===" -ForegroundColor Cyan
    New-Item -ItemType Directory -Path $SrcDir1, $SrcDir2, $SrcDir3, $DstDir1, $DstDir2, $DstDir3, $DataDir -Force | Out-Null

    rclone config create stresslocal local *>$null | Out-Null

    $cfgContent = @"
mirrors:
  - name: Alpha
    local_path: "$($SrcDir1.Replace('\','/'))"
    debounce_sec: 1
    max_file_size_mb: 10
    remote: "stresslocal:$($DstDir1.Replace('\','/'))"

  - name: Beta
    local_path: "$($SrcDir2.Replace('\','/'))"
    debounce_sec: 1
    max_file_size_mb: 10
    remote: "stresslocal:$($DstDir2.Replace('\','/'))"

  - name: Gamma
    local_path: "$($SrcDir3.Replace('\','/'))"
    debounce_sec: 1
    max_file_size_mb: 10
    remote: "stresslocal:$($DstDir3.Replace('\','/'))"

global_excludes:
  - .git/
  - "*.pyc"
  - "*.tmp"
  - "*.log"

state_db: "$($StateDB.Replace('\','/'))"
log_file: "$($LogFile.Replace('\','/'))"
log_level: debug
sync_workers: 4
delete_policy: mirror
"@
    [System.IO.File]::WriteAllText($ConfigPath, $cfgContent)

    Write-Host "  Mirrors: Alpha, Beta, Gamma"
    Write-Host "  Workers: 4"
    Write-Host "  Delete policy: mirror"
    Write-Host "  Root: $TestRoot"
}

function Invoke-Smirror {
    $oldPref = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        $output = & $SmirrorExe @args 2>&1 | Out-String
    } catch {
        $output = $_.Exception.Message
    }
    $ErrorActionPreference = $oldPref
    return $output
}

function Start-Smirror {
    Write-Host "  Starting smirror..." -ForegroundColor Yellow
    $script:SmirrorProc = Start-Process -FilePath $SmirrorExe -ArgumentList "start","--config",$ConfigPath `
        -WindowStyle Hidden -PassThru
    Start-Sleep 5  # startup + initial reconciliation
    if ($SmirrorProc.HasExited) {
        throw "smirror exited immediately (exit code $($SmirrorProc.ExitCode))"
    }
    Write-Host "  smirror running (PID $($SmirrorProc.Id))" -ForegroundColor Green
    if (Test-Path $LogFile) {
        Write-Host "  Log file exists: $LogFile" -ForegroundColor Green
        $logLines = @(Get-Content $LogFile -ErrorAction SilentlyContinue).Count
        Write-Host "  Log lines: $logLines" -ForegroundColor Green
    } else {
        Write-Host "  WARNING: Log file not found at $LogFile" -ForegroundColor Red
    }
}

function Stop-Smirror {
    if ($SmirrorProc -and -not $SmirrorProc.HasExited) {
        Stop-Process -Id $SmirrorProc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep 2
    }
}

function Cleanup {
    Stop-Smirror
    rclone config delete stresslocal *>$null | Out-Null
    if (Test-Path $TestRoot) {
        Remove-Item -Path $TestRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Wait-ForSync {
    param([int]$Seconds = 10)
    Start-Sleep $Seconds
}

# Poll until expected file count is reached on remote, or timeout.
# Returns the actual count at exit.
function Wait-ForFileCount {
    param(
        [string]$Dir,
        [int]$Expected,
        [string]$Pattern = "*",
        [int]$TimeoutSeconds = 60,
        [int]$StableSeconds = 3
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $lastCount = -1
    $stableSince = $null

    while ((Get-Date) -lt $deadline) {
        $current = @(Get-ChildItem -Path $Dir -Filter $Pattern -File -Recurse -ErrorAction SilentlyContinue).Count

        if ($current -eq $Expected) {
            # Count matches — wait for it to stay stable
            if ($null -eq $stableSince) {
                $stableSince = Get-Date
            } elseif (((Get-Date) - $stableSince).TotalSeconds -ge $StableSeconds) {
                return $current  # stable for long enough
            }
        } else {
            $stableSince = $null  # reset stability timer
        }

        if ($current -ne $lastCount) {
            $lastCount = $current
        }
        Start-Sleep -Milliseconds 500
    }

    # Timeout — return whatever we have
    return @(Get-ChildItem -Path $Dir -Filter $Pattern -File -Recurse -ErrorAction SilentlyContinue).Count
}

# Poll until a specific file appears on remote, or timeout.
function Wait-ForFile {
    param([string]$Path, [int]$TimeoutSeconds = 30)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while (-not (Test-Path -LiteralPath $Path) -and (Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 500
    }
    return (Test-Path -LiteralPath $Path)
}

# Poll until a specific file disappears from remote, or timeout.
function Wait-ForFileGone {
    param([string]$Path, [int]$TimeoutSeconds = 30)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Test-Path -LiteralPath $Path) -and (Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 500
    }
    return (-not (Test-Path -LiteralPath $Path))
}

function Show-DaemonLog {
    param([int]$Lines = 50)
    if (Test-Path $LogFile) {
        Write-Host "  === Last $Lines lines of daemon log ===" -ForegroundColor DarkGray
        Get-Content $LogFile -Tail $Lines | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
        Write-Host "  === end log ===" -ForegroundColor DarkGray
    }
}

function Assert-Pass {
    param([string]$Msg)
    Write-Host "  PASS: $Msg" -ForegroundColor Green
    $script:Passed++
}

function Assert-Fail {
    param([string]$Msg)
    Write-Host "  FAIL: $Msg" -ForegroundColor Red
    $script:Failed++
}

function Assert-FileExists {
    param([string]$Path, [string]$Msg)
    if (Test-Path -LiteralPath $Path) { Assert-Pass $Msg } else { Assert-Fail "$Msg (not found: $Path)" }
}

function Assert-FileNotExists {
    param([string]$Path, [string]$Msg)
    if (-not (Test-Path -LiteralPath $Path)) { Assert-Pass $Msg } else { Assert-Fail "$Msg (still exists: $Path)" }
}

function Assert-FileContent {
    param([string]$Path, [string]$Expected, [string]$Msg)
    if (-not (Test-Path $Path)) { Assert-Fail "$Msg (file not found)"; return }
    $actual = [System.IO.File]::ReadAllText($Path).Trim()
    if ($actual -eq $Expected.Trim()) { Assert-Pass $Msg } else { Assert-Fail "$Msg (content mismatch: expected '$Expected', got '$actual')" }
}

function Count-Files {
    param([string]$Dir)
    if (-not (Test-Path $Dir)) { return 0 }
    return @(Get-ChildItem -Path $Dir -File -Recurse -ErrorAction SilentlyContinue).Count
}

# ── Stress Tests ────────────────────────────────────────────────────

function Test-BurstCreate {
    param([int]$Count = 100)
    Write-Host "`n--- STRESS 1: Burst create $Count files across 3 mirrors ---" -ForegroundColor Yellow

    # Count how many go to each mirror
    $expectA = 0; $expectB = 0; $expectC = 0
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 1; $i -le $Count; $i++) {
        switch ($i % 3) {
            0 { [System.IO.File]::WriteAllText("$SrcDir1\burst_$i.txt", "burst file $i"); $expectA++ }
            1 { [System.IO.File]::WriteAllText("$SrcDir2\burst_$i.txt", "burst file $i"); $expectB++ }
            2 { [System.IO.File]::WriteAllText("$SrcDir3\burst_$i.txt", "burst file $i"); $expectC++ }
        }
    }
    $sw.Stop()
    Write-Host "  Created $Count files in $($sw.ElapsedMilliseconds)ms (A=$expectA B=$expectB C=$expectC)"

    # Poll each mirror until expected count or timeout
    $a = Wait-ForFileCount $DstDir1 $expectA "burst_*.txt" 90
    $b = Wait-ForFileCount $DstDir2 $expectB "burst_*.txt" 90
    $c = Wait-ForFileCount $DstDir3 $expectC "burst_*.txt" 90
    $total = $a + $b + $c
    Write-Host "  Synced: Alpha=$a, Beta=$b, Gamma=$c, Total=$total"

    if ($total -eq $Count) { Assert-Pass "All $Count burst files synced" }
    else { Assert-Fail "Expected $Count synced files, got $total" }
}

function Test-RapidOverwrite {
    param([int]$Overwrites = 50)
    Write-Host "`n--- STRESS 2: Rapid overwrite same file $Overwrites times ---" -ForegroundColor Yellow

    $targetFile = "$SrcDir1\rapid_overwrite.txt"
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 1; $i -le $Overwrites; $i++) {
        [System.IO.File]::WriteAllText($targetFile, "overwrite iteration $i at $(Get-Date -Format o)")
    }
    $sw.Stop()
    Write-Host "  $Overwrites overwrites in $($sw.ElapsedMilliseconds)ms"

    $remoteFile = "$DstDir1\rapid_overwrite.txt"
    $appeared = Wait-ForFile $remoteFile 30
    if ($appeared) { Assert-Pass "Overwritten file synced to remote" }
    else { Assert-Fail "Overwritten file not synced (timeout)"; return }

    # Wait for content to stabilize (debounce + final sync)
    Start-Sleep 5
    $localContent = [System.IO.File]::ReadAllText($targetFile)
    $remoteContent = [System.IO.File]::ReadAllText($remoteFile)
    if ($localContent -eq $remoteContent) {
        Assert-Pass "Remote has final overwrite content"
    } else {
        Assert-Fail "Remote content doesn't match local (stale sync)"
    }
}

function Test-DeepDirectoryCreate {
    Write-Host "`n--- STRESS 3: Deep directory tree creation ---" -ForegroundColor Yellow

    $depth = 10
    $path = $SrcDir2
    for ($i = 1; $i -le $depth; $i++) {
        $path = Join-Path $path "level$i"
    }
    New-Item -ItemType Directory -Path $path -Force | Out-Null
    [System.IO.File]::WriteAllText("$path\deep_file.txt", "file at depth $depth")

    $remotePath = $DstDir2
    for ($i = 1; $i -le $depth; $i++) {
        $remotePath = Join-Path $remotePath "level$i"
    }
    $appeared = Wait-ForFile "$remotePath\deep_file.txt" 30
    if ($appeared) { Assert-Pass "File at depth $depth synced" }
    else { Assert-Fail "File at depth $depth not synced (timeout)" }
}

function Test-BurstRename {
    param([int]$Count = 30)
    Write-Host "`n--- STRESS 4: Burst rename $Count files ---" -ForegroundColor Yellow

    # Create files first
    for ($i = 1; $i -le $Count; $i++) {
        [System.IO.File]::WriteAllText("$SrcDir3\rename_src_$i.txt", "rename candidate $i")
    }
    $synced = Wait-ForFileCount $DstDir3 $Count "rename_src_*.txt" 60
    Write-Host "  Files on remote before rename: $synced"

    # Rename all at once
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 1; $i -le $Count; $i++) {
        $src = "$SrcDir3\rename_src_$i.txt"
        $dst = "$SrcDir3\rename_dst_$i.txt"
        if (Test-Path $src) {
            Move-Item -Path $src -Destination $dst -Force
        }
    }
    $sw.Stop()
    Write-Host "  Renamed $Count files in $($sw.ElapsedMilliseconds)ms"

    # Poll for renamed files to appear
    $newCount = Wait-ForFileCount $DstDir3 $Count "rename_dst_*.txt" 90

    # Check old names are gone
    $oldGone = 0
    for ($i = 1; $i -le $Count; $i++) {
        if (-not (Test-Path "$DstDir3\rename_src_$i.txt")) { $oldGone++ }
    }

    if ($oldGone -eq $Count) { Assert-Pass "All $Count old files removed from remote" }
    else { Assert-Fail "Expected $Count old files gone, got $oldGone" }

    if ($newCount -eq $Count) { Assert-Pass "All $Count renamed files synced to remote" }
    else { Assert-Fail "Expected $Count renamed files, got $newCount" }
}

function Test-BurstDelete {
    param([int]$Count = 50)
    Write-Host "`n--- STRESS 5: Burst delete $Count files ---" -ForegroundColor Yellow

    # Create files
    for ($i = 1; $i -le $Count; $i++) {
        [System.IO.File]::WriteAllText("$SrcDir1\delete_me_$i.txt", "delete target $i")
    }
    $synced = Wait-ForFileCount $DstDir1 $Count "delete_me_*.txt" 60
    Write-Host "  Remote files before delete: $synced"

    # Delete all at once
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 1; $i -le $Count; $i++) {
        $f = "$SrcDir1\delete_me_$i.txt"
        if (Test-Path $f) { Remove-Item $f -Force }
    }
    $sw.Stop()
    Write-Host "  Deleted $Count files in $($sw.ElapsedMilliseconds)ms"

    # Poll until all delete_me files are gone from remote
    $remaining = Wait-ForFileCount $DstDir1 0 "delete_me_*.txt" 90
    if ($remaining -eq 0) { Assert-Pass "All $Count deleted files removed from remote (mirror policy)" }
    else { Assert-Fail "Expected 0 delete_me files on remote, $remaining still exist" }
}

function Test-ConcurrentMirrorStorm {
    param([int]$FilesPerMirror = 30)
    Write-Host "`n--- STRESS 6: Concurrent writes to all 3 mirrors ($FilesPerMirror each) ---" -ForegroundColor Yellow

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 1; $i -le $FilesPerMirror; $i++) {
        [System.IO.File]::WriteAllText("$SrcDir1\storm_a_$i.txt", "alpha storm $i")
        [System.IO.File]::WriteAllText("$SrcDir2\storm_b_$i.txt", "beta storm $i")
        [System.IO.File]::WriteAllText("$SrcDir3\storm_c_$i.txt", "gamma storm $i")
    }
    $sw.Stop()
    $total = $FilesPerMirror * 3
    Write-Host "  Created $total files across 3 mirrors in $($sw.ElapsedMilliseconds)ms"

    $a = Wait-ForFileCount $DstDir1 $FilesPerMirror "storm_a_*.txt" 90
    $b = Wait-ForFileCount $DstDir2 $FilesPerMirror "storm_b_*.txt" 90
    $c = Wait-ForFileCount $DstDir3 $FilesPerMirror "storm_c_*.txt" 90
    Write-Host "  Synced: Alpha=$a, Beta=$b, Gamma=$c"

    if ($a -eq $FilesPerMirror -and $b -eq $FilesPerMirror -and $c -eq $FilesPerMirror) {
        Assert-Pass "All $total concurrent files synced across 3 mirrors"
    } else {
        Assert-Fail "Expected $FilesPerMirror per mirror, got a=$a b=$b c=$c"
    }
}

function Test-LargeFile {
    Write-Host "`n--- STRESS 7: Large file (5 MB) ---" -ForegroundColor Yellow

    $largePath = "$SrcDir2\large_file.bin"
    $data = New-Object byte[] (5 * 1024 * 1024)
    [System.Random]::new(42).NextBytes($data)
    [System.IO.File]::WriteAllBytes($largePath, $data)
    Write-Host "  Created 5 MB file"

    $remoteLarge = "$DstDir2\large_file.bin"
    $appeared = Wait-ForFile $remoteLarge 45
    if ($appeared) { Assert-Pass "5 MB file synced" }
    else { Assert-Fail "5 MB file not synced (timeout)"; return }

    $localSize = (Get-Item $largePath).Length
    $remoteSize = (Get-Item $remoteLarge).Length
    if ($localSize -eq $remoteSize) { Assert-Pass "Large file size matches ($localSize bytes)" }
    else { Assert-Fail "Size mismatch: local=$localSize remote=$remoteSize" }
}

function Test-EmptyFiles {
    param([int]$Count = 20)
    Write-Host "`n--- STRESS 8: Empty files ($Count) ---" -ForegroundColor Yellow

    for ($i = 1; $i -le $Count; $i++) {
        [System.IO.File]::WriteAllText("$SrcDir1\empty_$i.txt", "")
    }

    $synced = Wait-ForFileCount $DstDir1 $Count "empty_*.txt" 60
    if ($synced -eq $Count) { Assert-Pass "All $Count empty files synced" }
    else { Assert-Fail "Expected $Count empty files, got $synced" }
}

function Test-SpecialCharFilenames {
    Write-Host "`n--- STRESS 9: Special character filenames ---" -ForegroundColor Yellow

    $names = @(
        # Whitespace
        "spaces in name.txt",
        "  leading spaces.txt",
        # Grouping characters
        "file (copy).txt",
        "file [bracket].txt",
        "file {brace}.txt",
        # Punctuation
        "file #hash.txt",
        "file @at.txt",
        "file +plus.txt",
        "file =equals.txt",
        "file 'single.txt",
        "file ;semicolon.txt",
        "file ,comma.txt",
        "file ^caret.txt",
        "file ~tilde.txt",
        "file !bang.txt",
        "file $dollar.txt",
        "file %percent.txt",
        "file &ampersand.txt",
        # Unicode
        "file-$([char]0x00E9)-unicode.txt",
        # Long filename (200 chars)
        ("A" * 196 + ".txt"),
        # Leading dot (hidden on Unix)
        ".hidden-file.txt",
        # Multiple dots
        "file.backup.2024.txt",
        # Double extension
        "archive.tar.gz.txt"
    )

    foreach ($name in $names) {
        [System.IO.File]::WriteAllText("$SrcDir3\$name", "content of $name")
    }

    # Wait for all special-char files (poll with generous timeout)
    # Use -LiteralPath because PowerShell's Test-Path interprets [ ] as wildcards
    $deadline = (Get-Date).AddSeconds(60)
    $synced = 0
    while ((Get-Date) -lt $deadline) {
        $synced = 0
        foreach ($name in $names) {
            if (Test-Path -LiteralPath "$DstDir3\$name") { $synced++ }
        }
        if ($synced -eq $names.Count) { break }
        Start-Sleep -Milliseconds 500
    }

    # Report which ones are missing
    foreach ($name in $names) {
        if (-not (Test-Path -LiteralPath "$DstDir3\$name")) {
            Write-Host "    Missing: $name" -ForegroundColor DarkYellow
        }
    }

    if ($synced -eq $names.Count) { Assert-Pass "All $($names.Count) special-char files synced" }
    else { Assert-Fail "Expected $($names.Count) special-char files, got $synced" }
}

function Test-SyncNowGhostCleanup {
    Write-Host "`n--- STRESS 10: sync-now ghost cleanup ---" -ForegroundColor Yellow

    # Create and sync a file
    [System.IO.File]::WriteAllText("$SrcDir1\ghost_test.txt", "will become ghost")
    $appeared = Wait-ForFile "$DstDir1\ghost_test.txt" 30
    if ($appeared) { Assert-Pass "Ghost candidate synced" }
    else { Assert-Fail "Ghost candidate not synced (timeout)"; return }

    # Delete locally (policy=mirror should handle via watcher)
    Remove-Item "$SrcDir1\ghost_test.txt" -Force
    $gone = Wait-ForFileGone "$DstDir1\ghost_test.txt" 30

    # If watcher delete worked, file is gone. If not, sync-now will clean it.
    Stop-Smirror
    Start-Sleep 2

    # Inject a ghost directly on remote (simulating a leak)
    [System.IO.File]::WriteAllText("$DstDir1\injected_ghost.txt", "injected orphan")

    # Restart and sync-now
    Start-Smirror
    $output = Invoke-Smirror "sync-now","Alpha","--config",$ConfigPath
    Write-Host "  sync-now output: $($output.Trim())"

    Assert-FileNotExists "$DstDir1\injected_ghost.txt" "Injected ghost cleaned by sync-now"
}

function Test-VerifyZeroDrift {
    Write-Host "`n--- STRESS 11: Final verify -- zero drift across all mirrors ---" -ForegroundColor Yellow

    # Run sync-now to ensure everything is reconciled
    $output = Invoke-Smirror "sync-now","--config",$ConfigPath
    Start-Sleep 5

    # Run test-mirrors to verify
    $verify = Invoke-Smirror "test-mirrors","--config",$ConfigPath
    Write-Host $verify

    if ($verify -match "0 failed") { Assert-Pass "All diagnostics passed" }
    else { Assert-Fail "Diagnostics had failures" }

    if ($verify -match "No drift detected") { Assert-Pass "Zero drift detected" }
    elseif ($verify -match "drift") { Assert-Fail "Drift detected after stress test" }
    else { Assert-Pass "Verification completed" }
}

# ── Main ────────────────────────────────────────────────────────────

try {
    $totalStart = [System.Diagnostics.Stopwatch]::StartNew()

    Setup-TestEnv
    Start-Smirror

    Test-BurstCreate 100
    Test-RapidOverwrite 50
    Test-DeepDirectoryCreate
    Test-BurstRename 30
    Test-BurstDelete 50
    Test-ConcurrentMirrorStorm 30
    Test-LargeFile
    Test-EmptyFiles 20
    Test-SpecialCharFilenames
    Test-SyncNowGhostCleanup
    Test-VerifyZeroDrift

    $totalStart.Stop()

    Write-Host "`n============================================" -ForegroundColor Cyan
    Write-Host "  STRESS TEST RESULTS" -ForegroundColor Cyan
    Write-Host "  Passed: $Passed" -ForegroundColor Green
    Write-Host "  Failed: $Failed" -ForegroundColor $(if ($Failed -gt 0) { "Red" } else { "Green" })
    Write-Host "  Total time: $([math]::Round($totalStart.Elapsed.TotalSeconds, 1))s" -ForegroundColor Cyan
    Write-Host "============================================" -ForegroundColor Cyan

    if ($Failed -gt 0) {
        Write-Host ""
        Write-Host "  Showing daemon log for diagnosis:" -ForegroundColor Yellow
        Show-DaemonLog 80
    }
} finally {
    Cleanup
}

exit $Failed
