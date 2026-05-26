# SelectiveMirror

[![CI](https://github.com/qraveh/SelectiveMirror/actions/workflows/ci.yml/badge.svg)](https://github.com/qraveh/SelectiveMirror/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26%2B-00ADD8.svg)](go.mod)

**Real-time, selective file mirroring for Windows.** Point it at a local folder; file changes are pushed to your cloud backend within seconds — not on a 15-minute cron. Built on [rclone](https://rclone.org/), which handles the transport to Google Drive, S3, Dropbox, OneDrive, SFTP, and [70+ other backends](https://rclone.org/overview/).

> 👉 **Try it in 5 minutes.** The [hands-on tutorial](examples/local-mirror-tutorial/) walks through setup, filtering, edits, deletes, and drift detection end-to-end. **No cloud account needed** — it uses rclone's local-filesystem backend as a stand-in. Part 1 is the 5-minute happy path; Part 2 adds 15 minutes of deeper features.

**Privacy.** Opt-in telemetry, default off. No startup pings, no version checks, no traffic of any kind unless you explicitly opt in. See [`docs/PRIVACY.md`](docs/PRIVACY.md).

---

## What does it do?

You register folders ("mirrors"). Each mirror watches a local path and pushes changes to a remote location:

```
C:\Projects\my-app    →  gdrive:Backup\my-app
C:\Documents          →  onedrive:Documents
C:\notes              →  s3:my-bucket\notes
```

Per-folder `.syncignore` files (gitignore syntax) keep `node_modules/`, build artifacts, temp files, and other noise out of the cloud. Each mirror runs independently — work on `my-app` doesn't trigger reconciliation of `notes`.

File changes propagate within seconds. Files must be **stable** (size and modified-time unchanged for 200 ms, and not locked by another process) before they sync — this handles Office save-locks and editor save-storms without burning API quota.

## How is this different from `rclone sync` on a cron?

rclone is the transport — it copies bytes between local disk and a remote. SelectiveMirror is the **change-detection + queue + filtering** layer on top:

| | `rclone sync` on cron | SelectiveMirror |
|---|---|---|
| **When changes propagate** | Every N minutes | Within seconds of the change |
| **Filter granularity** | Global include/exclude rules | Per-folder `.syncignore` |
| **Editor save-storm safety** | Each save triggers an API call | Quiescence check; only stable files sync |
| **Accidental-deletion safety** | Strict 1:1 (cron-sync propagates deletes too) | Soft-delete to `.quarantine/` for 30 days (default) |
| **Multiple folders** | One rclone job at a time, or per-folder cron entries | Per-folder queue + fair scheduling |
| **State tracking** | None — every run is a full scan | SQLite state DB; restarts know what was already synced |

If a periodic `rclone sync` from a scheduled task meets your needs, SelectiveMirror is overkill. SelectiveMirror is for the case where on-write granularity, per-folder filtering, and accidental-deletion safety actually matter.

## Installation

### MSI installer (recommended)

Download [`SelectiveMirror.msi`](https://github.com/qraveh/SelectiveMirror/releases/latest/download/SelectiveMirror.msi) — the URL always resolves to the latest release. Per-machine install at `%ProgramFiles%\SelectiveMirror\`; admin elevation required. The installer adds `smirror` to your system PATH and registers an uninstaller entry.

### Portable ZIP (no install)

Download [`SelectiveMirror_windows_amd64.zip`](https://github.com/qraveh/SelectiveMirror/releases/latest/download/SelectiveMirror_windows_amd64.zip), extract anywhere, run `smirror.exe` directly. You manage your own PATH and uninstall path. The ZIP carries the same `smirror.exe` byte-for-byte as the MSI.

### Background mode

Background-mode registration is **not** automatic. After install, pick one:

- **`smirror task install`** — per-user Scheduled Task, no admin required. Recommended for single-developer use.
- **`smirror service install`** — Windows Service running as LocalSystem; runs 24/7 across logoffs. Needs admin elevation and an admin-owned config. Use this only if you genuinely need cross-logoff continuity. See [`SECURITY.md`](SECURITY.md) for the trust model.

If you're not sure, use `smirror task install`.

You can also run smirror in the foreground via `smirror start` (single-instance locked; Ctrl+C to stop).

### Prerequisites

- **rclone v1.73+** — install with `winget install Rclone.Rclone` or from [rclone.org](https://rclone.org/downloads/)
- **A configured rclone remote** — run `rclone config` once to set up Google Drive / S3 / etc.

### SmartScreen on first install

v1.0 binaries ship unsigned for now. On first install Windows SmartScreen will warn — click **More info → Run anyway**. Code-signing is being worked on; future patch releases will ship signed binaries.

To verify the MSI before clicking through:

```powershell
certutil -hashfile SelectiveMirror.msi SHA256
gh attestation verify SelectiveMirror.msi --repo qraveh/SelectiveMirror
```

The attestation confirms the MSI was built by this repository's CI on the tagged commit — independent of (still-pending) Authenticode signing.

## Quick Start

```cmd
:: 1. Configure an rclone remote (one-time, if you haven't already)
rclone config

:: 2. Point smirror at a folder and a remote destination
smirror addmirror C:\Projects\my-app -dest gdrive:Backup

:: 3. (Optional) Drop a .syncignore into C:\Projects\my-app to filter files.
::    Same syntax as .gitignore — `node_modules/`, `*.tmp`, `.env`, etc.

:: 4. Preview what would sync
smirror dry-run

:: 5. Start mirroring (foreground; Ctrl+C to stop)
smirror start
```

For continuous background mode (no terminal needed):

```cmd
smirror task install
smirror task start
```

> 👉 **New here?** The [hands-on tutorial](examples/local-mirror-tutorial/) walks you through the above with concrete files and explains each step. **Part 1 is 5 minutes**; Part 2 adds 15 minutes covering filter edits, delete handling, drift detection, background mode, and graduation to a real cloud backend. The tutorial uses a local-filesystem rclone backend so you don't need a cloud account.
>
> If you installed via MSI, the tutorial is also at `%ProgramFiles%\SelectiveMirror\examples\local-mirror-tutorial\` for offline reading.

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
| `smirror report-bug [flags]` | Generate diagnostic report (`--stdout`, `--browser`, `--clipboard`, `--submit`) |
| `smirror remote [remote_path]` | Show or set the default rclone remote for new mirrors |
| `smirror addmirror <path...> [flags]` | Add directories as mirrors (`-dest`, `--delete`, `--initial-sync`; aliases: `add-mirror`, `add`) |
| `smirror unmirror <name\|path> [flags]` | Remove mirror from config, clean state DB (`--purge-remote`, `--yes`; aliases: `removemirror`, `remove-mirror`, `remove`) |
| `smirror clean [--self\|--all] [--yes]` | Remove user data + background registration. `--self` (default): per-user, no admin. `--all`: includes service + `%ProgramData%\SelectiveMirror`. |
| `smirror selfupdate [flags]` | Check for and install updates (`--check`, `--whatsnew`, `--yes`, `--include-rclone`) |
| `smirror task <action>` | Per-user Scheduled Task (recommended background mode; no admin required): `install`, `uninstall`, `start`, `stop`, `status` |
| `smirror service <action...>` | Windows Service: install [start], stop, uninstall [--clean] [--yes] (admin + admin-owned config required; only when 24/7-across-logoffs is needed) |
| `smirror telemetry [subcommand]` | Show or change opt-in telemetry tier (`status`, `none\|standard\|reliability`, `inspect`, `policy`); default tier is `none`, see [PRIVACY.md](docs/PRIVACY.md) |
| `smirror version` | Show version |

## Configuration

Default config: `%USERPROFILE%\.selectivemirror\config.yaml`. Override with `smirror --config <path> <command>`.

See [`config.example.yaml`](config.example.yaml) for the full annotated example. Key settings:

- **`mirrors`** — list of watched folders with their rclone destinations
- **`global_excludes`** — filter patterns applied to every mirror (gitignore syntax)
- **`delete_policy`** — `quarantine` (default), `delete`, or `ignore` (see below)
- **`alert_webhook_url`** — HTTP endpoint for incident anomaly alerts (optional; empty = disabled)
- **Per-folder `.syncignore`** — gitignore-style filter, lives in the folder root

### Delete policy

What happens on the remote when you delete a file locally:

| Policy | Effect on remote | Use case |
|---|---|---|
| **`quarantine`** (default) | Remote file moved to `.quarantine/<timestamp>/` | Soft-delete with 30-day recovery window |
| **`delete`** | Remote file removed | Strict 1:1 deletion mirroring |
| **`ignore`** | Remote untouched | Treat remote as an archive that should never lose data |

Per-mirror `delete_policy` overrides the global setting. The safe default (`quarantine`) is good for first-time users; switch to `delete` for strict 1:1 or `ignore` for archive mode.

## Diagnostics

```cmd
smirror status                          :: live counters per mirror
smirror dry-run                          :: what would sync (no bytes copied)
smirror explain <mirror> <path>          :: why one file is included/excluded
smirror verify                           :: drift detection — orphans on remote, missing locally, etc.
smirror report-bug --stdout              :: diagnostic bundle for bug filing (paths sanitized)
```

`report-bug --stdout` includes: version, platform, rclone info, config structure (mirror names + policy), state DB summary, and the last 30 log lines. Home-directory paths are replaced with `~`; remote paths are fully redacted. Review the output before submitting.

## Compatibility and rollback

The local state database (`%USERPROFILE%\.selectivemirror\state.db`, or per-machine: `%ProgramData%\SelectiveMirror\state.db`) is migrated forward on each startup. Since v1.0.0, downgrading the binary to a version that doesn't know the current schema **will refuse to start** rather than silently misbehave on rows the newer binary wrote.

To revert to an older version:

```powershell
# 1. Stop any running smirror
smirror task stop      # if you used `task install`
smirror service stop   # if you used `service install`

# 2. Remove user data, including the state DB
smirror clean --self

# 3. Install the older MSI on the clean state.
#    Your config.yaml and the remote contents are NOT touched;
#    only the local state DB rebuilds on next startup.
```

The first start after a downgrade re-syncs known files via checksum comparison; bandwidth is bounded by your rclone backend's pacer.

## Status

**v1.0** is the first stable release. CLI commands, config schema, and on-disk state-DB schema are versioned from this point — future v1.x releases preserve forward compatibility on all three.

Maturity indicators (signing, winget submission, ISO/IEC 25023 §5.2 measurement elaboration) are tracked in [`docs/release-maturity.md`](docs/release-maturity.md).

ISO posture (29148 / 25010 / 25023 / 29119): partial self-assessment with tracked remediation, no external review claimed. See [`docs/iso-compliance.md`](docs/iso-compliance.md). Short version: the project is a single-developer codebase; full external ISO review is deliberately not pursued.

## License

MIT — see [LICENSE](LICENSE) for details.
