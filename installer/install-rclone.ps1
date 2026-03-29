# install-rclone.ps1 — Download and install rclone if not already present.
# Called as a post-install custom action from the MSI installer.
# Tries winget first, falls back to direct download from rclone.org.
#
# Exit 0 = success (or rclone already installed)
# Exit 1 = failed to install rclone (non-fatal for MSI — user is warned)

$ErrorActionPreference = "Stop"
$logFile = Join-Path $env:TEMP "selectivemirror-rclone-install.log"

function Write-Log {
    param([string]$Message)
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $line = "$ts  $Message"
    Add-Content -Path $logFile -Value $line
    Write-Host $line
}

# Check if rclone is already available
function Test-RcloneInstalled {
    try {
        $result = & where.exe rclone 2>$null
        if ($LASTEXITCODE -eq 0 -and $result) {
            return $result | Select-Object -First 1
        }
    } catch {}

    # Check common locations
    $candidates = @(
        "$env:ProgramFiles\rclone\rclone.exe"
    )
    if ($env:LOCALAPPDATA) {
        $candidates += "$env:LOCALAPPDATA\Microsoft\WinGet\Links\rclone.exe"
    }
    foreach ($c in $candidates) {
        if (Test-Path $c) { return $c }
    }
    return $null
}

Write-Log "SelectiveMirror rclone installer starting"

$existing = Test-RcloneInstalled
if ($existing) {
    Write-Log "rclone already installed: $existing"
    exit 0
}

Write-Log "rclone not found — attempting installation"

# Method 1: Try winget
$wingetPath = $null
try {
    $wingetPath = (Get-Command winget -ErrorAction SilentlyContinue).Source
} catch {}

if ($wingetPath) {
    Write-Log "Found winget at: $wingetPath"
    Write-Log "Running: winget install Rclone.Rclone --accept-package-agreements --accept-source-agreements"
    try {
        & winget install Rclone.Rclone --accept-package-agreements --accept-source-agreements 2>&1 | ForEach-Object { Write-Log "  winget: $_" }
        if ($LASTEXITCODE -eq 0) {
            Write-Log "rclone installed successfully via winget"
            exit 0
        }
        Write-Log "winget exited with code $LASTEXITCODE — falling back to direct download"
    } catch {
        Write-Log "winget failed: $_ — falling back to direct download"
    }
} else {
    Write-Log "winget not available — using direct download"
}

# Method 2: Direct download from rclone.org
$rcloneDir = Join-Path $env:ProgramFiles "rclone"
$rcloneExe = Join-Path $rcloneDir "rclone.exe"
$downloadUrl = "https://downloads.rclone.org/rclone-current-windows-amd64.zip"
$tempZip = Join-Path $env:TEMP "rclone-download.zip"
$tempExtract = Join-Path $env:TEMP "rclone-extract"

try {
    Write-Log "Downloading rclone from $downloadUrl"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -UseBasicParsing
    Write-Log "Downloaded $(((Get-Item $tempZip).Length / 1MB).ToString('N1')) MB"

    # Clean up previous extraction
    if (Test-Path $tempExtract) { Remove-Item $tempExtract -Recurse -Force }

    Write-Log "Extracting archive"
    Expand-Archive -Path $tempZip -DestinationPath $tempExtract -Force

    # Find rclone.exe in the extracted directory (it's inside a versioned subfolder)
    $extracted = Get-ChildItem -Path $tempExtract -Filter "rclone.exe" -Recurse | Select-Object -First 1
    if (-not $extracted) {
        Write-Log "ERROR: rclone.exe not found in downloaded archive"
        exit 1
    }

    # Create install directory and copy
    if (-not (Test-Path $rcloneDir)) {
        New-Item -ItemType Directory -Path $rcloneDir -Force | Out-Null
    }
    Copy-Item $extracted.FullName $rcloneExe -Force
    Write-Log "Installed rclone to $rcloneExe"

    # Add to system PATH if not already there
    $systemPath = [Environment]::GetEnvironmentVariable("PATH", "Machine")
    if ($systemPath -notlike "*$rcloneDir*") {
        [Environment]::SetEnvironmentVariable("PATH", "$systemPath;$rcloneDir", "Machine")
        Write-Log "Added $rcloneDir to system PATH"
    }

    # Verify
    $ver = & $rcloneExe version 2>&1 | Select-Object -First 1
    Write-Log "Installed: $ver"

    exit 0
} catch {
    Write-Log "ERROR: Direct download failed: $_"
    exit 1
} finally {
    # Clean up temp files
    Remove-Item $tempZip -Force -ErrorAction SilentlyContinue
    Remove-Item $tempExtract -Recurse -Force -ErrorAction SilentlyContinue
}
