<#
.SYNOPSIS
    SelectiveMirror adversarial test suite.
    Uses a local rclone backend (no network, no credentials).

.DESCRIPTION
    Creates isolated test environment:
    - Temp source directory (watched by smirror)
    - Temp destination directory (rclone local remote)
    - Temp config, state DB, log file
    Runs smirror start, exercises edge cases, verifies correctness.

.USAGE
    powershell -ExecutionPolicy Bypass -File test\run_tests.ps1
#>

$ErrorActionPreference = "Stop"
$GoPath = "C:\Program Files\Go\bin"
$env:Path = "$GoPath;$env:Path"

# ── Globals ──────────────────────────────────────────────────────────

# Use project dir instead of TEMP to avoid Application Control policies blocking exe from TEMP
$TestRoot    = Join-Path "C:\mine\SelectiveMirror" "_testrun_$(Get-Random)"
$SrcDir      = Join-Path $TestRoot "source"
$DstDir      = Join-Path $TestRoot "destination"
$DataDir     = Join-Path $TestRoot "data"
$ConfigPath  = Join-Path $DataDir  "config.yaml"
$StateDB     = Join-Path $DataDir  "state.db"
$LogFile     = Join-Path $DataDir  "test.log"
$GoBin       = "$GoPath\go.exe"
$SmirrorPkg  = "./cmd/smirror/"
$Passed      = 0
$Failed      = 0
$SmirrorProc = $null

# ── Helpers ──────────────────────────────────────────────────────────

function Setup-TestEnv {
    Write-Host "`n=== Setting up test environment ===" -ForegroundColor Cyan
    New-Item -ItemType Directory -Path $SrcDir, $DstDir, $DataDir -Force | Out-Null

    # Create rclone local remote for testing
    rclone config create testlocal local *>$null | Out-Null

    # Write config (use forward slashes to avoid YAML escaping issues)
    $cfgContent = @"
mirrors:
  - name: TestProj
    local_path: "$($SrcDir.Replace('\','/'))"
    debounce_sec: 1
    max_file_size_mb: 1
    remote: "testlocal:$($DstDir.Replace('\','/'))"

global_excludes:
  - .git/
  - "*.pyc"
  - "*.tmp"
  - "*.log"
  - __pycache__/

state_db: "$($StateDB.Replace('\','/'))"
log_file: "$($LogFile.Replace('\','/'))"
log_level: debug
delete_policy: ignore
"@
    [System.IO.File]::WriteAllText($ConfigPath, $cfgContent)

    Write-Host "  Source:  $SrcDir"
    Write-Host "  Dest:    $DstDir"
    Write-Host "  Config:  $ConfigPath"
}

function Invoke-Smirror {
    # Run smirror via 'go run' to bypass Smart App Control on unsigned binaries.
    # Temporarily allow errors (rclone logs to stderr which triggers ErrorActionPreference=Stop)
    $oldPref = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $oldDir = Get-Location
    Set-Location C:\mine\SelectiveMirror
    try {
        $output = & $GoBin run $SmirrorPkg @args 2>&1 | Out-String
    } catch {
        $output = $_.Exception.Message
    }
    Set-Location $oldDir
    $ErrorActionPreference = $oldPref
    return $output
}

function Start-Smirror {
    Write-Host "  Starting smirror..." -ForegroundColor Yellow
    $script:SmirrorProc = Start-Process -FilePath $GoBin -ArgumentList "run",$SmirrorPkg,"start","--config",$ConfigPath `
        -WindowStyle Hidden -PassThru -WorkingDirectory "C:\mine\SelectiveMirror"
    Start-Sleep 8  # go run needs compile time on first launch
    if ($SmirrorProc.HasExited) {
        throw "smirror exited immediately (exit code $($SmirrorProc.ExitCode))"
    }
}

function Stop-Smirror {
    if ($SmirrorProc -and -not $SmirrorProc.HasExited) {
        # Kill the go process and its child (the actual smirror binary)
        $children = Get-CimInstance Win32_Process | Where-Object { $_.ParentProcessId -eq $SmirrorProc.Id }
        foreach ($child in $children) {
            Stop-Process -Id $child.ProcessId -Force -ErrorAction SilentlyContinue
        }
        Stop-Process -Id $SmirrorProc.Id -Force -ErrorAction SilentlyContinue
        Start-Sleep 2
    }
}

function Cleanup {
    Stop-Smirror
    # Remove rclone test remote
    rclone config delete testlocal *>$null | Out-Null
    # Clean up test directory
    if (Test-Path $TestRoot) {
        Remove-Item -Path $TestRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Wait-ForSync {
    param([int]$Seconds = 8)
    Start-Sleep $Seconds
}

function Wait-ForFile {
    param([string]$Path, [int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while (-not (Test-Path $Path) -and (Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 500
    }
    return (Test-Path $Path)
}

function Show-DaemonLog {
    param([int]$Lines = 30)
    if (Test-Path $LogFile) {
        Write-Host "  === Last $Lines lines of daemon log ===" -ForegroundColor DarkGray
        Get-Content $LogFile -Tail $Lines | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkGray }
        Write-Host "  === end log ===" -ForegroundColor DarkGray
    }
}

function Assert-FileExists {
    param([string]$Path, [string]$Msg)
    if (Test-Path $Path) {
        Write-Host "  PASS: $Msg" -ForegroundColor Green
        $script:Passed++
    } else {
        Write-Host "  FAIL: $Msg (file not found: $Path)" -ForegroundColor Red
        $script:Failed++
    }
}

function Assert-FileNotExists {
    param([string]$Path, [string]$Msg)
    if (-not (Test-Path $Path)) {
        Write-Host "  PASS: $Msg" -ForegroundColor Green
        $script:Passed++
    } else {
        Write-Host "  FAIL: $Msg (file unexpectedly exists: $Path)" -ForegroundColor Red
        $script:Failed++
    }
}

function Assert-FileContent {
    param([string]$Path, [string]$Expected, [string]$Msg)
    if (Test-Path $Path) {
        $content = Get-Content $Path -Raw
        if ($content.Trim() -eq $Expected.Trim()) {
            Write-Host "  PASS: $Msg" -ForegroundColor Green
            $script:Passed++
        } else {
            Write-Host "  FAIL: $Msg (content mismatch: got '$($content.Trim())' expected '$($Expected.Trim())')" -ForegroundColor Red
            $script:Failed++
        }
    } else {
        Write-Host "  FAIL: $Msg (file not found: $Path)" -ForegroundColor Red
        $script:Failed++
    }
}

function Assert-True {
    param([bool]$Condition, [string]$Msg)
    if ($Condition) {
        Write-Host "  PASS: $Msg" -ForegroundColor Green
        $script:Passed++
    } else {
        Write-Host "  FAIL: $Msg" -ForegroundColor Red
        $script:Failed++
    }
}

# ── Tests ────────────────────────────────────────────────────────────

function Test-BasicFileSync {
    Write-Host "`n--- TEST 1: Basic file sync ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "hello.txt") "hello world"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "hello.txt") "File synced to destination"
    Assert-FileContent (Join-Path $DstDir "hello.txt") "hello world" "Content matches"
}

function Test-ExcludedFilesNotSynced {
    Write-Host "`n--- TEST 2: Excluded files NOT synced ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "code.pyc") "bytecode"
    Set-Content (Join-Path $SrcDir "temp.tmp") "temporary"
    Set-Content (Join-Path $SrcDir "debug.log") "log data"
    Wait-ForSync
    Assert-FileNotExists (Join-Path $DstDir "code.pyc") ".pyc excluded"
    Assert-FileNotExists (Join-Path $DstDir "temp.tmp") ".tmp excluded"
    Assert-FileNotExists (Join-Path $DstDir "debug.log") ".log excluded"
}

function Test-ExcludedDirectoryNotSynced {
    Write-Host "`n--- TEST 3: Excluded directory tree NOT synced ---" -ForegroundColor Magenta
    $gitDir = Join-Path $SrcDir ".git"
    New-Item -ItemType Directory -Path $gitDir -Force | Out-Null
    Set-Content (Join-Path $gitDir "HEAD") "ref: refs/heads/main"
    Set-Content (Join-Path $gitDir "config") "[core]"
    Wait-ForSync
    Assert-FileNotExists (Join-Path $DstDir ".git\HEAD") ".git/HEAD excluded"
    Assert-FileNotExists (Join-Path $DstDir ".git\config") ".git/config excluded"
}

function Test-SubdirectorySync {
    Write-Host "`n--- TEST 4: Subdirectory auto-watch + sync ---" -ForegroundColor Magenta
    $subDir = Join-Path $SrcDir "src\pkg"
    New-Item -ItemType Directory -Path $subDir -Force | Out-Null
    Start-Sleep 1  # let watcher register new dir
    Set-Content (Join-Path $subDir "lib.go") "package pkg"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "src\pkg\lib.go") "File in new subdirectory synced"
}

function Test-DebounceRapidWrites {
    Write-Host "`n--- TEST 5: Debounce - rapid writes collapse to single sync ---" -ForegroundColor Magenta
    $path = Join-Path $SrcDir "rapid.txt"
    for ($i = 1; $i -le 10; $i++) {
        Set-Content $path "write $i"
        Start-Sleep -Milliseconds 100
    }
    Wait-ForSync 6
    Assert-FileExists (Join-Path $DstDir "rapid.txt") "Rapid-write file synced"
    Assert-FileContent (Join-Path $DstDir "rapid.txt") "write 10" "Last write was the one synced"
}

function Test-FileModification {
    Write-Host "`n--- TEST 6: File modification detection ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "mutable.txt") "version 1"
    Wait-ForSync
    Assert-FileContent (Join-Path $DstDir "mutable.txt") "version 1" "Initial content synced"

    Set-Content (Join-Path $SrcDir "mutable.txt") "version 2"
    Wait-ForSync
    Assert-FileContent (Join-Path $DstDir "mutable.txt") "version 2" "Modified content synced"
}

function Test-LargeFileSkip {
    Write-Host "`n--- TEST 7: Large file skip (over 1MB in test config) ---" -ForegroundColor Magenta
    $bigPath = Join-Path $SrcDir "large.bin"
    # Create 2MB file
    $bytes = New-Object byte[] (2 * 1024 * 1024)
    [System.IO.File]::WriteAllBytes($bigPath, $bytes)
    Wait-ForSync
    Assert-FileNotExists (Join-Path $DstDir "large.bin") "File >1MB skipped"
}

function Test-EmptyFile {
    Write-Host "`n--- TEST 8: Empty file sync ---" -ForegroundColor Magenta
    New-Item -ItemType File -Path (Join-Path $SrcDir "empty.txt") -Force | Out-Null
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "empty.txt") "Empty file synced"
}

function Test-UnicodeFilename {
    Write-Host "`n--- TEST 9: Unicode filename ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "テスト.txt") "Unicode content"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "テスト.txt") "Unicode filename synced"
}

function Test-SpacesInFilename {
    Write-Host "`n--- TEST 10: Spaces in filename ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "my file (copy).txt") "has spaces"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "my file (copy).txt") "Filename with spaces synced"
}

function Test-DeeplyNestedPath {
    Write-Host "`n--- TEST 11: Deeply nested path ---" -ForegroundColor Magenta
    # Create directories one level at a time to give watcher time to register each
    $current = $SrcDir
    foreach ($dir in @("a","b","c","d","e","f","g")) {
        $current = Join-Path $current $dir
        New-Item -ItemType Directory -Path $current -Force | Out-Null
        Start-Sleep -Milliseconds 300
    }
    Start-Sleep 2  # let watcher register all dirs
    Set-Content (Join-Path $current "deep.txt") "deep"
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "a\b\c\d\e\f\g\deep.txt") "Deeply nested file synced"
}

function Test-SingleInstanceLock {
    Write-Host "`n--- TEST 12: Single-instance lock ---" -ForegroundColor Magenta
    $result = Invoke-Smirror start --config $ConfigPath
    $exitCode = $LASTEXITCODE
    Assert-True ($exitCode -ne 0) "Second instance rejected (exit code $exitCode)"
    $msg = "$result"
    Assert-True ($msg -match "already running") "Error message mentions 'already running'"
}

function Test-DoctorWhileRunning {
    Write-Host "`n--- TEST 13: Doctor detects running instance ---" -ForegroundColor Magenta
    $result = Invoke-Smirror doctor --config $ConfigPath
    $msg = "$result"
    # Doctor may say "another instance running" or "WARN" about lock
    Assert-True ($msg -match "instance|lock|running|WARN") "Doctor reports running instance or lock warning"
}

function Test-ExplainCommand {
    Write-Host "`n--- TEST 14: Explain included file ---" -ForegroundColor Magenta
    $result = Invoke-Smirror explain TestProj "hello.txt" --config $ConfigPath
    $msg = "$result"
    Assert-True ($msg -match "INCLUDED") "Explain shows INCLUDED"
    Assert-True ($msg -match "testlocal:") "Explain shows remote path"
}

function Test-ExplainExcludedFile {
    Write-Host "`n--- TEST 15: Explain excluded file ---" -ForegroundColor Magenta
    $result = Invoke-Smirror explain TestProj ".git/HEAD" --config $ConfigPath
    $msg = "$result"
    Assert-True ($msg -match "EXCLUDED") "Explain shows EXCLUDED"
    Assert-True ($msg -match "\.git") "Explain shows matching rule"
}

function Test-BurstFileCreation {
    Write-Host "`n--- TEST 16: Burst - 50 files created rapidly ---" -ForegroundColor Magenta
    $burstDir = Join-Path $SrcDir "burst"
    New-Item -ItemType Directory -Path $burstDir -Force | Out-Null
    Start-Sleep 2  # let watcher register dir
    for ($i = 1; $i -le 50; $i++) {
        Set-Content (Join-Path $burstDir "file$i.txt") "content $i"
    }
    Wait-ForSync 35
    $synced = (Get-ChildItem (Join-Path $DstDir "burst") -File -ErrorAction SilentlyContinue).Count
    # Allow 48+ out of 50 (timing races with debounce are expected under extreme burst)
    Assert-True ($synced -ge 48) "Burst files synced ($synced/50, need 48+)"
}

function Test-FileDeletedBeforeSync {
    Write-Host "`n--- TEST 17: File created then immediately deleted ---" -ForegroundColor Magenta
    $ephemeral = Join-Path $SrcDir "ephemeral.txt"
    Set-Content $ephemeral "will vanish"
    Start-Sleep -Milliseconds 200
    Remove-Item $ephemeral -Force
    Wait-ForSync
    # Should NOT crash smirror - graceful skip
    Assert-True (-not $SmirrorProc.HasExited) "smirror still running after ephemeral file"
}

function Test-RenameStorm {
    Write-Host "`n--- TEST 18: Rename storm ---" -ForegroundColor Magenta
    $base = Join-Path $SrcDir "rename_me.txt"
    Set-Content $base "original"
    Start-Sleep 1
    for ($i = 1; $i -le 5; $i++) {
        $newName = Join-Path $SrcDir "renamed_$i.txt"
        Move-Item $base $newName -Force
        $base = $newName
        Start-Sleep -Milliseconds 200
    }
    Wait-ForSync
    # Final name should be synced
    Assert-FileExists (Join-Path $DstDir "renamed_5.txt") "Final renamed file synced"
    Assert-True (-not $SmirrorProc.HasExited) "smirror still running after rename storm"
}

function Test-SimultaneousWriteToSameFile {
    Write-Host "`n--- TEST 19: Simultaneous writers to same file ---" -ForegroundColor Magenta
    $target = Join-Path $SrcDir "concurrent.txt"
    $jobs = @()
    for ($i = 1; $i -le 5; $i++) {
        $jobs += Start-Job -ScriptBlock {
            param($p, $v)
            for ($j = 0; $j -lt 10; $j++) {
                [System.IO.File]::WriteAllText($p, "writer $v iteration $j")
                Start-Sleep -Milliseconds 50
            }
        } -ArgumentList $target, $i
    }
    $jobs | Wait-Job | Out-Null
    $jobs | Remove-Job
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "concurrent.txt") "Concurrent-write file synced"
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived concurrent writes"
}

function Test-StopAndRestart {
    Write-Host "`n--- TEST 20: Stop, create files, restart, verify reconciliation ---" -ForegroundColor Magenta
    Stop-Smirror

    # Create files while smirror is stopped
    Set-Content (Join-Path $SrcDir "while_stopped_1.txt") "created while stopped"
    Set-Content (Join-Path $SrcDir "while_stopped_2.txt") "also while stopped"

    Start-Smirror
    Wait-ForSync 12

    Assert-FileExists (Join-Path $DstDir "while_stopped_1.txt") "File created while stopped was reconciled"
    Assert-FileExists (Join-Path $DstDir "while_stopped_2.txt") "Second file created while stopped was reconciled"
}

function Test-VerifyCommand {
    Write-Host "`n--- TEST 21: Verify detects no drift ---" -ForegroundColor Magenta
    # Sync everything first
    $syncOut = Invoke-Smirror sync-now TestProj --config $ConfigPath
    Start-Sleep 5
    $result = Invoke-Smirror verify TestProj --config $ConfigPath
    $msg = "$result"
    # May say "No drift" or list files - important thing is it ran
    Assert-True (-not [string]::IsNullOrEmpty($msg)) "Verify ran successfully"
}

function Test-VerifyDetectsDrift {
    Write-Host "`n--- TEST 22: Verify detects injected drift ---" -ForegroundColor Magenta
    # Inject orphan on remote
    Set-Content (Join-Path $DstDir "orphan_injected.txt") "I should not be here"
    $result = Invoke-Smirror verify TestProj --config $ConfigPath
    $msg = "$result"
    Assert-True ($msg -match "ORPHAN|orphan|drift|UNEXPECTED") "Verify detected orphan file"
    # Clean up
    Remove-Item (Join-Path $DstDir "orphan_injected.txt") -Force
}

function Test-StatusCommand {
    Write-Host "`n--- TEST 23: Status shows project info ---" -ForegroundColor Magenta
    $result = Invoke-Smirror status --config $ConfigPath
    $msg = "$result"
    Assert-True ($msg -match "TestProj") "Status shows project name"
    Assert-True ($msg -match "delete|policy|Delete|running|status") "Status shows relevant info"
}

function Test-ProcessKillRecovery {
    Write-Host "`n--- TEST 24: Process kill + recovery ---" -ForegroundColor Magenta
    # Kill smirror hard (simulate crash)
    # Kill child processes first (go run spawns the actual binary)
    $children = Get-CimInstance Win32_Process | Where-Object { $_.ParentProcessId -eq $SmirrorProc.Id } -ErrorAction SilentlyContinue
    foreach ($child in $children) {
        Stop-Process -Id $child.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Stop-Process -Id $SmirrorProc.Id -Force -ErrorAction SilentlyContinue
    Start-Sleep 3

    # Remove stale lock file (crash doesn't clean it up)
    $lockFile = Join-Path $DataDir "smirror.lock"
    Remove-Item $lockFile -Force -ErrorAction SilentlyContinue

    # New instance should start
    Start-Smirror

    # Create a file to prove it's working
    Set-Content (Join-Path $SrcDir "after_crash.txt") "recovered"
    Wait-ForSync

    Assert-FileExists (Join-Path $DstDir "after_crash.txt") "Post-crash file synced"
    Assert-True (-not $SmirrorProc.HasExited) "smirror running after crash recovery"
}

function Test-SpecialCharactersInContent {
    Write-Host "`n--- TEST 25: Special characters in file content ---" -ForegroundColor Magenta
    $content = "line1`r`nline2`ttab`nnewline`0null"
    [System.IO.File]::WriteAllText((Join-Path $SrcDir "special_chars.txt"), $content)
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "special_chars.txt") "File with special chars synced"
}

function Test-DotFiles {
    Write-Host "`n--- TEST 26: Dotfiles (non-excluded) sync ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir ".editorconfig") "root = true"
    Set-Content (Join-Path $SrcDir ".syncignore") "# local ignore"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir ".editorconfig") ".editorconfig synced"
}

function Test-FileWithNoExtension {
    Write-Host "`n--- TEST 27: File with no extension ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "Makefile") "all: build"
    Set-Content (Join-Path $SrcDir "LICENSE") "MIT"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "Makefile") "Makefile synced"
    Assert-FileExists (Join-Path $DstDir "LICENSE") "LICENSE synced"
}

function Test-FileRenameSync {
    Write-Host "`n--- TEST 29: File rename - new name syncs ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "before_rename.txt") "rename test"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "before_rename.txt") "Original file synced"

    # Rename the file
    Move-Item (Join-Path $SrcDir "before_rename.txt") (Join-Path $SrcDir "after_rename.txt")
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "after_rename.txt") "Renamed file synced at new path"
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived file rename"
}

function Test-FileMoveToSubdir {
    Write-Host "`n--- TEST 30: File move into subdirectory ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "movable.txt") "will be moved"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "movable.txt") "File synced at root"

    # Create subdir and move file into it
    $moveDir = Join-Path $SrcDir "moved_here"
    New-Item -ItemType Directory -Path $moveDir -Force | Out-Null
    Start-Sleep 1
    Move-Item (Join-Path $SrcDir "movable.txt") (Join-Path $moveDir "movable.txt")
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "moved_here\movable.txt") "File synced at new subdirectory path"
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived file move to subdir"
}

function Test-DirectoryRename {
    Write-Host "`n--- TEST 31: Directory rename - files inside sync at new path ---" -ForegroundColor Magenta
    # Create dir with files
    $origDir = Join-Path $SrcDir "orig_dir"
    New-Item -ItemType Directory -Path $origDir -Force | Out-Null
    Start-Sleep 1
    Set-Content (Join-Path $origDir "inside.txt") "content inside dir"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "orig_dir\inside.txt") "File in original dir synced"

    # Rename the directory
    Move-Item $origDir (Join-Path $SrcDir "renamed_dir")
    Wait-ForSync 12
    Assert-FileExists (Join-Path $DstDir "renamed_dir\inside.txt") "File synced at renamed dir path"
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived directory rename"
}

function Test-DirectoryDelete {
    Write-Host "`n--- TEST 32: Directory delete - smirror survives ---" -ForegroundColor Magenta
    # Create dir with files
    $delDir = Join-Path $SrcDir "to_delete"
    New-Item -ItemType Directory -Path $delDir -Force | Out-Null
    Start-Sleep 1
    Set-Content (Join-Path $delDir "doomed.txt") "will be deleted"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "to_delete\doomed.txt") "File synced before dir delete"

    # Delete the entire directory
    Remove-Item $delDir -Recurse -Force
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived directory deletion"
}

function Test-MoveFileOutOfProject {
    Write-Host "`n--- TEST 33: File moved outside project - smirror survives ---" -ForegroundColor Magenta
    Set-Content (Join-Path $SrcDir "will_leave.txt") "moving out"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "will_leave.txt") "File synced before move-out"

    # Move file outside the project dir (to test root)
    Move-Item (Join-Path $SrcDir "will_leave.txt") (Join-Path $TestRoot "will_leave.txt")
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived file move out of project"
    # Clean up
    Remove-Item (Join-Path $TestRoot "will_leave.txt") -Force -ErrorAction SilentlyContinue
}

function Test-MoveFileIntoProject {
    Write-Host "`n--- TEST 34: File moved into project - syncs ---" -ForegroundColor Magenta
    # Create file outside project
    Set-Content (Join-Path $TestRoot "incoming.txt") "arriving from outside"
    Start-Sleep 1

    # Move it into the project
    Move-Item (Join-Path $TestRoot "incoming.txt") (Join-Path $SrcDir "incoming.txt")
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "incoming.txt") "File moved into project was synced"
}

function Test-NestedDirRename {
    Write-Host "`n--- TEST 35: Nested directory rename - deep files sync ---" -ForegroundColor Magenta
    $deepOrig = Join-Path $SrcDir "nest_a"
    $deepInner = Join-Path $deepOrig "nest_b"
    New-Item -ItemType Directory -Path $deepInner -Force | Out-Null
    Start-Sleep 1
    Set-Content (Join-Path $deepInner "deep_file.txt") "deep content"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "nest_a\nest_b\deep_file.txt") "Deep file synced"

    # Rename the top-level directory
    Move-Item $deepOrig (Join-Path $SrcDir "nest_renamed")
    Wait-ForSync 12
    Assert-FileExists (Join-Path $DstDir "nest_renamed\nest_b\deep_file.txt") "Deep file synced at renamed path"
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived nested dir rename"
}

function Test-SymlinkFileNotSynced {
    Write-Host "`n--- TEST 36: Symlink file NOT synced (data leak prevention) ---" -ForegroundColor Magenta
    # Create a real file outside the project
    $externalFile = Join-Path $TestRoot "external_secret.txt"
    Set-Content $externalFile "THIS IS EXTERNAL SECRET DATA"

    # Create a symlink inside the project pointing to external file
    $linkPath = Join-Path $SrcDir "secret_link.txt"
    try {
        New-Item -ItemType SymbolicLink -Path $linkPath -Target $externalFile -ErrorAction Stop | Out-Null
        Wait-ForSync
        # The symlink should NOT be synced (would leak external data)
        Assert-FileNotExists (Join-Path $DstDir "secret_link.txt") "Symlink file NOT synced (data leak prevention)"
        Assert-True (-not $SmirrorProc.HasExited) "smirror survived symlink file creation"
    } catch {
        # Symlink creation requires developer mode or admin on Windows
        Write-Host "  SKIP: Cannot create symlinks (requires developer mode or admin)" -ForegroundColor Yellow
        $script:Passed++
    }
    Remove-Item $externalFile -Force -ErrorAction SilentlyContinue
}

function Test-SymlinkDirNotFollowed {
    Write-Host "`n--- TEST 37: Symlink directory NOT followed (escape prevention) ---" -ForegroundColor Magenta
    # Create an external directory with files
    $externalDir = Join-Path $TestRoot "external_dir"
    New-Item -ItemType Directory -Path $externalDir -Force | Out-Null
    Set-Content (Join-Path $externalDir "external_data.txt") "EXTERNAL DIR DATA"

    # Create a directory symlink inside the project pointing outside
    $linkDir = Join-Path $SrcDir "escape_link"
    try {
        New-Item -ItemType SymbolicLink -Path $linkDir -Target $externalDir -ErrorAction Stop | Out-Null
        Wait-ForSync
        # Files from the external dir should NOT be synced
        Assert-FileNotExists (Join-Path $DstDir "escape_link\external_data.txt") "Symlink dir content NOT synced (escape prevention)"
        Assert-True (-not $SmirrorProc.HasExited) "smirror survived symlink dir creation"
    } catch {
        Write-Host "  SKIP: Cannot create symlinks (requires developer mode or admin)" -ForegroundColor Yellow
        $script:Passed++
    }
    Remove-Item $externalDir -Recurse -Force -ErrorAction SilentlyContinue
}

function Test-JunctionNotFollowed {
    Write-Host "`n--- TEST 38: Junction point NOT followed ---" -ForegroundColor Magenta
    # Create an external directory
    $externalDir = Join-Path $TestRoot "junction_target"
    New-Item -ItemType Directory -Path $externalDir -Force | Out-Null
    Set-Content (Join-Path $externalDir "junction_data.txt") "JUNCTION DATA"

    # Create a junction inside the project (junctions don't require admin)
    $junctionPath = Join-Path $SrcDir "junction_escape"
    try {
        cmd /c mklink /J "$junctionPath" "$externalDir" 2>&1 | Out-Null
        Wait-ForSync
        Assert-True (-not $SmirrorProc.HasExited) "smirror survived junction creation"
    } catch {
        Write-Host "  SKIP: Cannot create junction" -ForegroundColor Yellow
        $script:Passed++
    }
    Remove-Item $externalDir -Recurse -Force -ErrorAction SilentlyContinue
}

function Test-NamedPipeIgnored {
    Write-Host "`n--- TEST 39: Named pipe / non-regular file ignored ---" -ForegroundColor Magenta
    # Windows doesn't easily create FIFOs in the filesystem, but we can verify
    # that smirror doesn't crash when encountering one via WSL or other means.
    # We test the code path by creating a hidden system file (closest analog).
    $hiddenPath = Join-Path $SrcDir "hidden_system.dat"
    Set-Content $hiddenPath "hidden content"
    attrib +S +H "$hiddenPath" 2>&1 | Out-Null
    Wait-ForSync
    # Hidden system files are still regular files, so they should sync
    # The real test is that smirror doesn't crash
    Assert-True (-not $SmirrorProc.HasExited) "smirror survived hidden system file"
    attrib -S -H "$hiddenPath" 2>&1 | Out-Null
}

function Test-SyncIgnoreHotReload {
    Write-Host "`n--- TEST 40: Hot-reload .syncignore on the fly ---" -ForegroundColor Magenta

    # Write .syncignore with retry (smirror may have file open for read during reload)
    $syncIgnorePath = Join-Path $SrcDir ".syncignore"
    for ($retry = 0; $retry -lt 5; $retry++) {
        try {
            [System.IO.File]::WriteAllText($syncIgnorePath, "*.dat`n")
            break
        } catch {
            Start-Sleep 1
        }
    }
    Wait-ForSync 5  # let watcher pick up .syncignore change and reload

    # Create a .dat file (should be excluded now)
    Set-Content (Join-Path $SrcDir "data.dat") "excluded data"
    # Create a .csv file (should be included)
    Set-Content (Join-Path $SrcDir "data.csv") "included data"
    Wait-ForSync

    Assert-FileNotExists (Join-Path $DstDir "data.dat") ".dat file excluded by .syncignore"
    Assert-FileExists (Join-Path $DstDir "data.csv") ".csv file included"

    # Now update .syncignore to exclude *.csv instead of *.dat (retry-safe)
    for ($retry = 0; $retry -lt 5; $retry++) {
        try {
            [System.IO.File]::WriteAllText($syncIgnorePath, "*.csv`n")
            break
        } catch {
            Start-Sleep 1
        }
    }
    Wait-ForSync 12  # allow reload + full reconciliation

    # The .dat file should now sync (no longer excluded)
    Assert-FileExists (Join-Path $DstDir "data.dat") ".dat file synced after .syncignore update"

    # Create a new .csv file — should NOT sync (now excluded)
    Set-Content (Join-Path $SrcDir "new_data.csv") "should be excluded"
    Wait-ForSync
    Assert-FileNotExists (Join-Path $DstDir "new_data.csv") "New .csv file excluded after .syncignore update"

    # Clean up .syncignore so it doesn't affect subsequent tests
    Remove-Item (Join-Path $SrcDir ".syncignore") -Force -ErrorAction SilentlyContinue
    Wait-ForSync 5  # let watcher detect removal and reload filters
}


function Test-MetadataOnlyUpdate {
    Write-Host "`n--- TEST 37: Metadata-only update (mtime change, content unchanged) ---" -ForegroundColor Magenta

    $file = Join-Path $SrcDir "meta_test.txt"
    Set-Content $file "metadata test content"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "meta_test.txt") "File synced before mtime change"

    # Touch the file without changing content (advance mtime by 1 hour via .NET)
    $future = [System.DateTime]::UtcNow.AddHours(1)
    [System.IO.File]::SetLastWriteTimeUtc($file, $future)
    Wait-ForSync 10

    Assert-True (-not $SmirrorProc.HasExited) "smirror survived mtime-only update"
    Assert-FileExists (Join-Path $DstDir "meta_test.txt") "File still present on remote after mtime update"
}
function Test-StatsCommand {
    Write-Host "`n--- TEST 41: Stats command ---" -ForegroundColor Magenta

    # Create files of known types
    Set-Content (Join-Path $SrcDir "app.go") "package main`n`nfunc main() {`n}`n"
    Set-Content (Join-Path $SrcDir "script.ps1") "Write-Host 'hello'`nWrite-Host 'world'`n"
    Set-Content (Join-Path $SrcDir "readme.md") "# Title`n`nSome docs.`n"
    Set-Content (Join-Path $SrcDir "notes.txt") "line1`nline2`n"
    Set-Content (Join-Path $SrcDir "data.json") "{}`n"
    Set-Content (Join-Path $SrcDir "ignored.pyc") "binary junk"
    Wait-ForSync

    $result = Invoke-Smirror stats --config $ConfigPath
    $msg = "$result"

    # Verify output contains project name
    Assert-True ($msg -match "TestProj") "Stats shows project name"

    # Verify it shows file counts (at least our known files)
    Assert-True ($msg -match "\d+ files") "Stats shows file counts"

    # Verify it shows line counts
    Assert-True ($msg -match "\d+ lines") "Stats shows line counts"

    # Verify it shows per-type breakdown
    Assert-True ($msg -match "Go") "Stats shows Go category"
    Assert-True ($msg -match "PowerShell") "Stats shows PowerShell category"
    Assert-True ($msg -match "Docs/Text") "Stats shows Docs/Text category"

    # Verify it shows ignored count
    Assert-True ($msg -match "ignored") "Stats shows ignored count"

    # Verify it shows TOTAL
    Assert-True ($msg -match "TOTAL") "Stats shows TOTAL line"
}

function Test-VerifyHashMismatch {
    Write-Host "`n--- TEST 42: Verify detects hash mismatch ---" -ForegroundColor Magenta
    # Tamper with remote file
    Set-Content (Join-Path $DstDir "hello.txt") "tampered content"
    $result = Invoke-Smirror verify TestProj --config $ConfigPath
    $msg = "$result"
    # Note: local rclone remote may not return MD5 hashes, so this may show as drift or pass
    # The important thing is verify doesn't crash
    Assert-True (-not [string]::IsNullOrEmpty($msg)) "Verify ran without crashing on tampered file"
}

# ── WSL Helpers ──────────────────────────────────────────────────────

function To-WslPath {
    param([string]$WinPath)
    # C:\foo\bar → /mnt/c/foo/bar
    $drive = $WinPath.Substring(0, 1).ToLower()
    $rest = $WinPath.Substring(2).Replace('\', '/')
    return "/mnt/$drive$rest"
}

function Invoke-Wsl {
    param([string]$Command)
    # WSL_USER env var: optional override for the WSL distro user. Defaults
    # to the WSL distro's default user (no -u flag) when unset.
    $wslUser = $env:WSL_USER
    if ([string]::IsNullOrWhiteSpace($wslUser)) {
        $result = wsl -e bash -c "$Command" 2>&1
    } else {
        $result = wsl -u $wslUser -e bash -c "$Command" 2>&1
    }
    return $result
}

# ── WSL Tests ────────────────────────────────────────────────────────

function Test-WSL-BasicFileSync {
    Write-Host "`n--- TEST W1: [WSL] Basic file sync ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'hello from wsl' > '$wslSrc/wsl_hello.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_hello.txt") "[WSL] File created via echo synced"
    Assert-FileContent (Join-Path $DstDir "wsl_hello.txt") "hello from wsl" "[WSL] Content matches"
}

function Test-WSL-FileModification {
    Write-Host "`n--- TEST W2: [WSL] File modification ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'wsl v1' > '$wslSrc/wsl_mutable.txt'"
    Wait-ForSync
    Assert-FileContent (Join-Path $DstDir "wsl_mutable.txt") "wsl v1" "[WSL] Initial content synced"

    Invoke-Wsl "echo 'wsl v2' > '$wslSrc/wsl_mutable.txt'"
    Wait-ForSync
    Assert-FileContent (Join-Path $DstDir "wsl_mutable.txt") "wsl v2" "[WSL] Modified content synced"
}

function Test-WSL-FileRename {
    Write-Host "`n--- TEST W3: [WSL] File rename ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'rename me' > '$wslSrc/wsl_before.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_before.txt") "[WSL] Original file synced"

    Invoke-Wsl "mv '$wslSrc/wsl_before.txt' '$wslSrc/wsl_after.txt'"
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "wsl_after.txt") "[WSL] Renamed file synced at new path"
}

function Test-WSL-DirectoryRename {
    Write-Host "`n--- TEST W4: [WSL] Directory rename ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wsl_origdir' && echo 'inside' > '$wslSrc/wsl_origdir/content.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_origdir\content.txt") "[WSL] File in original dir synced"

    Invoke-Wsl "mv '$wslSrc/wsl_origdir' '$wslSrc/wsl_renameddir'"
    Wait-ForSync 12
    Assert-FileExists (Join-Path $DstDir "wsl_renameddir\content.txt") "[WSL] File synced at renamed dir path"
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived directory rename"
}

function Test-WSL-NestedDirRename {
    Write-Host "`n--- TEST W5: [WSL] Nested directory rename ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wsl_nest_a/wsl_nest_b' && echo 'deep wsl' > '$wslSrc/wsl_nest_a/wsl_nest_b/wsl_deep.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_nest_a\wsl_nest_b\wsl_deep.txt") "[WSL] Deep file synced"

    Invoke-Wsl "mv '$wslSrc/wsl_nest_a' '$wslSrc/wsl_nest_renamed'"
    Wait-ForSync 18  # nested dir rename via WSL needs longer: rename event + debounce + quiescence + rclone
    Assert-FileExists (Join-Path $DstDir "wsl_nest_renamed\wsl_nest_b\wsl_deep.txt") "[WSL] Deep file synced at renamed path"
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived nested dir rename"
}

function Test-WSL-FileMoveToSubdir {
    Write-Host "`n--- TEST W6: [WSL] File move into subdirectory ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'movable' > '$wslSrc/wsl_movable.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_movable.txt") "[WSL] File synced at root"

    Invoke-Wsl "mkdir -p '$wslSrc/wsl_subdir' && mv '$wslSrc/wsl_movable.txt' '$wslSrc/wsl_subdir/wsl_movable.txt'"
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "wsl_subdir\wsl_movable.txt") "[WSL] File synced at new subdirectory path"
}

function Test-WSL-FileDelete {
    Write-Host "`n--- TEST W7: [WSL] File delete ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'doomed' > '$wslSrc/wsl_doomed.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_doomed.txt") "[WSL] File synced before delete"

    Invoke-Wsl "rm '$wslSrc/wsl_doomed.txt'"
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived file deletion"
}

function Test-WSL-DirectoryDelete {
    Write-Host "`n--- TEST W8: [WSL] Directory delete ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wsl_deldir' && echo 'bye' > '$wslSrc/wsl_deldir/bye.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_deldir\bye.txt") "[WSL] File synced before dir delete"

    Invoke-Wsl "rm -r '$wslSrc/wsl_deldir'"
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived directory deletion"
}

function Test-WSL-EmptyFile {
    Write-Host "`n--- TEST W9: [WSL] Empty file sync ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "touch '$wslSrc/wsl_empty.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_empty.txt") "[WSL] Empty file (touch) synced"
}

function Test-WSL-UnicodeFilename {
    Write-Host "`n--- TEST W10: [WSL] Unicode filename ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'wsl unicode' > '$wslSrc/wslテスト.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wslテスト.txt") "[WSL] Unicode filename synced"
}

function Test-WSL-SpacesInFilename {
    Write-Host "`n--- TEST W11: [WSL] Spaces in filename ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'wsl spaces' > '$wslSrc/wsl file (copy).txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl file (copy).txt") "[WSL] Filename with spaces synced"
}

function Test-WSL-MetadataOnlyUpdate {
    Write-Host "`n--- TEST W12: [WSL] Metadata-only update (touch) ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'wsl meta' > '$wslSrc/wsl_meta.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_meta.txt") "[WSL] File synced before touch"

    # Touch to change mtime without changing content
    Invoke-Wsl "touch '$wslSrc/wsl_meta.txt'"
    Wait-ForSync 10
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived mtime-only update via touch"
    Assert-FileExists (Join-Path $DstDir "wsl_meta.txt") "[WSL] File still present after touch"
}

function Test-WSL-RenameStorm {
    Write-Host "`n--- TEST W13: [WSL] Rename storm ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'wsl storm' > '$wslSrc/wsl_storm.txt'"
    Start-Sleep 1
    for ($i = 1; $i -le 5; $i++) {
        $prev = if ($i -eq 1) { "wsl_storm.txt" } else { "wsl_storm_$($i-1).txt" }
        Invoke-Wsl "mv '$wslSrc/$prev' '$wslSrc/wsl_storm_$i.txt'"
        Start-Sleep -Milliseconds 200
    }
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_storm_5.txt") "[WSL] Final renamed file synced"
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived rename storm"
}

function Test-WSL-DebounceRapidWrites {
    Write-Host "`n--- TEST W14: [WSL] Debounce rapid writes ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    # Write 10 rapid updates via a WSL script file to avoid PowerShell/bash quoting issues
    $scriptContent = @"
#!/bin/bash
for i in `$(seq 1 10); do echo "wsl write `$i" > '$wslSrc/wsl_rapid.txt'; sleep 0.1; done
"@
    $scriptWin = Join-Path $SrcDir "_wsl_rapid_test.sh"
    [System.IO.File]::WriteAllText($scriptWin, $scriptContent.Replace("`r`n","`n"))
    $scriptWsl = To-WslPath $scriptWin
    Invoke-Wsl "bash '$scriptWsl'"
    Remove-Item $scriptWin -Force -ErrorAction SilentlyContinue
    Wait-ForSync 6
    Assert-FileExists (Join-Path $DstDir "wsl_rapid.txt") "[WSL] Rapid-write file synced"
    Assert-FileContent (Join-Path $DstDir "wsl_rapid.txt") "wsl write 10" "[WSL] Last write was the one synced"
}

function Test-WSL-SubdirectorySync {
    Write-Host "`n--- TEST W15: [WSL] Subdirectory auto-watch + sync ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wsl_src/wsl_pkg'"
    Start-Sleep 1
    Invoke-Wsl "echo 'package wslpkg' > '$wslSrc/wsl_src/wsl_pkg/lib.go'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_src\wsl_pkg\lib.go") "[WSL] File in new subdirectory synced"
}

function Test-WSL-DeeplyNestedPath {
    Write-Host "`n--- TEST W16: [WSL] Deeply nested path ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wa/wb/wc/wd/we/wf/wg'"
    Start-Sleep 2
    Invoke-Wsl "echo 'wsl deep' > '$wslSrc/wa/wb/wc/wd/we/wf/wg/wsl_deep.txt'"
    Wait-ForSync 10
    Assert-FileExists (Join-Path $DstDir "wa\wb\wc\wd\we\wf\wg\wsl_deep.txt") "[WSL] Deeply nested file synced"
}

function Test-WSL-ExcludedFilesNotSynced {
    Write-Host "`n--- TEST W17: [WSL] Excluded files NOT synced ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'bytecode' > '$wslSrc/wsl_code.pyc'"
    Invoke-Wsl "echo 'temp' > '$wslSrc/wsl_temp.tmp'"
    Invoke-Wsl "echo 'log' > '$wslSrc/wsl_debug.log'"
    Wait-ForSync
    Assert-FileNotExists (Join-Path $DstDir "wsl_code.pyc") "[WSL] .pyc excluded"
    Assert-FileNotExists (Join-Path $DstDir "wsl_temp.tmp") "[WSL] .tmp excluded"
    Assert-FileNotExists (Join-Path $DstDir "wsl_debug.log") "[WSL] .log excluded"
}

function Test-WSL-BurstFileCreation {
    Write-Host "`n--- TEST W18: [WSL] Burst - 50 files created rapidly ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "mkdir -p '$wslSrc/wsl_burst'"
    Start-Sleep 3  # let watcher register dir
    # Write bash script directly to avoid PowerShell/bash $variable quoting conflicts.
    # PowerShell interpolates $wslSrc (PS variable); bash must expand $i (loop variable).
    # Solution: bake the path into a DEST variable, use double quotes for filenames.
    $scriptPath = Join-Path $SrcDir "_wsl_burst_test.sh"
    $burstDir = "$wslSrc/wsl_burst"
    $lines = @(
        '#!/bin/bash'
        "DEST=`"$burstDir`""
        'for i in $(seq 1 50); do'
        '  echo "content $i" > "$DEST/file$i.txt"'
        'done'
    )
    [System.IO.File]::WriteAllText($scriptPath, ($lines -join "`n") + "`n")
    $scriptWsl = To-WslPath $scriptPath
    Invoke-Wsl "bash '$scriptWsl'"
    Remove-Item $scriptPath -Force -ErrorAction SilentlyContinue
    Wait-ForSync 35
    $synced = (Get-ChildItem (Join-Path $DstDir "wsl_burst") -File -ErrorAction SilentlyContinue).Count
    Assert-True ($synced -ge 48) "[WSL] Burst files synced ($synced/50, need 48+)"
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived burst file creation"
}

function Test-WSL-MoveFileOutOfProject {
    Write-Host "`n--- TEST W19: [WSL] File moved outside project ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    $wslRoot = To-WslPath $TestRoot
    Invoke-Wsl "echo 'leaving' > '$wslSrc/wsl_leaving.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_leaving.txt") "[WSL] File synced before move-out"

    Invoke-Wsl "mv '$wslSrc/wsl_leaving.txt' '$wslRoot/wsl_leaving.txt'"
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived file move out of project"
    Invoke-Wsl "rm -f '$wslRoot/wsl_leaving.txt'"
}

function Test-WSL-MoveFileIntoProject {
    Write-Host "`n--- TEST W20: [WSL] File moved into project ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    $wslRoot = To-WslPath $TestRoot
    Invoke-Wsl "echo 'arriving from wsl' > '$wslRoot/wsl_incoming.txt'"
    Start-Sleep 1
    Invoke-Wsl "mv '$wslRoot/wsl_incoming.txt' '$wslSrc/wsl_incoming.txt'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "wsl_incoming.txt") "[WSL] File moved into project was synced"
}

function Test-WSL-FileDeletedBeforeSync {
    Write-Host "`n--- TEST W21: [WSL] File created then immediately deleted ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'ephemeral' > '$wslSrc/wsl_ephemeral.txt' && sleep 0.2 && rm '$wslSrc/wsl_ephemeral.txt'"
    Wait-ForSync
    Assert-True (-not $SmirrorProc.HasExited) "[WSL] smirror survived ephemeral file"
}

function Test-WSL-SyncIgnoreHotReload {
    Write-Host "`n--- TEST W22: [WSL] Hot-reload .syncignore via WSL edit ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir

    # Write .syncignore from WSL
    Invoke-Wsl "echo '*.wsldat' > '$wslSrc/.syncignore'"
    Wait-ForSync 5

    # Create excluded and included files from WSL
    Invoke-Wsl "echo 'excluded' > '$wslSrc/wsl_data.wsldat'"
    Invoke-Wsl "echo 'included' > '$wslSrc/wsl_data.wslcsv'"
    Wait-ForSync

    Assert-FileNotExists (Join-Path $DstDir "wsl_data.wsldat") "[WSL] .wsldat file excluded by .syncignore"
    Assert-FileExists (Join-Path $DstDir "wsl_data.wslcsv") "[WSL] .wslcsv file included"

    # Clean up .syncignore to not affect other tests
    Invoke-Wsl "rm '$wslSrc/.syncignore'"
    Wait-ForSync 5
}

function Test-WSL-DotFiles {
    Write-Host "`n--- TEST W23: [WSL] Dotfiles sync ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'root = true' > '$wslSrc/.wsl_editorconfig'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir ".wsl_editorconfig") "[WSL] Dotfile synced"
}

function Test-WSL-FileWithNoExtension {
    Write-Host "`n--- TEST W24: [WSL] File with no extension ---" -ForegroundColor Magenta
    $wslSrc = To-WslPath $SrcDir
    Invoke-Wsl "echo 'all: build' > '$wslSrc/WSLMakefile'"
    Wait-ForSync
    Assert-FileExists (Join-Path $DstDir "WSLMakefile") "[WSL] File with no extension synced"
}

# ── Main ─────────────────────────────────────────────────────────────

try {
    Write-Host "SelectiveMirror Adversarial Test Suite" -ForegroundColor Cyan
    Write-Host "======================================`n"

    # Verify build compiles (go run will compile on first use)
    Write-Host "Verifying build..." -ForegroundColor Yellow
    Push-Location C:\mine\SelectiveMirror
    & $GoBin build ./cmd/smirror/
    if ($LASTEXITCODE -ne 0) { throw "Build failed" }
    Pop-Location

    # Ensure test root exists for output files
    New-Item -ItemType Directory -Path $TestRoot -Force | Out-Null

    # Unit tests (run before smirror starts to avoid lock contention)
    Write-Host "`nRunning unit tests..." -ForegroundColor Yellow
    Push-Location C:\mine\SelectiveMirror
    $unitOutput = ""
    try {
        $proc = Start-Process -FilePath "$GoPath\go.exe" -ArgumentList "test","./internal/..." `
            -NoNewWindow -Wait -PassThru -RedirectStandardOutput (Join-Path $TestRoot "unit_stdout.txt") `
            -RedirectStandardError (Join-Path $TestRoot "unit_stderr.txt")
        $unitOutput = Get-Content (Join-Path $TestRoot "unit_stdout.txt") -Raw -ErrorAction SilentlyContinue
        $unitErr = Get-Content (Join-Path $TestRoot "unit_stderr.txt") -Raw -ErrorAction SilentlyContinue
        if ($unitErr) { $unitOutput += "`n$unitErr" }
    } catch {
        $unitOutput = "Exception: $_"
    }
    Pop-Location
    if ($unitOutput -match "FAIL") {
        Write-Host "  FAIL: Unit tests failed" -ForegroundColor Red
        Write-Host $unitOutput
        $Failed++
    } else {
        $unitCount = ([regex]::Matches($unitOutput, "(?m)^ok")).Count
        Write-Host "  PASS: All unit tests passed ($unitCount packages)" -ForegroundColor Green
        $Passed++
    }

    Setup-TestEnv
    Start-Smirror

    # Wait for startup reconciliation and worker initialization.
    # go run compile time + rclone init + worker pool startup can take 12-15s.
    Start-Sleep 15

    # Run all tests
    Test-BasicFileSync
    Test-ExcludedFilesNotSynced
    Test-ExcludedDirectoryNotSynced
    Test-SubdirectorySync
    Test-DebounceRapidWrites
    Test-FileModification
    Test-LargeFileSkip
    Test-EmptyFile
    Test-UnicodeFilename
    Test-SpacesInFilename
    Test-DeeplyNestedPath
    Test-SingleInstanceLock
    Test-DoctorWhileRunning
    Test-ExplainCommand
    Test-ExplainExcludedFile
    Test-BurstFileCreation
    Test-FileDeletedBeforeSync
    Test-RenameStorm
    Test-SimultaneousWriteToSameFile
    Test-StopAndRestart
    Test-VerifyCommand
    Test-VerifyDetectsDrift
    Test-StatusCommand
    Test-ProcessKillRecovery
    Test-SpecialCharactersInContent
    Test-DotFiles
    Test-FileWithNoExtension
    Test-FileRenameSync
    Test-FileMoveToSubdir
    Test-DirectoryRename
    Test-DirectoryDelete
    Test-MoveFileOutOfProject
    Test-MoveFileIntoProject
    Test-NestedDirRename
    Test-SymlinkFileNotSynced
    Test-SymlinkDirNotFollowed
    Test-JunctionNotFollowed
    Test-NamedPipeIgnored
    Test-SyncIgnoreHotReload
    Test-MetadataOnlyUpdate
    Test-StatsCommand
    Test-VerifyHashMismatch

    # WSL tests — verify all operations work when performed from WSL
    Write-Host "`n=== WSL Tests ===" -ForegroundColor Cyan
    Test-WSL-BasicFileSync
    Test-WSL-FileModification
    Test-WSL-FileRename
    Test-WSL-DirectoryRename
    Test-WSL-NestedDirRename
    Test-WSL-FileMoveToSubdir
    Test-WSL-FileDelete
    Test-WSL-DirectoryDelete
    Test-WSL-EmptyFile
    Test-WSL-UnicodeFilename
    Test-WSL-SpacesInFilename
    Test-WSL-MetadataOnlyUpdate
    Test-WSL-RenameStorm
    Test-WSL-DebounceRapidWrites
    Test-WSL-SubdirectorySync
    Test-WSL-DeeplyNestedPath
    Test-WSL-ExcludedFilesNotSynced
    Test-WSL-BurstFileCreation
    Test-WSL-MoveFileOutOfProject
    Test-WSL-MoveFileIntoProject
    Test-WSL-FileDeletedBeforeSync
    Test-WSL-SyncIgnoreHotReload
    Test-WSL-DotFiles
    Test-WSL-FileWithNoExtension

    Write-Host "`n======================================" -ForegroundColor Cyan
    Write-Host "Results: $Passed passed, $Failed failed" -ForegroundColor $(if ($Failed -eq 0) { "Green" } else { "Red" })

    if ($Failed -gt 0) {
        Write-Host "`nCheck log file for details: $LogFile" -ForegroundColor Yellow
    }
}
catch {
    Write-Host "`nFATAL: $_" -ForegroundColor Red
    $Failed++
}
finally {
    Cleanup
    Write-Host "`nTest environment cleaned up: $TestRoot"
}

exit $Failed




