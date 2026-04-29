---
title: "SelectiveMirror Installation Manual"
author: "Raveh (smirror@qodeh.com)"
date: "2026-03-27"
toc: true
toc-depth: 3
geometry: margin=1in
fontsize: 11pt
---

# 1. System Requirements

SelectiveMirror runs on Windows desktops and servers. Before installing, verify that your system meets the following requirements.

## Operating System

- **Windows 10** (version 1809 or later) or **Windows 11**, 64-bit (amd64)
- The file watcher uses the Windows `ReadDirectoryChangesW` API, which is available on all supported Windows versions

## rclone

- **rclone v1.73 or later** is required for full compatibility
- rclone v1.50--1.72 will work with reduced functionality (the `--skip-links` flag is unavailable, which means symlinks may be uploaded to the remote)
- rclone versions below 1.50 are not supported

rclone is not bundled with SelectiveMirror. It must be installed separately. See Section 3 for installation instructions.

## Disk Space

- **SelectiveMirror binary**: approximately 23 MB (statically links SQLite via CGo)
- **State database**: grows proportionally to the number of tracked files. A typical installation with 10,000 files uses under 5 MB
- **Log files**: configurable, typically under 50 MB with log rotation

## Building from Source (optional)

End users do **not** need a C compiler — the MSI/ZIP binaries have SQLite embedded. If you build from source:

- **Go 1.26+** and **CGo enabled** (default).
- **MinGW-w64** (for `gcc.exe` on PATH) — `winget install BrechtSanders.WinLibs.POSIX.UCRT`. CGo is required because the SQLite driver (`github.com/mattn/go-sqlite3`) compiles SQLite C into the binary.

## .NET Framework (MSI installer only)

- The MSI installer requires **.NET Framework 4.5** or later, which is included in all supported Windows versions
- The portable ZIP installation has no .NET dependency

## Network

- An active internet connection is required for cloud backends (Google Drive, S3, Dropbox, etc.)
- Local and SFTP backends require only LAN connectivity
- Firewall rules must allow outbound HTTPS (port 443) for cloud backends


# 2. rclone --- SelectiveMirror's Engine

## What Is rclone?

rclone is an open-source command-line program for managing files on cloud storage. It supports over 70 storage backends, including Google Drive, Amazon S3, Dropbox, OneDrive, SFTP, Backblaze B2, Cloudflare R2, and many others. rclone handles authentication, chunked uploads, checksums, retries, and rate limiting for each backend.

## Why rclone Is Central to SelectiveMirror

SelectiveMirror does not implement any cloud storage protocols directly. Instead, it delegates all file transfer operations to rclone. This design gives SelectiveMirror immediate access to every backend that rclone supports, without embedding cloud-specific code.

The practical benefit: you configure a backend once using `rclone config`, and SelectiveMirror uses it transparently. If rclone can talk to your storage, SelectiveMirror can mirror to it.

## The Subprocess Architecture

SelectiveMirror invokes rclone as a subprocess for each sync operation. It does not link against rclone as a library or embed it into its binary. The interaction follows this pattern:

1. SelectiveMirror detects a file change via the Windows filesystem watcher
2. It constructs an rclone command. Single-file sync uses `rclone copyto <source> <dest>` (default rclone size+mtime comparison — no `--checksum`, so mtime-only changes propagate; SM-017). Batch reconciliation uses `rclone copy --checksum --filter-from <filter>` against the project root.
3. It executes rclone as a child process and captures its exit code
4. The exit code, along with timestamps and checksums, is recorded in the state database

This subprocess model provides clean error isolation. If an rclone operation fails (network timeout, authentication expiry, backend error), SelectiveMirror captures the exit code, logs the failure, and retries on the next sync cycle. A failing rclone call never crashes SelectiveMirror itself.

Each rclone invocation has a 5-minute timeout to prevent indefinite hangs on unresponsive backends.

## 70+ Backends

Because rclone handles the backend communication, SelectiveMirror inherits support for all rclone backends. Some of the most commonly used include:

| Backend | rclone Remote Prefix | Notes |
|---------|---------------------|-------|
| Google Drive | `gdrive:` | OAuth2 authentication |
| Amazon S3 | `s3:` | Also covers MinIO, Wasabi, etc. |
| Dropbox | `dropbox:` | OAuth2 authentication |
| OneDrive | `onedrive:` | OAuth2 authentication |
| SFTP | `sftp:` | SSH key or password |
| Local filesystem | `/path/` or `C:\path\` | For local-to-local mirroring |
| Backblaze B2 | `b2:` | Application key authentication |
| Cloudflare R2 | `s3:` (S3-compatible) | Uses S3 protocol |

The complete list is available at <https://rclone.org/overview/>.


# 3. Installing rclone

## Option A: WinGet (Recommended)

WinGet is the Windows Package Manager, included in Windows 10 (1809+) and Windows 11. Open a terminal (Command Prompt, PowerShell, or Windows Terminal) and run:

```
winget install Rclone.Rclone
```

WinGet places rclone on your system PATH automatically. No restart is required.

## Option B: Chocolatey

If you use the Chocolatey package manager, open an elevated terminal and run:

```
choco install rclone
```

## Option C: Scoop

If you use Scoop:

```
scoop install rclone
```

## Option D: Manual Download

1. Download the latest Windows AMD64 ZIP from <https://rclone.org/downloads/>
2. Extract the ZIP to a permanent location (e.g., `C:\Program Files\rclone\`)
3. Add that directory to your system PATH:
   - Open **Settings > System > About > Advanced system settings**
   - Click **Environment Variables**
   - Under **System variables**, select `Path` and click **Edit**
   - Click **New** and add the directory containing `rclone.exe`
   - Click **OK** to save

## Verifying the Installation

Open a new terminal window and run:

```
rclone version
```

You should see output similar to:

```
rclone v1.73.0
- os/version: Microsoft Windows 11 Home 10.0.26200 (64 bit)
- os/kernel: windows (amd64)
- os/type: windows
- os/arch: amd64
- go/version: go1.23.4
- go/linking: static
- go/tags: cmount
```

Verify that the version number is **1.73.0 or higher**. SelectiveMirror checks this automatically and will warn you if the version is too old.

## Version Requirements Explained

SelectiveMirror requires rclone v1.73+ because it uses the `--skip-links` flag, introduced in that version, to prevent symlinks from being followed and uploaded to the remote. Without this flag, a symlink pointing to a large directory tree could cause unintended bulk uploads.

SelectiveMirror performs a version check at startup and during `smirror test-mirrors`. If your rclone version is between 1.50 and 1.72, the application will run but report "partial compatibility" in diagnostics. Versions below 1.50 are rejected outright because they lack critical subcommands.


# 4. Configuring rclone

Before SelectiveMirror can sync files, rclone needs at least one configured remote. Remotes are configured using the interactive `rclone config` command.

## Google Drive (Most Common)

Google Drive is the most frequently used backend. To configure it:

```
rclone config
```

Follow the interactive prompts:

1. Type `n` for new remote
2. Enter a name (e.g., `gdrive`)
3. Select **Google Drive** from the list (type the number or `drive`)
4. Leave `client_id` and `client_secret` blank (uses rclone's built-in credentials)
5. Choose the scope --- **Full access** is recommended for mirroring
6. Leave `service_account_file` blank
7. When asked to auto-config, type `y` --- a browser window will open for OAuth2 authorization
8. Authorize rclone in the browser
9. Choose whether this is a Team Drive (usually `n` for personal accounts)
10. Confirm with `y`

Test connectivity:

```
rclone lsd gdrive:
```

This lists the top-level directories in your Google Drive. If you see your folders, the remote is configured correctly.

## Other Common Backends

### Amazon S3

```
rclone config
```

Select `s3`, provide your Access Key ID and Secret Access Key, choose your region and endpoint. Test with `rclone lsd s3:your-bucket`.

### SFTP

```
rclone config
```

Select `sftp`, provide the host, user, and authentication method (SSH key recommended). Test with `rclone lsd sftp:`.

### Dropbox

```
rclone config
```

Select `dropbox`, follow the OAuth2 browser flow. Test with `rclone lsd dropbox:`.

### Local Filesystem

No `rclone config` is needed for local paths. Use a plain path as the remote in your SelectiveMirror config (e.g., `D:\Backups\MyProject`). This is useful for mirroring to external drives or network shares.

## Where rclone Stores Its Configuration

rclone stores its configuration at `%APPDATA%\rclone\rclone.conf` on Windows. This file contains OAuth tokens and credentials. Protect it accordingly. SelectiveMirror reads remotes from this file indirectly, by invoking rclone commands that reference the remote name.


# 5. Installing SelectiveMirror

## Option A: MSI Installer (Recommended)

1. Download `SelectiveMirror.msi` from the [Releases page](https://github.com/qraveh/SelectiveMirror/releases)
2. Double-click the MSI file
3. Follow the installation wizard
4. The installer places `smirror.exe` in `C:\Program Files\SelectiveMirror\` and adds it to the system PATH
5. The installer creates the configuration directory `%USERPROFILE%\.selectivemirror\` with a template `config.yaml` if one does not already exist

Verify the installation by opening a new terminal:

```
smirror version
```

Expected output:

```
smirror 0.8.x
```

## Option B: Portable Installation (ZIP)

1. Download `SelectiveMirror-windows-amd64.zip` from the [Releases page](https://github.com/qraveh/SelectiveMirror/releases)
2. Extract the ZIP to a directory of your choice (e.g., `C:\Tools\SelectiveMirror\`)
3. Add that directory to your system PATH (same procedure as described in Section 3, Option D)
4. Create the configuration directory manually:

```
mkdir %USERPROFILE%\.selectivemirror
```

5. Copy the example configuration:

```
copy config.example.yaml %USERPROFILE%\.selectivemirror\config.yaml
```

6. Verify with `smirror version`


# 6. Initial Configuration

## Configuration File Location

SelectiveMirror reads its configuration from:

```
%USERPROFILE%\.selectivemirror\config.yaml
```

This is typically `C:\Users\<YourName>\.selectivemirror\config.yaml`. You can override this with the `--config` flag:

```
smirror --config C:\path\to\custom\config.yaml test-mirrors
```

## Configuration Reference

Below is the complete list of configuration fields with their defaults.

### Project Settings

Each entry under `projects` defines a directory to watch and its sync destination.

```yaml
mirrors:
  - name: MyProject              # Required. Unique project name.
    local_path: C:\Projects\MyProject  # Required. Local directory to watch.
    remote: "gdrive:backup/MyProject"  # Required. rclone remote destination.
    debounce_sec: 5              # Wait N seconds after last change (default: 5)
    max_file_size_mb: 100        # Skip files larger than this (default: 100)
    syncignore_path: ""          # Custom .syncignore path (default: <local_path>/.syncignore)
```

### Global Settings

```yaml
global_excludes:     # Patterns applied to ALL projects (.gitignore syntax)
  - .git/
  - __pycache__/
  - node_modules/
  - "*.log"
  - .env

state_db: ~/.selectivemirror/state.db          # SQLite state database path
log_file: ~/.selectivemirror/selectivemirror.log  # Log file path
log_level: info                                # debug, info, warn, error

rclone_path: rclone          # Path to rclone binary (default: "rclone", searches PATH)
rclone_extra_flags: []       # Extra flags passed to every rclone invocation
bandwidth_limit: ""          # Bandwidth cap, e.g. "10M" for 10 MB/s

heartbeat_interval_sec: 300  # Write heartbeat to log every N seconds (default: 300)
reconcile_interval_sec: 300  # Periodic full sync interval in seconds (default: 300)
sync_workers: 4              # Concurrent sync workers, 1--16 (default: 4)

delete_policy: delete        # delete (default), ignore, or quarantine
quarantine_days: 30          # Days to keep quarantined files (default: 30)
```

### Delete Policies

| Policy | Behavior |
|--------|----------|
| `delete` (default) | Local deletions are mirrored to the remote. `mirror` is a deprecated alias. |
| `ignore` | Local deletions are not propagated — the remote is append-only |
| `quarantine` | Deleted files are moved to a `.quarantine/` directory on the remote. Files older than `quarantine_days` are cleaned up automatically |

### Timestamps

All timestamps produced by SelectiveMirror (in logs, state database, and diagnostic output) include timezone information. This ensures unambiguous interpretation regardless of the machine's locale or when reviewing logs from a different timezone.

## Project Setup Example

Suppose you want to mirror two project directories to Google Drive. Your `config.yaml` would look like this:

```yaml
mirrors:
  - name: Research
    local_path: C:\Projects\Research
    remote: "gdrive:Backups/Research"
    debounce_sec: 10
    max_file_size_mb: 200

  - name: WebApp
    local_path: C:\Projects\WebApp
    remote: "gdrive:Backups/WebApp"

global_excludes:
  - .git/
  - node_modules/
  - __pycache__/
  - "*.log"
  - .env
  - "~$*"
  - "*.tmp"

delete_policy: quarantine
quarantine_days: 14
sync_workers: 4
bandwidth_limit: "20M"
```

## .syncignore Basics

Each project can have a `.syncignore` file in its root directory (e.g., `C:\Projects\Research\.syncignore`). The syntax is identical to `.gitignore`:

```
# Build artifacts
build/
dist/
*.exe
*.dll

# IDE files
.idea/
.vscode/
*.suo
*.user

# Large data files
data/raw/
*.h5
*.parquet
```

Rules in `.syncignore` are combined with `global_excludes` from the config file. The `.syncignore` file is hot-reloaded: edits take effect immediately without restarting SelectiveMirror.

You can inspect the effective filter rules for any project:

```
smirror list-filters Research
```

To understand why a specific file is included or excluded:

```
smirror explain Research data/results.csv
```


# 7. First Run

Follow this three-step sequence for your first run: validate, dry-run, start.

## Step 1: Validate

The `test-mirrors` command checks your configuration file for syntax errors, verifies that all local paths exist, and tests rclone connectivity to each configured remote.

```
smirror test-mirrors
```

Expected output for a healthy setup:

```
config: OK (2 projects)
rclone: v1.73.0 (full compatibility)
remote "gdrive:Backups/Research": OK
remote "gdrive:Backups/WebApp": OK
All checks passed.
```

If validation reports errors, fix them before proceeding. Common issues are covered in Section 10.

## Step 2: Dry Run

The `dry-run` command scans all projects and shows what would be synced, without actually transferring any files.

```
smirror dry-run
```

Or for a specific project:

```
smirror dry-run Research
```

Review the output to confirm that the right files are being picked up and that excluded patterns are working as expected.

## Step 3: Start

Once you are satisfied with the dry-run output, start the watcher:

```
smirror start
```

SelectiveMirror runs in the foreground. It will:

1. Acquire a single-instance lock (only one `smirror start` can run at a time)
2. Perform an initial batch sync for each project (reconciliation)
3. Begin watching all configured directories for file changes
4. Sync changed files to the configured remotes in near-real-time

You will see log output in the terminal. A heartbeat message is logged every 5 minutes (configurable) to confirm the watcher is alive.

Press `Ctrl+C` to stop the watcher gracefully.

## Verifying It Works

While the watcher is running, create or modify a file in one of your watched directories. Within a few seconds, you should see log output indicating the file was synced. With default settings (queue-based fairness), syncs happen immediately. If you configured `debounce_sec > 0`, wait that many seconds after the last save.

To check the overall status:

```
smirror status
```

To verify that the remote matches the local state:

```
smirror test-mirrors
```

The `test-mirrors` command runs diagnostic checks including rclone version verification, config validation, state database integrity, lock file status, remote connectivity, and drift detection.


# 8. Background Modes

SelectiveMirror supports three ways to keep the file watcher running:

| Mode | When it runs | Privilege | Admin to set up? | Best for |
|------|--------------|-----------|------------------|----------|
| **Foreground** (`smirror start`) | While the terminal is open | Current user | No | Development, debugging, one-off syncs |
| **Scheduled Task** (`smirror task ...`) | From user logon to logoff | Current user | **No** | **Default recommended mode — desktop installs** |
| **Windows Service** (`smirror service ...`) | Continuously, across logoff and reboot | LocalSystem | Yes (admin) | 24/7 unattended servers; when sync must survive logoff |

The MSI installer places `smirror.exe` in `%ProgramFiles%\SelectiveMirror\` and adds it to PATH. It does **not** automatically register a background service — you choose the mode that matches your use case.

## Option A: Scheduled Task (recommended)

The task mode is the equivalent of how Dropbox, Google Drive Desktop, and OneDrive run: a per-user background process that starts at logon, owned and cleanable by the user with no admin rights.

```
smirror task install   # register for current user (no admin)
smirror task status    # show installed/running state
smirror task start     # run once now without waiting for logon
smirror task stop      # terminate any running instance
smirror task uninstall # remove the task
```

Key properties:

- Data files in `~/.selectivemirror/` are owned by you — `smirror clean --self` can remove them without admin.
- The task is named `SelectiveMirror` under your user's task folder. You can also inspect/edit it from `taskschd.msc`.
- Each user on a shared machine installs and controls their own task independently.
- The task is disabled while your screen is locked only if you have Group Policy forcing that; by default it keeps running.

## Option B: Windows Service (advanced, 24/7)

Use the Windows Service when you need sync to continue running across user logoffs, or on a headless/unattended server. Service mode requires admin and runs as LocalSystem.

### Prerequisite: admin-owned config (SEC-C5)

Because the service runs as LocalSystem and reads your config as LocalSystem, the config file must be owned by an administrative principal. Otherwise any standard user who can edit `config.yaml` could use `rclone_path`, `rclone_extra_flags`, or hooks to execute arbitrary code as SYSTEM.

Move the config to an admin-owned location before installing the service:

```powershell
# From an elevated (admin) terminal:
New-Item -ItemType Directory -Path "$env:ProgramData\SelectiveMirror" -Force
Copy-Item "$env:USERPROFILE\.selectivemirror\config.yaml" "$env:ProgramData\SelectiveMirror\config.yaml"
# Optional: lock down the ACL so only admins can edit
icacls "$env:ProgramData\SelectiveMirror\config.yaml" /inheritance:r /grant:r "Administrators:F" "SYSTEM:F"
```

### Install, start, stop, uninstall

Open an **elevated** terminal and run:

```
smirror --config "%ProgramData%\SelectiveMirror\config.yaml" service install start
```

`smirror service install` refuses to register the service if the config is not admin-owned; follow the printed remedy.

You can also run the actions separately:

```
smirror service install    # register with SCM
smirror service start      # start the service
smirror service stop       # stop the service
smirror service stop uninstall        # stop and remove registration
smirror service uninstall --clean     # remove registration and %ProgramData%\SelectiveMirror
smirror service uninstall --clean --yes   # same, no prompts
```

The service also appears in Windows Services (`services.msc`) as "SelectiveMirror".

## Checking status

```
smirror status
```

Shows whether the task and/or service is running, the PID, uptime, sync metrics, and ghost scan results.

## Foreground Mode

For development or debugging, run `smirror start` directly in a terminal. The single-instance lock ensures only one instance runs per config.


# 9. Upgrading and Uninstalling

## Upgrading SelectiveMirror

### MSI Upgrade

Download the new MSI and run it. The installer upgrades in place, preserving your configuration and state database. Stop the running `smirror start` process before upgrading.

### Portable Upgrade

1. Stop the running `smirror start` process
2. Replace `smirror.exe` with the new version
3. Run `smirror test-mirrors` to verify the upgrade

Your configuration (`~/.selectivemirror/config.yaml`), state database (`state.db`), and log files are not modified during an upgrade.

## Upgrading rclone

```
winget upgrade Rclone.Rclone
```

Or, if rclone was installed manually:

```
rclone selfupdate
```

After upgrading rclone, run `smirror test-mirrors` to verify continued compatibility.

## Uninstalling SelectiveMirror

### Per-user cleanup (no admin)

To remove your scheduled task and personal data directory:

```
smirror clean --self
```

This removes the per-user task and `~/.selectivemirror/`. The binary in Program Files and any Windows Service remain untouched.

### Full removal (admin)

```
smirror clean --all
```

Removes the task, your user data, the Windows Service (if installed), and `%ProgramData%\SelectiveMirror`. Prompts for UAC if the service is installed. Then use **Settings > Apps > Installed apps** or **Control Panel > Programs and Features** to uninstall the MSI, which removes `smirror.exe` from Program Files.

### MSI Uninstall

The MSI uninstaller removes the binary and PATH entry. It does **not** automatically remove scheduled tasks or user data — each user on the machine should run `smirror clean --self` from their own account before the MSI is uninstalled.

### Portable Uninstall

1. Run `smirror clean --self` (and `smirror clean --all` if you installed a service)
2. Delete the `smirror.exe` binary
3. Remove the directory from PATH


# 10. Troubleshooting Installation Issues

## rclone Not Found

**Symptom**: `smirror test-mirrors` reports "rclone not found".

**Causes and fixes**:

- **PATH not updated**: If you installed rclone via manual download, ensure the directory containing `rclone.exe` is in your system PATH. Open a *new* terminal window after modifying PATH --- existing windows do not pick up changes.
- **WinGet installed but not linked**: WinGet places a symlink in `%LOCALAPPDATA%\Microsoft\WinGet\Links\`. If this directory is not on PATH, rclone will not be found. Running `winget install Rclone.Rclone` a second time usually fixes this.
- **Custom rclone location**: If rclone is installed in a non-standard location, set the `rclone_path` field in `config.yaml` to the absolute path:

```yaml
rclone_path: C:\Tools\rclone\rclone.exe
```

SelectiveMirror searches for rclone in this order: (1) the configured `rclone_path`, (2) the system PATH, (3) common Windows install locations including Program Files, WinGet Links, Chocolatey, and Scoop directories.

## rclone Version Too Old

**Symptom**: `smirror test-mirrors` reports "partial compatibility" or "incompatible".

**Fix**: Upgrade rclone to v1.73 or later:

```
winget upgrade Rclone.Rclone
```

Verify with:

```
rclone version
```

## Remote Unreachable

**Symptom**: `smirror test-mirrors` reports a remote connectivity failure.

**Possible causes**:

- **Expired OAuth token**: Re-authorize by running `rclone config reconnect gdrive:` (replace `gdrive` with your remote name)
- **Network issue**: Verify internet connectivity. Try `rclone lsd gdrive:` directly to isolate whether the problem is rclone or SelectiveMirror
- **Firewall blocking**: Ensure outbound HTTPS (port 443) is allowed
- **Incorrect remote name**: Check that the remote name in `config.yaml` matches exactly what `rclone listremotes` shows, including the trailing colon

## Configuration Validation Errors

**Symptom**: `smirror test-mirrors` reports a config error.

Common errors and their fixes:

| Error Message | Fix |
|--------------|-----|
| "no projects defined in config" | Add at least one project under the `mirrors:` key |
| "name is required" | Every project entry must have a `name` field |
| "duplicate name" | Project names must be unique |
| "local_path is required" | Every project must specify a `local_path` |
| "remote is required" | Every project must specify a `remote` |
| "local_path ... does not exist" | The specified directory must exist on disk before starting |
| "local_path ... is not a directory" | `local_path` must point to a directory, not a file |

## General Diagnostic

When in doubt, run:

```
smirror test-mirrors
```

The `test-mirrors` command performs diagnostic checks covering configuration integrity, rclone detection and version, remote connectivity, state database integrity, lock file status, drift detection, and more. Its output will point you directly to the source of any problem.
