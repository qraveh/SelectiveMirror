<#
.SYNOPSIS
    SelectiveMirror tiered verification runner.

.DESCRIPTION
    Runs three tiers of checks. Each mode is a strict superset of the previous:

      fast       (~40 sec)  — vet + unit tests + cross-platform builds
      pre-commit (~3 min)   — fast + linter + race + 30s fuzz + coverage goals
      release    (~30 min)  — pre-commit + 5m fuzz + integration + SLA + stress

.PARAMETER Mode
    fast | pre-commit | release (default: fast)

.PARAMETER ContinueOnError
    Run all checks even after a failure (default: stop on first failure).

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File test\verify.ps1
    powershell -ExecutionPolicy Bypass -File test\verify.ps1 -Mode pre-commit
    powershell -ExecutionPolicy Bypass -File test\verify.ps1 -Mode release -ContinueOnError
#>

param(
    [ValidateSet('fast', 'pre-commit', 'release')]
    [string]$Mode = 'fast',

    [switch]$ContinueOnError
)

$ErrorActionPreference = 'Continue'  # we handle errors ourselves via $LASTEXITCODE

# ── Coverage goals (edit here to adjust) ─────────────────────────────

# Fuzz goals reflect the *ceiling* reachable from the given fuzz target.
# Raising these requires adding new seed inputs or unblocking unreachable paths.
# - FuzzFilter 85%: 'return false' when merged==nil is unreachable when filter
#   is constructed with rules (which the fuzz test always does).
# - FuzzConfig 75%: file system error paths (Abs/ReadFile) are unreachable from
#   pure-YAML byte fuzzing; KnownFields typo branch needs specific shapes.

$script:Goals = @{
    TotalCoverage   = 60.0    # overall internal/... coverage
    WatcherCoverage = 60.0    # internal/watcher/ coverage
    FuzzFilter      = 85.0    # filter.Engine.IsExcluded via fuzz corpus
    FuzzConfig      = 75.0    # config.Load via fuzz corpus
}

# ── State ────────────────────────────────────────────────────────────

$script:Results   = @()
$script:StartTime = Get-Date
$script:RepoRoot  = Split-Path -Parent $PSScriptRoot

Set-Location $script:RepoRoot

# ── Output helpers ───────────────────────────────────────────────────

function Write-Banner($text, $color = 'Cyan') {
    $line = '=' * 72
    Write-Host ""
    Write-Host $line -ForegroundColor $color
    Write-Host "  $text" -ForegroundColor $color
    Write-Host $line -ForegroundColor $color
}

function Write-Section($text) {
    Write-Host ""
    Write-Host "-- $text" -ForegroundColor Yellow
}

function Add-Result($name, $passed, $detail = '') {
    $script:Results += [PSCustomObject]@{
        Name   = $name
        Passed = $passed
        Detail = $detail
    }
    $icon  = if ($passed) { '[OK]' }   else { '[FAIL]' }
    $color = if ($passed) { 'Green' }  else { 'Red' }
    $msg   = if ($detail) { "$icon $name  $detail" } else { "$icon $name" }
    Write-Host "  $msg" -ForegroundColor $color

    if (-not $passed -and -not $ContinueOnError) {
        Write-Host ""
        Write-Host "  Failed check: $name (use -ContinueOnError to run all)" -ForegroundColor Red
        Show-Summary
        exit 1
    }
}

# ── Checks ───────────────────────────────────────────────────────────

function Invoke-GoVet {
    Write-Section "go vet"
    $out = go vet ./... 2>&1
    if ($LASTEXITCODE -eq 0) {
        Add-Result "go vet" $true
    } else {
        Add-Result "go vet" $false
        $out | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkRed }
    }
}

function Invoke-UnitTests {
    Write-Section "Unit tests (all packages)"
    $out = go test ./internal/... ./cmd/... -p 24 -count=1 2>&1
    $ok = $LASTEXITCODE -eq 0
    $passCount = ($out | Select-String '^ok\s').Count
    $failCount = ($out | Select-String '^FAIL\s').Count
    if ($ok) {
        Add-Result "Unit tests" $true "($passCount packages pass)"
    } else {
        Add-Result "Unit tests" $false "($failCount packages fail)"
        $out | Where-Object { $_ -match 'FAIL|---.*FAIL' } | ForEach-Object {
            Write-Host "    $_" -ForegroundColor DarkRed
        }
    }
}

function Invoke-CrossPlatformBuilds {
    # SM-148: Project is Windows-first. mattn/go-sqlite3 requires CGo; cross-
    # compiling CGo from Windows to Linux/Darwin needs a cross-C toolchain we
    # do not set up. If you need Linux/Darwin binaries, build natively on that
    # platform. This check now just verifies windows/amd64 compiles.
    Write-Section "Build (windows/amd64)"
    $savedGOOS   = $env:GOOS
    $savedGOARCH = $env:GOARCH

    $targets = @(
        @{ OS = 'windows'; Arch = 'amd64' }
    )

    try {
        foreach ($t in $targets) {
            $env:GOOS   = $t.OS
            $env:GOARCH = $t.Arch
            $tmp = [System.IO.Path]::GetTempFileName()
            $out = go build -o $tmp ./cmd/smirror/ 2>&1
            Remove-Item $tmp -ErrorAction SilentlyContinue
            if ($LASTEXITCODE -eq 0) {
                Add-Result ("Build {0}/{1}" -f $t.OS, $t.Arch) $true
            } else {
                Add-Result ("Build {0}/{1}" -f $t.OS, $t.Arch) $false ($out -join ' ')
            }
        }
    }
    finally {
        $env:GOOS   = $savedGOOS
        $env:GOARCH = $savedGOARCH
    }
}

function Invoke-Linter {
    Write-Section "golangci-lint"
    $lint = Get-Command golangci-lint -ErrorAction SilentlyContinue
    if (-not $lint) {
        Add-Result "golangci-lint" $true "(not installed, skipped)"
        return
    }
    $out = golangci-lint run ./... 2>&1
    if ($LASTEXITCODE -eq 0) {
        Add-Result "golangci-lint" $true
    } else {
        $issueCount = ($out | Select-String ':\d+:\d+:').Count
        Add-Result "golangci-lint" $false "$issueCount issues"
        $out | Select-Object -First 10 | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkRed }
    }
}

function Invoke-RaceTests {
    Write-Section "Race detector"
    $cgo = (go env CGO_ENABLED).Trim()
    if ($cgo -ne '1') {
        Add-Result "Race tests" $true "(skipped: CGO_ENABLED=$cgo; requires C compiler)"
        return
    }
    $out = go test -race ./internal/filter ./internal/logging ./internal/lock ./internal/metrics ./internal/watcher 2>&1
    if ($LASTEXITCODE -eq 0) {
        Add-Result "Race tests" $true
    } else {
        Add-Result "Race tests" $false
        $out | Where-Object { $_ -match 'DATA RACE|FAIL' } | ForEach-Object {
            Write-Host "    $_" -ForegroundColor DarkRed
        }
    }
}

# Parse `go tool cover -func` output for a specific function's coverage.
# Returns the percentage as double, or -1 if not found.
# NOTE: `-func=<path>` must be quoted — PowerShell splits bare `-func=X.cov` on the dot.
function Get-FunctionCoverage($coverFile, $funcName) {
    if (-not (Test-Path $coverFile)) { return -1.0 }
    $funcArg = "`"-func=$coverFile`""
    $lines = & go tool cover $funcArg 2>&1
    # Format: "pkg/file.go:123:   FuncName   90.0%"
    # Function may have receiver: "(*Engine).IsExcluded"
    foreach ($line in $lines) {
        if ($line -match "\b$funcName\s+(\d+\.\d+)%") {
            return [double]$Matches[1]
        }
    }
    return -1.0
}

function Get-CorpusCount($package, $target) {
    $pkgDir = Join-Path $script:RepoRoot ($package -replace '^\./', '')
    $corpusDir = Join-Path $pkgDir "testdata\fuzz\$target"
    if (Test-Path $corpusDir) {
        return (Get-ChildItem $corpusDir -File -ErrorAction SilentlyContinue).Count
    }
    return 0
}

# Run a fuzz target for a budget, then measure function coverage by replaying corpus.
function Invoke-FuzzWithCoverage {
    param(
        [string]$Target,
        [string]$Package,
        [string]$FuzzTime,
        [string]$FuncName,
        [string]$GoalKey
    )
    Write-Section "Fuzz $Target (${FuzzTime} budget)"

    $before = Get-CorpusCount $Package $Target

    # Build args as array to avoid PowerShell parser confusion with $ in regex anchors
    $runArg      = '-run=^{0}$' -f $Target
    $fuzzArg     = "-fuzz=$Target"
    $fuzzTimeArg = "-fuzztime=$FuzzTime"

    $fuzzOut = & go test $fuzzArg $fuzzTimeArg $runArg $Package 2>&1
    if ($LASTEXITCODE -ne 0) {
        Add-Result "Fuzz $Target" $false "fuzz run failed"
        $fuzzOut | Select-Object -First 5 | ForEach-Object { Write-Host "    $_" -ForegroundColor DarkRed }
        return
    }

    $after = Get-CorpusCount $Package $Target
    $newEntries = $after - $before

    # Replay corpus as normal test with coverage profiling.
    # Use "--coverprofile=<path>" wrapped in quotes because PowerShell native-arg
    # parsing splits "-coverprofile=X.cov" on the dot.
    $coverFile = Join-Path $script:RepoRoot "fuzz_$Target.cov"
    $coverArg  = "`"-coverprofile=$coverFile`""
    $replayOut = & go test $runArg $coverArg $Package 2>&1
    if ($LASTEXITCODE -ne 0) {
        Add-Result "Fuzz $Target" $false "replay failed"
        Remove-Item $coverFile -ErrorAction SilentlyContinue
        return
    }

    $pct = Get-FunctionCoverage $coverFile $FuncName
    Remove-Item $coverFile -ErrorAction SilentlyContinue

    $goal = $script:Goals[$GoalKey]

    if ($pct -lt 0) {
        Add-Result "Fuzz $Target" $false "function '$FuncName' not found in coverage"
    }
    elseif ($pct -ge $goal) {
        $detail = "cov={0:F1}% >= goal={1:F0}%  (corpus +{2})" -f $pct, $goal, $newEntries
        Add-Result "Fuzz $Target" $true $detail
    }
    else {
        $detail = "cov={0:F1}% < goal={1:F0}%  (corpus +{2})" -f $pct, $goal, $newEntries
        Add-Result "Fuzz $Target" $false $detail
    }
}

function Invoke-CoverageCheck {
    Write-Section "Coverage thresholds"

    # Total internal/ coverage
    # Note: quote -coverprofile=<path> because PowerShell argument parsing
    # splits bare -coverprofile=foo.ext on the dot into separate arguments.
    $totalCover = Join-Path $script:RepoRoot "verify_total.cov"
    $coverArg = "`"-coverprofile=$totalCover`""
    $testOut = & go test $coverArg ./internal/... 2>&1
    $testExit = $LASTEXITCODE
    if ($testExit -ne 0 -or -not (Test-Path $totalCover)) {
        Add-Result "Total coverage" $false "test build/run failed or coverage file missing"
        $testOut | Where-Object { $_ -match 'FAIL|error' } | Select-Object -First 5 | ForEach-Object {
            Write-Host "    $_" -ForegroundColor DarkRed
        }
    }
    else {
        $fnArg = "`"-func=$totalCover`""
        $totalLine = (& go tool cover $fnArg | Select-String '^total:')
        if ($totalLine -and ($totalLine -join ' ') -match '(\d+\.\d+)%') {
            $total = [double]$Matches[1]
            $goal  = $script:Goals.TotalCoverage
            if ($total -ge $goal) {
                Add-Result "Total coverage" $true ("{0:F1}% >= {1:F0}%" -f $total, $goal)
            } else {
                Add-Result "Total coverage" $false ("{0:F1}% < {1:F0}%" -f $total, $goal)
            }
        } else {
            Add-Result "Total coverage" $false "could not parse 'total:' line from coverage output"
        }
    }
    Remove-Item $totalCover -ErrorAction SilentlyContinue

    # Watcher-specific coverage
    $watcherCover = Join-Path $script:RepoRoot "verify_watcher.cov"
    $wCoverArg = "`"-coverprofile=$watcherCover`""
    & go test $wCoverArg ./internal/watcher/ 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $watcherCover)) {
        Add-Result "Watcher coverage" $false "test failed or coverage file missing"
    }
    else {
        $fnArg = "`"-func=$watcherCover`""
        $line = (& go tool cover $fnArg | Select-String '^total:')
        if ($line -and ($line -join ' ') -match '(\d+\.\d+)%') {
            $pct  = [double]$Matches[1]
            $goal = $script:Goals.WatcherCoverage
            if ($pct -ge $goal) {
                Add-Result "Watcher coverage" $true ("{0:F1}% >= {1:F0}%" -f $pct, $goal)
            } else {
                Add-Result "Watcher coverage" $false ("{0:F1}% < {1:F0}%" -f $pct, $goal)
            }
        } else {
            Add-Result "Watcher coverage" $false "could not parse 'total:' line"
        }
    }
    Remove-Item $watcherCover -ErrorAction SilentlyContinue
}

function Invoke-PSScript($name, $path) {
    Write-Section $name
    if (-not (Test-Path $path)) {
        Add-Result $name $false "script not found: $path"
        return
    }
    & powershell -NoProfile -ExecutionPolicy Bypass -File $path
    if ($LASTEXITCODE -eq 0) {
        Add-Result $name $true
    } else {
        Add-Result $name $false "exit code $LASTEXITCODE"
    }
}

# ── Summary ──────────────────────────────────────────────────────────

function Show-Summary {
    Write-Banner "Summary (mode: $Mode)"

    $passed  = ($script:Results | Where-Object { $_.Passed }).Count
    $failed  = ($script:Results | Where-Object { -not $_.Passed }).Count
    $total   = $script:Results.Count
    $elapsed = (Get-Date) - $script:StartTime

    foreach ($r in $script:Results) {
        $icon  = if ($r.Passed) { '[OK]'   } else { '[FAIL]' }
        $color = if ($r.Passed) { 'Green'  } else { 'Red' }
        $line  = "  {0,-7} {1,-25} {2}" -f $icon, $r.Name, $r.Detail
        Write-Host $line -ForegroundColor $color
    }

    Write-Host ""
    $status = if ($failed -eq 0) { 'ALL GREEN' } else { 'FAILED' }
    $color  = if ($failed -eq 0) { 'Green' }     else { 'Red' }
    Write-Host ("  {0}: {1}/{2} passed in {3:mm\:ss}" -f $status, $passed, $total, $elapsed) -ForegroundColor $color
    Write-Host ""
}

# ── Main ─────────────────────────────────────────────────────────────

Write-Banner "SelectiveMirror verify.ps1  --  mode: $Mode"

# Always (fast mode includes these)
Invoke-GoVet
Invoke-UnitTests
Invoke-CrossPlatformBuilds

# pre-commit adds linter, race, fuzz-30s, coverage goals
if ($Mode -eq 'pre-commit' -or $Mode -eq 'release') {
    Invoke-Linter
    Invoke-RaceTests
    Invoke-FuzzWithCoverage -Target 'FuzzFilterIsExcluded' -Package './internal/filter/' -FuzzTime '30s' -FuncName 'IsExcluded' -GoalKey 'FuzzFilter'
    Invoke-FuzzWithCoverage -Target 'FuzzConfigLoad'       -Package './internal/config/' -FuzzTime '30s' -FuncName 'Load'       -GoalKey 'FuzzConfig'
    Invoke-CoverageCheck
}

# release adds long fuzz + integration tests
if ($Mode -eq 'release') {
    Invoke-FuzzWithCoverage -Target 'FuzzFilterIsExcluded' -Package './internal/filter/' -FuzzTime '5m' -FuncName 'IsExcluded' -GoalKey 'FuzzFilter'
    Invoke-FuzzWithCoverage -Target 'FuzzConfigLoad'       -Package './internal/config/' -FuzzTime '5m' -FuncName 'Load'       -GoalKey 'FuzzConfig'
    Invoke-PSScript 'Integration (run_tests.ps1)' 'test\run_tests.ps1'
    Invoke-PSScript 'SLA smoke (sla_smoke.ps1)'   'test\sla_smoke.ps1'
    Invoke-PSScript 'Stress (stress_test.ps1)'    'test\stress_test.ps1'
}

Show-Summary

if (($script:Results | Where-Object { -not $_.Passed }).Count -gt 0) {
    exit 1
}
exit 0
