# Changelog

All notable changes to SelectiveMirror are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [semver](https://semver.org/).

## [Unreleased]

### Documentation / ISO compliance

- **`docs/iso-compliance.md` baseline added** (v0.3). Single source of truth for compliance status against ISO/IEC/IEEE 29148:2018, ISO/IEC 25010:2023, ISO/IEC 25023:2016, and ISO/IEC/IEEE 29119 family (Parts 1-4). 63 action items registered with priority and owner. v1.0 ships with **Partial ISO compliance** disclosed; SELF-ASSESSMENT label retained. External independent review committed for v1.0.1.
- **`docs/SRS.md` revised to v1.1**. Added §4.0 schema-deviation note (NFR section uses 25010:2011 layout; 25010:2023 mapping documented). Added ISO/IEC/IEEE 29119:2023 to §1.4 Applicable Standards (was missing — see SM-154). Cross-link added to `docs/iso-compliance.md`.
- **NFR target revisions (SM-153)**: NFR-TB-01 detection latency 50ms p99 → 100ms p99 (target loosened with rationale). NFR-TB-02 sync latency 3s p95 → 5s p95. NFR-RU-03 idle CPU 0.5% → 1%. NFR-RU-01 idle memory: target 25 MB retained but Status changed from "Met (at 30 MB)" to **Not Met** (target stays as v1.1 optimization goal). The "Met at [looser value]" standards-gaming framing is eliminated from the SRS.
- **NFR-TE-01 status updated** to disclose `internal/watcher/` coverage gap (16.6% statement; 15 of 20 functions at 0%). Refactor for testability (X-04) deferred to v1.0.1, ETA 2026-05-06.
- **`docs/VV-Plan.md` cross-link** to `docs/iso-compliance.md` added (§2). Pre-existing V&V conflation in §1.1 (integration tests mis-categorized under Validation) filed as SM-152 — fix pending.
- BugTracker entries: SM-152 (V&V conflation, open), SM-153 (NFR Status standards-gaming, fixed in this commit), SM-154 (SRS §1.4 missing 29119 reference, fixed in this commit).

### Breaking (CLI)

- **`smirror addmirror --backup` removed.** The flag, the interactive `[b] Backup` menu option, and the `backupDestination` rotation logic (`<dest>` → `.bak` → `.bak.2`) are gone. smirror no longer manages backups of pre-existing destination content. If the destination already has files, addmirror aborts unless `--delete` is set; clean the destination manually and retry otherwise. Interactive conflict menu is now `[d] Delete / [a] Abort`. Unknown flags are rejected cleanly instead of being treated as positional paths.

### Added

- **`smirror unmirror --purge-remote` flag.** Deletes the remote directory for the mirror being removed. Only `<remote>` itself is purged; sibling `.bak` / `.bak.2` directories (if any) are left alone — smirror does not own them. Local paths purge via `os.RemoveAll`; rclone remotes via a new `rclone.Purge` helper (with `ErrRemoteNotFound` sentinel for missing paths).
- **State DB auto-cleanup on unmirror.** `unmirror` now always removes the mirror's rows from `sync_state` and `sync_log` via a new `state.DeleteProject` helper — regardless of `--purge-remote`. Previously stale rows lingered until the daemon's next startup, and `sync_log` rows were never swept by `PruneOrphanedProjects`.
- **Friendlier config error for the mirrors-as-map mistake.** When a user comments out all `- name: ...` entries and leaves an indented sibling field under `mirrors:`, the cryptic yaml.v3 `"cannot unmarshal !!map into []config.Project"` is now wrapped with an explanation: mirrors must be a list, why commented-out entries cause the misparse, and an example of the correct shape.

### Fixed

- **`cfg.RclonePath` now honored by every rclone-using command.** Previously, `addmirror`, `smirror remote`, `smirror report-bug`, and `smirror selfupdate --include-rclone` passed `""` to `rclone.Detect`, ignoring the configured `rclone_path` and failing with "rclone not found in PATH" even when a valid path was set in `config.yaml`. A new shared `loadConfigBestEffort` helper tries `Load` then `LoadRaw`, so the configured path is read even when the config fails full validation (e.g. no mirrors yet).

## [0.9.0] — 2026-04-18

The deployment-model and security-hardening release. Not backward-compatible with the 0.2.x–0.8.x MSI flow: fresh install required (perMachine → different install path; no auto-service-install).

### Deployment model (breaking for MSI users; no wire/format breaks)

- **New per-user Scheduled Task mode** (recommended default). `smirror task install/uninstall/start/stop/status`. Registers via `schtasks.exe` with an XML task definition (schema 1.2, Win 7+). Trigger: at user logon. Principal: current user, InteractiveToken, LeastPrivilege. Restart-on-failure 3x PT1M. **No admin required** — users own their own tasks. Data files stay user-owned, so `smirror clean --self` reverts everything without UAC. New package: `internal/task/` with runner indirection for test injection. 25 new tests.
- **MSI flipped to `perMachine` + `ProgramFiles64Folder` + HKLM** (SEC-C2 fix). Binary is no longer in user-writable `%LOCALAPPDATA%`; standard users cannot replace `smirror.exe`. Each component's KeyPath is now its file with `Guid="*"` for WiX auto-generation — eliminates the prior hand-rolled-GUID collision class that caused uninstall to leave files behind.
- **MSI no longer auto-registers a service** as a side effect of install. Background registration is an explicit user step (`smirror task install` recommended, `smirror service install` for 24/7 admin-only mode).
- **SEC-C5 widened**: admin-owned-config gate for service mode is always-on (previously only when hooks configured). `smirror service install` refuses at install time if config isn't admin-owned; service re-checks at startup as defense-in-depth. Remedy: move config to `%ProgramData%\SelectiveMirror\config.yaml`, or use task mode instead.
- **MSI version propagation fixed**. `ProductVersion` in the MSI now tracks the git tag (or `cmd/smirror/main.go` for local builds), flowing through `build-msi.ps1` → `/p:Version` → wixproj `DefineConstants` → `Variables.wxi`. Previously every MSI advertised 0.8.0 regardless of the bundled binary version.

### CLI

- `smirror clean` replaced its old alias-for-`service stop uninstall --clean` with an explicit plan-and-confirm flow:
  - `--self` (default): remove current user's task + `~/.selectivemirror/`. No admin required.
  - `--all`: `--self` plus Windows Service uninstall + `%ProgramData%\SelectiveMirror`. Admin for service parts.
  - Prints a preview of what will be removed; prompts for confirmation unless `--yes`.
- `smirror task <action>` new command family. Actions: `install`, `uninstall`, `start`, `stop`, `status`.

### Security (critical audit findings)

- **SEC-C1 (SM-147)**: Supply-chain hardening for `git-pkgs/gitignore v1.1.1`. Audited source (874 LOC, stdlib-only). CI runs `go mod verify` on every build. Dependency-upgrade policy added to CONTRIBUTING.md.
- **SEC-C2 (SM-152)**: MSI LPE via binary-replace. Fixed by perMachine + `ProgramFiles64Folder` (this release).
- **SEC-C3 (SM-144)**: Webhook path sanitizer missing in service mode. Fixed; webhook payloads now redact home-dir paths in both foreground and service modes.
- **SEC-C4 (SM-145)**: Webhook SSRF hardening. Sender now rejects non-HTTPS URLs, blocks private/loopback/link-local/CGNAT IP ranges, disables HTTP redirects, and re-checks the resolved IP inside the TCP DialContext (DNS-rebind-resistant). 12 new tests.
- **SEC-C5 (SM-146)**: Hook injection hardening. Admin-owned-config gate for service-mode installs (`internal/config/acl_windows.go` + `acl_other.go`). `SMIRROR_*` environment values are rejected if they contain shell metacharacters (`& | < > " ^ $ \` ( ) ;` or control chars) before hook spawn. 19 new tests.

Outstanding from the audit: 11 HIGH findings (rclone flag allowlist, copyto TOCTOU, NTFS junctions, ACL-on-Windows accuracy, code signing, OAuth tokens in stderr logs, SHA256 on rclone download, etc.) and 16 MEDIUM / 5 LOW deferred to post-0.9.0.

### CI / release pipeline

- `release.yml` now runs `installer/smoke-test.ps1` between MSI build and upload. SEC-C2 regressions, registry-scope regressions, service-install regressions, version-propagation regressions, and uninstall-leftover regressions all block the release.
- `installer/smoke-test.ps1` is the new regression harness. 16 invariants covering MSI tables, install side, task round-trip, uninstall cleanup. Runs idempotently with self-cleanup in phase 0.

### Dependencies / toolchain

- **SM-148: SQLite driver swap**. `modernc.org/sqlite` → `github.com/mattn/go-sqlite3 v1.14.42`. Dependency tree collapsed from 13 transitive packages to 5 direct + 0 indirect. Binary grew from 17.6 MB → 23.5 MB (statically-linked SQLite C). Build requires a C toolchain (MinGW-w64 on Windows); end users still get a zero-dependency binary.
- **SM-151: Windows-only release pipeline**. `.goreleaser.yaml` dropped linux/darwin; `build-msi.ps1` flipped to `CGO_ENABLED=1`; `test/verify.ps1` cross-compile check reduced to `windows/amd64`. SRS NFR-AD-01/NFR-AD-03 and VV-Plan Portability row updated.

### Fixed

- **SM-149**: Three data races in watcher/notify/sync test code. All fixed using event-based synchronisation (channels / WaitGroups) per user preference — no timeout-based fixes.
- **SM-150**: Inverted system-validation test (`TestBugHunter_SyncIgnoreIsNotSynced` had asserted the wrong behavior for SM-125).
- SM-107 through SM-143: filter validation, batch `--max-size`, `sync-now` exit code, fail-open filter on parse error, UTF-8 BOM handling, trailing-space in `.syncignore`, ghost scan race, `report-bug --open` prefill, filter reload hardening, sync/status output, help-flag on every subcommand, Codex verification-suite fixes, validation-report fixes.

### Changed

- **SM-142**: Centralized runtime repo constants (`repoOwner` / `repoName` in `main.go`).
- `report-bug` title format improved.

### Verification state at release

- `go build`, `go vet`, `go mod verify`: clean.
- 15 packages pass unit tests (558+ cases, including 25 new in `internal/task/`). 65%+ coverage on `internal/` (gate 35%).
- Race detector clean across all 15 packages.
- 2 fuzz targets × 30s (18M+ execs): clean.
- system-validation: 61/61 goals.
- Integration tests (`test/run_tests.ps1`): 123/123 pass.
- MSI smoke test: 16/16 invariants pass.
- golangci-lint: clean modulo 2 documented gocyclo warnings (cmdStatus=64, cmdAddMirror=52).

---

## [0.7.0] — 2026-04-02

### Added

- **FR-ASP-17**: Pre/post-sync hook system. Shell commands run before and after per-file sync with environment variables (SMIRROR_PROJECT, SMIRROR_FILE, SMIRROR_REMOTE, SMIRROR_EVENT). Per-mirror and global config. 30s timeout. Errors are warnings, never block sync.
- Config: `pre_sync_hook`, `post_sync_hook` on both mirror and global level
- 5 new hook tests

### Fixed

- **SM-073**: `sync-now` acquires single-instance lock (prevents race with running service)
- **SM-074**: Stale health error cleared on service restart

---

## [0.6.0] — 2026-04-02

### Added

- **FR-ANOM-01/02**: Anomaly classification engine with 11 categories (Panic, CircuitBreaker, Watcher:Error, Queue:DepthWarning, Ghost:Leak/Orphan/Stale, Reconciliation:Stale, Path:Gone, Sync:Timeout, Sync:Failure)
- **FR-ANOM-03/04**: JSON-lines anomaly recording (anomalies-YYYY-MM-DD.jsonl) with automatic date rollover
- **FR-ANOM-05**: Causal hypothesis templates per anomaly kind
- **FR-ANOM-07**: Anomaly counts in metrics Status and status.json
- **FR-ANOM-08**: Path sanitization (home directory redacted before persistence)
- **FR-ANOM-10**: Anomaly file rotation (30 days, 50MB limit)
- Config: `anomaly_detection_enabled` (default true)
- 22 new anomaly tests
- **SM-072**: 4-category ghost taxonomy (LEAK, RETAINED, STALE, ORPHAN). RETAINED files no longer reported as drift.
- **SM-071**: Testable clock abstraction for debounce tests (18x faster watcher suite)
- **SM-069**: Auto-clean LEAKs when .syncignore filter rules change
- **SM-068**: Exit code 5 (ExitDrift) for test-mirrors drift detection
- SRS.md and VV-Plan.md committed to version control

### Changed

- `FairQueue.RecordFailure()` returns `bool` (circuit breaker just tripped)
- Circuit breaker trips emit `KindCircuitBreaker` anomaly
- Panic recovery in processTask emits `KindPanic` anomaly
- fsnotify errors emit `KindWatcherError` anomaly

---

## [0.5.0] — 2026-04-02

### Added

- **FR-ASP-06**: Per-mirror `delete_policy` and `quarantine_days` config overrides. Each mirror can set its own delete policy independent of the global setting
- **FR-SYNC-13**: Signal-based adaptive cooldown: `max(base*freq, syncDuration*1.5)` replaces fixed 30s cooldown
- **FR-SYNC-09**: Adaptive reconciliation intervals (doubles after 3 clean cycles, caps at 30min, resets on drift)
- **FR-SYNC-14**: Per-mirror `rclone_extra_flags` (appended after global flags)
- **FR-SYNC-16**: Transient retry on rclone exit codes 1 (general error) and 5 (temporary/rate-limit)
- **FR-DEL-07**: Atomic directory delete via `rclone purge` with fallback to per-file deletion
- **FR-DEL-09**: Quarantine auto-purge (expired files cleaned during reconciliation, per-mirror retention)
- **FR-FILTER-11**: Malformed .syncignore safety — saves/restores last-known-good rules on parse error
- **FR-QUEUE-08/10**: Unbounded queue (removed 10K artificial limit) with overflow callback at 50K
- **FR-ASP-16**: State DB auto-migration framework (numbered Go functions, idempotent)
- **FR-ASP-11**: Sync log pruning (30-day retention, cleaned during reconciliation)
- **FR-CLI-07**: Documented exit codes: 0=success, 1=error, 2=config, 3=rclone, 4=lock, 5=drift
- Pre-release SLA smoke test (`test/sla_smoke.ps1`): latency, integrity, throughput, memory checks
- CI coverage gate: build fails if coverage drops below 35%
- Lint warnings for unanchored negation patterns in .syncignore
- .syncignore documentation rewrite with anchoring guidance
- 62 new tests (347 total), 2 fuzz tests

### Changed

- **FR-DEL-01**: `delete_policy: mirror` renamed to `delete_policy: delete` with deprecation warning
- Rclone filter generator hoists global directory exclusions to top of filter file, enforcing gitignore excluded-parent constraint (SM-062)
- `test-mirrors` exits with code 5 (ExitDrift) for drift, code 3 (ExitRcloneError) for check failures (SM-068)
- Quarantine lsjson errors now distinguish "no quarantine dir" (debug) from real failures (warn) (SM-066)
- ExitRcloneError (3) used in preflight and test-mirrors exits (SM-067)
- Watcher: extracted pure functions (ClassifyEvent, ShouldSync, ComputeRelPath, IsSymlinkToDir, IsSyncIgnoreFile)
- All errcheck violations resolved; golangci-lint clean
- `*~` backup file pattern added to default global_excludes

### Fixed

- **SM-062**: Rclone filter excluded-parent constraint — unanchored `!hooks/*` could override global `.git/` exclusion, causing .git/hooks/ files to sync to remote
- **SM-065**: Exit code 5 (temporary error) was not retried, causing API rate-limited files to fail permanently
- **SM-066**: Quarantine auto-purge silently swallowed all lsjson errors, hiding auth/network failures
- **SM-067**: ExitRcloneError constant was declared but never used
- **SM-068**: test-mirrors conflated drift detection with rclone failure in exit code

---

## [0.4.0] — 2026-04-02

### Added

- **Ghost cleanup**: `sync-now` automatically removes LEAKs (excluded files still on remote) and ORPHANs (remote-only files with no local counterpart) after syncing (SM-052)
- **Ghost preview**: `dry-run` shows what ghost files would be cleaned without executing
- **FairQueue**: Dedup, move-to-back fairness, priority deletes, per-file cooldown
- **Circuit breaker**: Per-mirror exponential backoff on consecutive failures (SM-059, SM-060)
- **Task completion callback**: `Done func()` on sync tasks enables WaitGroup-based coordination
- **ListRemoteFunc**: injectable remote lister for testability (same pattern as RcloneRunner)
- 28 new unit tests for ghost detection, cleanup, dry-run preview, and task completion (285 total)
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
