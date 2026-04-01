# Changelog

All notable changes to SelectiveMirror are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [semver](https://semver.org/).

## [0.4.0] — Unreleased

### Added

- **Ghost cleanup**: `sync-now` automatically removes LEAKs (excluded files still on remote) and ORPHANs (remote-only files with no local counterpart) after syncing (SM-052)
- **Ghost preview**: `dry-run` shows what ghost files would be cleaned without executing
- **Task completion callback**: `Done func()` on sync tasks enables WaitGroup-based coordination
- **ListRemoteFunc**: injectable remote lister for testability (same pattern as RcloneRunner)
- 28 new unit tests for ghost detection, cleanup, dry-run preview, and task completion (259 total)
- Test-driven bug discovery policy added to BugTracker

### Changed

- `reconcileAll` uses WaitGroup to wait for actual sync completion before ghost scan, replacing hardcoded 30-second sleep (SM-054)
- `cmdSyncNow` resolves target projects once and reuses for both sync and ghost cleanup
- `.goreleaser.yaml`: CGO_ENABLED=0 (matches pure-Go SQLite — no CGo needed)
- Documentation: all `smirror doctor`/`verify`/`stats` references updated to primary names `test-mirrors`/`project-stats`
- Installation manual: version references updated, Windows Service section rewritten with actual instructions (was "planned for v2.0")
- SECURITY.md: version support table updated

### Fixed

- **Verify double-counted LEAKs** (SM-053): excluded files were counted once during local walk and again during remote iteration, inflating drift totals. Fixed with `leaksCounted` deduplication set in both `verifyProject` and `verifyProjectQuiet`
- **Auto-verify missing LEAK distinction** (SM-055): `verifyProjectQuiet` logged all remote-only files as "orphan remote" without checking filter exclusion. Now correctly distinguishes LEAKs from ORPHANs
- **Ghost scan race condition** (SM-054): `scanForGhosts` in service startup could run before reconciliation finished. Replaced `time.Sleep(30s)` with `sync.WaitGroup` coordination
- **Duplicate FindProject call** in `cmdSyncNow`: hoisted project resolution to avoid redundant lookup and potential nil dereference

---

## [0.3.0] — 2026-03-30 (retracted)

Retracted due to service crash-loop caused by corrupted config and os.Exit in service code path. All changes included in 0.4.0.

### Added

- **OSS Polish (Phase 4)**: CONTRIBUTING.md, SECURITY.md, PR template, winget manifest
- config.example.yaml documents all config fields (sync_workers, reconcile_interval_sec, syncignore_path)

### Changed

- README.txt merged into README.md (single source of truth); all 6 references updated
- CI workflows use Go 1.26.1 (matches go.mod)
- MSI installer version aligned (Variables.wxi + build-msi.ps1)
- Command aliases documented in README.md and help output

### Fixed

- Stale dependency entries removed from CREDITS.md and THIRD-PARTY-LICENSES.txt (hashicorp/golang-lru)
- Stale command references in docs (validate → test-mirrors, doctor → test-mirrors, stats → project-stats)
- `project-stats` output banner said "smirror stats" instead of "smirror project-stats"
- test_install.ps1: removed PDF checks (not yet built), fixed PATH trailing-backslash comparison

### Removed

- README.txt (merged into README.md)

---

## [0.2.25-dev] — 2026-03-29

### Bugs fixed

- **State DB and log written to wrong directory when running as Windows service** (0.2.8)
  `DefaultDataDir()` used `os.UserHomeDir()` which resolves to SYSTEM's home (`C:\Windows\System32\config\systemprofile\.selectivemirror\`) when running as a service. State DB, log, and lock file were invisible to the user session. Fixed by deriving data directory from the config file's own directory.

- **Relative config path produced CWD-dependent data paths** (0.2.9)
  `filepath.Dir("config.yaml")` returns `"."`, making state DB and log relative to CWD. The Windows service has CWD=`C:\Windows\System32`, so paths broke silently. Fixed by resolving config path to absolute at the top of `config.Load()`.

- **Log file held with exclusive lock on Windows** (0.2.16)
  `os.OpenFile` on Windows opens with no share mode by default. Every `Get-Content -Wait` (PowerShell log tail) would block indefinitely, spawning zombie PowerShell processes. Fixed by using `syscall.CreateFile` with `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE`.

- **rclone not found when running as Windows service** (0.2.10, 0.2.13)
  SYSTEM account has a different PATH. `exec.LookPath("rclone")` failed. Fixed by auto-resolving `rclone_path` to an absolute path during `smirror service install` and writing it into config.yaml.

- **rclone remotes not found when running as Windows service** (0.2.21)
  SYSTEM has its own `%APPDATA%` with no `rclone.conf`. All sync operations failed with exit code 1 (no remotes configured). Fixed by adding `rclone_config` support — all rclone calls now pass `--config` when set. Auto-resolved during `smirror service install`.

- **PID unreadable from lock file** (0.2.1, replaced in 0.2.2)
  `LockFileEx` locks file bytes, preventing even shared reads. `IsLocked()` couldn't read the PID written inside the lock file. Initially tried a `.pid` sidecar file (fragile). Replaced with SQLite state DB approach — `smirror start` writes `instance_pid`, `instance_exe` to the state DB, which is always readable.

- **Service didn't write instance info to state DB** (0.2.10)
  `serviceMain` never called `SetMeta("instance_pid", ...)`, so `smirror status` showed "instance running:" with no PID or path. Fixed by adding the same instance info writes as foreground mode.

- **Machine account displayed as username** (0.2.20)
  When running as LocalSystem, `user.Current()` returns the machine account (e.g., `MSI\MSI$`), not `NT AUTHORITY\SYSTEM`. Users saw cryptic `MSI$` as the username. Fixed by detecting the trailing `$` and displaying `SYSTEM (LocalSystem)`.

- **Timestamps inconsistent between commands** (0.2.1)
  `smirror status` showed UTC (Zulu time) while `smirror sync-now` showed local time. Fixed all user-facing timestamps to use `.Local().Format(time.RFC3339)`.

- **Stale command references** (0.2.1)
  Error messages and help text referenced deleted commands (`smirror doctor`, `smirror verify`). Fixed throughout to reference `smirror status` and `smirror test-mirrors`.

- **test-mirrors log-writable check conflicted with running instance** (0.2.17)
  The check opened the log file with `os.OpenFile` (exclusive on Windows), which would fail if the service held it open. Fixed by using shared file open.

- **test-mirrors failure summary not shown** (0.2.22)
  With 15+ checks, a single failure scrolled off screen. Only `"15 passed, 1 failed"` was visible at the bottom. Fixed by repeating all failure details after the summary.

- **Running instance reported as test failure** (0.2.25)
  The single-instance lock check counted a running smirror as a "failed" check, even though that's the normal operating state. Changed from pass/fail to informational.

- **`report-bug` showed duplicate version line** (0.2.3)
  The new version header printed before every command duplicated the version already in the bug report output.

- **Bundled rclone created version drift risk** (0.2.14)
  Release ZIP and MSI bundled a copy of rclone.exe alongside the user's own winget-installed copy, creating two potentially different versions. Removed bundled rclone; declared as prerequisite.

### Added

- Windows Service: full SCM integration (`smirror service install/uninstall/start/stop`)
- `smirror status` shows instance mode (foreground/service), user, PID, executable path, start time
- `smirror service install` auto-resolves `rclone_path` and `rclone_config` into config.yaml
- `smirror service start` prints log tail command that works in both cmd and PowerShell
- Version header printed at start of every command
- Copyright line in `smirror version`
- MSI installer with post-install rclone download (winget or direct from rclone.org)
- `rclone_config` config field for explicit rclone.conf path
- `config.RcloneArgs()` helper for consistent `--config` flag injection

### Changed

- `projects:` renamed to `mirrors:` in config (internal Go identifiers unchanged)
- `validate` renamed to `test-mirrors`
- `doctor` and `verify` merged into `test-mirrors` (kept as hidden aliases)
- `stats` renamed to `project-stats` (kept as hidden alias)
- rclone is a declared prerequisite, not bundled
