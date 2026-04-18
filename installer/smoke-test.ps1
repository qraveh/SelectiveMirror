# installer/smoke-test.ps1 -- End-to-end MSI regression test.
#
# Builds the MSI, installs it, validates every SEC-C2 / perMachine invariant,
# round-trips task install, and uninstalls cleanly. Run from an elevated
# PowerShell.
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File installer\smoke-test.ps1
#   powershell -ExecutionPolicy Bypass -File installer\smoke-test.ps1 -SkipBuild
#   powershell -ExecutionPolicy Bypass -File installer\smoke-test.ps1 -MsiPath "path\to\existing.msi"
#
# Exit codes:
#   0 = all invariants pass
#   1 = not elevated
#   2 = build / install failure
#   3 = invariant violation (service registered, wrong path, leftover artifacts, etc.)

param(
    [string]$Version = "0.8.52",
    [switch]$SkipBuild,
    [string]$MsiPath = ""
)

$ErrorActionPreference = "Continue"

function Heading($s) { Write-Host "`n=== $s ===" -ForegroundColor Cyan }
function Pass($s)    { Write-Host "PASS: $s" -ForegroundColor Green }
function Fail($s)    { Write-Host "FAIL: $s" -ForegroundColor Red; $script:FailCount++ }

$script:FailCount = 0
$root      = Split-Path $PSScriptRoot -Parent
$defaultMsi = Join-Path $root "installer\bin\Release\SelectiveMirror.msi"
if (-not $MsiPath) { $MsiPath = $defaultMsi }

$installLog   = Join-Path $env:TEMP "smirror-install.log"
$uninstallLog = Join-Path $env:TEMP "smirror-uninstall.log"
$target       = Join-Path $env:ProgramFiles "SelectiveMirror\smirror.exe"
$targetDir    = Split-Path $target

#-------------------------------------------------------------------------------
Heading "0. Preflight"
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { Fail "not elevated -- re-run from admin PowerShell"; exit 1 }
Pass "running elevated"

# Wait for any leftover msiexec processes from prior runs
Get-Process msiexec -ErrorAction SilentlyContinue | Wait-Process -Timeout 60 -ErrorAction SilentlyContinue

# Clean slate: uninstall any previously-registered SelectiveMirror
$prior = @()
$prior += Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\* -ErrorAction SilentlyContinue
$prior += Get-ItemProperty HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\* -ErrorAction SilentlyContinue
$priorHits = $prior | Where-Object { $_.DisplayName -match 'SelectiveMirror' }
foreach ($h in $priorHits) {
    Write-Host "  removing prior install: $($h.DisplayName) $($h.DisplayVersion)"
    Start-Process msiexec -ArgumentList "/x",$h.PSChildName,"/quiet","/norestart" -Wait -ErrorAction Continue | Out-Null
}
if (Test-Path $targetDir) {
    Write-Host "  removing leftover folder: $targetDir"
    Remove-Item $targetDir -Recurse -Force -ErrorAction SilentlyContinue
}

#-------------------------------------------------------------------------------
Heading "1. Build MSI"
if ($SkipBuild) {
    Write-Host "  (skipped by -SkipBuild)"
} else {
    $buildScript = Join-Path $root "installer\build-msi.ps1"
    & $buildScript -Version $Version
    if ($LASTEXITCODE -ne 0) { Fail "build-msi.ps1 exit=$LASTEXITCODE"; exit 2 }
}
if (-not (Test-Path $MsiPath)) { Fail "MSI not found: $MsiPath"; exit 2 }
$msiSize = (Get-Item $MsiPath).Length / 1MB
Pass ("MSI built: {0} ({1:N1} MB)" -f $MsiPath, $msiSize)

#-------------------------------------------------------------------------------
Heading "2. MSI table invariants (no install needed)"
$installer = New-Object -ComObject WindowsInstaller.Installer
$db = $installer.GetType().InvokeMember('OpenDatabase','InvokeMethod',$null,$installer,@($MsiPath,0))
function Query([string]$sql) {
    $view = $db.GetType().InvokeMember('OpenView','InvokeMethod',$null,$db,@($sql))
    $view.GetType().InvokeMember('Execute','InvokeMethod',$null,$view,$null)
    $rows = @()
    while ($true) {
        $rec = $view.GetType().InvokeMember('Fetch','InvokeMethod',$null,$view,$null)
        if (-not $rec) { break }
        $rows += ,$rec
    }
    return $rows
}
function Field($rec,[int]$idx) {
    return $rec.GetType().InvokeMember('StringData','GetProperty',$null,$rec,@($idx))
}

# ALLUSERS=1
$props = Query 'SELECT Property,Value FROM Property'
$allusers = $props | Where-Object { (Field $_ 1) -eq 'ALLUSERS' } | ForEach-Object { Field $_ 2 }
if ($allusers -eq '1') { Pass "ALLUSERS=1 (perMachine)" } else { Fail "ALLUSERS='$allusers' (expected '1')" }

# INSTALLFOLDER parent = ProgramFiles64Folder
$dirs = Query 'SELECT * FROM Directory'
$installDir = $dirs | Where-Object { (Field $_ 1) -eq 'INSTALLFOLDER' }
if ($installDir) {
    $parent = Field $installDir 2
    if ($parent -eq 'ProgramFiles64Folder') { Pass "INSTALLFOLDER parent is ProgramFiles64Folder" }
    else { Fail "INSTALLFOLDER parent='$parent' (expected ProgramFiles64Folder)" }
}

# No ServiceInstall entries
try {
    $svc = Query 'SELECT ServiceInstall FROM ServiceInstall'
    if ($svc.Count -eq 0) { Pass "ServiceInstall table empty (no service registered by MSI)" }
    else                  { Fail "ServiceInstall has $($svc.Count) entries" }
} catch {
    Pass "ServiceInstall table absent (no service registered by MSI)"
}

# Only InstallRclone custom action
$ca = Query 'SELECT Action,Type,Source,Target FROM CustomAction'
$svcActions = $ca | Where-Object { (Field $_ 1) -match '^(Service|Smirror)' }
if ($svcActions.Count -eq 0) { Pass "no Service* custom actions" }
else                         { Fail "$($svcActions.Count) Service* custom actions found" }

# Registry -- all HKLM
$reg = Query 'SELECT * FROM Registry'
$hklm = 0; $hkcu = 0
foreach ($r in $reg) {
    $root = Field $r 2
    if ($root -eq '2') { $hklm++ } elseif ($root -eq '1') { $hkcu++ }
}
if ($hkcu -eq 0 -and $hklm -gt 0) { Pass "$hklm/$($reg.Count) Registry entries use HKLM, 0 HKCU" }
else                               { Fail "HKCU entries present: $hkcu (HKLM: $hklm)" }

#-------------------------------------------------------------------------------
Heading "3. Install (synchronous)"
$p = Start-Process msiexec -ArgumentList "/i","`"$MsiPath`"","/quiet","/l*v","`"$installLog`"" -Wait -PassThru
if ($p.ExitCode -eq 0) { Pass "msiexec /i exit=0" }
else                   { Fail "msiexec /i exit=$($p.ExitCode) -- see $installLog"; Get-Content $installLog -Tail 30 }

#-------------------------------------------------------------------------------
Heading "4. Post-install state"
if (Test-Path $target) { Pass "binary at $target" }
else                   { Fail "binary missing at $target" }

if (Test-Path $target) {
    $ver = & $target version 2>&1 | Select-Object -First 1
    Write-Host "  version: $ver"
}

$svc = Get-Service -Name smirror -ErrorAction SilentlyContinue
if ($svc) { Fail "'smirror' service is registered (SEC-C2 regression)"; $svc | Format-Table -AutoSize }
else      { Pass "no 'smirror' service registered" }

$pathMachine = [Environment]::GetEnvironmentVariable("PATH","Machine")
if ($pathMachine -match [regex]::Escape($targetDir)) { Pass "$targetDir on machine PATH" }
else                                                  { Fail "$targetDir NOT on machine PATH" }

#-------------------------------------------------------------------------------
Heading "5. Per-user task round-trip"
if (Test-Path $target) {
    $taskOut = & $target task install 2>&1
    Write-Host ($taskOut | Out-String).Trim()
    if ($taskOut -match 'installed for the current user') { Pass "task install" }
    else                                                   { Fail "task install output unexpected" }

    $statusOut = & $target task status 2>&1
    Write-Host ($statusOut | Out-String).Trim()
    if ($statusOut -match 'installed') { Pass "task status reports installed" }
    else                               { Fail "task status unexpected" }

    $uninstallOut = & $target task uninstall 2>&1
    Write-Host ($uninstallOut | Out-String).Trim()
    if ($uninstallOut -match 'uninstalled') { Pass "task uninstall" }
    else                                    { Fail "task uninstall output unexpected" }
}

#-------------------------------------------------------------------------------
Heading "6. MSI uninstall (synchronous)"
$p2 = Start-Process msiexec -ArgumentList "/x","`"$MsiPath`"","/quiet","/l*v","`"$uninstallLog`"" -Wait -PassThru
if ($p2.ExitCode -eq 0) { Pass "msiexec /x exit=0" }
else                    { Fail "msiexec /x exit=$($p2.ExitCode) -- see $uninstallLog" }

#-------------------------------------------------------------------------------
Heading "7. Clean uninstall verification"
if (-not (Test-Path $target))      { Pass "binary removed" }       else { Fail "binary still present at $target" }
if (-not (Test-Path $targetDir))   { Pass "install folder removed" } else { Fail "install folder still present at $targetDir" }

$svc2 = Get-Service -Name smirror -ErrorAction SilentlyContinue
if (-not $svc2) { Pass "no 'smirror' service" } else { Fail "'smirror' service still registered" }

$pathMachine2 = [Environment]::GetEnvironmentVariable("PATH","Machine")
if ($pathMachine2 -notmatch [regex]::Escape($targetDir)) { Pass "$targetDir removed from machine PATH" }
else                                                      { Fail "$targetDir still on machine PATH" }

$stillRegistered = @()
$stillRegistered += Get-ItemProperty HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\* -ErrorAction SilentlyContinue
$stillRegistered += Get-ItemProperty HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\* -ErrorAction SilentlyContinue
$stillHits = $stillRegistered | Where-Object { $_.DisplayName -match 'SelectiveMirror' }
if ($stillHits.Count -eq 0) { Pass "no Uninstall registry entry" }
else                        { Fail "Uninstall registry still has: $($stillHits.DisplayName)" }

#-------------------------------------------------------------------------------
Heading "Summary"
if ($script:FailCount -eq 0) {
    Write-Host "ALL CHECKS PASSED" -ForegroundColor Green
    exit 0
} else {
    Write-Host "$($script:FailCount) CHECK(S) FAILED" -ForegroundColor Red
    Write-Host "Install log:   $installLog"
    Write-Host "Uninstall log: $uninstallLog"
    exit 3
}
