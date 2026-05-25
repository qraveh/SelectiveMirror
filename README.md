# SelectiveMirror

[![CI](https://github.com/qraveh/SelectiveMirror/actions/workflows/ci.yml/badge.svg)](https://github.com/qraveh/SelectiveMirror/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26%2B-00ADD8.svg)](go.mod)

Real-time selective file synchronization for Windows. Watches local directories for changes and mirrors them to any [rclone](https://rclone.org/)-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, and 70+ others).

**Privacy: opt-in telemetry, default off.** No startup pings, no version checks, no anonymous-counts traffic unless you explicitly opt in. See [docs/PRIVACY.md](docs/PRIVACY.md).

## Why not just rclone?

rclone is the **transport** — it copies bytes between local disk and a remote. SelectiveMirror is the **change-detection + queue + filtering** layer that decides *when* and *which* files rclone should touch. Concretely:

- **On-write**, not on-schedule. ReadDirectoryChangesW catches file changes as they happen; a periodic reconciliation pass (default 5 min) covers the rare cases where the OS event stream misses a change.
- **Selective.** Per-project `.syncignore` files (gitignore-style) keep `node_modules/`, `.git/`, build artifacts out of the cloud.
- **Bandwidth-aware.** Files must be quiescent (size+mtime stable, not locked) before sync; rapid-rewrite hot files don't burn API quota.
- **Per-project isolation.** A change in project A doesn't trigger reconciliation of project B.
- **Single binary.** No daemon framework, no service-mesh; `smirror.exe` + `rclone.exe` is the whole stack.

If a periodic `rclone sync` from a scheduled task meets your needs, that's a reasonable choice and SelectiveMirror is overkill. SelectiveMirror is for the case where on-write granularity, per-mirror filtering, and a state DB that survives editor save-storms actually matter.

## Features

- **On-write sync** — detects file changes via Windows `ReadDirectoryChangesW` (no per-file polling; a periodic reconciliation pass covers OS-event misses)
- **Selective filtering** — per-directory `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient** — MD5 checksum comparison, deduplicating fair queue, rate limiting
- **Single binary** — `smirror.exe`, no runtime dependencies beyond rclone
- **Backend-agnostic** — rclone handles all cloud/remote storage
- **Single-instance** — file-based lock prevents duplicate watchers
- **Quiescence** — files must be stable before sync (handles Office saves, long writes)
- **Delete policy** — configurable delete/ignore/quarantine for local deletions (default: quarantine — 30-day recovery window)
- **Fair scheduling** — hot files cycle to the back of the queue; no single file can starve other mirrors

## Installation

The MSI is the recommended path on Windows. The ZIP is for portable use; both are top-level assets on every release. The MSI is **not** bundled inside the ZIP — winget consumers and most users want the MSI as a direct URL.

### MSI installer (recommended)

Download [`SelectiveMirror.msi`](https://github.com/qraveh/SelectiveMirror/releases/latest/download/SelectiveMirror.msi) (always the latest release) or browse all versions on the [Releases page](https://github.com/qraveh/SelectiveMirror/releases). The installer adds `smirror` to the system PATH and registers an uninstaller entry. Per-machine install (`%ProgramFiles%\SelectiveMirror\`) — admin elevation required. Background registration is **not** automatic; pick the privilege model after install with `smirror task install` (per-user, no admin) or `smirror service install` (LocalSystem, admin + admin-owned config). **If you're not sure, use `smirror task install`** — per-user mode covers the typical single-developer case without elevation. See [SECURITY.md](SECURITY.md#scope) for the trust model.

**SmartScreen on first install**

v1.0.0 ships unsigned. Windows SmartScreen will warn on first run — click **More info → Run anyway**. SelectiveMirror is applying to [SignPath Foundation](https://signpath.org/apply) for free OSS code signing (the foundation's application gate is "released in the form to be signed", which v1.0.0 satisfies). Once the foundation cert is issued, a subsequent patch release will ship signed binaries.

**Publisher field expectations after signing.** When SignPath-signed releases ship, the **verified publisher** field in the SmartScreen prompt and certificate chain will read **"SignPath Foundation"** — that is the certificate issuer, not the project author. Foundation issues certificates in its own name on behalf of approved OSS projects; this is the normal pattern, not a sign of compromise. Author attribution stays under the **MSI's "Publisher"** field (Add/Remove Programs / Apps & features), which reads **"Raveh Neeman"**, and under **smirror.exe → Properties → Details → Copyright** once PE version-info embedding lands in a later release.

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

Download [`SelectiveMirror_windows_amd64.zip`](https://github.com/qraveh/SelectiveMirror/releases/latest/download/SelectiveMirror_windows_amd64.zip) for portable use — that URL always resolves to the latest release. Extract anywhere, run `smirror.exe` directly, manage your own PATH and uninstall path. The ZIP carries the same `smirror.exe` byte-for-byte as the MSI (CI builds once and feeds the binary into both artifacts). Run `smirror version` to see which release you have.

### Compatibility and rollback

The local state database (`~/.selectivemirror/state.db`, per-machine: `%ProgramData%\SelectiveMirror\state.db`) is migrated forward on each startup. Since v1.0.0, downgrading the binary to a version that does not know the current schema **will refuse to start** rather than silently misbehave. This protects against undefined behavior on rows the newer binary wrote.

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

- **rclone v1.73+** — install with `winget install Rclone.Rclone` or from [rclone.org](https://rclone.org/downloads/)
- **An rclone remote** — configure with `rclone config` (one-time setup)

## Quick Start

```cmd
:: 1. Configure an rclone remote (if you haven't already)
rclone config

:: 2. Create your config from the bundled template
mkdir %USERPROFILE%\.selectivemirror
copy "%ProgramFiles%\SelectiveMirror\config.example.yaml" "%USERPROFILE%\.selectivemirror\config.yaml"
:: Edit %USERPROFILE%\.selectivemirror\config.yaml with your mirrors and remote

:: 3. Run diagnostics and test mirror connectivity
smirror test-mirrors

:: 4. Preview what would sync
smirror dry-run

:: 5. Start mirroring
smirror start
```

### First time? Try the hands-on tutorial

A self-contained walkthrough lives at [`examples/local-mirror-tutorial/`](examples/local-mirror-tutorial/). It exercises the core commands (`dry-run`, `sync-now`, `explain`, `verify`, `task install`) end-to-end using rclone's local-filesystem backend as a stand-in for a real cloud remote — no cloud account, network access, or credentials needed. **Part 1 is ~5 minutes**; Part 2 covers diagnostics, deletes, drift, and background mode in another ~15.

The MSI installs the tutorial alongside the binary at `%ProgramFiles%\SelectiveMirror\examples\local-mirror-tutorial\` — open the README.md there with any text editor to read it offline. (Source-cloners can read it from `examples/local-mirror-tutorial/` in the repo.)

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

Default config location: `%USERPROFILE%\.selectivemirror\config.yaml`

Override with: `smirror --config path\to\config.yaml <command>`

See [`config.example.yaml`](config.example.yaml) for a full annotated example.

### Key settings

- **mirrors** — list of watched directories with rclone remote destinations
- **global_excludes** — patterns applied to all mirrors (`.gitignore` syntax)
- **delete_policy** — `ignore`, `delete`, or `quarantine` (default)
- **alert_webhook_url** — HTTP endpoint for incident-based anomaly alerts (empty = disabled)
- **Per-directory `.syncignore`** — place in the directory root for per-mirror filtering

### Delete policy

Controls what happens on the remote when a file is deleted locally.

| Policy | Batch sync verb | On delete event | Use case |
|--------|----------------|-----------------|----------|
| `quarantine` (default) | `rclone copy` | `rclone moveto .quarantine/` | Soft-delete with 30-day recovery window |
| `delete` | `rclone sync` | `rclone deletefile` | Mirror deletions to remote (strict 1:1) |
| `ignore` | `rclone copy` | no action | Preserve remote as archive |

Per-mirror `delete_policy` overrides the global setting. If neither is set, the default is `quarantine` (30-day recovery window) — the safe default for first-time users. Switch to `delete` if you want strict 1:1 deletion mirroring; switch to `ignore` if you treat the remote as an archive that should never lose data.

### Diagnostics report

`smirror report-bug --stdout` generates a diagnostic bundle for bug filing. It includes: version, platform, rclone info, config structure (mirror names, policy, workers), state DB summary, and last 30 log lines. All paths are sanitized (home directory replaced with `~`). Remote paths are fully redacted. Review the output before submitting.

## Roadmap

- [x] **Phase 1** — Core mirror: config, filters, watcher, sync, state, CLI
- [x] **Phase 1.5** — Hardening: lock, quiescence, metrics, doctor/verify, delete policy
- [x] **Phase 2** — Windows service: native SCM integration via `golang.org/x/sys/windows/svc`
- [x] **Phase 2.5** — Distribution: GoReleaser, WiX MSI installer, rclone auto-provisioning, smoke-test gate
- [ ] **Phase 3** — USN journal recovery (post-1.0): fast restart reconciliation; current restart cost is one full reconciliation pass per mirror, which is acceptable for the typical < 5k-file mirror but not for very large trees. No committed target version — picked up when a user reports a real-world reconciliation cost worth fixing.
- [x] **Phase 4** — OSS polish: CI, issue templates, documentation, winget manifest
- [x] **Phase 5** — Telemetry: opt-in analytics + update check (Supabase backend, Cloudflare Worker proxy, MSI consent UI; wired and deployed in v1.0.0)
- [x] **Phase 6** — Anomaly detection: classification, recording, rotation, webhook alerts
- [⏸] **Phase 7** — Hooks: pre/post-sync hook execution with environment variables. **Deferred from v1.0** per [docs/RESOLUTION-2026-04-29-hooks-deferred.md](docs/RESOLUTION-2026-04-29-hooks-deferred.md). The implementation remains in tree and the config keys (`pre_sync_hook` / `post_sync_hook`) are still accepted, but hooks are reclassified as **experimental** — not part of the v1.0 stability surface. Use `alert_webhook_url` for notifications, `sync_log` for audit, and `.syncignore` for gating; revisit hooks if a use case appears that those three don't cover.

---

## Status and maturity (v1.0)

v1.0.0 is the first stable release. CLI commands, config schema, and on-disk state-DB schema are versioned from this point forward; future v1.x releases preserve forward compatibility on all three. First installs on Windows trigger SmartScreen until the SignPath Foundation Authenticode certificate is provisioned — see [SECURITY.md § Code Signing](SECURITY.md#code-signing) and the SmartScreen workaround in the Installation section above. Maturity indicators (winget submission, signing, ISO/IEC 25023 §5.2 measurement elaboration) are tracked in [docs/release-maturity.md](docs/release-maturity.md).

ISO posture and compliance details (29148, 25010, 25023, 29119): see [docs/iso-compliance.md](docs/iso-compliance.md). Short version: partial self-assessment with tracked remediation; no external review claimed.

## License

MIT — see [LICENSE](LICENSE) for details.
