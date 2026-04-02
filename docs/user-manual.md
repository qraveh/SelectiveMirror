---
title: "SelectiveMirror User Manual"
author: "Raveh (raveh@qodeh.com)"
date: "2026-03-27"
toc: true
toc-depth: 3
geometry: margin=1in
fontsize: 11pt
---

# 1. Overview and Concepts

## What SelectiveMirror Does

SelectiveMirror is a real-time file synchronization engine for Windows that watches local project directories and mirrors changes to any of rclone's 70+ supported cloud backends -- Google Drive, Amazon S3, Dropbox, SFTP, and more. It is designed for developers and researchers who need continuous, automatic backup of working directories without manual intervention.

Unlike raw `rclone sync`, which operates as a batch command that you must run manually, SelectiveMirror provides:

- **Real-time detection** of file creates, modifications, renames, and deletes via the Windows `ReadDirectoryChangesW` API.
- **Fair queue with deduplication** -- when you save a file ten times in five seconds, only one sync occurs. Hot files cycle to the back; other files get their turn first.
- **Checksum-based deduplication** -- files are hashed with MD5 before upload. If the content has not changed since the last sync, no upload occurs.
- **Per-file locking** -- concurrent sync workers cannot race on the same file.
- **Selective filtering** via `.syncignore` files (gitignore syntax) and global exclude patterns.
- **Three delete policies** controlling how local deletions propagate to remotes.
- **State tracking** in a SQLite database recording every sync operation, hash, and rclone exit code.
- **Built-in diagnostics** including a 12-point self-test, drift detection, and automated bug report generation.

## Architecture

The system is composed of four stages:

```
  Filesystem Events          FairQueue              Sync Engine            rclone
 +-----------------+    +----------------+    +-----------------+    +----------+
 | ReadDirectory   |--->| Dedup          |--->| Worker pool     |--->| copyto   |
 | ChangesW        |    | (move-to-back) |    | (default 4)     |    | copy     |
 | (fsnotify)      |    | Priority lane  |    | per-file locks  |    | sync     |
 |                 |    | (deletes first)|    | MD5 dedup       |    | deletefile|
 | Rename tracking |    | 30s cooldown   |    | quiescence check|    | moveto   |
 | Delete tracking |    | per file       |    | state DB update |    | touch    |
 +-----------------+    +----------------+    +-----------------+    +----------+
```

1. **Watcher** -- Uses `fsnotify` (backed by `ReadDirectoryChangesW` on Windows) to detect file system events recursively within each project directory. Tracks renames as delete + create pairs. Monitors `.syncignore` files for hot-reload.

2. **FairQueue** -- File events are enqueued into a deduplicating priority queue. If a file is already waiting in the queue and changes again, the old entry is removed and a new one is placed at the back -- ensuring hot files don't starve cold ones. Delete events get priority and jump to the front. When `debounce_sec` is configured (for Office-style save patterns), a quiet-window timer fires before enqueuing.

3. **Sync Engine** -- A pool of concurrent workers (default 4, max 16) processes the FairQueue. Before syncing, each file undergoes:
   - A quiescence check (200ms stability wait + 3 lock-acquisition attempts).
   - An MD5 hash comparison against the state database. If the hash matches the last successful sync and the modification time is unchanged, the file is skipped entirely.
   - Per-file mutex locking to prevent two workers from operating on the same file.

4. **rclone** -- The actual file transfer is delegated to rclone as a subprocess. Different rclone subcommands are used depending on the operation: `copyto` for single files, `copy` or `sync` for full-project reconciliation, `deletefile` for mirror deletes, `moveto` for quarantine, `touch` for metadata-only updates, and `lsjson` for remote file listing.

## Fair Scheduling

SelectiveMirror uses a FairQueue to prevent any single file or mirror from monopolizing the sync engine. The policy is simple: **every file gets its turn -- hot files cycle to the back, cold files advance to the front.**

Without fairness, a large file that changes every few seconds (e.g., a database or session log) would continuously re-enter the queue at the same priority as every other file, consuming sync workers and API quota indefinitely. Other mirrors would starve.

The FairQueue enforces three rules:

1. **Coalesce.** If a file is already waiting in the queue and changes again, the old entry is removed and a new one is placed at the back. No duplicates, no wasted work.

2. **Move to back.** A hot file that keeps changing keeps getting pushed to the tail. Cold files naturally advance to the front and get synced first.

3. **Priority lane.** Delete events jump to the front of the queue. Remote cleanup should never wait behind a large upload.

This design eliminates fixed debounce timers for the default mode (`debounce_sec: 0`). For directories with Office-style save patterns (temp file, delete, rename), static debounce (`debounce_sec > 0`) is preserved and provides a quiet-window before enqueuing.

### Per-File Cooldown

After a file is successfully synced, it enters a 30-second cooldown. During cooldown, the file stays in the queue but is skipped -- other files dequeue and sync first. When the cooldown expires, the file becomes eligible again. If the file changed during cooldown, it syncs the latest version; if not, the hash check skips it as a no-op.

This prevents a continuously-changing file (e.g., a 21MB session log) from consuming 100% of the API quota on repeated uploads that are immediately obsoleted by the next change. The first sync is always immediate -- cooldown only affects *repeated* syncs of the same file. Delete events and full-project syncs bypass cooldown entirely.

## How It Differs from Raw rclone

| Feature | `rclone sync` | SelectiveMirror |
|---|---|---|
| Trigger | Manual / cron | Real-time (fsnotify) |
| Granularity | Entire directory tree | Per-file events |
| Deduplication | Checksum or mtime | MD5 hash + mtime in state DB |
| Delete handling | Always mirrors | Configurable: ignore, mirror, quarantine |
| Filtering | `--filter` flags | `.syncignore` (gitignore syntax) + hot reload |
| State tracking | None | SQLite DB with full history |
| Diagnostics | None | doctor, verify, explain, stats, report-bug |
| Concurrency | Single process | Configurable worker pool with per-file locks |

## Resource Usage

SelectiveMirror is designed to be lightweight:

- **Memory**: approximately 15 MB RAM at idle.
- **CPU**: less than 1% CPU when idle, proportional to event rate during active syncing.
- **Disk**: the state database (SQLite) grows with the number of tracked files, typically a few MB for thousands of files.


# 2. rclone Integration Deep Dive

## Subprocess Model

SelectiveMirror never links against rclone as a library. Every rclone operation spawns a new subprocess using the configured `rclone_path` (default: `rclone` on PATH). This means:

- rclone must be installed separately and reachable from the system PATH or configured explicitly.
- Each operation is isolated -- a crash or hang in rclone does not bring down the SelectiveMirror process.
- rclone's stdout and stderr are passed through to the console in foreground mode.

## rclone Subcommands Used

SelectiveMirror uses the following rclone subcommands:

| Subcommand | Used For |
|---|---|
| `copyto` | Uploading a single changed file to remote |
| `copy` | Full-project sync (upload-only, default delete policy) |
| `sync` | Full-project sync (mirrors deletes, when `delete_policy: mirror`) |
| `deletefile` | Removing a single file from remote (mirror delete policy) |
| `moveto` | Moving a file to `.quarantine/` on remote (quarantine delete policy) |
| `touch` | Updating modification time on remote without re-uploading content |
| `lsjson` | Listing remote files with hashes for `verify` and ghost scan |
| `lsd` | Checking remote connectivity during `test-mirrors` and `doctor` |
| `version` | Detecting rclone version and compatibility |

## What SelectiveMirror Inherits from rclone

By delegating all file transfer to rclone, SelectiveMirror inherits a set of production-grade capabilities without reimplementing them:

- **Per-backend rate limiting.** rclone's internal pacer adapts API call frequency to each backend's limits. Google Drive, S3, Dropbox, and OneDrive each have different rate limit profiles; rclone knows them all and throttles accordingly.
- **Exponential backoff with jitter.** When a backend returns 429 (Too Many Requests) or 503, rclone automatically backs off using a truncated exponential strategy with randomization, preventing request storms.
- **Chunked uploads.** Large files are split into chunks (configurable via `--drive-chunk-size` for Google Drive). Each chunk is uploaded independently with retry, so a network interruption mid-upload loses only one chunk, not the entire file.
- **Server-side checksums.** rclone verifies uploads against backend-provided checksums (MD5 for Google Drive, SHA-1 for Dropbox, etc.), catching silent corruption during transfer.
- **70+ backend drivers.** Each backend has its own authentication, API conventions, and error handling. rclone encapsulates all of this behind a uniform CLI interface.
- **Automatic retries.** Failed operations are retried (default 3 times with configurable sleep between retries) before being reported as errors.
- **Connection pooling and HTTP/2.** rclone reuses connections and leverages HTTP/2 multiplexing where supported, reducing handshake overhead for many small files.

SelectiveMirror's architecture ensures these capabilities work correctly by using a single rclone process per sync operation. Running multiple concurrent rclone processes against the same backend would create independent, uncoordinated rate limiters -- each process has its own pacer with no shared state. This is why smirror serializes rclone calls to the same remote and relies on rclone's internal `--transfers` parallelism (default 4 concurrent file transfers within one process) for throughput.

## Flag Mapping

### Common Flags (applied to all sync operations)

Every rclone invocation for file synchronization includes these flags:

```
--retries 3            Retry failed operations up to 3 times
--retries-sleep 10s    Wait 10 seconds between retries
--stats 0              Suppress periodic statistics output
--log-level NOTICE     Show only warnings and errors from rclone
--skip-links           Never follow symlinks (safety: prevents syncing outside project)
```

### Additional Flags for Full-Project Sync

Full-project syncs (triggered by `sync-now`, startup reconciliation, and periodic reconciliation) add:

```
--checksum             Compare files by MD5 hash, not mtime
--filter-from <file>   Apply .syncignore rules via temporary filter file
```

### Delete Flags (applied to delete and quarantine operations)

Delete and quarantine operations use a reduced flag set optimized for speed:

```
--retries 1            Single attempt (deletes are best-effort)
--stats 0              Suppress statistics
--log-level NOTICE     Show only warnings and errors
--skip-links           Never follow symlinks
```

### Optional Flags

```
--bwlimit <limit>      Applied when bandwidth_limit is set in config
```

Any flags specified in `rclone_extra_flags` are appended to all operations.

### Dry-Run Flags

The `dry-run` command uses a distinct flag set:

```
--checksum             Compare by hash
--filter-from <file>   Apply filters
--dry-run              Do not actually transfer files
--log-level INFO       Show what would be transferred
```

## Exit Code Meanings

SelectiveMirror interprets rclone exit codes as follows:

| Exit Code | Meaning |
|---|---|
| `0` | Success -- operation completed without errors |
| `1` | Syntax or usage error |
| `2` | Error not otherwise categorized |
| `3` | Directory not found |
| `4` | File not found |
| `5` | Temporary error (more retries might fix it) |
| `6` | Less serious errors (e.g., delete failures with `--ignore-errors`) |
| `7` | Fatal error (more retries won't fix it) |
| `8` | Transfer limit reached |
| `9` | Operation successful but no files transferred |
| `10` | Duration limit reached |
| `-1` | SelectiveMirror-specific: `exec.Command` failed (rclone binary not found or could not be started) |
| `-2` | SelectiveMirror-specific: operation timed out (5-minute deadline exceeded) |

## Retry and Timeout Strategy

- **Sync operations**: 3 retries with 10-second sleep between attempts. This handles transient network failures and API rate limits.
- **Delete operations**: 1 retry with no retry sleep. Deletes are best-effort; orphaned remote files are caught by subsequent `verify` or reconciliation passes.
- **Timeout**: Every rclone subprocess has a 5-minute deadline (`context.WithTimeout`). If rclone does not complete within 5 minutes, the process is killed and exit code `-2` is recorded. This prevents a single stuck operation from blocking a sync worker indefinitely.


# 3. Configuration Reference

SelectiveMirror reads its configuration from a YAML file. The default location is:

```
%USERPROFILE%\.selectivemirror\config.yaml
```

You can override this with the `--config` flag on any command:

```
smirror --config C:\path\to\config.yaml start
```

## Complete Configuration Example

```yaml
# Projects to watch and sync
mirrors:
  - name: MyProject
    local_path: C:\Projects\MyProject
    remote: gdrive:backup/MyProject
    debounce_sec: 5
    max_file_size_mb: 100
    syncignore_path: ""   # default: <local_path>/.syncignore

  - name: Research
    local_path: C:\Research
    remote: s3:my-bucket/research
    debounce_sec: 10
    max_file_size_mb: 500

# Global settings
rclone_path: rclone                    # path to rclone binary
rclone_extra_flags: []                 # additional flags for all rclone calls
bandwidth_limit: ""                    # e.g. "10M" for 10 MB/s
delete_policy: ignore                  # ignore | mirror | quarantine
quarantine_days: 30                    # days to keep quarantined files
sync_workers: 4                        # concurrent sync workers (1-16)
reconcile_interval_sec: 300            # periodic full sync interval
heartbeat_interval_sec: 300            # heartbeat and status.json interval
state_db: ~/.selectivemirror/state.db
log_file: ~/.selectivemirror/selectivemirror.log
log_level: info                        # debug | info | warn | error

# Patterns excluded from ALL projects
global_excludes:
  - "*.tmp"
  - "*.bak"
  - ".git/"
  - Thumbs.db
  - desktop.ini
```

## Field Reference

### Project Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | *required* | Unique identifier for the project. Used in commands, logs, and state DB. |
| `local_path` | string | *required* | Absolute path to the local directory to watch. Must exist and be a directory. |
| `remote` | string | *required* | rclone remote destination in `remote:path` format (e.g., `gdrive:backup/project`). |
| `debounce_sec` | int | `0` | Quiet-window before enqueuing (0 = immediate queue-based fairness, default). When > 0, waits N seconds after last change — use for Office-style saves. |
| `max_file_size_mb` | int | `100` | Maximum file size in megabytes. Files exceeding this limit are silently skipped. |
| `syncignore_path` | string | `<local_path>/.syncignore` | Override path to the `.syncignore` file for this project. |

### Global Fields

| Field | Type | Default | Description |
|---|---|---|---|
| `rclone_path` | string | `"rclone"` | Path to the rclone binary. Can be a bare name (searched in PATH and common locations) or an absolute path. |
| `rclone_extra_flags` | list of strings | `[]` | Additional command-line flags appended to every rclone invocation. Use for backend-specific options. |
| `bandwidth_limit` | string | `""` (unlimited) | Bandwidth limit passed to rclone's `--bwlimit` flag. Examples: `"10M"` (10 MB/s), `"1G"` (1 GB/s), `"500k"` (500 KB/s). |
| `delete_policy` | string | `"ignore"` | How local file deletions are handled on remote. One of: `ignore`, `mirror`, `quarantine`. See Section 6. |
| `quarantine_days` | int | `30` | Number of days to retain files in the `.quarantine/` directory on remote. Only relevant when `delete_policy: quarantine`. |
| `sync_workers` | int | `4` | Number of concurrent sync worker goroutines. Range: 1--16. Higher values increase throughput but may trigger API rate limits on some backends. |
| `reconcile_interval_sec` | int | `300` (5 min) | Interval in seconds between periodic full-project reconciliation passes. These catch changes invisible to fsnotify (WSL operations, network drive edits, external tools). |
| `heartbeat_interval_sec` | int | `300` (5 min) | Interval in seconds between heartbeat writes (state DB + `heartbeat.txt` + `status.json`). |
| `state_db` | string | `~/.selectivemirror/state.db` | Path to the SQLite state database. Stores sync history, file hashes, and metadata. |
| `log_file` | string | `~/.selectivemirror/selectivemirror.log` | Path to the log file. In foreground mode, logs are written to both the console and this file. |
| `log_level` | string | `"info"` | Minimum log level. One of: `debug`, `info`, `warn`, `error`. |
| `global_excludes` | list of strings | `[]` | Patterns excluded from all projects (gitignore syntax). Applied in addition to per-project `.syncignore` rules. |


# 4. .syncignore Reference

## Syntax

`.syncignore` files use **gitignore syntax**. Each non-empty, non-comment line is a pattern:

```
# Comment lines start with #
*.pyc              # Exclude all .pyc files in any directory
__pycache__/       # Exclude __pycache__ directories
node_modules/      # Exclude node_modules directories
.git/              # Exclude .git directories
*.log              # Exclude all log files
build/             # Exclude build output
dist/              # Exclude distribution output
.env               # Exclude environment files (may contain secrets)
```

### Pattern Rules

| Pattern | Matches | Scope |
|---|---|---|
| `*.ext` | Files ending in `.ext` | Any directory (unanchored) |
| `dirname/` | Directories named `dirname` | Any depth (unanchored) |
| `/dirname/` | Directory named `dirname` at root only | Root only (anchored) |
| `path/to/file` | A specific file (patterns with `/` are anchored) | Relative to root |
| `!pattern` | Negation: re-include a previously excluded pattern | Same scope as the pattern |
| `**/pattern` | Match in any directory depth (explicit) | Any depth |
| `*~` | Backup files (editor/OS artifacts) | Any directory |

### Anchored vs. Unanchored Patterns

This is the most important concept in `.syncignore`. A pattern is **anchored** (matches only at the project root) when it starts with `/`. Without the leading `/`, it matches at **any directory depth**.

```
# UNANCHORED -- matches logs/ at ANY depth:
#   logs/, src/logs/, deep/nested/logs/
logs/

# ANCHORED -- matches logs/ only at the project root:
/logs/
```

This distinction applies equally to negation patterns:

```
# UNANCHORED negation -- re-includes ANY file named config.yaml:
#   config.yaml, src/config.yaml, deep/nested/config.yaml
!config.yaml

# ANCHORED negation -- re-includes config.yaml only at root:
!/config.yaml
```

**Rule of thumb**: When writing negation patterns (`!`), always ask "do I want this to match at any depth, or only at the root?" If only at the root, use a leading `/`.

### Negation Example

```
# Exclude all log files...
*.log
# ...except the important one AT THE ROOT
!/important.log
```

### Whitelist Strategy (Include-Only)

To sync only specific files, start with `*` to exclude everything, then use anchored negation patterns to selectively include:

```
# Exclude everything by default
*

# Include only these root-level items:
!/src/
!/src/**
!/README.md
!/config.yaml
```

**Common mistake**: Using unanchored negation in a whitelist. The pattern `!hooks/` will match ANY directory named `hooks` at any depth, including `.git/hooks/` or `node_modules/.cache/hooks/`. Always anchor with `/` when using a whitelist strategy:

```
# WRONG -- includes hooks/ directories inside .git/, node_modules/, etc.
!hooks/
!hooks/*

# CORRECT -- includes only the root-level hooks/ directory
!/hooks/
!/hooks/*
```

### Interaction with global_excludes

Patterns in `global_excludes` (from `config.yaml`) are evaluated **before** per-mirror `.syncignore` patterns. Per-mirror negation can override global excludes:

```yaml
# config.yaml
global_excludes:
  - "*.log"
```

```
# .syncignore -- override global exclude for this mirror
!important.log
```

The combined filter uses last-match-wins semantics: the most specific matching rule wins, regardless of which file it came from.

### Debugging Filters

Use `smirror explain` to check why a specific file is included or excluded:

```
smirror explain MyProject path/to/file.txt
```

This shows the matched rule and filter status. Use it whenever a file is unexpectedly syncing or not syncing.

Use `smirror list-filters` to see all effective rules (global + per-mirror combined):

```
smirror list-filters MyProject
```

## File Location

By default, SelectiveMirror looks for `.syncignore` in the root of each project's `local_path`:

```
C:\Projects\MyProject\.syncignore
```

You can override this with the `syncignore_path` field in the project config:

```yaml
mirrors:
  - name: MyProject
    local_path: C:\Projects\MyProject
    remote: gdrive:backup/MyProject
    syncignore_path: C:\configs\myproject-syncignore.txt
```

## Hot Reload

When SelectiveMirror is running, changes to `.syncignore` files are detected automatically. The filter engine reloads immediately when `.syncignore` is modified. No restart is required.

Hot reload works because the watcher monitors `.syncignore` files alongside all other files in the project directory. When a change is detected, the filter engine re-reads and recompiles the rules. A log message confirms whether the rules actually changed.

## Relationship to rclone Filters

Internally, `.syncignore` patterns are translated to rclone filter syntax for full-project syncs:

| .syncignore | rclone filter |
|---|---|
| `pattern` | `- pattern` (exclude) |
| `dir/` | `- dir/**` (exclude directory recursively) |
| `!pattern` | `+ pattern` (include / negate) |

A final `+ **` line is appended to include everything not explicitly excluded.

For single-file syncs (`copyto`), the filter is evaluated in-process using the `go-gitignore` library -- no temporary file is created, and no `--filter-from` flag is passed to rclone.

**Note**: gitignore uses last-match-wins; rclone uses first-match-wins. SelectiveMirror reverses the rule order when generating rclone filters to preserve correct semantics. If you see unexpected behavior during full-project syncs, use `smirror explain` to verify the filter evaluation matches your expectations.


# 5. Command Reference

## Global Option

All commands accept:

```
--config PATH    Path to config file (default: ~/.selectivemirror/config.yaml)
```

## start

Start the file watcher in the foreground. Runs until interrupted with Ctrl+C.

```
smirror start
```

On startup, the engine:

1. Acquires a single-instance lock (only one smirror process may run at a time).
2. Opens the state database and builds filter engines.
3. Runs a startup reconciliation (full-project sync) for all projects.
4. Starts the filesystem watcher for all project directories.
5. Starts the sync engine worker pool.
6. Starts the heartbeat loop (periodic status writes and reconciliation).

```
> smirror start
2026-03-27T10:00:00Z INF smirror starting version=1.0.0
2026-03-27T10:00:00Z INF running startup reconciliation
2026-03-27T10:00:05Z INF full sync complete project=MyProject ms=4823
2026-03-27T10:00:05Z INF smirror running projects=[MyProject Research]
Press Ctrl+C to stop
```

## sync-now

Trigger an immediate full-project sync for one or all projects. Runs synchronously and exits.

```
smirror sync-now              # sync all projects
smirror sync-now MyProject    # sync one project
```

This command does not start the watcher. It is useful for forcing a one-time full sync, for example after manually editing files or recovering from an error.

## dry-run

Show what would be synced without actually transferring files. Uses rclone's `--dry-run` flag.

```
smirror dry-run              # all projects
smirror dry-run MyProject    # one project
```

Example output:

```
=== Dry run: MyProject ===
Source: C:\Projects\MyProject
Destination: gdrive:backup/MyProject
Running: rclone copy C:\Projects\MyProject gdrive:backup/MyProject --checksum --filter-from C:\...\smirror-filter-123.txt --dry-run --log-level INFO

2026/03/27 10:05:00 NOTICE: src/main.go: Skipped copy as --dry-run is set
2026/03/27 10:05:00 NOTICE: docs/readme.md: Skipped copy as --dry-run is set
```

## status

Show sync status, live metrics, and project details.

```
smirror status
```

Example output:

```
SelectiveMirror Status
======================

Config: C:\Users\raveh\.selectivemirror\config.yaml
State DB: C:\Users\raveh\.selectivemirror\state.db
Delete policy: ignore

Live Metrics (from running instance):
  Uptime: 2h15m (started 2026-03-27T08:00:00Z)
  Files synced: 142
  Bytes uploaded: 5242880
  Sync errors: 2
  Avg latency: 350ms
  Queue depth: 0
  Last reconciliation: 2026-03-27T10:10:00Z
  Status generated: 2026-03-27T10:15:00Z

Last heartbeat: 2026-03-27T10:15:00Z

Instance: running (PID 12345)

Project: MyProject
  Path:    C:\Projects\MyProject
  Remote:  gdrive:backup/MyProject
  Files synced: 85
  Last file sync: 2026-03-27T10:12:00Z (3m ago)
  Last full sync: 2026-03-27T10:10:00Z (5m ago)
```

## test-mirrors

Check configuration validity and rclone connectivity. Exits with code 0 on success, 1 on failure.

```
smirror test-mirrors
```

Performs these checks:

1. Parses the config file and validates all required fields.
2. Verifies each project's `local_path` exists.
3. Checks `.syncignore` file presence for each project.
4. Detects the rclone binary and checks version compatibility.
5. Tests connectivity to each project's remote via `rclone lsd`.

## list-filters

Show the effective filter rules for one or all projects.

```
smirror list-filters              # all projects
smirror list-filters MyProject    # one project
```

Example output:

```
=== MyProject ===
  # Global excludes
  *.tmp
  *.bak
  .git/
  # Project .syncignore
  node_modules/
  dist/
  *.pyc
```

## explain

Explain why a specific file is included or excluded from sync, and show its sync state.

```
smirror explain <project> <relative-path>
```

Example:

```
> smirror explain MyProject src/main.go

=== Explain: MyProject / src/main.go ===

Status: INCLUDED
Remote path: gdrive:backup/MyProject/src/main.go
Local file: C:\Projects\MyProject\src\main.go
  Size: 4096 bytes
  Modified: 2026-03-27T10:00:00Z
  MD5: a1b2c3d4e5f6...

Sync state:
  Last synced: 2026-03-27T09:55:00Z
  Synced hash: a1b2c3d4e5f6...
  Synced size: 4096 bytes
  rclone exit: 0 (success)
```

For excluded files:

```
> smirror explain MyProject node_modules/express/index.js

=== Explain: MyProject / node_modules/express/index.js ===

Status: EXCLUDED
Matched rule: node_modules/
```

## test-mirrors

Run a comprehensive self-test diagnostic (aliases: `doctor`, `verify`). Exits with code 0 if all checks pass, 1 if any fail.

```
smirror test-mirrors
```

Example output:

```
smirror test-mirrors

  Config file parses                                OK (5ms)
  All mirror paths exist                            OK (1ms)
  No duplicate mirror names                         OK (0ms)
  rclone binary found                               OK (120ms)
    version: 1.68.2
    path:    C:\Program Files\rclone\rclone.exe
    os:      windows (amd64)
  rclone version compatibility                      OK (0ms)
  Remote reachable: gdrive:backup/MyProject         OK (850ms)
  Remote reachable: s3:my-bucket/research            OK (320ms)
  State DB opens and schema valid                   OK (8ms)
  State DB integrity check                          OK (15ms)
  Log file writable                                 OK (1ms)
  Single-instance lock available                    OK (0ms)
  Filesystem watcher available                      OK (3ms)
  Write permissions on mirror dirs                  OK (2ms)
  Filter engines load without error                 OK (1ms)

14 passed, 0 failed
```

See Section 9 for details on each check.

## project-stats

Show file counts, line counts, and size breakdown by language category across all mirrors (alias: `stats`).

```
smirror project-stats
```

Example output:

```
smirror project-stats
=============

MyProject  (245 files, 18420 lines, 1.2 MB, 312 ignored)
  Go              42 files    8200 lines
  YAML/JSON       15 files     620 lines
  Docs/Text        8 files     340 lines
  Other          180 files    9260 lines

TOTAL  (245 files, 18420 lines, 1.2 MB, 312 ignored)
  Go              42 files    8200 lines
  ...
```

Language categories recognized: Go, PowerShell, Python, Shell, YAML/JSON, XML, Docs/Text, VBScript, Batch/Cmd.

## report-bug

Generate a diagnostic report for bug filing. The report includes version info, rclone details, sanitized config summary, state DB summary, and the last 30 log lines (with user paths redacted).

```
smirror report-bug              # write to file (smirror-bug-report-TIMESTAMP.txt)
smirror report-bug --stdout     # print to console
smirror report-bug --open       # print to console + open GitHub issue form in browser
```

Remote paths are redacted to show only the remote name (e.g., `gdrive:<REDACTED>`). User home paths are replaced with `<USER_HOME>`.

## version

Show the smirror version.

```
> smirror version
smirror 1.0.0
```


# 6. Delete Policies

The `delete_policy` configuration field controls what happens on the remote when a file is deleted locally. There are three policies:

## ignore (default)

```yaml
delete_policy: ignore
```

Local deletions are **not propagated** to the remote. The remote retains its copy of the file. This is the safest option -- the remote acts as an append-only archive.

**rclone mapping**: Full-project syncs use `rclone copy` (upload-only, never deletes remote files). Single-file delete events are logged but no rclone command is executed.

**Use case**: Backup scenarios where you want to keep deleted files recoverable on the remote.

## mirror

```yaml
delete_policy: mirror
```

Local deletions **are propagated** to the remote. The remote is made to mirror the local state exactly.

**rclone mapping**:

- Full-project syncs use `rclone sync` (uploads new/changed files and deletes remote files not present locally).
- Single-file deletes use `rclone deletefile <remote-path>`.
- Directory deletes iterate over all synced files under the directory and delete each one individually.

**Use case**: Maintaining an exact mirror of your working directory. Suitable when the remote is a staging area or deployment target.

## quarantine

```yaml
delete_policy: quarantine
quarantine_days: 30
```

Local deletions cause the remote file to be **moved** to a `.quarantine/` directory on the remote, with a timestamp suffix. Files are not permanently deleted.

**rclone mapping**: Uses `rclone moveto <remote-path> <remote>/.quarantine/<relative-path>.<timestamp>`.

The timestamp format is `YYYYMMDDTHHMMSSZ` (UTC), for example:

```
.quarantine/src/old-module.go.20260327T100000Z
```

**Use case**: Cautious mirroring where you want deletions to propagate but with a safety net for recovery.

### Rename Handling

When a file is renamed, SelectiveMirror detects this as a delete + create pair. Regardless of the configured delete policy, the old remote path is **always deleted** (force-delete) to prevent orphaned copies. The new path is synced as a normal file creation.


# 7. Monitoring

## status.json

While running, SelectiveMirror writes a `status.json` file to the data directory (same directory as the state DB, typically `~/.selectivemirror/`). This file is updated at each heartbeat interval (default 5 minutes).

### Fields

```json
{
  "version": "1.0.0",
  "uptime": "2h15m30s",
  "start_time": "2026-03-27T08:00:00Z",
  "last_scan_time": "2026-03-27T10:10:00Z",
  "queue_depth": 0,
  "files_synced": 142,
  "metadata_synced": 18,
  "bytes_uploaded": 5242880,
  "sync_errors": 2,
  "avg_sync_latency_ms": 350,
  "projects": {
    "MyProject": {
      "last_sync": "2026-03-27T10:12:00Z",
      "last_error": "",
      "last_error_time": ""
    }
  },
  "generated_at": "2026-03-27T10:15:00Z"
}
```

| Field | Description |
|---|---|
| `version` | SelectiveMirror version |
| `uptime` | Time since process start |
| `start_time` | Process start timestamp (UTC) |
| `last_scan_time` | Timestamp of the last startup reconciliation |
| `queue_depth` | Number of tasks currently waiting in the sync queue |
| `files_synced` | Total number of file content uploads since start |
| `metadata_synced` | Total number of metadata-only (mtime) updates since start |
| `bytes_uploaded` | Total bytes uploaded since start |
| `sync_errors` | Total number of failed sync operations since start |
| `avg_sync_latency_ms` | Average sync duration in milliseconds |
| `projects` | Per-project status with last sync time and last error |
| `generated_at` | Timestamp when this status snapshot was written |

## Heartbeat Mechanism

The heartbeat loop runs on a configurable interval (default 5 minutes) and performs:

1. Writes the current UTC timestamp to the `last_heartbeat` key in the state database.
2. Writes the timestamp to `heartbeat.txt` in the data directory.
3. Writes `status.json` with current metrics.
4. Logs the current filesystem watch count for diagnostics.
5. Records any runtime health errors to the state database.

## Periodic Reconciliation

On a separate timer (default 5 minutes, configured by `reconcile_interval_sec`), the engine queues a full-project sync for every project. This catches changes that the filesystem watcher may miss, such as:

- Files modified via WSL or network shares.
- Changes made by external tools that do not trigger `ReadDirectoryChangesW`.
- Edge cases in filesystem notification delivery.

## Ghost Scan

After the startup reconciliation completes, a background goroutine runs a ghost scan. This compares remote files against the local filesystem and identifies:

- **Orphans**: Files on the remote that do not exist locally (e.g., from renames or manual deletions while smirror was not running).
- **Leaks**: Files that are excluded by filter rules but still exist on the remote (e.g., from a rule added after initial sync).

Ghost scan results are stored in the state database and displayed by `smirror status`. The scan does not auto-delete anything -- it is diagnostic only.


# 8. Verifying Integrity

The `smirror test-mirrors` command (with aliases `verify` and `doctor`) performs a comprehensive comparison between local and remote state as part of its diagnostic checks.

```
smirror test-mirrors              # all mirrors
smirror test-mirrors MyMirror     # one mirror
```

## How It Works

1. **Remote listing**: Calls `rclone lsjson <remote> --recursive --hash --no-mimetype --no-modtime` to get a complete file listing with MD5 hashes.

2. **Local walk**: Walks the local project directory, respecting `.syncignore` filters.

3. **Comparison**: For each local file that is not excluded:
   - If the file does not exist on the remote: reports **MISSING REMOTE**.
   - If the file exists on remote and MD5 hashes are available: compares hashes. If they differ: reports **HASH MISMATCH**.

4. **Orphan detection**: For each remote file (excluding `.quarantine/`):
   - If the file does not exist locally and is not excluded: reports **ORPHAN REMOTE**.
   - If the file is excluded locally but exists on remote: reports **LEAK**.

## Example Output

```
=== Verify: MyProject ===
Local:  C:\Projects\MyProject
Remote: gdrive:backup/MyProject

  MISSING REMOTE: src/new-file.go
  HASH MISMATCH: src/utils.go (local=a1b2c3d4 remote=e5f6a7b8)
  ORPHAN REMOTE: old/deprecated.go (not in local tree)
  3 drift issues found (1250ms)

Total drift: 3 files
```

When no drift is detected:

```
  No drift detected (245 local files, 245 remote files, 980ms)

No drift detected.
```


# 9. Diagnostics

## smirror test-mirrors

The `test-mirrors` command (aliases: `doctor`, `verify`) runs diagnostic checks. Each check reports OK or FAIL with timing:

### Check Details

| # | Check | What It Tests |
|---|---|---|
| 1 | Config file parses | YAML syntax, required fields, type correctness |
| 2 | All project paths exist | Every `local_path` is a directory accessible on disk |
| 3 | No duplicate project names | All project `name` values are unique |
| 4 | rclone binary found | rclone can be located via configured path, PATH, or common install locations |
| 5 | rclone version compatibility | Version is 1.73+ (full), 1.50+ (partial), or incompatible |
| 6 | Remote reachable | `rclone lsd <remote> --max-depth 0` succeeds for each project |
| 7 | State DB opens and schema valid | SQLite database can be opened and schema is correct |
| 8 | State DB integrity check | SQLite `PRAGMA integrity_check` returns "ok" |
| 9 | Log file writable | Log file can be opened for appending |
| 10 | Single-instance lock available | No other smirror instance is running |
| 11 | Filesystem watcher available | `fsnotify` watcher can be created for a project directory |
| 12 | Write permissions on project dirs | A test file can be written to and removed from each project directory |
| 13 | Filter engines load without error | All `.syncignore` files parse correctly |

### rclone Version Compatibility

| Version | Level | Notes |
|---|---|---|
| 1.73+ | Full | All features supported, including `--skip-links` |
| 1.50--1.72 | Partial | `--skip-links` unavailable; symlinks may leak to remote |
| Below 1.50 | Incompatible | Missing critical subcommands; SelectiveMirror will refuse to start |


# 10. Multi-Backend Setup

SelectiveMirror works with any rclone backend. Below are complete setup examples for common providers.

## Google Drive

### rclone Configuration

```
rclone config

Name: gdrive
Type: drive
Scope: drive.file
```

This creates a `gdrive` remote. Complete the OAuth flow in your browser when prompted.

### config.yaml Entry

```yaml
mirrors:
  - name: Research
    local_path: C:\Research
    remote: gdrive:SelectiveMirror/Research
    debounce_sec: 5
    max_file_size_mb: 500
```

### Notes

- Google Drive supports MD5 hashes, so `--checksum` works natively.
- API rate limits apply. If you see exit code 5 (temporary error), consider reducing `sync_workers` to 2 and adding `--tpslimit 10` to `rclone_extra_flags`.

## Amazon S3

### rclone Configuration

```
rclone config

Name: s3
Type: s3
Provider: AWS
Access Key ID: <your-key>
Secret Access Key: <your-secret>
Region: us-east-1
```

### config.yaml Entry

```yaml
mirrors:
  - name: Codebase
    local_path: C:\Projects\Codebase
    remote: s3:my-backup-bucket/codebase
    max_file_size_mb: 1000
```

### Notes

- S3 uses MD5-based ETags for standard uploads, so checksum verification works for files under 5 GB.
- For multi-part uploads (large files), ETags are not MD5 hashes. Consider setting `max_file_size_mb` to avoid very large files.

## SFTP Server

### rclone Configuration

```
rclone config

Name: myserver
Type: sftp
Host: backup.example.com
User: backupuser
Port: 22
Key File: C:\Users\raveh\.ssh\id_ed25519
```

### config.yaml Entry

```yaml
mirrors:
  - name: Website
    local_path: C:\Projects\Website
    remote: myserver:/home/backupuser/website-mirror
    debounce_sec: 3
```

### Notes

- SFTP does not support server-side hashing by default. `smirror verify` may report that hash comparison is unavailable. Full-project syncs with `--checksum` will compute hashes locally and compare against cached metadata.
- Consider adding `--sftp-set-modtime=false` to `rclone_extra_flags` if the server does not support modification time updates.

## Local Filesystem Mirror

### rclone Configuration

No rclone configuration needed. Local paths are used directly.

### config.yaml Entry

```yaml
mirrors:
  - name: LocalBackup
    local_path: C:\Projects\Important
    remote: D:\Backups\Important
    debounce_sec: 2
```

### Notes

- This creates a mirror on a second drive (e.g., an external USB drive). SelectiveMirror watches the source and copies changed files to the destination.
- MD5 checksums work natively on local paths.

## Dropbox

### rclone Configuration

```
rclone config

Name: dropbox
Type: dropbox
```

Complete the OAuth flow in your browser when prompted.

### config.yaml Entry

```yaml
mirrors:
  - name: Documents
    local_path: C:\Documents\Work
    remote: dropbox:SelectiveMirror/Documents
    debounce_sec: 5
```

### Notes

- Dropbox supports content hashing, but uses its own hash algorithm (not MD5). rclone handles the translation transparently.
- Dropbox has a default upload size limit of 150 MB per file. Set `max_file_size_mb: 150` to avoid failures on large files.


# 11. Troubleshooting

## rclone Exit Codes

| Code | Meaning | Suggested Action |
|---|---|---|
| 0 | Success | No action needed |
| 1 | Syntax/usage error | Check `rclone_extra_flags` for invalid flags |
| 2 | Uncategorized error | Check rclone logs; may be a permissions issue |
| 3 | Directory not found | Remote path may not exist; check `remote` config |
| 4 | File not found | File may have been deleted between detection and sync |
| 5 | Temporary error | Transient network/API issue; retries should handle it |
| 6 | Less serious errors | Some transfers failed with `--ignore-errors`; check logs |
| 7 | Fatal error | Permanent failure; check credentials or backend status |
| 8 | Transfer limit reached | rclone's `--max-transfer` limit hit (if configured via extra flags) |
| 9 | Success, no transfers | All files already up to date |
| 10 | Duration limit reached | rclone's `--max-duration` limit hit (if configured via extra flags) |
| -1 | Exec failure | rclone binary not found or could not be started. Run `smirror test-mirrors` |
| -2 | Timeout (5 min) | Single operation exceeded 5-minute deadline. May indicate a very large file or network stall |

## Common Errors and Solutions

### "another smirror instance is already running"

Only one instance of smirror can run at a time. The lock file is located in the data directory.

**Solutions**:

- Check if another smirror process is running: `tasklist | findstr smirror`
- If the previous process crashed without releasing the lock, the lock file may be stale. Run `smirror test-mirrors` to check, then delete the lock file manually if needed.

### "rclone not found"

SelectiveMirror searches for rclone in this order:

1. The `rclone_path` value in config (if absolute).
2. System PATH (`where rclone`).
3. Common install locations: `%ProgramFiles%\rclone`, WinGet links, Chocolatey, Scoop.

**Solutions**:

- Install rclone: `winget install Rclone.Rclone`
- Set `rclone_path` to the full path: `rclone_path: C:\Program Files\rclone\rclone.exe`

### "remote unreachable" during test-mirrors

The remote is not accessible. This could mean:

- OAuth token has expired (Google Drive, Dropbox). Run `rclone config reconnect <remote>:` to re-authenticate.
- Network is down or firewall is blocking the connection.
- The remote path does not exist and the backend does not auto-create directories.

### Files not syncing (no errors in log)

1. Check if the file is excluded: `smirror explain <project> <path>`
2. Check if the file exceeds `max_file_size_mb`: `smirror explain` reports this.
3. Check if the content hash matches the last sync (no-op deduplication): set `log_level: debug` and look for "unchanged" messages.
4. Check debounce: if you are saving very rapidly, the debounce timer resets each time. Wait `debounce_sec` seconds after the last save.

### High sync latency or slow transfers

- Reduce `sync_workers` if the backend has API rate limits.
- Set `bandwidth_limit` to prevent saturating your connection.
- Increase `debounce_sec` to reduce the frequency of individual file syncs.
- Check if large files are being synced unnecessarily: set a lower `max_file_size_mb`.

### State database corruption

If `smirror test-mirrors` reports a state DB integrity failure:

1. Stop smirror.
2. Delete or rename the state database file (default: `~/.selectivemirror/state.db`).
3. Restart smirror. A new database will be created, and the startup reconciliation will re-sync all files.

No data is lost on the remote -- only the local tracking state is reset.

## Log Analysis Tips

The log file (default: `~/.selectivemirror/selectivemirror.log`) uses structured logging with `slog`. Key fields to look for:

- `component=sync` -- sync engine messages (file copies, errors, skips).
- `component=watcher` -- filesystem event messages.
- `project=<name>` -- filter by project.
- `exit=<code>` -- rclone exit codes for failed operations.
- `ms=<duration>` -- operation duration in milliseconds. Values above 5000 are logged as warnings ("slow task").

To find recent errors:

```powershell
Select-String -Path "$env:USERPROFILE\.selectivemirror\selectivemirror.log" `
  -Pattern "ERROR|WARN|FAIL" | Select-Object -Last 20
```

## WSL-Specific Issues

If your project directory is on a WSL filesystem (e.g., `\\wsl.localhost\Ubuntu\...`):

- The Windows `ReadDirectoryChangesW` API does not monitor Plan 9 (P9) filesystem mounts reliably. File events from WSL-side operations may be missed.
- The periodic reconciliation (`reconcile_interval_sec`) is your safety net. It catches all changes regardless of how they were made.
- Consider setting a shorter reconciliation interval (e.g., 60 seconds) for WSL-hosted projects.
- Do not use symlinks that cross the WSL/Windows boundary in watched directories.
