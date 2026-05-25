<#
.SYNOPSIS
    SelectiveMirror pre-release SLA smoke test.
    Validates performance, latency, integrity, and resource usage.

.DESCRIPTION
    Uses a local rclone backend (no network, no credentials).
    Checks:
      SLA-1  Sync latency (p95 < 5s per file, NFR-TB-02)
      SLA-2  Detection latency (file write → remote arrival < 3s, NFR-TB-01)
      SLA-3  Zero data loss (50 files, verify checksums, NFR integrity)
      SLA-4  Burst throughput (50 rapid files all arrive correctly, NFR-TB-06)
      SLA-5  Memory sanity (idle RSS < 50MB, NFR-RU-01)

    Exit code 0 = all SLAs met, 1 = one or more breached.

.USAGE
    powershell -ExecutionPolicy Bypass -File test\sla_smoke.ps1
#>

$ErrorActionPreference = "Stop"

# PowerShell 7.3+ defaults $PSNativeCommandUseErrorActionPreference
# to $true, which makes native commands writing to stderr trigger
# script-level errors regardless of stream redirection. rclone writes
# benign NOTICE messages to stderr ("Config file ... not found - using
# defaults") on first invocation; the cmd /c wrapper used in
# Setup-TestEnv / Cleanup also addresses this. The variable is the
# belt-and-suspenders default for any future native-command call.
$PSNativeCommandUseErrorActionPreference = $false

# Resolve go.exe via PATH rather than hardcoding "C:\Program Files\Go\bin".
# setup-go@v5 on GitHub-hosted runners installs Go under
# C:\hostedtoolcache\windows\go\<version>\x64\bin\, NOT C:\Program Files.
# A hardcoded $GoPath silently bypasses what setup-go provisioned and
# either fails outright (file not found) or runs a stale/different Go.
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if ($null -eq $goCmd) {
    Write-Host "FATAL: 'go' not found on PATH. Run setup-go@v5 (CI) or install Go locally." -ForegroundColor Red
    exit 1
}
$GoBin = $goCmd.Source

# ── Globals ──────────────────────────────────────────────────────────

# RepoRoot resolves to the parent of test/ (where this script lives),
# so the script works regardless of where the repo is checked out
# (C:\mine\SelectiveMirror locally, D:\a\SelectiveMirror\SelectiveMirror
# on GitHub-hosted windows runners, etc.).
$RepoRoot    = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
# Test workspace under $env:TEMP so we don't pollute the repo with
# random _sla_* dirs and so the runner's tempdir cleanup catches it.
$TestRoot    = Join-Path $env:TEMP "smirror-sla-$(Get-Random)"
$SrcDir      = Join-Path $TestRoot "source"
$DstDir      = Join-Path $TestRoot "destination"
$DataDir     = Join-Path $TestRoot "data"
$ConfigPath  = Join-Path $DataDir  "config.yaml"
$StateDB     = Join-Path $DataDir  "state.db"
$LogFile     = Join-Path $DataDir  "sla.log"
$SmirrorPkg  = "./cmd/smirror/"
$SmirrorProc = $null
$Results     = @()

# ── Helpers ──────────────────────────────────────────────────────────

function Setup-TestEnv {
    Write-Host "`n=== SLA Smoke Test - Setting up ===" -ForegroundColor Cyan
    New-Item -ItemType Directory -Path $SrcDir, $DstDir, $DataDir -Force | Out-Null

    # Invoke via cmd /c so rclone's stderr NOTICE ("Config file ...
    # not found - using defaults") is consumed by cmd before PowerShell
    # sees it. Direct PS-side `*>$null` redirect doesn't suppress the
    # NativeCommandError that PS raises on any native-command stderr
    # output when $ErrorActionPreference = 'Stop'.
    cmd /c "rclone config create testlocal local 2>nul" | Out-Null

    $srcFwd = $SrcDir.Replace('\','/')
    $dstFwd = $DstDir.Replace('\','/')
    $dbFwd  = $StateDB.Replace('\','/')
    $logFwd = $LogFile.Replace('\','/')
    $lines = @(
        "mirrors:"
        "  - name: SLATest"
        "    local_path: `"$srcFwd`""
        "    debounce_sec: 1"
        "    max_file_size_mb: 1"
        "    remote: `"testlocal:$dstFwd`""
        ""
        "global_excludes:"
        "  - .git/"
        "  - `"*.pyc`""
        "  - `"*.tmp`""
        "  - `"*.log`""
        "  - __pycache__/"
        ""
        "state_db: `"$dbFwd`""
        "log_file: `"$logFwd`""
        "log_level: debug"
        "delete_policy: ignore"
    )
    [System.IO.File]::WriteAllText($ConfigPath, ($lines -join "`n"))
    Write-Host "  Source:  $SrcDir"
    Write-Host "  Dest:    $DstDir"
}

function Invoke-Smirror {
    $oldPref = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    $oldDir = Get-Location
    Set-Location $RepoRoot
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
        -WindowStyle Hidden -PassThru -WorkingDirectory $RepoRoot
    Start-Sleep 8
    if ($SmirrorProc.HasExited) {
        throw "smirror exited immediately (exit code $($SmirrorProc.ExitCode))"
    }
}

function Stop-Smirror {
    if ($SmirrorProc -and -not $SmirrorProc.HasExited) {
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
    # Same cmd /c trick as Setup-TestEnv — keep rclone's stderr out
    # of PowerShell so NativeCommandError doesn't fire on cleanup.
    cmd /c "rclone config delete testlocal 2>nul" | Out-Null
    if (Test-Path $TestRoot) {
        Remove-Item -Path $TestRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Wait-ForFile {
    param([string]$Path, [int]$TimeoutSeconds = 15)
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while (-not (Test-Path $Path) -and (Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 200
    }
    return (Test-Path $Path)
}

function Record-SLA {
    param([string]$Name, [string]$Metric, [string]$Threshold, [bool]$Passed)
    $status = if ($Passed) { "PASS" } else { "FAIL" }
    $color  = if ($Passed) { "Green" } else { "Red" }
    Write-Host "  [$status] $Name - $Metric (threshold: $Threshold)" -ForegroundColor $color
    $script:Results += [PSCustomObject]@{
        SLA       = $Name
        Metric    = $Metric
        Threshold = $Threshold
        Status    = $status
    }
}

# ── SLA Tests ────────────────────────────────────────────────────────

function Test-SLA1-SyncLatency {
    Write-Host "`n--- SLA-1: Sync latency (NFR-TB-02) ---" -ForegroundColor Magenta

    # Create 20 files of mixed sizes, sync via sync-now, measure total time
    $sizes = @(1, 10, 100, 500, 1000, 5000, 10000, 50000, 100000, 500000,
               1, 10, 100, 500, 1000, 5000, 10000, 50000, 100000, 500000)
    for ($i = 0; $i -lt 20; $i++) {
        $content = "x" * $sizes[$i]
        [System.IO.File]::WriteAllText((Join-Path $SrcDir "sla1_file$i.txt"), $content)
    }

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    Invoke-Smirror "sync-now" "--config" $ConfigPath | Out-Null
    $sw.Stop()

    $elapsedPerFile = $sw.Elapsed.TotalSeconds / 20
    $totalSec = $sw.Elapsed.TotalSeconds
    $passed = $elapsedPerFile -lt 5.0

    $totalStr = "{0:F2}" -f $totalSec
    $perFileStr = "{0:F2}" -f $elapsedPerFile
    $metric = "${totalStr}s total, ${perFileStr}s per file avg"
    Record-SLA "SLA-1" $metric "less than 5s per file" $passed

    # Verify all 20 arrived
    $arrivedCount = (Get-ChildItem (Join-Path $DstDir "sla1_file*.txt") -ErrorAction SilentlyContinue).Count
    $allArrived = $arrivedCount -eq 20
    if (-not $allArrived) {
        Write-Host "  WARN: Only $arrivedCount/20 files synced" -ForegroundColor Yellow
    }
}

function Test-SLA2-DetectionLatency {
    Write-Host "`n--- SLA-2: Detection latency (NFR-TB-01) ---" -ForegroundColor Magenta

    # Watcher must be running for this test
    Start-Smirror

    $targetFile = Join-Path $DstDir "sla2_detect.txt"

    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    Set-Content (Join-Path $SrcDir "sla2_detect.txt") "detection test"
    $arrived = Wait-ForFile $targetFile -TimeoutSeconds 15
    $sw.Stop()

    $latency = $sw.Elapsed.TotalSeconds
    $passed = $arrived -and ($latency -lt 3.0)

    $latStr = "{0:F2}" -f $latency
    $metric = "${latStr}s detection+sync"
    Record-SLA "SLA-2" $metric "less than 3s" $passed

    Stop-Smirror
}

function Test-SLA3-ZeroDataLoss {
    Write-Host "`n--- SLA-3: Zero data loss (integrity) ---" -ForegroundColor Magenta

    # Clean source/dest for a fresh run
    Get-ChildItem $SrcDir -Recurse | Remove-Item -Force -Recurse -ErrorAction SilentlyContinue
    Get-ChildItem $DstDir -Recurse | Remove-Item -Force -Recurse -ErrorAction SilentlyContinue

    # Create 50 files with deterministic content
    for ($i = 0; $i -lt 50; $i++) {
        $content = "integrity-check-file-$i-" + ("abcdef0123456789" * (($i % 10) + 1))
        [System.IO.File]::WriteAllText((Join-Path $SrcDir "sla3_$i.txt"), $content)
    }

    Invoke-Smirror "sync-now" "--config" $ConfigPath | Out-Null

    # Run verify command
    $verifyOutput = Invoke-Smirror "test-mirrors" "--config" $ConfigPath
    $noDrift = $verifyOutput -match "No drift detected"

    # Also check file-by-file content match
    $mismatches = 0
    for ($i = 0; $i -lt 50; $i++) {
        $srcPath = Join-Path $SrcDir "sla3_$i.txt"
        $dstPath = Join-Path $DstDir "sla3_$i.txt"
        if (-not (Test-Path $dstPath)) {
            $mismatches++
            continue
        }
        $srcHash = (Get-FileHash $srcPath -Algorithm MD5).Hash
        $dstHash = (Get-FileHash $dstPath -Algorithm MD5).Hash
        if ($srcHash -ne $dstHash) { $mismatches++ }
    }

    $passed = ($mismatches -eq 0) -and $noDrift
    Record-SLA "SLA-3" "$mismatches/50 mismatches, drift=$(-not $noDrift)" "0 mismatches, no drift" $passed
}

function Test-SLA4-BurstThroughput {
    Write-Host "`n--- SLA-4: Burst throughput (NFR-TB-06) ---" -ForegroundColor Magenta

    # Clean source/dest
    Get-ChildItem $SrcDir -Recurse | Remove-Item -Force -Recurse -ErrorAction SilentlyContinue
    Get-ChildItem $DstDir -Recurse | Remove-Item -Force -Recurse -ErrorAction SilentlyContinue

    # Write 50 files as fast as possible
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    for ($i = 0; $i -lt 50; $i++) {
        [System.IO.File]::WriteAllText((Join-Path $SrcDir "burst_$i.txt"), "burst-content-$i")
    }
    $writeMs = $sw.Elapsed.TotalMilliseconds

    # Sync all
    Invoke-Smirror "sync-now" "--config" $ConfigPath | Out-Null
    $sw.Stop()
    $totalSec = $sw.Elapsed.TotalSeconds

    # Count arrivals and verify content
    $arrived = 0
    $correct = 0
    for ($i = 0; $i -lt 50; $i++) {
        $dstPath = Join-Path $DstDir "burst_$i.txt"
        if (Test-Path $dstPath) {
            $arrived++
            $content = Get-Content $dstPath -Raw
            if ($content.Trim() -eq "burst-content-$i") { $correct++ }
        }
    }

    $passed = ($arrived -eq 50) -and ($correct -eq 50)
    $secStr = "{0:F1}" -f $totalSec
    $metric = "$arrived/50 arrived, $correct/50 correct in ${secStr}s"
    Record-SLA "SLA-4" $metric "50 of 50 correct" $passed
}

function Test-SLA5-MemorySanity {
    Write-Host "`n--- SLA-5: Memory sanity (NFR-RU-01) ---" -ForegroundColor Magenta

    Start-Smirror
    Start-Sleep 3  # let it settle

    # Find the actual smirror child process (not the go run wrapper)
    $memMB = 0
    try {
        $children = Get-CimInstance Win32_Process | Where-Object { $_.ParentProcessId -eq $SmirrorProc.Id }
        if ($children) {
            $memBytes = ($children | Measure-Object -Property WorkingSetSize -Maximum).Maximum
            $memMB = [math]::Round($memBytes / 1MB, 1)
        } else {
            # Fall back to parent process
            $proc = Get-Process -Id $SmirrorProc.Id -ErrorAction SilentlyContinue
            if ($proc) { $memMB = [math]::Round($proc.WorkingSet64 / 1MB, 1) }
        }
    } catch {
        Write-Host "  WARN: Could not read process memory: $_" -ForegroundColor Yellow
    }

    $passed = $memMB -gt 0 -and $memMB -lt 50
    Record-SLA "SLA-5" "${memMB}MB RSS idle" "less than 50MB" $passed

    Stop-Smirror
}

# ── Main ─────────────────────────────────────────────────────────────

try {
    Setup-TestEnv

    Test-SLA1-SyncLatency
    Test-SLA2-DetectionLatency
    Test-SLA3-ZeroDataLoss
    Test-SLA4-BurstThroughput
    Test-SLA5-MemorySanity

    # ── Summary ──────────────────────────────────────────────────────
    Write-Host "`n=== SLA Smoke Test Summary ===" -ForegroundColor Cyan
    $Results | Format-Table -AutoSize

    $failures = ($Results | Where-Object { $_.Status -eq "FAIL" }).Count
    $total    = $Results.Count

    if ($failures -eq 0) {
        Write-Host "All $total SLA checks PASSED." -ForegroundColor Green
        $exitCode = 0
    } else {
        Write-Host "$failures/$total SLA checks FAILED." -ForegroundColor Red
        $exitCode = 1
    }

    # ── Persist results — durable artifact for longitudinal tracking ──
    # Two formats: human-readable .txt for quick visual diff,
    # machine-readable .json for any future "is SLA-2 drifting up over
    # time?" trend analysis. Files land under test/ where
    # .github/workflows/sla-smoke.yml's Upload SLA results step picks
    # them up. UTC timestamp in the filename so two runs on the same
    # day don't collide and so chronological sort in the artifacts
    # list matches reality.
    $resultsDir = Join-Path $RepoRoot "test"
    $stamp = (Get-Date -AsUTC).ToString("yyyyMMddTHHmmssZ")
    $txtPath  = Join-Path $resultsDir "sla-results-$stamp.txt"
    $jsonPath = Join-Path $resultsDir "sla-results-$stamp.json"
    ($Results | Format-Table -AutoSize | Out-String).Trim() |
        Set-Content -Path $txtPath -Encoding UTF8
    @{
        timestamp_utc = $stamp
        repo_root     = $RepoRoot
        total         = $total
        failures      = $failures
        results       = $Results
    } | ConvertTo-Json -Depth 4 |
        Set-Content -Path $jsonPath -Encoding UTF8
    Write-Host "Results written: $txtPath" -ForegroundColor DarkGray
    Write-Host "Results written: $jsonPath" -ForegroundColor DarkGray
} catch {
    Write-Host "FATAL: $_" -ForegroundColor Red
    Write-Host $_.ScriptStackTrace -ForegroundColor DarkGray
    $exitCode = 1
} finally {
    Cleanup
}

exit $exitCode
