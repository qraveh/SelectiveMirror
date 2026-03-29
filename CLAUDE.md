# SelectiveMirror — Selective Near-Real-Time File Mirror

**Project root**: `C:\SelectiveMirror\`
**Author**: Raveh (raveh@qodeh.com)
**Status**: Phase 1.5 (core mirror + hardening)
**License**: MIT
**Language**: Go 1.26+

---

## What This Is

A Windows-first service that watches local directories for file changes and mirrors them to any rclone-supported backend (Google Drive, S3, Dropbox, OneDrive, SFTP, etc. — 70+ backends) with explicit include/exclude filtering.

**Key properties**:
- **On-write**: Detects file changes via Windows ReadDirectoryChangesW (no polling)
- **Selective**: Per-project `.syncignore` files with `.gitignore` syntax
- **Bandwidth-efficient**: MD5 checksum comparison, debouncing, rate limiting
- **Single binary**: `smirror.exe` — zero runtime dependencies
- **Backend-agnostic**: rclone handles all cloud/remote backends
- **Single-instance**: File-based lock prevents duplicate instances
- **Quiescence**: Files must be stable (size+mtime unchanged for 200ms, not locked) before sync
- **Delete policy**: Configurable ignore/mirror/quarantine for local deletions

---

## Quick Start

```bash
# Build
go build -o smirror.exe ./cmd/smirror/

# Configure
# Edit ~/.selectivemirror/config.yaml (see config.example.yaml)

# Run diagnostics
smirror doctor

# Validate config + rclone connectivity
smirror validate

# See what would sync
smirror dry-run

# Start watching (foreground)
smirror start

# Immediate full sync
smirror sync-now

# Check status + metrics
smirror status

# Investigate a file
smirror explain Orch CLAUDE.md

# Detect drift between local and remote
smirror verify
```

---

## Commands

| Command | What it does |
|---------|-------------|
| `smirror start` | Start foreground watcher (single-instance locked) |
| `smirror sync-now [project]` | Immediate full sync |
| `smirror dry-run [project]` | Show what would sync |
| `smirror status` | Show sync status, metrics, instance state |
| `smirror validate` | Check config + rclone connectivity |
| `smirror list-filters [project]` | Show effective filter rules |
| `smirror explain <project> <path>` | Show include/exclude status, matched rule, sync state |
| `smirror doctor` | Run 12-point self-test diagnostics |
| `smirror verify [project]` | Compare local vs remote, report drift |

---

## Architecture

```
File saved (any editor/tool)
  → fsnotify detects change (ReadDirectoryChangesW)
  → filter check (.syncignore + global excludes)
  → debounce (5s quiet window, per-project)
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
internal/watcher/watcher.go      — fsnotify + debounce + delete event routing
internal/sync/sync.go            — rclone invocation + quiescence + delete handling
internal/filter/filter.go        — .syncignore parser + rclone filter generation
internal/state/state.go          — SQLite state store (WAL mode)
internal/lock/lock.go            — Single-instance file lock (LockFileEx/flock)
internal/metrics/metrics.go      — Thread-safe counters + status.json writer
internal/logging/logging.go      — slog + rotating file handler
```

### Dependencies

```
github.com/fsnotify/fsnotify      — Filesystem monitoring
github.com/sabhiram/go-gitignore  — .gitignore-style pattern matching
gopkg.in/yaml.v3                  — Config parsing
modernc.org/sqlite                — Pure Go SQLite (no CGo)
golang.org/x/sys                  — Windows syscalls (LockFileEx, OpenProcess)
```

---

## Configuration

File: `~/.selectivemirror/config.yaml`

- **projects**: List of watched directories with rclone remote destinations
- **global_excludes**: Patterns applied to all projects (.gitignore syntax)
- **delete_policy**: `ignore` (default), `mirror`, or `quarantine`
- **quarantine_days**: Days to keep quarantined files (default 30)
- **Per-project .syncignore**: Place a `.syncignore` file in the project root

See `config.example.yaml` for full annotated example.

---

## Testing

```bash
# Run all unit tests (38 tests across 5 packages)
go test ./internal/... -v

# Packages with tests:
# internal/config/    — 12 tests: parsing, validation, defaults, delete policy
# internal/filter/    — 7 tests: patterns, negation, Unicode, rclone filter gen
# internal/state/     — 11 tests: CRUD, hash, concurrent access, schema
# internal/lock/      — 5 tests: acquire, release, double-acquire, reacquire
# internal/metrics/   — 8 tests: counters, snapshot, status.json, human format
```

### Test tiers

| Tier | Scope | How to run |
|------|-------|-----------|
| Unit | All packages, no I/O | `go test ./internal/...` |
| Local integration | Real watcher + local rclone remote | `go test ./test/ -tags integration` |
| Backend integration | Real Google Drive | `go test ./test/ -tags e2e` (manual) |

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go | Single binary, native Windows service (Phase 2), rclone is Go |
| rclone subprocess | Clean error codes, zero coupling. Overhead negligible vs network I/O |
| `modernc.org/sqlite` | Pure Go — no CGo, no gcc needed. Binary stays dependency-free |
| `rclone copy` not `sync` | Never deletes remote files (unless delete_policy=mirror) |
| MD5 hashing | Matches rclone's checksum for Google Drive / most backends |
| Sequential sync worker | Prevents API rate limit exhaustion |
| Per-project debounce | Changes in project A don't trigger sync of project B |
| Batch on startup, incremental on steady state | Reconciliation: 1 rclone call per project, not per file |
| Quiescence before sync | Prevents partial file sync on Windows (Office, long writes) |
| File-based lock | Prevents dual-instance corruption; cross-platform |

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
- [ ] **Phase 2**: Windows service — native via `golang.org/x/sys/windows/svc`
- [ ] **Phase 3**: USN journal recovery — fast restart reconciliation
- [ ] **Phase 4**: OSS release — README, CI, GoReleaser, winget manifest

---

## Immediate Use Case

Mirror `C:\ClaudeWork`, `C:\Orch`, `C:\HPL`, `C:\Zotero` → Google Drive `AI-hub/` folder for inter-AI orchestration. Replaces the LLM-dependent PostToolUse hook in Claude Code.

---

## Prerequisites

- **rclone** (v1.73+): `winget install Rclone.Rclone`
- **rclone remote**: Configure with `rclone config` (one-time)
- **Go** (for building): `winget install GoLang.Go`

---

## Dependencies & Upgrade Policy

### rclone (runtime, external process)

SelectiveMirror uses rclone features: `copyto --checksum`, `deletefile`, `moveto`, `lsjson --recursive --hash`, `copy --filter-from`. These are stable rclone APIs that haven't changed across major versions.

**Upgrade policy**:
- **Safe to upgrade**: rclone follows semver. Minor/patch upgrades are safe.
- **Before upgrading**: Run `smirror doctor` to verify rclone connectivity post-upgrade.
- **Pinned minimum**: v1.73+ (for `--skip-links` flag support).
- **Upgrade command**: `winget upgrade Rclone.Rclone` or `rclone selfupdate`
- **Risk**: rclone backend-specific changes (e.g., Google Drive API v3 → v4) could change behavior. The `verify` command detects drift after such changes.

### Go modules (compiled in)

All dependencies are permissive-licensed (MIT, BSD, Apache 2.0). See CREDITS.md for full list.

**Upgrade policy**:
- `go get -u ./...` to update all dependencies.
- Run `go test ./internal/...` and `test/run_tests.ps1` after any dependency update.
- `modernc.org/sqlite` is the most sensitive dependency (pure-Go SQLite). Major upgrades should be tested carefully against the state database.

### Licenses

- **SelectiveMirror**: MIT
- **All compiled dependencies**: MIT / BSD / Apache 2.0 (permissive, no copyleft)
- **rclone**: MIT (runtime only, not compiled in)
- **No license embedding required in binary** for current dependency set, but binary distributions should include a NOTICE file listing BSD 3-Clause dependencies per their license terms.
