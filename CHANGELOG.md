# Changelog

All notable changes to SelectiveMirror are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/). Versioning follows [semver](https://semver.org/).

## [Unreleased]

### Robustness / polish (panel review 2026-04-28 — last batch)

- **GAP-6 — `--config` last-wins.** `cmd/smirror/main.go::extractConfigPath`. Previous parsing broke out of the loop on the FIRST `--config` it saw and left subsequent `--config` args in the stripped slice — so `smirror --config bogus.yaml --config good.yaml version` used the bogus path and confused downstream parsers. Now extracted to a helper used by both cliMain and serviceMain: scans all args, last `--config` wins, ALL occurrences removed from the result. Mixed separate-form (`--config X`) and `=`-form (`--config=X`) handled. 8 sub-cases in `cmd/smirror/config_args_test.go`.
- **PF-A8 — async `OnRecord` callback (anomaly recorder).** `internal/anomaly/anomaly.go::Recorder`. Previously OnRecord ran synchronously inside `Record()`, so a slow webhook (HTTP timeout up to 5s) blocked the sync engine for the full timeout per anomaly. Now Record enqueues to a bounded channel (size 64) drained by a dedicated goroutine that calls OnRecord with panic recovery. Overflow drops the callback (writer.Write path is unchanged — on-disk record is preserved); first overflow records a `Queue:DepthWarning` anomaly so the operator sees the alerting stream is degraded; subsequent drops are counted in `DroppedCallbacks()`. `Close()` closes the channel and waits for the goroutine to drain. Regression test `TestRecord_DoesNotBlockOnSlowCallback` enqueues 70 anomalies against a blocked callback and asserts the loop completes in < 2s with non-zero drops.

### Security / robustness (panel review 2026-04-28 — service-mode hardening)

- **PF-A3 / audit SEC-H5 — service-mode default-rejects symlinks-to-files.** `internal/sync/sync.go::quiesceFile`. Service mode (LocalSystem) running with the previous follow-symlink behavior would happily sync a symlink in a watched directory targeting `C:\Windows\System32\config\SAM` — exfiltrating the SAM hive to the configured remote. Foreground / per-user-task mode is unchanged (legitimate monorepo / dotfiles use). New `Engine.RejectSymlinkedFiles` field; serviceMain sets it to true unconditionally. POSIX test added (Windows symlink creation requires elevated privilege; skip on CI runners that lack it).
- **GAP-7 — `state.Open` refuses forward-version state DBs.** `internal/state/state.go`. Previously, downgrading the binary (newer 0.9.20 wrote schema 17 → user runs older 0.9.12 binary that knows only 0..12) used to silently skip the missing migrations and operate at the older schema, with undefined behavior on rows the newer binary wrote. Now Open reads `meta.schema_version` BEFORE running migrations and refuses with a clear error: `"state DB schema version %d is newer than this binary supports (%d). Upgrade smirror or restore an older state DB."` Unit test `TestOpen_RefusesOnForwardSchemaVersion` added.

### Security (panel review 2026-04-28 — config validation hardening)

- **GAP-1 (Critical) — `rclone_extra_flags` denylist.** `internal/config/config.go::validateRcloneExtraFlags`. The list is appended verbatim into every rclone invocation; an attacker (or an honest typo) that lands `--rc --rc-addr 0.0.0.0:5572 --rc-no-auth` exposed an unauthenticated rclone control plane on the network — full filesystem access as the smirror principal (LocalSystem in service mode). `--log-file` enabled arbitrary file overwrite. `--config` swapped the rclone backend out from under smirror. Now rejected at config load: any flag starting with `--rc*` (the entire remote-control plane), plus `--log-file`, `--log-format`, `--config`, `--password-command`, `--ask-password` — both global `rclone_extra_flags` and per-mirror lists checked. Both separate-form (`--flag value`) and `=`-form (`--flag=value`) caught.
- **GAP-2 (High) — `rclone_config` path validated at config load.** `internal/config/config.go::Validate`. A bogus or non-regular `rclone_config` path was previously accepted and only failed at first sync. Combined with GAP-1 it was a backend-pivot vector. Now `os.Stat`'d at load time; non-regular files (directories, devices, symlinks-to-non-files) rejected.
- **GAP-3 (Medium) — overlapping mirror local_paths rejected.** `internal/config/config.go::validateNoLocalPathOverlap`. Configuring `parent: C:\Project` and `child: C:\Project\Sub` would have fired both watchers on every event under `Sub/`, double-syncing files and burning 2× API quota with non-deterministic remote convergence. Now rejected at load — pairwise prefix check after `filepath.Abs` + case-insensitive comparison (Windows-correct). Same path under different names also rejected.
- **GAP-4 (Medium) — drive-root and system-dir local_paths rejected.** `internal/config/config.go::isUnsafeLocalPath`. `local_path: C:\` would have recursed across the entire volume, exhausting `ReadDirectoryChangesW` buffers and starving fsnotify. Now rejected with a friendly hint pointing the user at a sub-directory. Also rejects `%SystemRoot%`, `%ProgramFiles%`, `%ProgramFiles(x86)%`, `%ProgramData%`, `%windir%`, and POSIX `/`.
- **GAP-5 (Low, defense-in-depth) — traversal-shaped remote paths rejected.** `internal/config/config.go::isUnsafeRemote`. `remote: local:../../etc` previously passed `Validate()` and only failed at first sync, leaving `status` output saying "OK" until then. Now rejected at load — the `..` segment is a typo or escape attempt either way; rclone's actual remotes never need traversal segments.

11 unit tests added across `internal/config/config_validation_test.go` covering the denylist (10 sub-cases for `--rc*`, `--log-file`, `--config`, `--password-command`, etc.), per-mirror flag rejection, missing/non-regular `rclone_config`, parent/child overlap, same-path different-names, drive-root rejection, system-dir rejection, normal-dir-allowed, traversal-remote rejection, and normal-remote-allowed.

### Fixed (panel review 2026-04-28 — quick wins)

- **BUG-1: `Validate()` now rejects case-only duplicate mirror names** (`internal/config/config.go:343`). On Windows (case-insensitive NTFS), `WorkProject` and `workproject` resolve to the same on-disk path and the same `state.sync_state` lookup key — accepting both as separate mirrors meant two watchers triggered on the same files and two FairQueue workers raced on the same DB rows. The dedup map is now keyed on `strings.ToLower(name)` and the error message identifies both the new and the conflicting name. Unit test `TestLoad_CaseOnlyDuplicateNames` added.
- **BUG-2: `cli_test.go` `TestCLI_ReportBug_FailureScenario/{VerifyContent,VerifyURLPrefill}` updated to match SM-164's privacy-honest output**. The tests asserted presence of user-chosen mirror names (`working-mirror`, `broken-mirror`) and accumulated counters (`sync_errors: 17`, `queue_depth: 3`, `files_synced: 142`) in the `report-bug --stdout` env section — but SM-164 deliberately replaced those with placeholder labels (`mirror_0:`, `mirror_1:`) and removed the Live Metrics block entirely (because `report-bug --open` posts the report to a public GitHub issue). Tests now assert the placeholder labels present AND user names + counters absent.

### Changed (P2 — rclone stall detection: replaces 5-minute wall-clock timeout)

The single hard `context.WithTimeout(ctx, 5*time.Minute)` wrapping every rclone subprocess is gone. It was wrong in both directions: too short for legitimate large-file transfers (a 4 GB file at 5 MB/s = 13 min, killed at 5 min) and too long for hung metadata operations. Replaced with a layered defense:

**Layer 1 — let rclone fail itself (primary).** `commonFlags` and `deleteFlags` now inject `--contimeout 30s --timeout 60s --low-level-retries 3` (and tighten existing `--retries 3 --retries-sleep 10s` semantics). rclone exits non-zero on persistent failure inside its own retry layer. New `injectFlagsAvoidingCollision` helper detects user-supplied overrides in `RcloneExtraFlags` (separate-form `--flag value` and `=`-form `--flag=value`) and skips injection for any name already present, with a debug log. `--low-level-retries` was dialed down from rclone's default 10 to 3 so worst-case in-rclone time stays bounded (~12 min) and doesn't compete with Layer 2's grace.

**Layer 2 — multi-signal stall backstop (rare cases).** `internal/sync/liveness.go` runs every rclone subprocess under a supervisor that observes three signals at each tick: `output` (timestamp on every byte from stdout/stderr via `io.MultiWriter` + atomic.Int64), `cpu_time` (Windows `GetProcessTimes(handle)`, kernel + user combined), and `io_bytes` (Windows `GetProcessIoCounters` via `kernel32.dll.NewProc` — not exposed in `golang.org/x/sys/windows@v0.42`, loaded via LazyDLL). Decision rule: ANY one signal moving since last tick resets the stall counter; ALL three flat for K consecutive ticks triggers a kill (`Sync:Stalled` anomaly).

**Two buckets, derived from rclone verb:**
- Transfer ops (`copyto`, `copy`, `sync`, `moveto`, `touch`): interval 10s, K=6, ~60s flat-grace.
- Metadata ops (`lsjson`, `deletefile`, `purge`, default for unknown verbs): interval 30s, K=8, ~240s flat-grace.

For transfer verbs, `--stats=15s --stats-one-line` is auto-injected so rclone produces a regular heartbeat — keeping Layer 2 calm during legitimate Layer 1 retry-sleep windows.

**lsjson on huge trees.** Past ~60s with signals still moving, the supervisor records a `Sync:LsJsonSlow` (info severity) anomaly for operator awareness. It does NOT kill; it lets the operation run as long as it's making progress. Honors the project's "no use a different strategy" constraint — the system either succeeds or fails-gracefully on its own.

**Process lifecycle.** `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` after `cmd.Start()`; handle held for sampling lifetime; `defer windows.CloseHandle`. `sync.Once` guards `cmd.Process.Kill()`. Wait goroutine + `done` channel ensure pipe-reader goroutines drain before return; no fd leaks. PID-reuse race avoided by holding the OS handle, not re-OpenProcess'ing each tick.

**Anomaly kinds.** `Sync:Stalled` (warning) and `Sync:LsJsonSlow` (info) added to `internal/anomaly/anomaly.go`. `Sync:Timeout` retained for the legacy escape-hatch path; no new emissions in the supervised path.

**Escape hatch.** `SMIRROR_DISABLE_LIVENESS=1` env var reverts to the legacy 5-minute `context.WithTimeout` path for one release. After that, the legacy path is deleted.

**Test design.** `internal/sync/liveness_test.go` drives the supervisor against real short / long subprocesses with an injected tick channel + scripted probe — no `time.Sleep`, no wall-clock asserts. 13 new tests (bucket selector × 10 verbs, OR-combinator, kill-on-flat, ctx-cancel, natural-exit, lsjson-with-movement, activity writer, flag-injection collision detection in three forms).

**Design rigor.** Design v2 (`docs/rclone-stall-design-for-review.md`) was revised from v1 after a multirole BMad review (architect, senior dev, adversarial, edge-case hunter). Key revisions: collapsed five buckets to two (Winston: "no principled stopping rule" for the larger table), dropped `dst_size` signal (Winston / Adversarial / Edge-case all flagged it), switched the four-signal AND combinator to a three-signal OR (panel cross-agreement), `--low-level-retries 10 → 3` to prevent Layer 1 / Layer 2 collision (Adversarial's killer finding), introduced `Sync:Stalled` instead of silently shifting `Sync:Timeout`'s meaning, threaded `RcloneInvocation` struct from callers, used `kernel32.dll.NewProc` for `GetProcessIoCounters` (Amelia's verified x/sys/windows gap), confirmed `unsafe` scope is needed for the IO_COUNTERS struct cast.

### Documentation (P4 — from adversarial review)

- **`CLAUDE.md` status line updated.** Reflects v0.9.x-dev, Phase 5 (telemetry) live, Phase 6/7 complete. Phases section ticked Phase 5 with deployment notes (Supabase, Cloudflare Worker proxy, MSI consent UI). The 530+ test count was updated to 600+ (608 actual today).
- **`SECURITY.md` Supported Versions updated** to list 0.9.x as supported and 0.8.x as best-effort backports for security-critical fixes.
- **`README.md` phase list aligned with CLAUDE.md.** Added Phases 2.5, 5, 6, 7 (previously absent) and updated their statuses.
- **`docs/VV-Plan.md` test pyramid + summary table** refreshed to v0.9.x and 600+ unit tests.
- **`installer/TelemetryConsent.wxi` self-comment fixed.** The file claimed "NOT yet wired into Package.wxs" but it has been included since v0.9.4-dev. Comment now reflects shipping state and lists the still-pending UI checkbox as the only remaining work.

### Workspace hygiene (P6 — from adversarial review)

- **`.gitignore` covers `*.out` and `coverage*.txt`.** Test coverage profiles (`go test -coverprofile=*.out`) used to accumulate in the worktree and could be `git add .`'d into a commit. Stray `coverage*.out` and `watcher_cov.out` files removed from the worktree.

### CI / release pipeline (P3 — from adversarial review)

- **`release.yml` now runs `go vet` and `go test ./internal/... ./cmd/...` before GoReleaser.** CI runs on `push:branches:[master]` and `pull_request`, while release runs on `push:tags:['v*']`. GitHub Actions `needs:` does not cross-link between workflows, so prior to this commit a tag pushed onto a commit whose CI failed would still publish the release. Adding the test step into release.yml itself closes that gap (defense-in-depth — we don't skip on already-green CI status because re-running is cheap relative to a broken release).
- **Race detector now covers `internal/sync` and `internal/state`.** The previous step list was justified with a "CGO-free packages" comment, but `CGO_ENABLED=1` was set on that very step (mattn/go-sqlite3 requires it). The two excluded packages are exactly where races live (FairQueue, per-file mutex map, `gosync.Map`, concurrent SQLite goroutine writers); both pass `go test -race` cleanly today.

### Fixed (P1 — security & correctness, from adversarial review)

- **`config.SetField` no longer overwrites indented sibling keys.** The previous `TrimSpace` + `HasPrefix` match treated `    delete_policy: ignore` (per-mirror) and top-level `delete_policy: ignore` as the same line — so `smirror remote` (or anything that touched a global key) could silently rewrite a per-mirror policy. Now matches only at column 0; comment lines are skipped.
- **Config edits no longer downgrade file mode from 0600 to 0644.** New `writePreservingMode` helper reads the existing file mode and reuses it (or 0600 for newly created configs); applies to `SetField`, `AddMirror`, `RemoveMirror`. Closes the SEC-H6 invariant violation where every edit silently widened permissions.
- **Removed duplicate `updateConfigKey` in `cmd/smirror/main.go`.** Both rclone-resolution sites now go through `config.SetField` (single source of truth).
- **`deleteRemoteFile` and `deleteRemoteDir` now reject relPath/dirPath containing traversal segments or rooted prefixes.** New `isUnsafeRelPath` helper rejects empty-after-clean (`.`), absolute paths (`/`, `\`, drive-letter), and any literal `..` segment — even when `..` cancels out under `Clean`, because some rclone backends evaluate raw segments. `syncSingleFile` already guarded its local path; this brings the destructive paths to the same standard. Defense-in-depth against a malformed event source.
- **`config.IsAdminOwnedPath` now also walks the DACL** and refuses any file whose ACL grants write-class permissions to a non-admin trustee. Previously checked the owner SID only — an Administrators-owned file ACL'd `Authenticated Users:Modify` passed the SEC-C5 gate, defeating the LPE protection. Closes audit SEC-H6.
- **`AllowLoopbackWebhooks` made unexported (`allowLoopbackWebhooks`).** A future contributor can no longer flip the SSRF defense from an unrelated package. Test access remains via the same-package `testmain_test.go` `init()` (only compiled into the test binary).
- **`smirror report-bug` no longer writes to the current working directory.** Reports now land under `<configdir>/reports/` (with `~/.selectivemirror/reports/` and `%TEMP%/smirror-reports/` as graceful fallbacks). Mode 0600 for the file, 0700 for the directory. Running `report-bug` from inside a watched mirror used to round-trip the report up to the configured remote.
- **`state.Open` no longer writes meta on every invocation.** `last_startup` is no longer set by Open — only by an explicit `MarkDaemonStartup()` call from the foreground/service daemons. `schema_version` writes are now downgrade-safe (an older binary running `smirror status` no longer rewinds the meta entry, which used to trigger redundant migration re-runs on the next daemon start).
- **Emergency crash logs no longer write to `C:\` root.** Service-mode lands at `%ProgramData%\SelectiveMirror\service-crash.log`; CLI mode lands at `%TEMP%\smirror-early.log`. Both are mode 0600 with mode-0700 parent dirs. Previous fixed `C:\smirror-*.log` paths were unwritable on locked-down machines for non-admins (silent breadcrumb loss) and exposed `os.Args` to anyone with `C:\` read access.

### Fixed (P0 — latent crashes & data-loss bugs)

- **`alert_webhook_url` + `anomaly_detection: false` no longer panics at startup.** Both foreground and service-mode wired the webhook by assigning `anomalyRecorder.OnRecord = ...`, but `anomalyRecorder` is nil whenever anomaly detection is disabled — so this combo crashed the daemon on boot. Replaced with a new nil-safe `Recorder.SetOnRecord` method, and emit a `slog.Warn` instead of silently no-opping so the user sees that their webhook will receive nothing.
- **Service mode no longer panics on the first `sync-now` signal when the Windows Event Log is unavailable.** The deferred close of `elog` was guarded with `if elog != nil`; the goroutine handler that emits "Immediate sync requested" was not. Added the same nil guard there.
- **`smirror clean --self` now refuses to run while a daemon holds the single-instance lock.** The doc comment promised this check; the code did not implement it. `os.RemoveAll(userDataDir)` racing the live daemon's open `state.db`, `smirror.lock`, and `anomalies/` could produce partial deletions and silent state corruption.
- **`smirror clean --self` no longer falls back to wiping `~/.selectivemirror/` when a custom `--config` path fails to load or its directory does not exist.** Previously the load was a side-effecting probe whose failure handed the deletion target back to the home-dir default — running `smirror --config /custom/path/config.yaml clean --self --yes` against a typo'd custom config would wipe the user's actual home data dir. The home fallback now applies only when `--config` was not explicitly passed.

### Documentation / ISO compliance (v0.4 integration of parallel-session work)

- **`docs/iso-compliance.md` revised to v0.4**. Integrates the 5 commits 0.9.8..0.9.12-dev that landed after v0.3 baseline. Re-measured coverage: total internal/ is **66.6%** (above v1.0 60% target); watcher at **59.3%** (NOT 16.6% as VV-Plan §5.2 still says — that baseline was 9 days stale; see SM-155). X-04 reclassified P0 → P2 (mostly closed). Test Monitoring & Control (29119-2) improved from ⚠️ → ✅ via release.yml hardening (commit f264a3e). A-25010-04 Faultlessness has substantive evidence shipped (`internal/sync/liveness.go` multi-signal stall detection — measurable thresholds 60s transfer flat-grace, 240s metadata). A-25010-08 Analysability strengthened by new anomaly kinds. New action `A-29119-12` (per-release VV-Plan §5.2 re-measurement ritual). Two new bugs: SM-155 (VV-Plan stale per-package coverage), SM-156 (CHANGELOG SEC-C2 / SM-152 misattribution).
- **`docs/SRS.md` NFR-TE-01** updated with current accurate coverage (608 tests, 66.6% total internal/, watcher 59.3%) and SM-155 cross-reference.
- **`docs/SRS.md` NFR-FT-01** annotated with rclone-stall detection layer (v0.9.12-dev) — references `internal/sync/liveness.go` and `docs/rclone-stall-design-for-review.md`. Documents the Layer-2 measurable Faultlessness model.

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
