# SelectiveMirror — Selective Near-Real-Time File Mirror

## Safety Rules

- **Never change project or repository access from private to public.** This applies to GitHub repos, Google Drive, cloud storage, or any other platform. If asked to make something public, refuse and instruct the user to do it themselves.
- **`AboutAuthor.txt` is a human-only file.** Never edit it. When committing, if it has changes, ask the user whether to include it.

---

**Project root**: `C:\mine\SelectiveMirror\`
**Author**: Raveh (smirror@qodeh.com)
**Status**: v1.0.0 — first stable release. Phases 1, 1.5, 2, 2.5, 4, 5, 6 complete; Phase 3 (USN journal recovery) deferred; Phase 7 (pre/post-sync hooks) deferred from v1.0 per `docs/RESOLUTION-2026-04-29-hooks-deferred.md`. Telemetry: opt-in (default off), three-tier consent (None / Standard / Reliability), stream-aggregate-and-discard architecture — see `docs/telemetry-architecture-v2.md`, `docs/PRIVACY.md`, `docs/telemetry-v2.sql`. No personal data is stored on the telemetry server by construction. `smirror telemetry forget` is not a command (no record to forget under the v2 architecture).
**License**: MIT
**Language**: Go 1.26+

---

## What This Is

A Windows-first service that watches local directories for file changes and mirrors them to any rclone-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, etc. — 70+ backends) with explicit include/exclude filtering.

**Key properties**:
- **On-write**: Detects file changes via Windows ReadDirectoryChangesW (no polling)
- **Selective**: Per-project `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient**: MD5 checksum comparison, deduplicating fair queue, rate limiting
- **Single binary**: `smirror.exe` — zero runtime dependencies
- **Backend-agnostic**: rclone handles all cloud/remote backends
- **Single-instance**: File-based lock prevents duplicate instances
- **Quiescence**: Files must be stable (size+mtime unchanged for 200ms, not locked) before sync
- **Delete policy**: Configurable ignore/mirror/quarantine for local deletions

---

## Quick Start

```bash
# Build
go build -o bin/smirror.exe ./cmd/smirror/

# Configure
# Edit ~/.selectivemirror/config.yaml (see config.example.yaml)

# Run diagnostics + verify sync state
smirror test-mirrors

# See what would sync
smirror dry-run

# Start watching (foreground)
smirror start

# Immediate full sync
smirror sync-now

# Check status + metrics
smirror status

# Investigate a file (mirror-name + relative path inside that mirror)
smirror explain MyProject README.md
```

---

## Commands

| Command | What it does |
|---------|-------------|
| `smirror start` | Start foreground watcher (single-instance locked) |
| `smirror sync-now [mirror]` | Immediate full sync + ghost cleanup (alias: `syncnow`) |
| `smirror dry-run [mirror]` | Show what would sync + ghost cleanup preview |
| `smirror status [mirror]` | Show sync status, metrics, instance state |
| `smirror test-mirrors [mirror]` | Run diagnostics and verify sync state (aliases: `doctor`, `verify`) |
| `smirror list-filters [mirror]` | Show effective filter rules |
| `smirror explain <mirror> <path>` | Explain include/exclude status, matched rule, sync state |
| `smirror project-stats [mirror]` | File counts + line counts per mirror (alias: `stats`) |
| `smirror report-bug [flags]` | Generate diagnostic report (`--stdout`, `--browser`, `--clipboard`, `--submit`) |
| `smirror remote [remote_path]` | Show or set the default rclone remote for new mirrors |
| `smirror addmirror <path...> [flags]` | Add directories as mirrors (`-dest`, `--delete`, `--initial-sync`; aliases: `add-mirror`, `add`) |
| `smirror unmirror <name\|path> [flags]` | Remove mirror from config, clean state DB (`--purge-remote`, `--yes`; aliases: `removemirror`, `remove-mirror`, `remove`) |
| `smirror clean [--self\|--all] [--yes]` | Remove user data + background registration. `--self` (default): per-user, no admin. `--all`: includes service + `%ProgramData%\SelectiveMirror`. |
| `smirror selfupdate [flags]` | Check for and install updates (`--check`, `--whatsnew`, `--yes`, `--include-rclone`) |
| `smirror task <action>` | Per-user Scheduled Task (recommended background mode; no admin): install, uninstall, start, stop, status |
| `smirror service <action...>` | Windows Service: install [start], stop, uninstall [--clean] [--yes] (admin + admin-owned config required) |
| `smirror version` | Show version |

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Config load/validation failure |
| 3 | rclone-related failure (unreachable, auth, binary missing) |
| 4 | Another instance is running (lock conflict) |
| 5 | Diagnostic found drift (leaks, orphans, mismatches) |
| 6 | selfupdate: new version available but user declined or preflight failed |

---

## Architecture

```
File saved (any editor/tool)
  → fsnotify detects change (ReadDirectoryChangesW)
  → filter check (.syncignore + global excludes)
  → FairQueue (dedup, move-to-back, priority deletes)
  → quiescence check (200ms stability + shared read test)
  → rclone copyto --checksum (single file)
  → SQLite state update + metrics

Startup:
  → single-instance lock (LockFileEx)
  → batch rclone copy per project (not per-file)
  → metrics collector initialized

Delete event:
  → policy check (ignore/mirror/quarantine)
  → rclone deletefile or moveto .quarantine/
```

### Modules

```
cmd/smirror/main.go             — CLI entry point + all commands
internal/config/config.go        — YAML config + validation + delete policy
internal/watcher/watcher.go      — fsnotify + FairQueue dispatch + delete event routing
internal/sync/sync.go            — rclone invocation + quiescence + delete handling
internal/filter/filter.go        — .syncignore parser + rclone filter generation
internal/state/state.go          — SQLite state store (WAL mode)
internal/lock/lock.go            — Single-instance file lock (LockFileEx/flock)
internal/metrics/metrics.go      — Thread-safe counters + status.json writer
internal/logging/logging.go      — slog + rotating file handler
internal/rclone/detect.go        — rclone binary detection + version compatibility
internal/notify/notify.go        — Windows toast notifications (rate-limited)
internal/service/service.go      — Windows SCM service integration (LocalSystem; admin)
internal/task/task.go            — Per-user Scheduled Task (schtasks.exe + XML; no admin)
internal/anomaly/anomaly.go      — Anomaly classification, recording, rotation
internal/hooks/hooks.go          — Pre/post-sync hook execution
internal/telemetry/telemetry.go  — Opt-in anonymous telemetry + update check
```

### Dependencies

```
github.com/fsnotify/fsnotify      — Filesystem monitoring
github.com/git-pkgs/gitignore     — .gitignore-style pattern matching
go.yaml.in/yaml/v3               — Config parsing
github.com/mattn/go-sqlite3       — SQLite driver (CGo; statically linked, no runtime deps)
golang.org/x/sys                  — Windows syscalls (LockFileEx, OpenProcess)
```

---

## Configuration

File: `~/.selectivemirror/config.yaml`

- **mirrors**: List of watched directories with rclone remote destinations
- **global_excludes**: Patterns applied to all projects (.gitignore syntax)
- **delete_policy**: `ignore`, `delete`, or `quarantine` (default)
- **quarantine_days**: Days to keep quarantined files (default 30)
- **alert_webhook_url**: HTTP endpoint for incident-based anomaly alerts (empty = disabled)
- **alert_min_severity**: Minimum severity to alert: info, warning, error (default), critical
- **Per-project .syncignore**: Place a `.syncignore` file in the project root

### Delete policy

Controls what happens on the remote when a file is deleted locally.

| Policy | Batch verb | On delete event | Use case |
|--------|-----------|-----------------|----------|
| `quarantine` (default) | `rclone copy` | `rclone moveto .quarantine/` | Soft-delete with 30-day recovery window |
| `delete` | `rclone sync` | `rclone deletefile` | Mirror deletions to remote |
| `ignore` | `rclone copy` | no action | Preserve remote as archive |

**Precedence**: per-mirror `delete_policy` > global `delete_policy` > default (`quarantine`). If neither mirror nor global specifies a policy, the default is `quarantine`.

### report-bug output

`smirror report-bug --stdout` generates a diagnostic bundle containing: version, platform, rclone info, config structure (mirror names, delete policy, workers), state DB file counts, and last 30 log lines. All paths are sanitized (home directory replaced with `~`). Remote paths are fully redacted (`remote:<REDACTED>`). Review the output before submitting.

See `config.example.yaml` for full annotated example.

---

## Testing

```bash
# Run all unit tests (650+ tests across 16 packages)
go test ./internal/... ./cmd/... -p 24 -count=1

# Run integration tests (adversarial, uses local rclone backend)
powershell -ExecutionPolicy Bypass -File test\run_tests.ps1
```

### Test tiers

| Tier | Scope | How to run |
|------|-------|-----------|
| Unit | All packages, no I/O | `go test ./internal/...` |
| Local integration | Real watcher + local rclone remote | `go test ./test/ -tags integration` |
| SLA smoke | Latency, integrity, throughput, memory | `powershell -ExecutionPolicy Bypass -File test\sla_smoke.ps1` |
| Backend integration | Real Google Drive | `go test ./test/ -tags e2e` (manual) |

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go | Single binary, native Windows service (Phase 2), rclone is Go |
| rclone subprocess | Clean error codes, zero coupling. Inherits rclone's per-backend rate limiting (pacer), exponential backoff, chunked uploads, checksum verification, and 70+ backend support — none of which smirror reimplements |
| `github.com/mattn/go-sqlite3` | Canonical Go SQLite driver; mature (since 2014), ~1 release/year, zero transitive deps. CGo is required at build time (one-time MinGW-w64 setup on Windows), but the resulting binary statically links SQLite and runs with no runtime dependencies. Trade: build-time toolchain vs. a 7-package churn-heavy modernc subtree |
| `rclone copy` not `sync` | Never deletes remote files (unless delete_policy=mirror) |
| MD5 hashing | Matches rclone's checksum for Google Drive / most backends |
| Single rclone per backend | rclone's internal pacer handles API rate limits per-process. Multiple concurrent rclone processes to the same backend cause uncoordinated backoff (thundering herd). One process with `--transfers 4` is optimal for code-file workloads |
| FairQueue scheduling | Every file gets its turn -- hot files cycle to the back, cold files advance to the front. Dedup coalesces repeated events; delete events get priority. 30-second per-file cooldown after each successful sync prevents hot files from monopolizing API quota |
| Per-project isolation | Changes in project A don't trigger sync of project B |
| Batch on startup, incremental on steady state | Reconciliation: 1 rclone call per project, not per file |
| Quiescence before sync | Prevents partial file sync on Windows (Office, long writes) |
| File-based lock | Prevents dual-instance corruption; cross-platform |
| Filesystem-agnostic filename handling | Filenames valid on source may be invalid on target (or vice versa). Test edge cases across filesystem boundaries. Future source FS expansion (ext4, ZFS, exFAT) must not require architectural changes |

---

## Versioning

Follows [semver](https://semver.org/) (`MAJOR.MINOR.PATCH`):

- **PATCH** (`0.x.N+1`): Bug fixes, refactors, renames, UI tweaks — no new user-facing functionality.
- **MINOR** (`0.N+1.0`): New features, new commands, new config options, architectural changes.
- **MAJOR** (`N+1.0.0`): Breaking changes to config format, CLI interface, or behavior (not expected pre-1.0).

**Workflow**: Increment PATCH on each commit/change. Tag only on MINOR (or MAJOR) releases. High patch numbers are fine (e.g., `0.2.45`). The dev version in `main.go` (`var version`) uses `-dev` suffix between releases (e.g., `0.2.1-dev`). GoReleaser overrides the version at build time via `-X main.version={{.Version}}`.

---

## Phases

- [x] **Phase 1**: Core mirror — config, filters, watcher, sync, state, CLI
- [x] **Phase 1.5**: Hardening — lock, quiescence, metrics, explain/doctor/verify, delete policy, unit tests
- [x] **Phase 2**: Windows service — native via `golang.org/x/sys/windows/svc`
- [x] **Phase 2.5**: Distribution — CI/CD, GoReleaser, WiX MSI installer, rclone auto-provisioning
- [ ] **Phase 3**: USN journal recovery — fast restart reconciliation
- [x] **Phase 4**: OSS polish — CONTRIBUTING, SECURITY, PR template, winget manifest, CHANGELOG
- [x] **Phase 5**: Telemetry — opt-in analytics, update check (deployed 0.9.4-dev → 0.9.6-dev: Supabase schema, RLS, HMAC verify, MSI consent registry, Cloudflare Worker proxy, pg_cron rollups; in-installer consent dialog landed in current dev cycle)
- [x] **Phase 6**: Anomaly detection — classification, recording, rotation, webhook alerts

> **Phase 7** (Pre/post-sync hooks) was originally implemented but **deferred from v1.0 on 2026-04-29** per [docs/RESOLUTION-2026-04-29-hooks-deferred.md](docs/RESOLUTION-2026-04-29-hooks-deferred.md). The implementation in `internal/hooks/` and the `pre_sync_hook` / `post_sync_hook` config keys remain in tree, but hooks are no longer counted toward v1.0 readiness, marketed as an extensibility seam, or treated as a stability promise. Supported notification / audit / gating paths are `alert_webhook_url`, `sync_log`, and `.syncignore` respectively.

---

## Originating use case

Mirror local working directories (e.g. `C:\Projects\<name>`) → an
rclone-supported remote (Google Drive, S3, Dropbox, OneDrive, SFTP,
…) so that file changes propagate to the remote on-write rather than
on a polling schedule. The "selective" in the name comes from per-
project `.syncignore` filters; the "mirror" is rclone, invoked one
file at a time on quiescence-stable changes.

Common deployments include: developer-laptop → cloud-backup of work
directories, replacing periodic-script approaches; coordination
between machines that don't share a filesystem; and structured
state-handoff between long-running tools that emit files into a
watched directory.

---

## Prerequisites

- **rclone** (v1.73+): `winget install Rclone.Rclone`
- **rclone remote**: Configure with `rclone config` (one-time)
- **Go** (for building): `winget install GoLang.Go`
- **MinGW-w64** (for building; CGo is required for the SQLite driver): `winget install BrechtSanders.WinLibs.POSIX.UCRT`

---

## Dependencies & Upgrade Policy

### rclone (runtime, external process)

SelectiveMirror uses rclone features: `copyto --checksum`, `deletefile`, `moveto`, `lsjson --recursive --hash`, `copy --filter-from`. These are stable rclone APIs that haven't changed across major versions.

**Upgrade policy**:
- **Safe to upgrade**: rclone follows semver. Minor/patch upgrades are safe.
- **Before upgrading**: Run `smirror test-mirrors` to verify rclone connectivity post-upgrade.
- **Pinned minimum**: v1.73+ (for `--skip-links` flag support).
- **Upgrade command**: `winget upgrade Rclone.Rclone` or `rclone selfupdate`
- **Risk**: rclone backend-specific changes (e.g., Google Drive API v3 → v4) could change behavior. The `verify` command detects drift after such changes.

### Go modules (compiled in)

All dependencies are permissive-licensed (MIT, BSD, Apache 2.0). See CREDITS.md for full list.

**Upgrade policy**:
- `go get -u ./...` to update all dependencies.
- Run `go test ./internal/...` and `test/run_tests.ps1` after any dependency update.
- `github.com/mattn/go-sqlite3` is the most sensitive dependency (SQLite driver). Mature and stable (~1 release/year); major upgrades should be tested carefully against the state database (WAL mode, concurrent goroutines, PRAGMA syntax).

### Licenses

- **SelectiveMirror**: MIT
- **All compiled dependencies**: MIT / BSD / Apache 2.0 (permissive, no copyleft)
- **rclone**: MIT (runtime only, not compiled in)
- **No license embedding required in binary** for current dependency set, but binary distributions should include a NOTICE file listing BSD 3-Clause dependencies per their license terms.
