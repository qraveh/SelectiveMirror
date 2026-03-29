# SelectiveMirror

Real-time selective file synchronization for Windows. Watches local directories for changes and mirrors them to any [rclone](https://rclone.org/)-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

## Features

- **On-write sync** -- detects file changes via Windows `ReadDirectoryChangesW` (no polling)
- **Selective filtering** -- per-directory `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient** -- MD5 checksum comparison, debouncing, rate limiting
- **Single binary** -- `smirror.exe`, no runtime dependencies beyond rclone
- **Backend-agnostic** -- rclone handles all cloud/remote storage
- **Single-instance** -- file-based lock prevents duplicate watchers
- **Quiescence** -- files must be stable before sync (handles Office saves, long writes)
- **Delete policy** -- configurable ignore/mirror/quarantine for local deletions

## Installation

### MSI Installer (recommended)

Download `SelectiveMirror.msi` from [Releases](https://github.com/qraveh/SelectiveMirror/releases). The installer adds `smirror` to your system PATH.

### ZIP Archive

Download the ZIP from [Releases](https://github.com/qraveh/SelectiveMirror/releases), extract to a directory of your choice, and add it to your PATH.

## Prerequisites

- **rclone v1.73+** -- install with `winget install Rclone.Rclone` or from [rclone.org](https://rclone.org/downloads/)
- **An rclone remote** -- configure with `rclone config` (one-time setup)

## Quick Start

```bash
# 1. Configure an rclone remote (if you haven't already)
rclone config

# 2. Create your config
copy config.example.yaml %USERPROFILE%\.selectivemirror\config.yaml
# Edit config.yaml with your mirrors and remote

# 3. Run diagnostics and test mirror connectivity
smirror test-mirrors

# 4. Preview what would sync
smirror dry-run

# 5. Start mirroring
smirror start
```

## Commands

| Command | Description |
|---------|-------------|
| `smirror start` | Start foreground watcher (single-instance locked) |
| `smirror sync-now [mirror]` | Immediate full sync |
| `smirror dry-run [mirror]` | Show what would sync |
| `smirror status` | Show sync status, metrics, instance state |
| `smirror test-mirrors [mirror]` | Run diagnostics, check connectivity, verify sync state (aliases: `doctor`, `verify`) |
| `smirror list-filters [mirror]` | Show effective filter rules |
| `smirror explain <mirror> <path>` | Show include/exclude status and sync state |
| `smirror project-stats` | Show file counts and line counts across all mirrors (alias: `stats`) |
| `smirror report-bug` | Generate diagnostic report for bug filing |
| `smirror service <action>` | Windows Service: install, uninstall, start, stop |
| `smirror version` | Show version |

## Configuration

Default config location: `%USERPROFILE%\.selectivemirror\config.yaml`

Override with: `smirror --config path\to\config.yaml <command>`

See [`config.example.yaml`](config.example.yaml) for a full annotated example.

### Key settings

- **mirrors** -- list of watched directories with rclone remote destinations
- **global_excludes** -- patterns applied to all mirrors (`.gitignore` syntax)
- **delete_policy** -- `ignore` (default), `mirror`, or `quarantine`
- **Per-directory `.syncignore`** -- place in the directory root for per-mirror filtering

## Roadmap

- [x] **Phase 1** -- Core mirror: config, filters, watcher, sync, state, CLI
- [x] **Phase 1.5** -- Hardening: lock, quiescence, metrics, doctor/verify, delete policy
- [x] **Phase 2** -- Windows service: native SCM integration via `golang.org/x/sys/windows/svc`
- [ ] **Phase 3** -- USN journal recovery: fast restart reconciliation
- [x] **Phase 4** -- OSS polish: CI, issue templates, documentation, winget manifest

## License

MIT -- see [LICENSE](LICENSE) for details.
