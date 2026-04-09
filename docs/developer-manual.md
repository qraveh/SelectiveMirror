---
title: "SelectiveMirror Developer Manual"
author: "Raveh (raveh@qodeh.com)"
date: "2026-03-27"
toc: true
toc-depth: 3
geometry: margin=1in
---

# Architecture Overview

SelectiveMirror is a real-time file synchronization engine for Windows that watches local directories and mirrors changes to any rclone-supported cloud backend. The system is structured as a pipeline of loosely coupled components, each in its own Go package under `internal/`.

## Module Diagram

```
cmd/smirror/main.go          CLI entry point, command dispatch
        |
        +-- internal/config       YAML config loading + validation
        +-- internal/filter       .syncignore parsing, rclone filter generation
        +-- internal/rclone       Binary detection, version parsing
        +-- internal/state        SQLite state database (sync history, hashes)
        +-- internal/lock         Single-instance file lock (Windows/Unix)
        +-- internal/logging      Structured logging with file rotation
        +-- internal/metrics      Atomic counters, status.json generation
        +-- internal/notify       Desktop notifications (Windows toast)
        +-- internal/service      Windows Service integration (SCM handler)
        +-- internal/watcher      fsnotify event loop, FairQueue dispatch, recursive watch
        +-- internal/sync         FairQueue, task processing, rclone subprocess, hash check
```

## Data Flow

The core data flow follows a pipeline pattern:

```
  fsnotify event
       |
       v
  watcher.eventLoop()       Match event to project, check filters
       |
       v
  watcher.handleEvent()     Lstat (not Stat) to detect symlinks, check size
       |
       v
  sync.FairQueue.Enqueue()  Dedup (move-to-back), priority (deletes to front)
       |
       v
  sync.FairQueue.Dequeue()  Workers block until task available
       |
       v
  sync.Engine.runWorker()   N concurrent workers (default 4, max 16)
       |
       v
  sync.processTask()        Per-file lock -> quiescence -> hash check -> rclone
       |
       v
  rclone subprocess         copyto / deletefile / moveto / sync / touch
       |
       v
  state.Store               SQLite: record hash, mtime, exit code, log action
```

## Key Design Decisions

**Subprocess model for rclone.** SelectiveMirror invokes rclone as a child process rather than linking it as a library. This is discussed in detail in the next section.

**Per-file locking with sync.Map.** Two workers must never sync the same file simultaneously. Mutexes are stored in a `sync.Map` keyed by `"project:relPath"` and are never deleted after unlock -- deleting would create a race where two goroutines hold different mutex instances for the same key.

**FairQueue scheduling.** File change events are enqueued into a deduplicating priority queue. If a file is already queued, the old entry is removed and a new one placed at the back -- hot files cycle to the back while cold files advance. Delete events get priority. When `debounce_sec > 0` is configured (for Office-style saves), a quiet-window timer fires before enqueuing.

**Hash-based deduplication.** Before invoking rclone, the engine computes an MD5 hash of the local file and compares it against the last known hash in the state database. If the hash and mtime are unchanged and the previous sync succeeded (exit code 0), the file is skipped entirely.

**Three-tier delete policy.** Local file deletions can be handled with `ignore` (do nothing on remote), `mirror` (delete remote file), or `quarantine` (move to `.quarantine/` with timestamp). Rename events always force-delete the old remote path regardless of policy.


# rclone Subprocess Architecture

## Why Subprocess Over Library

SelectiveMirror deliberately invokes rclone as an external process rather than importing it as a Go library. This decision is driven by several engineering constraints:

1. **Isolation.** rclone is a large, complex project (~50MB binary, 70+ backend drivers). Linking it as a library would couple SelectiveMirror to rclone's internal APIs, which are not designed for library use and change between releases without stability guarantees.

2. **Zero coupling.** The only contract between SelectiveMirror and rclone is the CLI interface: command-line arguments, exit codes, and stdout/stderr. This contract is stable across rclone versions.

3. **User-managed upgrades.** Users install and update rclone independently (via winget, chocolatey, scoop, or direct download). SelectiveMirror does not need to ship rclone or manage its lifecycle.

4. **Binary bloat avoided.** Embedding rclone's dependency tree would add approximately 50MB to the SelectiveMirror binary. The standalone smirror binary is under 15MB.

5. **Testability.** The `RcloneRunner` function type allows tests to inject a fake implementation that returns predetermined exit codes without spawning any process.

6. **Inherited rate limiting.** Each rclone process contains a per-backend pacer that handles API rate limits with exponential backoff. This pacer state is per-process and not shared across processes. smirror therefore serializes rclone calls to the same backend -- running multiple concurrent rclone processes against one backend causes uncoordinated backoff (thundering herd). Internal parallelism within a single rclone call (`--transfers`, default 4) is the correct way to achieve throughput.

## RcloneRunner Interface

The sync engine uses a function type for dependency injection:

```go
type RcloneRunner func(ctx context.Context, args []string) int
```

The `Engine` struct holds a `RunRcloneFunc` field initialized to `defaultRunRclone` in production. Tests replace it:

```go
e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
    capturedArgs = args
    return 0
})
```

## defaultRunRclone Lifecycle

The production runner follows this sequence:

1. Resolve the rclone binary path from `cfg.RclonePath` (default: `"rclone"`, resolved via PATH).
2. Create a child context with a **5-minute timeout** to prevent any single operation from blocking a worker indefinitely.
3. Spawn `exec.CommandContext` with stdout/stderr forwarded to the parent process.
4. Wait for completion. On timeout (`context.DeadlineExceeded`), return exit code `-2`. On exec failure, return `-1`. Otherwise, return the process exit code.

## Exit Code Handling

| Exit Code | Meaning | Engine Behavior |
|-----------|---------|-----------------|
| 0 | Success | Update state DB, log action, record metrics |
| 1-9 | rclone error (e.g., directory not found, permission denied) | Log warning, record in state DB with non-zero exit |
| -1 | exec.Command failed to start (binary not found) | Log error |
| -2 | 5-minute timeout exceeded | Log error |

## Retry Strategy

For content sync operations (`copyto`, `copy`, `sync`), rclone is invoked with `--retries 3 --retries-sleep 10s`. For delete/quarantine operations (`deletefile`, `moveto`), retries are reduced to `--retries 1` with no sleep to avoid blocking the sync engine for 30+ seconds on transient failures. Failed deletes are caught by subsequent `verify` or reconciliation passes.

## Filter File Generation Pipeline

Full-project syncs require an rclone filter file that mirrors the in-memory `.syncignore` rules. The pipeline:

1. `filter.Engine.GenerateRcloneFilterFile()` acquires a read lock and converts all rules to rclone syntax.
2. Negation patterns (`!pattern`) become `+ pattern` (include).
3. Directory patterns (`dir/`) become `- dir/**` (exclude recursively).
4. Regular patterns become `- pattern` (exclude).
5. A final `+ **` line includes everything not excluded.
6. Rules are written to a temp file (`smirror-filter-*.txt`).
7. The temp file path is passed to rclone via `--filter-from`.
8. The caller removes the temp file after rclone exits.


# Building from Source

## Prerequisites

- **Go 1.22 or later** (the module uses `go 1.26.1` in go.mod, but builds with any compatible toolchain)
- **Git** for cloning the repository
- **rclone v1.73+** for runtime (not needed at build time)

## Clone and Build

```bash
git clone https://github.com/qraveh/SelectiveMirror.git
cd SelectiveMirror
go build -o bin/smirror.exe ./cmd/smirror/
```

The binary is self-contained with no external dependencies (SQLite is embedded via `modernc.org/sqlite`, a pure-Go implementation).

## Version Injection

The `version` variable in `main.go` defaults to `"dev"`. Inject a version string at build time:

```bash
go build -ldflags "-X main.version=1.0.0" -o smirror.exe ./cmd/smirror/
```

For release builds, GoReleaser handles this automatically (see the Release Process section).

## Cross-Compilation

SelectiveMirror is designed for Windows but the codebase compiles on Linux and macOS. Platform-specific behavior is isolated behind build tags:

- `internal/watcher/recurse_windows.go` -- sets `supportsRecursiveWatch = true`
- `internal/watcher/recurse_other.go` -- sets `supportsRecursiveWatch = false`
- `internal/lock/lock_windows.go` -- Windows file locking via `LockFileEx`
- `internal/lock/lock_unix.go` -- Unix file locking via `syscall.Flock`


# Running Tests

## Unit Tests

Run all unit tests with:

```bash
go test ./internal/...
```

Or test a specific package:

```bash
go test ./internal/sync/ -v
go test ./internal/watcher/ -v -run TestIsSubPath
```

The unit tests do not require rclone to be installed. They use injected `RcloneRunner` functions that return predetermined exit codes.

## Integration Tests

The integration test suite is a PowerShell script at `test/run_tests.ps1`. It exercises the full pipeline end-to-end using a local rclone backend (no network, no credentials):

```powershell
powershell -ExecutionPolicy Bypass -File test\run_tests.ps1
```

The script:

1. Creates an isolated test environment in a temp directory under the project root.
2. Configures a `testlocal` rclone remote pointing at a local destination directory.
3. Builds smirror from source using `go run`.
4. Starts `smirror start` as a background process.
5. Exercises file creation, modification, rename, delete, and edge cases.
6. Verifies that files appear (or are removed) on the destination.
7. Tears down the environment.

There are currently 123 integration test cases covering rename, symlink, filter reload, large file skip, concurrent writes, and other scenarios.

## Test Patterns

### RcloneRunner Injection

The primary test pattern is replacing the rclone subprocess with a closure:

```go
var capturedArgs []string
e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
    capturedArgs = args
    return 0
})
```

This allows tests to verify which rclone commands would be invoked, capture arguments, simulate failures (return non-zero), or trigger panics for recovery testing.

### Test Helpers

The `sync` package provides three helper functions used across tests:

- **`testProject(t)`** -- creates a `config.Project` pointing at `t.TempDir()` with a fake remote.
- **`testConfig(proj)`** -- wraps a project in a `config.Global` with 2 workers.
- **`testEngine(t, cfg, runner)`** -- creates an `Engine` with an in-memory state DB and the provided `RcloneRunner`.

The `watcher` package has a similar `makeProject(localPath, name)` helper for constructing lightweight project configs.

### Test Filter Helper

```go
func testFilter(t *testing.T) *filter.Engine {
    fe, err := filter.New(nil, "")
    // ...
    return fe
}
```

Creates a filter engine with no global excludes and no `.syncignore`, useful when filter behavior is not under test.


# Code Organization

## Package Responsibilities

| Package | Path | Approx. LOC | Responsibility |
|---------|------|-------------|----------------|
| `cmd/smirror` | `cmd/smirror/` | ~1400 | CLI entry point, command dispatch, heartbeat loop, reconciliation, doctor, verify, stats, report-bug |
| `sync` | `internal/sync/` | ~700 | Task processing, rclone invocation, hash comparison, quiescence, delete policies, per-file locking |
| `watcher` | `internal/watcher/` | ~700 | fsnotify event loop, FairQueue dispatch, recursive directory watching, symlink policy, health monitoring |
| `config` | `internal/config/` | ~250 | YAML config loading, validation, defaults, path expansion |
| `state` | `internal/state/` | ~300 | SQLite state database: schema, CRUD for sync_state/sync_log/meta tables, file hashing |
| `filter` | `internal/filter/` | ~200 | .syncignore parsing via go-gitignore, hot-reload, rclone filter file generation |
| `rclone` | `internal/rclone/` | ~170 | Binary detection (PATH + common install locations), version parsing, compatibility checking |
| `metrics` | `internal/metrics/` | ~150 | Atomic counters, per-project status, status.json snapshot, human-readable formatting |
| `notify` | `internal/notify/` | ~100 | Desktop notifications via Windows toast (drift alerts, sync failures) |
| `service` | `internal/service/` | ~150 | Windows Service integration: SCM handler via `golang.org/x/sys/windows/svc`, compound commands (install [start], stop [uninstall]) |
| `lock` | `internal/lock/` | ~80 | Single-instance file lock with PID recording, platform-specific locking |
| `logging` | `internal/logging/` | ~80 | slog setup, rotating file writer (10MB, 5 backups), console + file multi-writer |

## Dependency Graph

The packages form a directed acyclic graph with `cmd/smirror` at the root:

```
cmd/smirror
  +-- config       (no internal deps)
  +-- filter        depends on: (external: go-gitignore)
  +-- rclone        (no internal deps)
  +-- state         (no internal deps, uses: modernc.org/sqlite)
  +-- lock          (no internal deps)
  +-- logging       (no internal deps)
  +-- metrics       (no internal deps)
  +-- notify        (no internal deps, uses: golang.org/x/sys/windows)
  +-- service       depends on: config, logging (uses: golang.org/x/sys/windows/svc)
  +-- watcher       depends on: config, filter, sync
  +-- sync          depends on: config, filter, metrics, state
```


# Adding a New Command

SelectiveMirror's CLI uses a manual switch dispatch in `main()`. Adding a new command requires three steps:

## Step 1: Add Case to Main Switch

In `cmd/smirror/main.go`, add a new case to the `switch cmd` block:

```go
case "my-command":
    cmdMyCommand(configPath, cmdArgs)
```

## Step 2: Add to printUsage

Add a line to the `printUsage()` function to document the command:

```go
fmt.Printf(`
Commands:
  ...
  my-command              Description of what it does
  ...
`)
```

## Step 3: Implement the Command Function

Create a function following the naming convention `cmdXxx`:

```go
func cmdMyCommand(configPath string, args []string) {
    cfg := loadConfig(configPath)

    // Optional: set up logging
    logging.Setup(cfg.LogLevel, "", true)

    // Optional: open state store
    st, err := state.Open(cfg.StateDB)
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }
    defer st.Close()

    // Command logic here
}
```

Most commands follow this pattern: load config, optionally open the state database, execute logic, print results. Commands that need rclone also build filters and create a sync engine.


# The Sync Engine

## Task Lifecycle

A task's lifecycle from creation to completion:

1. **Creation.** The watcher (or `cmdSyncNow`, or the reconciliation loop) creates a `sync.Task` struct with a project reference, a relative path, and a task type (`TaskSync` or `TaskDelete`).

2. **FairQueue.** The task is enqueued via `Engine.Queue.Enqueue(task)`. If a task for the same file is already in the queue, the old entry is removed and the new one goes to the back (dedup + move-to-back). Delete events use `EnqueuePriority` to jump to the front.

3. **Worker dequeue.** One of N workers (default 4) calls `Queue.Dequeue(ctx)`, which blocks until a task is available or the context is cancelled.

4. **Per-file lock.** The worker calls `acquireFileLock(task)` which does `LoadOrStore` on a `sync.Map` to get or create a mutex for the key `"project:relPath"`, then locks it. Full-project syncs use just the project name as the key.

5. **Quiescence check** (sync tasks only). The engine confirms the file is stable:
   - `Lstat` to detect symlinks. Symlinks to directories are rejected. Symlinks to files are followed.
   - `Stat` to get file info (follows symlinks).
   - 200ms sleep.
   - Second `Stat`. If size or mtime changed, the file is still being written -- the task is silently dropped (the watcher will re-fire).
   - Three attempts to `os.Open` for shared read, detecting Windows file locks. Each failed attempt waits 1 second.

6. **Hash check.** MD5 hash is computed and compared against the state database. If hash and mtime match the last successful sync, the task is a no-op. If hash matches but mtime differs, a lightweight `rclone touch --timestamp` updates remote metadata without re-uploading.

7. **rclone invocation.** For content changes, `rclone copyto <local> <remote>` is called. For full-project syncs, `rclone copy` (or `rclone sync` under mirror policy) with `--checksum --filter-from <file>`.

8. **State update.** The state database is updated with the new hash, file size, mtime (nanoseconds), and rclone exit code.

## Per-File Locking

```go
func (e *Engine) acquireFileLock(task Task) {
    key := e.lockKey(task)
    val, _ := e.fileLocks.LoadOrStore(key, &gosync.Mutex{})
    mu := val.(*gosync.Mutex)
    mu.Lock()
}
```

The critical invariant: **mutexes are never deleted from the map.** If a mutex were deleted between one goroutine's `Unlock` and another's `LoadOrStore`, the second goroutine would create a new mutex for the same key, and two goroutines would hold different mutexes simultaneously -- breaking mutual exclusion. The memory cost is negligible (one `sync.Mutex` per unique file path ever synced in the session).

## Delete Policies and rclone Verbs

| Delete Policy | rclone Verb | Behavior |
|---------------|-------------|----------|
| `ignore` | (none) | No remote action on local delete |
| `mirror` | `deletefile` | Remote file is deleted |
| `quarantine` | `moveto` | Remote file is moved to `.quarantine/<path>.<timestamp>` |

Rename events bypass the delete policy entirely. When a file is renamed, the old path's remote copy is an orphan. The watcher sends a `TaskDelete` with `ForceDelete: true`, which forces mirror-delete regardless of the configured policy.


# The Watcher

## fsnotify and Recursive Watching

SelectiveMirror uses the `fsnotify` library for filesystem event monitoring. The recursive watching strategy differs by platform:

**Windows (ReadDirectoryChangesW).** A single watch on the project root with the `...` suffix monitors the entire subtree through one kernel handle:

```go
recursivePath := pw.project.LocalPath + string(os.PathSeparator) + "..."
m.fsw.Add(recursivePath)
```

This is efficient (one handle per project) and allows subdirectories to be freely renamed or deleted without breaking the watch.

**Linux/macOS (inotify/kqueue).** These APIs do not support recursive watching natively. The watcher walks the directory tree at startup and adds each subdirectory individually via `addRecursive()`. When a new subdirectory is created, the watcher detects the `Create` event and adds it dynamically.

## FairQueue Dispatch

The watcher dispatches events directly to the sync engine's FairQueue:

- **Default mode** (`debounce_sec = 0`): Every file event calls `queue.Enqueue(task)` immediately. The FairQueue handles deduplication (move-to-back) and fairness. No timers, no pending map.
- **Static mode** (`debounce_sec > 0`): Events start/reset a per-file timer. When the timer fires (quiet window elapsed), it calls `queue.Enqueue(task)`. This preserves the quiet-window behavior needed for Office-style saves.
- **Delete events**: Always use `queue.EnqueuePriority(task)` to jump to the front, regardless of mode.

The FairQueue's `Enqueue` is non-blocking (mutex-guarded, not channel-based), so the watcher event loop never blocks on a full queue.

## Event Handling

| fsnotify Event | Handler | Action |
|----------------|---------|--------|
| `Create` / `Write` | `handleEvent` | Check filters, validate file type (Lstat), enqueue to FairQueue |
| `Remove` | `handleRemove` | Clean stale watchers (non-Windows), clear pending timers, enqueue `TaskDelete` with priority |
| `Rename` | `handleRename` | Clean stale watchers, clear pending, enqueue `TaskDelete` with `ForceDelete: true` and priority |

When a new directory is created (or moved in), the watcher walks its contents and queues individual file sync tasks via `queueFilesInDir()`, directly into the FairQueue since the files already exist and are stable.

## Symlink Policy

SelectiveMirror uses `Lstat` (not `Stat`) in the event handler to see the filesystem object itself, not its target. The policy:

- **Symlinks to directories**: never followed. Watching a symlinked directory could escape the project boundary and monitor arbitrary paths. These are logged and skipped.
- **Symlinks to files**: allowed. The target file's content is uploaded at the symlink's relative path. The quiescence check follows the link to verify the target.
- **Broken symlinks**: rejected during quiescence with an error logged.

## Health Monitoring

A background goroutine runs every 60 seconds and checks:

- **Watch count**: warns if the number of watched directories exceeds 50,000 (possible handle leak).
- **Last event age**: tracked via `lastEventTime` for diagnostic reporting.
- **Health errors**: panics recovered by `safeGo()` are recorded in a capped ring buffer (max 100 entries), accessible via `HealthErrors()`.


# State Database Schema

SelectiveMirror uses an embedded SQLite database (via `modernc.org/sqlite`, a pure-Go implementation) with WAL journal mode and normal synchronous mode for performance.

## Tables

### sync_state

Tracks the last known state of every synced file.

```sql
CREATE TABLE sync_state (
    project     TEXT NOT NULL,
    rel_path    TEXT NOT NULL,
    local_hash  TEXT,            -- MD5 hex digest
    file_size   INTEGER,         -- bytes
    mtime_ns    INTEGER NOT NULL DEFAULT 0,  -- nanoseconds since epoch
    synced_at   TEXT NOT NULL,   -- RFC3339 timestamp
    rclone_exit INTEGER,         -- 0 = success, non-zero = failure
    PRIMARY KEY (project, rel_path)
);
```

### sync_log

Append-only audit log of all sync actions.

```sql
CREATE TABLE sync_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp   TEXT NOT NULL,   -- RFC3339
    project     TEXT NOT NULL,
    rel_path    TEXT NOT NULL,
    action      TEXT NOT NULL,   -- copy, delete, quarantine, skip_gone, error, etc.
    detail      TEXT,
    duration_ms INTEGER
);
```

### meta

Key-value store for metadata (schema version, last heartbeat, last full sync timestamps).

```sql
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT
);
```

## Key Operations

| Function | Description |
|----------|-------------|
| `UpdateFileState` | Upserts a row in `sync_state` using `ON CONFLICT DO UPDATE` |
| `GetFileState` | Retrieves the sync state for a specific project + relative path |
| `GetFilesUnderDir` | Returns all synced paths under a directory prefix (for directory rename/delete cleanup) |
| `CountFiles` | Returns the number of synced files for a project |
| `GetPendingFiles` | Returns files with non-zero rclone exit (failed syncs) |
| `GetLastSyncTime` | Returns the most recent successful sync timestamp for a project |
| `DeleteFileState` | Removes a state entry after successful remote delete |
| `LogAction` | Appends to the `sync_log` audit trail |
| `SetMeta` / `GetMeta` | Key-value operations on the `meta` table |

## Schema Migration

The `mtime_ns` column was added in schema version 2. The migration is idempotent -- `Open()` runs `ALTER TABLE ... ADD COLUMN` and ignores the error if the column already exists.


# Filter Engine

## gitignore-Compatible Patterns

The filter engine uses the `go-gitignore` library (`github.com/sabhiram/go-gitignore`) to evaluate file paths against exclusion patterns. Patterns follow `.gitignore` syntax:

- `*.log` -- exclude all `.log` files
- `node_modules/` -- exclude directory and everything under it
- `!important.log` -- negate (re-include) a previously excluded pattern
- `build/` -- trailing slash means directory-only match

## Two-Layer Filtering

Each project has two layers of filter rules:

1. **Global excludes** -- defined in `config.yaml` under `global_excludes`. Applied to all projects.
2. **Per-project `.syncignore`** -- a file at the project root (or a custom path via `syncignore_path`). Project rules take precedence.

Both layers are compiled into `GitIgnore` matchers at startup. The `IsExcluded(relPath)` method checks project rules first, then global rules.

## Hot-Reload

When the watcher detects a write to a project's `.syncignore` file, it calls `filter.Engine.Reload()`:

1. Read the current rules under a read lock.
2. Acquire a write lock.
3. Re-read and recompile the `.syncignore` file.
4. Compare old and new rules. If changed, return `true`.
5. The watcher then triggers a full project reconciliation so newly included files get synced.

The `Reload()` method uses content comparison (not file hash) to detect changes. During reload, concurrent `IsExcluded()` callers are briefly blocked by the `RWMutex`.

## Rclone Filter File Generation

For full-project syncs, the filter engine generates a temporary file with rclone-native filter syntax:

```go
func (e *Engine) GenerateRcloneFilterFile() (string, error) {
    // Project rules first (negations must precede exclusions in rclone)
    // Global rules second
    // Final: "+ **" (include everything else)
}
```

The conversion rules:

| .syncignore Pattern | rclone Filter |
|---------------------|---------------|
| `*.log` | `- *.log` |
| `build/` | `- build/**` |
| `!important.log` | `+ important.log` |


# Multi-User Architecture

## Current Support

SelectiveMirror already supports multi-user deployment without code changes. All paths are derived from `os.UserHomeDir()`:

- Config: `~/.selectivemirror/config.yaml`
- State DB: `~/.selectivemirror/state.db`
- Lock file: `~/.selectivemirror/smirror.lock`
- Log file: `~/.selectivemirror/selectivemirror.log`
- Status: `~/.selectivemirror/status.json`

There are no global variables, singletons, or shared resources. Each user's smirror instance operates in complete isolation.

## What Is Needed for Production Multi-User

For deployment on shared machines (e.g., terminal servers):

1. **Per-user scheduled task** -- each user needs their own Windows Task Scheduler entry to run `smirror start` at logon. This can be deployed via Group Policy or a setup script.
2. **Per-user rclone configuration** -- rclone stores its config in `%APPDATA%\rclone\rclone.conf`, which is already per-user on Windows.
3. **Documentation** -- setup guide for IT administrators covering the per-user deployment model.


# Bug Tracking

SelectiveMirror uses an interproject bug tracker located at `C:\BugTracker\`.

## Bug Report Format

Each bug report is a Markdown file with YAML frontmatter:

```yaml
---
id: SM-001
project: SelectiveMirror
status: open
severity: medium
created: 2026-03-15
---
```

Followed by Markdown sections:

- **Raw Report** -- the original observation or error log.
- **Analysis** -- root cause investigation, hypotheses, evidence.
- **Reproducing Test** -- steps or test case to reproduce the bug.
- **Validation Tests** -- tests that confirm the fix works.

## GitHub Sync

The script `sync-github.ps1` in the BugTracker directory synchronizes local bug reports with GitHub Issues on the SelectiveMirror repository. It creates or updates issues based on the YAML metadata and Markdown content.


# Contributing Guidelines

## Getting Started

1. **Fork** the repository at `github.com/qraveh/SelectiveMirror`.
2. **Clone** your fork and create a feature branch:
   ```bash
   git checkout -b feature/my-improvement
   ```
3. **Make changes**, ensuring all tests pass.
4. **Submit a pull request** against `master`.

## Code Style

- Run `go fmt ./...` before committing. All code must be formatted with `gofmt`.
- Run `go vet ./...` to catch common issues.
- Use `slog` for all logging (not `fmt.Printf` or `log.Printf`), except for user-facing command output.
- Error messages should be lowercase and not end with punctuation (Go convention).

## Commit Message Format

Use imperative mood in commit messages:

```
Add per-file locking to prevent concurrent sync of same path

The sync engine now acquires a mutex keyed by project:relPath before
processing each task. Mutexes are stored in a sync.Map and never
deleted to prevent a race condition where two goroutines hold
different mutexes for the same key.
```

The first line should be under 72 characters. Use a blank line to separate the summary from the body.

## Testing Requirements

- All new features must include unit tests.
- Bug fixes should include a test that reproduces the bug before the fix.
- Integration tests (`test/run_tests.ps1`) should pass before submitting.
- Tests must not require network access or cloud credentials.

## Package Boundaries

- `internal/` packages must not import `cmd/smirror`.
- `internal/` packages should minimize cross-imports. Check the dependency graph before adding new imports.
- Platform-specific code uses build tags (`_windows.go`, `_unix.go`), not runtime checks.


# Release Process

## Versioning

SelectiveMirror follows semantic versioning (`MAJOR.MINOR.PATCH`). Tags use the `v` prefix: `v1.0.0`, `v1.1.0`, etc.

## Tagging a Release

```bash
git tag -a v1.1.0 -m "Release v1.1.0"
git push origin v1.1.0
```

## GoReleaser

GoReleaser builds platform-specific binaries and archives. It is triggered by pushing a version tag. The `.goreleaser.yaml` configuration handles:

- Building `smirror.exe` for `windows/amd64` with `-ldflags "-X main.version={{ .Version }}"`.
- Creating a zip archive with the binary, `README.md`, and `LICENSE`.

## MSI Installer

The MSI installer is built using WiX v5. The WiX source files define:

- Installation to `%ProgramFiles%\SelectiveMirror\`.
- Adding the install directory to the system PATH.
- Creating a Start Menu shortcut.
- Registering with Windows Add/Remove Programs.

## GitHub Release

Release artifacts are attached to the GitHub Release:

- `smirror_<version>_windows_amd64.zip` -- standalone binary
- `SelectiveMirror-<version>.msi` -- MSI installer
- Source code archives (auto-generated by GitHub)

The release notes are written manually, summarizing changes since the previous release, with links to relevant pull requests and issues.
