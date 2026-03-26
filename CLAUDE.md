# SelectiveMirror — Selective Near-Real-Time File Mirror

**Project root**: `C:\SelectiveMirror\`
**Author**: Raveh (raveh@qodeh.com)
**Status**: Phase 1 (core mirror) complete
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

---

## Quick Start

```bash
# Build
go build -o smirror.exe ./cmd/smirror/

# Configure
# Edit ~/.selectivemirror/config.yaml (see config.example.yaml)

# Validate config + rclone connectivity
smirror validate

# See what would sync
smirror dry-run

# Start watching (foreground)
smirror start

# Immediate full sync
smirror sync-now
```

---

## Commands

| Command | What it does |
|---------|-------------|
| `smirror start` | Start foreground watcher |
| `smirror sync-now [project]` | Immediate full sync |
| `smirror dry-run [project]` | Show what would sync |
| `smirror status` | Show last sync times per project |
| `smirror validate` | Check config + rclone connectivity |
| `smirror list-filters [project]` | Show effective filter rules |

---

## Architecture

```
File saved (any editor/tool)
  → fsnotify detects change (ReadDirectoryChangesW)
  → filter check (.syncignore + global excludes)
  → debounce (5s quiet window, per-project)
  → rclone copyto --checksum (single file)
  → SQLite state update
```

### Modules

```
cmd/smirror/main.go          — CLI entry point
internal/config/config.go    — YAML config + validation
internal/watcher/watcher.go  — fsnotify + debounce goroutines
internal/sync/sync.go        — rclone invocation + error handling
internal/filter/filter.go    — .syncignore parser + rclone filter generation
internal/state/state.go      — SQLite state store
internal/logging/logging.go  — slog + rotating file handler
```

### Dependencies

```
github.com/fsnotify/fsnotify      — Filesystem monitoring
github.com/sabhiram/go-gitignore  — .gitignore-style pattern matching
gopkg.in/yaml.v3                  — Config parsing
modernc.org/sqlite                — Pure Go SQLite (no CGo)
```

---

## Configuration

File: `~/.selectivemirror/config.yaml`

- **projects**: List of watched directories with rclone remote destinations
- **global_excludes**: Patterns applied to all projects (.gitignore syntax)
- **Per-project .syncignore**: Place a `.syncignore` file in the project root

See `config.example.yaml` for full annotated example.

---

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Go | Single binary, native Windows service (Phase 2), rclone is Go |
| rclone subprocess | Clean error codes, zero coupling. Overhead negligible vs network I/O |
| `modernc.org/sqlite` | Pure Go — no CGo, no gcc needed. Binary stays dependency-free |
| `rclone copy` not `sync` | Never deletes remote files. One-way mirror safety |
| MD5 hashing | Matches rclone's checksum for Google Drive / most backends |
| Sequential sync worker | Prevents API rate limit exhaustion |
| Per-project debounce | Changes in project A don't trigger sync of project B |

---

## Phases

- [x] **Phase 1**: Core mirror — config, filters, watcher, sync, state, CLI
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
