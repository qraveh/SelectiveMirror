<#
.SYNOPSIS
    Uninstall any installed version of SelectiveMirror via its stable UpgradeCode.

.DESCRIPTION
    Stable counterpart to scripts/msi-info.ps1. Resolves the currently-installed
    SelectiveMirror ProductCode by looking up the project's UpgradeCode in
    Windows Installer's registration database, then calls `msiexec /x` against
    that ProductCode.

    Why not msiexec /x <path-to-msi> or msiexec /x {ProductCode}?

      * `<path-to-msi>` works only if the original install MSI is still on
        disk AND its ProductCode matches the installed product. Both
        constraints break easily: dev builds overwrite installer/bin/...,
        and every WiX build gets a fresh ProductCode (`Guid="*"`).
      * `{ProductCode}` requires you to know the current ProductCode, which
        also changes every build.

    The UpgradeCode never changes (it's `?define UpgradeCode = ...?` in
    installer/Variables.wxi, marked "DO NOT change after first release").
    So this script:

      1. Reads installer/Variables.wxi (single source of truth)
      2. Asks Windows Installer "what ProductCode(s) currently belong to this
         UpgradeCode?" via the WindowsInstaller.Installer COM API
      3. Runs `msiexec /x <pc>` for each (MajorUpgrade guarantees at most one)

    Read the script's exit code, not its stderr, to decide success.

.PARAMETER Interactive
    Show the MSI uninstall UI. Default is silent (`/qn`). Use this when
    you want to see Windows' confirmation prompts or the standard progress
    bar.

.PARAMETER WhatIf
    Standard PowerShell -WhatIf: report what would be uninstalled, take no
    action. Useful for verifying which ProductCode the script would target
    before committing.

.EXAMPLE
    .\scripts\msi-uninstall.ps1 -WhatIf

    Found: SelectiveMirror 1.0.21  [{713474A2-2B5A-4961-B280-A22B30180128}]
    What if: Performing the operation "Uninstall" on target ...

.EXAMPLE
    .\scripts\msi-uninstall.ps1

    Silently uninstall whatever SelectiveMirror version is currently
    installed. Exit code 0 on success or no-product-found.

.EXAMPLE
    .\scripts\msi-uninstall.ps1 -Interactive

    Show the standard Windows Installer UI during uninstall (progress bar,
    confirmation prompts).

.NOTES
    Exit codes:
      0  success (uninstalled, OR no SelectiveMirror installed = nothing to do)
      1  msiexec exited non-zero on at least one ProductCode (see log path printed
         on failure for full msiexec verbose log)
      2  could not parse UpgradeCode from installer/Variables.wxi
      3  not running as Administrator (per-machine MSI uninstall needs HKLM
         + Program Files write access; impossible from a non-elevated session)

    The UpgradeCode is read from installer/Variables.wxi at runtime, so this
    script stays in sync with any future UpgradeCode change automatically.
    If you ever DO change the UpgradeCode (post-1.0 rebrand, license change,
    etc.), older installs become unfindable to this script -- that's the
    correct semantics: a new UpgradeCode means "this is a different product
    line", uninstall the old one by its old UpgradeCode separately.

    Uses late-bound COM (InvokeMember) so it runs identically on Windows
    PowerShell 5.1 and PowerShell 7+ without an interop assembly. Same
    convention as scripts/msi-info.ps1.
#>
[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param(
    [switch]$Interactive
)

$ErrorActionPreference = 'Stop'

# ── Step 1: resolve UpgradeCode from installer/Variables.wxi ─────────────

$variablesWxi = Join-Path $PSScriptRoot "..\installer\Variables.wxi"
if (-not (Test-Path -LiteralPath $variablesWxi)) {
    [Console]::Error.WriteLine("msi-uninstall: cannot find $variablesWxi")
    [Console]::Error.WriteLine("  (script must run from a SelectiveMirror checkout)")
    exit 2
}

$wxiContent = Get-Content -LiteralPath $variablesWxi -Raw
if ($wxiContent -notmatch '<\?define\s+UpgradeCode\s*=\s*"([0-9A-Fa-f-]{36})"\s*\?>') {
    [Console]::Error.WriteLine("msi-uninstall: could not parse UpgradeCode from $variablesWxi")
    [Console]::Error.WriteLine("  Expected a line of the form: <?define UpgradeCode = `"<36-char-guid>`" ?>")
    exit 2
}
$upgradeCode = "{$($Matches[1])}"   # WindowsInstaller COM expects braced form

Write-Verbose "UpgradeCode: $upgradeCode (from $variablesWxi)"

# ── Step 2: ask Windows Installer for ProductCodes under this UpgradeCode ─

# Track COM refs so finally can release them. Avoids leaving msiserver.exe
# in a state that blocks later wix/msiexec invocations (the v1.0.20-cycle
# 'Windows Installer service failed to start' class of build error).
$com = [System.Collections.Generic.List[object]]::new()
$exitCode = 0
try {
    $installer = New-Object -ComObject WindowsInstaller.Installer
    $com.Add($installer) | Out-Null

    # RelatedProducts() takes a braced-GUID UpgradeCode string and returns
    # an enumerable of installed braced-GUID ProductCodes. MajorUpgrade
    # invariant: at most one ProductCode shares the UpgradeCode at any time.
    $products = @($installer.GetType().InvokeMember(
        'RelatedProducts', 'GetProperty', $null, $installer, @($upgradeCode)))

    if ($products.Count -eq 0) {
        Write-Host "No SelectiveMirror installation found (UpgradeCode $upgradeCode)."
        Write-Host "Nothing to do."
        # Intentional exit 0: 'no product to uninstall' is the desired end state
        # for callers running this defensively (e.g., uninstall-before-install).
        exit 0
    }

    foreach ($pc in $products) {
        # ProductInfo(<pc>, '<prop>') reads attributes from the installed-product
        # registry under HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall.
        $productName = $installer.GetType().InvokeMember(
            'ProductInfo', 'GetProperty', $null, $installer, @($pc, 'ProductName'))
        $version = $installer.GetType().InvokeMember(
            'ProductInfo', 'GetProperty', $null, $installer, @($pc, 'VersionString'))

        Write-Host ("Found: {0} {1}  [{2}]" -f $productName, $version, $pc)

        if (-not $PSCmdlet.ShouldProcess(
                "$productName $version [$pc]",
                "Uninstall")) {
            # -WhatIf path: ShouldProcess prints "What if: ..." and returns $false.
            # No admin needed in -WhatIf — we never invoke msiexec.
            continue
        }

        # Real-run path: per-machine MSI uninstall touches HKLM + Program Files.
        # Without admin, msiexec /qn fails silently with 1603 (it can't pop
        # a UAC prompt under /qn). Fail fast with clear remediation instead.
        $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
            [Security.Principal.WindowsBuiltInRole]::Administrator)
        if (-not $isAdmin) {
            [Console]::Error.WriteLine("msi-uninstall: Administrator privileges required.")
            [Console]::Error.WriteLine("  The SelectiveMirror MSI is per-machine (Scope=`"perMachine`" in Package.wxs);")
            [Console]::Error.WriteLine("  uninstall must write to HKLM and remove files from %ProgramFiles%.")
            [Console]::Error.WriteLine("  Re-run from an elevated shell. Examples:")
            [Console]::Error.WriteLine("    # Right-click PowerShell -> Run as Administrator, then:")
            [Console]::Error.WriteLine("    .\scripts\msi-uninstall.ps1")
            [Console]::Error.WriteLine("    # Or via gsudo (if installed):")
            [Console]::Error.WriteLine("    gsudo powershell -NoProfile -ExecutionPolicy Bypass -File scripts\msi-uninstall.ps1")
            exit 3
        }

        # Log file: %TEMP%\smirror-msi-uninstall-<pc-tail>-<timestamp>.log
        # Captured for every uninstall so the next 1603 (or similar) has a
        # full diagnostic trail. Path is reported on failure (and on -Verbose).
        $logDir = [System.IO.Path]::GetTempPath()
        $stamp = (Get-Date).ToString('yyyyMMdd-HHmmss')
        $pcTail = $pc.Substring($pc.Length - 13, 12)   # last 12 chars before `}`
        $logFile = Join-Path $logDir ("smirror-msi-uninstall-{0}-{1}.log" -f $pcTail, $stamp)

        $msiexecArgs = if ($Interactive) {
            @('/x', $pc, '/norestart', '/L*V', "`"$logFile`"")
        } else {
            @('/x', $pc, '/qn', '/norestart', '/L*V', "`"$logFile`"")
        }
        Write-Host ("  Running: msiexec {0}" -f ($msiexecArgs -join ' '))

        $proc = Start-Process -FilePath 'msiexec.exe' `
                              -ArgumentList $msiexecArgs `
                              -Wait -PassThru -NoNewWindow
        if ($proc.ExitCode -ne 0) {
            # Common codes:
            #   1602 — user cancelled
            #   1603 — fatal install error (very generic; check the log)
            #   1605 — product not installed (race with concurrent uninstall)
            #   1612 — installation source unavailable (rare for uninstall)
            #   3010 — restart required (success, but reboot needed)
            Write-Host ("  FAILED: msiexec exit code {0}" -f $proc.ExitCode) -ForegroundColor Red
            Write-Host ("  Log:    {0}" -f $logFile) -ForegroundColor Yellow
            Write-Host ("    Open the log to see the actual cause (search for 'returning' or 'error 1603').")
            $exitCode = 1
        } else {
            Write-Host "  OK"
            Write-Verbose ("  Log: {0}" -f $logFile)
        }
    }
}
finally {
    # Release children before parents.
    $com.Reverse()
    foreach ($obj in $com) {
        if ($null -ne $obj) {
            try { [void][System.Runtime.InteropServices.Marshal]::ReleaseComObject($obj) } catch { }
        }
    }
    [System.GC]::Collect()
    [System.GC]::WaitForPendingFinalizers()
}

exit $exitCode
