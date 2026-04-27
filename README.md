# SelectiveMirror

Real-time selective file synchronization for Windows. Watches local directories for changes and mirrors them to any [rclone](https://rclone.org/)-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

> **ISO compliance status (v1.0)**: SelectiveMirror applies four ISO standards as engineering scaffolding — ISO/IEC/IEEE 29148:2018 (requirements), ISO/IEC 25010:2023 (quality model), ISO/IEC 25023:2016 (measurement), and ISO/IEC/IEEE 29119 family (testing). Compliance status is **Partial** with 63 tracked remediation actions. The audit is currently a **self-assessment**; independent external review is committed for v1.0.1. See [docs/iso-compliance.md](docs/iso-compliance.md) for the full audit, gap list, and per-standard status.

## Features

- **On-write sync** -- detects file changes via Windows `ReadDirectoryChangesW` (no polling)
- **Selective filtering** -- per-directory `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient** -- MD5 checksum comparison, deduplicating fair queue, rate limiting
- **Single binary** -- `smirror.exe`, no runtime dependencies beyond rclone
- **Backend-agnostic** -- rclone handles all cloud/remote storage
- **Single-instance** -- file-based lock prevents duplicate watchers
- **Quiescence** -- files must be stable before sync (handles Office saves, long writes)
- **Delete policy** -- configurable delete/ignore/quarantine for local deletions (default: delete)
- **Fair scheduling** -- hot files cycle to the back of the queue; no single file can starve other mirrors

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
| `smirror sync-now [mirror]` | Immediate full sync + ghost cleanup |
| `smirror dry-run [mirror]` | Show what would sync + ghost cleanup preview |
| `smirror status [mirror]` | Show sync status, metrics, instance state |
| `smirror test-mirrors [mirror]` | Run diagnostics and verify sync state (aliases: `doctor`, `verify`) |
| `smirror list-filters [mirror]` | Show effective filter rules |
| `smirror explain <mirror> <path>` | Explain include/exclude status, matched rule, sync state |
| `smirror project-stats [mirror]` | Show file counts and line counts per mirror (alias: `stats`) |
| `smirror report-bug [flags]` | Generate diagnostic report (`--stdout`, `--open`) |
| `smirror remote [remote_path]` | Show or set the default rclone remote for new mirrors |
| `smirror addmirror <path...> [flags]` | Add directories as mirrors (`-dest`, `--delete`, `--initial-sync`; aliases: `add-mirror`, `add`) |
| `smirror unmirror <name\|path> [flags]` | Remove mirror from config, clean state DB (`--purge-remote`, `--yes`; aliases: `removemirror`, `remove-mirror`, `remove`) |
| `smirror clean [--yes]` | Stop service, uninstall, and remove all user data |
| `smirror selfupdate [flags]` | Check for and install updates (`--check`, `--whatsnew`, `--yes`, `--include-rclone`) |
| `smirror service <action...>` | Windows Service: install [start], stop, uninstall [--clean] [--yes] |
| `smirror version` | Show version |

## Configuration

Default config location: `%USERPROFILE%\.selectivemirror\config.yaml`

Override with: `smirror --config path\to\config.yaml <command>`

See [`config.example.yaml`](config.example.yaml) for a full annotated example.

### Key settings

- **mirrors** -- list of watched directories with rclone remote destinations
- **global_excludes** -- patterns applied to all mirrors (`.gitignore` syntax)
- **delete_policy** -- `ignore`, `delete` (default), or `quarantine`
- **alert_webhook_url** -- HTTP endpoint for incident-based anomaly alerts (empty = disabled)
- **Per-directory `.syncignore`** -- place in the directory root for per-mirror filtering

### Delete policy

Controls what happens on the remote when a file is deleted locally.

| Policy | Batch sync verb | On delete event | Use case |
|--------|----------------|-----------------|----------|
| `delete` (default) | `rclone sync` | `rclone deletefile` | Mirror deletions to remote |
| `ignore` | `rclone copy` | no action | Preserve remote as archive |
| `quarantine` | `rclone copy` | `rclone moveto .quarantine/` | Soft-delete with recovery window |

Per-mirror `delete_policy` overrides the global setting. If neither is set, the default is `delete`.

### Diagnostics report

`smirror report-bug --stdout` generates a diagnostic bundle for bug filing. It includes: version, platform, rclone info, config structure (mirror names, policy, workers), state DB summary, and last 30 log lines. All paths are sanitized (home directory replaced with `~`). Remote paths are fully redacted. Review the output before submitting.

## Roadmap

- [x] **Phase 1** -- Core mirror: config, filters, watcher, sync, state, CLI
- [x] **Phase 1.5** -- Hardening: lock, quiescence, metrics, doctor/verify, delete policy
- [x] **Phase 2** -- Windows service: native SCM integration via `golang.org/x/sys/windows/svc`
- [ ] **Phase 3** -- USN journal recovery: fast restart reconciliation
- [x] **Phase 4** -- OSS polish: CI, issue templates, documentation, winget manifest
- [ ] **Phase 5** -- Telemetry: opt-in analytics, update check

## License

MIT -- see [LICENSE](LICENSE) for details.
