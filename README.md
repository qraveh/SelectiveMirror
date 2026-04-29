# SelectiveMirror

Real-time selective file synchronization for Windows. Watches local directories for changes and mirrors them to any [rclone](https://rclone.org/)-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

> **Audience and maturity (v1.0)**: v1.0.0 is the first stable release. The maintainer + small-group-of-testers audience the v0.9.x line shipped under remains the recommended audience for the **first 30 days post-tag** while telemetry signature data accumulates and the SignPath Authenticode certificate (in flight, applies to first-install SmartScreen friction) lands. Public-launch indicators — winget submission to `microsoft/winget-pkgs`, full ISO/IEC 25023 §5.2 measurement-function elaboration (deferred to v1.0.1), and one final round of panel review — are tracked in [docs/release-maturity.md](docs/release-maturity.md). If you are evaluating the project for the first time, expect SmartScreen friction on first install (see below) and read [CHANGELOG.md](CHANGELOG.md) `[1.0.0]` for the closure record and known issues at tag.

> **ISO compliance status (v1.0)**: SelectiveMirror applies four ISO standards as engineering scaffolding — ISO/IEC/IEEE 29148:2018 (requirements), ISO/IEC 25010:2023 (quality model), ISO/IEC 25023:2016 (measurement), and ISO/IEC/IEEE 29119 family (testing). Compliance status is **Partial** with tracked remediation actions per `docs/iso-compliance.md` §9. The audit is a permanent **self-assessment retained as deliberate Non-Conformity by Choice** on ISO/IEC/IEEE 29148:2018 §5.2.4 (peer review of requirements) and §6.5 (stakeholder validation) per A-GOV-01 (decided 2026-04-29); independent external review is **not** planned and **not** claimed. The project does not pretend to compliance on those clauses. See [docs/iso-compliance.md](docs/iso-compliance.md) for the full audit, gap list, per-standard status, and the §10.6 SM-NNN traceability + collision-acknowledgment block.

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

The MSI is the recommended path on Windows. The ZIP is for portable use; both are top-level assets on every release. The MSI is **not** bundled inside the ZIP — winget consumers and most users want the MSI as a direct URL.

### MSI installer (recommended)

Download `SelectiveMirror.msi` from [Releases](https://github.com/qraveh/SelectiveMirror/releases). The installer adds `smirror` to the system PATH and registers an uninstaller entry. perMachine install (`%ProgramFiles%\SelectiveMirror\`) — admin elevation required. Background registration is **not** automatic; pick the privilege model after install with `smirror task install` (per-user, no admin) or `smirror service install` (LocalSystem, admin + admin-owned config). See [SECURITY.md](SECURITY.md#scope) for the trust model.

**SmartScreen on first install (v0.9.x — pre-SignPath)**

Until [SignPath Foundation](https://signpath.io/) issues an EV certificate for the project (in flight; tracked in [SECURITY.md § Code Signing](SECURITY.md#code-signing)), the MSI ships unsigned. Microsoft Defender SmartScreen will display **"Microsoft Defender SmartScreen prevented an unrecognized app from starting"** on first launch. Click **More info → Run anyway**.

To verify the binary is the one published from CI before clicking through:

```powershell
# Compare the published MSI's SHA-256 against the entry in checksums.txt
# from the same release.
certutil -hashfile SelectiveMirror.msi SHA256
```

Each release also carries a [GitHub build-provenance attestation](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds), verifiable with the GitHub CLI:

```bash
gh attestation verify SelectiveMirror.msi --repo qraveh/SelectiveMirror
```

This confirms the MSI was built by this repository's CI on the tagged commit — independent of the (still-pending) Authenticode signature.

### Portable ZIP (no install)

Download `SelectiveMirror_<version>_windows_amd64.zip` from [Releases](https://github.com/qraveh/SelectiveMirror/releases) for portable use. Extract anywhere, run `smirror.exe` directly, manage your own PATH and uninstall path. The ZIP carries the same `smirror.exe` byte-for-byte as the MSI (CI builds once and feeds the binary into both artifacts).

### Compatibility and rollback

The local state database (`~/.selectivemirror/state.db`, perMachine: `%ProgramData%\SelectiveMirror\state.db`) is migrated forward on each startup. As of v0.9.20-dev, downgrading the binary to a version that does not know the current schema **will refuse to start** rather than silently misbehave (GAP-7). This protects against undefined-behavior on rows the newer binary wrote.

If you need to revert to an older version:

```powershell
# 1. Stop any running smirror (foreground / task / service)
smirror task stop      # if you used `task install`
smirror service stop   # if you used `service install`

# 2. Remove user data, including the state DB
smirror clean --self

# 3. Install the older MSI on a clean state. Your config.yaml and the
#    remote contents are NOT touched; only the local state DB is
#    rebuilt on next startup.
```

The first start after a downgrade re-syncs known files via checksum comparison, which is bandwidth-bounded by your rclone backend's pacer.

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
- [x] **Phase 2.5** -- Distribution: GoReleaser, WiX MSI installer, rclone auto-provisioning, smoke-test gate
- [ ] **Phase 3** -- USN journal recovery: fast restart reconciliation
- [x] **Phase 4** -- OSS polish: CI, issue templates, documentation, winget manifest
- [x] **Phase 5** -- Telemetry: opt-in analytics + update check (Supabase backend, Cloudflare Worker proxy, MSI consent UI; live since 0.9.4-dev)
- [x] **Phase 6** -- Anomaly detection: classification, recording, rotation, webhook alerts
- [x] **Phase 7** -- Hooks: pre/post-sync hook execution with environment variables

## License

MIT -- see [LICENSE](LICENSE) for details.
