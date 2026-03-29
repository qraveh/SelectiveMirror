# SelectiveMirror

Real-time selective file synchronization for Windows. Watches local directories for changes and mirrors them to any [rclone](https://rclone.org/)-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

## Features

- **On-write sync** -- detects file changes via Windows `ReadDirectoryChangesW` (no polling)
- **Selective filtering** -- per-project `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient** -- MD5 checksum comparison, debouncing, rate limiting
- **Single binary** -- `smirror.exe`, no runtime dependencies beyond rclone
- **Backend-agnostic** -- rclone handles all cloud/remote storage
- **Single-instance** -- file-based lock prevents duplicate watchers
- **Quiescence** -- files must be stable before sync (handles Office saves, long writes)
- **Delete policy** -- configurable ignore/mirror/quarantine for local deletions

## Installation

### MSI Installer (recommended)

Download `SelectiveMirror.msi` from [Releases](https://github.com/qraveh/SelectiveMirror/releases). The installer adds `smirror` to your system PATH and bundles rclone.

### ZIP Archive

Download the ZIP from [Releases](https://github.com/qraveh/SelectiveMirror/releases), extract to a directory of your choice, and add it to your PATH.

## Prerequisites

- **rclone v1.73+** -- bundled in the release package, or install separately: `winget install Rclone.Rclone`
- **An rclone remote** -- configure with `rclone config` (one-time setup)

## Quick Start

```bash
# 1. Configure an rclone remote (if you haven't already)
rclone config

# 2. Create your config
copy config.example.yaml %USERPROFILE%\.selectivemirror\config.yaml
# Edit config.yaml with your projects and remote

# 3. Run diagnostics
smirror doctor

# 4. Validate config + rclone connectivity
smirror validate

# 5. Preview what would sync
smirror dry-run

# 6. Start mirroring
smirror start
```

## Commands

| Command | Description |
|---------|-------------|
| `smirror start` | Start foreground watcher (single-instance locked) |
| `smirror sync-now [project]` | Immediate full sync |
| `smirror dry-run [project]` | Show what would sync |
| `smirror status` | Show sync status, metrics, instance state |
| `smirror validate` | Check config + rclone connectivity |
| `smirror list-filters [project]` | Show effective filter rules |
| `smirror explain <project> <path>` | Show include/exclude status and sync state |
| `smirror doctor` | Run 12-point self-test diagnostics |
| `smirror verify [project]` | Compare local vs remote, report drift |
| `smirror version` | Show version |

## Configuration

Default config location: `%USERPROFILE%\.selectivemirror\config.yaml`

Override with: `smirror --config path\to\config.yaml <command>`

See [`config.example.yaml`](config.example.yaml) for a full annotated example.

### Key settings

- **projects** -- list of watched directories with rclone remote destinations
- **global_excludes** -- patterns applied to all projects (`.gitignore` syntax)
- **delete_policy** -- `ignore` (default), `mirror`, or `quarantine`
- **Per-project `.syncignore`** -- place in the project root for project-specific filtering

## Roadmap

- [x] **Phase 1** -- Core mirror: config, filters, watcher, sync, state, CLI
- [x] **Phase 1.5** -- Hardening: lock, quiescence, metrics, doctor/verify, delete policy
- [ ] **Phase 2** -- Windows service: native SCM integration via `golang.org/x/sys/windows/svc`
- [ ] **Phase 3** -- USN journal recovery: fast restart reconciliation
- [ ] **Phase 4** -- OSS polish: CI, winget manifest, documentation

## License

MIT -- see [LICENSE](LICENSE) for details.
