<#
.SYNOPSIS
    Read metadata from a Windows Installer (.msi) file without installing it.

.DESCRIPTION
    Opens an MSI's internal database in read-only mode via the WindowsInstaller
    COM API, fetches rows from the Property table, and reports the standard
    identification fields (ProductName, ProductVersion, ProductCode, UpgradeCode,
    Manufacturer) plus file size and SHA-256.

    Read-only. No admin required. The MSI is never executed.

    Typical uses:
      * Confirm a fresh build's version before publishing.
      * Verify a downloaded MSI matches an expected ProductCode (e.g., in CI).
      * Compare two builds (same UpgradeCode + bumped ProductVersion = upgrade
        path is intact).

.PARAMETER Path
    Path to the .msi file. Required.

.PARAMETER Property
    If supplied, returns just the value of that one Property-table row
    (e.g., 'ProductVersion'). Useful in pipelines:

        $v = .\msi-info.ps1 -Path build.msi -Property ProductVersion

    Exit code 2 if the property is absent.

.PARAMETER Json
    Emit a JSON object on stdout instead of the default formatted list.
    Useful for CI jobs that consume the output programmatically.

.EXAMPLE
    .\scripts\msi-info.ps1 -Path installer\bin\Release\SelectiveMirror.msi

    ProductName    : SelectiveMirror
    ProductVersion : 1.0.18
    ProductCode    : {EE45728C-BE88-432B-9C16-51DB282258B7}
    UpgradeCode    : {7E3A9F12-4B5C-4D8E-A1F0-3C6B8D9E2F4A}
    Manufacturer   : Raveh Neeman
    File           : C:\...\SelectiveMirror.msi
    SizeMB         : 3.75
    SHA256         : 7A00FF21...92B0

.EXAMPLE
    # CI gate: fail if the built MSI's version doesn't match the git tag.
    $expected = '1.0.18'
    $actual   = .\scripts\msi-info.ps1 -Path SelectiveMirror.msi -Property ProductVersion
    if ($actual -ne $expected) { throw "MSI version $actual <> tag $expected" }

.EXAMPLE
    # JSON for downstream tools.
    .\scripts\msi-info.ps1 -Path SelectiveMirror.msi -Json | ConvertFrom-Json

.NOTES
    The MSI format is a structured-storage database; the Property table holds
    install-time variables, including the five identification fields above and
    others like ARPCONTACT, ARPHELPLINK, INSTALLLEVEL, etc. To inspect more
    fields, extend the $wanted array below or pass -Property explicitly.

    The script uses late-bound COM (InvokeMember) so it runs the same way on
    Windows PowerShell 5.1 and PowerShell 7+ without an interop assembly.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true, Position=0)]
    [ValidateScript({ Test-Path $_ -PathType Leaf })]
    [string]$Path,

    [string]$Property,

    [switch]$Json
)

$ErrorActionPreference = 'Stop'
$abs = (Resolve-Path -LiteralPath $Path).Path

# Fields reported in the default view. Add rows here to extend the report
# (every entry must be a real key in the MSI's Property table).
$wanted = @(
    'ProductName',
    'ProductVersion',
    'ProductCode',
    'UpgradeCode',
    'Manufacturer'
)

# Track COM refs so finally can release them. msiserver.exe lingers if we
# don't, which matters when this script runs inside a long-lived CI host.
$com = [System.Collections.Generic.List[object]]::new()
try {
    $installer = New-Object -ComObject WindowsInstaller.Installer
    $com.Add($installer) | Out-Null

    # mode 0 = msiOpenDatabaseModeReadOnly. Never use modes that take a write
    # lock on the .msi -- this script is read-only by contract.
    $db = $installer.GetType().InvokeMember(
        'OpenDatabase', 'InvokeMethod', $null, $installer, @($abs, 0))
    $com.Add($db) | Out-Null

    # MSI's SQL dialect is restricted: no IN(), no parameter binding, string
    # quoting is finicky. Easiest correct path is SELECT the whole Property
    # table and filter in PowerShell.
    $view = $db.GetType().InvokeMember(
        'OpenView', 'InvokeMethod', $null, $db,
        @("SELECT Property, Value FROM Property"))
    $com.Add($view) | Out-Null
    $null = $view.GetType().InvokeMember(
        'Execute', 'InvokeMethod', $null, $view, $null)

    $all = [ordered]@{}
    while ($true) {
        $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
        if ($null -eq $rec) { break }
        $com.Add($rec) | Out-Null
        $k = $rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, 1)
        $v = $rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, 2)
        $all[$k] = $v
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

if ($Property) {
    if (-not $all.Contains($Property)) {
        # Bypass Write-Error: with $ErrorActionPreference='Stop' it would
        # throw and clobber our explicit exit code. Write straight to stderr
        # and exit 2 so callers can distinguish "missing property" from the
        # generic exit 1 that any other failure produces.
        [Console]::Error.WriteLine("msi-info: property '$Property' not found in $abs")
        exit 2
    }
    # Bare value so $x = ... captures cleanly.
    Write-Output $all[$Property]
    return
}

# Default view: requested properties first, then file metadata.
$report = [ordered]@{}
foreach ($k in $wanted) {
    if ($all.Contains($k)) { $report[$k] = $all[$k] }
}
$report['File']   = $abs
$report['SizeMB'] = [math]::Round((Get-Item -LiteralPath $abs).Length / 1MB, 2)
$report['SHA256'] = (Get-FileHash -LiteralPath $abs -Algorithm SHA256).Hash

if ($Json) {
    $report | ConvertTo-Json -Depth 3
} else {
    [pscustomobject]$report | Format-List
}
