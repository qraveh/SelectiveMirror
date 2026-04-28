# SelectiveMirror — Panel Review & System-Validation Report

**Date**: 2026-04-28
**Version under review**: v0.9.17-dev
**Reviewer**: Multi-role panel (architect / senior dev / edge-case hunter / adversarial), synthesized
**Validation suite added**: `system-validation/panel_findings_test.go` (28 tests, 7 sub-tables)

---

## 0. Method

Four parallel reviews were run against the project, each with a different lens:

| Lens | Focus | Output |
|------|-------|--------|
| **Architect** (Winston-style) | Layer integrity, invariants at boundaries, lifecycle, schema evolution | 11 findings |
| **Senior dev** (Amelia-style) | Race conditions, leaks, error handling, off-by-one, Windows path subtleties | 18 findings |
| **Edge-case hunter** | Boundary conditions, unhandled inputs across 11 focus areas | 32 findings |
| **Adversarial** | Cynical attacks on every claim in CLAUDE.md, SECURITY.md, SRS, audit | 16 findings |

The panel collectively produced 77 distinct findings (some overlap). The findings most amenable to automated black-box validation were converted into 28 system-validation tests in `panel_findings_test.go`. The suite was then executed against `smirror.exe` built from `master`.

This report consolidates: (a) defects confirmed by the test run, (b) gaps the tests revealed but did not assert as failures, and (c) high-confidence panel findings that warrant follow-up but are not testable from a black-box harness.

---

## 1. Confirmed source defects (test FAIL)

### BUG-1 — Validate() accepts case-only duplicate mirror names (Medium)

**Test**: [TestPanel_Config_CaseOnlyDuplicateNames](system-validation/panel_findings_test.go:51)
**Where**: [internal/config/config.go:343-351](internal/config/config.go:343)
**Status**: FAIL — `Validate()` returned exit 0 for a config with `WorkProject` and `workproject`.

```go
names := make(map[string]bool)            // case-sensitive map
for i, p := range g.Projects {
    if names[p.Name] { return ... duplicate name ... }
    names[p.Name] = true
}
```

On Windows (case-insensitive NTFS), `WorkProject` and `workproject` resolve to the same on-disk path and the same state-DB lookup key. With both accepted as separate mirrors, two watchers trigger on the same files, two FairQueue workers compete on the same `state.sync_state` rows, and metrics are reported under both names.

**Remediation**: change the dedup map key to `strings.ToLower(p.Name)` on Windows (or always — names are user-facing and case-insensitive collision is almost always a typo). Reject with a friendly message that points at the offending pair.

---

### BUG-2 — Stale system-validation expectations after SM-164 (Low — test bug)

**Tests**: [TestCLI_ReportBug_FailureScenario/VerifyContent](system-validation/cli_test.go:776), [TestCLI_ReportBug_FailureScenario/VerifyURLPrefill](system-validation/cli_test.go:817)
**Where**: assertions in `cli_test.go:788-794, 880`
**Status**: FAIL — assertions expect `working-mirror`, `broken-mirror`, `files_synced: 142`, `sync_errors: 17` in the report's env section.

After reading [main.go:2073-2091](cmd/smirror/main.go:2073) the failure is **expected behavior**, not a source bug:

- `cmd/smirror/main.go:2075` writes `mirror_%d: <count>` (using index, not name) — by design, mirror names are placeholder-labeled.
- The "Live Metrics" block (`files_synced`, `sync_errors`, `queue_depth`, `avg_latency_ms`, …) was **deliberately removed by SM-164** because `report-bug --open` posts the report to a public GitHub issue, and the privacy doc forbids accumulated counters in such posts.

The test was not updated when SM-164 landed.

**Remediation**: in `cli_test.go`, drop the `working-mirror`/`broken-mirror`/`files_synced: 142`/`sync_errors: 17`/`queue_depth: 3` assertions. Replace with: env section MUST contain `mirror_0:` and `mirror_1:` and MUST NOT contain `files_synced` (or any other value listed in `docs/PRIVACY.md`'s no-accumulated-counts rule).

A side-effect to flag: the redaction trade-off means an operator triaging a multi-mirror failure cannot tell *which* mirror has 17 errors — the public-issue use case wins, but the local-debugging use case (`report-bug --stdout` for personal triage) suffers. Consider distinguishing `--stdout` (personal, allow real names) from `--open` (public, sanitize).

---

## 2. Defects the tests **didn't fail** but the suite documented as gaps (`PANEL OBS`)

These are real gaps. The tests log `t.Logf` rather than `t.Errorf` because either (a) the system's current behavior is technically defensible, (b) confirming the bug requires admin / network / sqlite-CLI access we don't have in the harness, or (c) the panel's claim is at the design level — testable later.

### GAP-1 — `rclone_extra_flags` allows arbitrary rclone flags (Critical)

**Test**: [TestPanel_Config_RcloneExtraFlags_DangerousFlagsAccepted](system-validation/panel_findings_test.go:185) (3 sub-cases, all PASS without rejection)
**Where**: [internal/sync/sync.go:1046-1047](internal/sync/sync.go:1046) appends user flags verbatim; [internal/config/config.go:Validate()](internal/config/config.go:338) does not validate the list.

Tested-and-accepted flag sets:

1. `["--rc", "--rc-addr", "127.0.0.1:5572", "--rc-no-auth"]` — exposes an unauthenticated rclone control plane on localhost. Anyone on the box (or any process able to bind localhost — e.g. malware) gets full file-system access *as the smirror principal*. Under service mode that is `LocalSystem`.
2. `["--log-file", "<path>", "--log-level", "DEBUG"]` — overwrites the named file. Combined with `LocalSystem` runtime, this is arbitrary-file-overwrite.
3. `["--config", "<path>"]` — a second `--config` flag tells rclone to use a different rclone.conf, bypassing the configured `cfg.RcloneConfig`. Combined with **GAP-2**, an attacker with config-write access can swap rclone backends out from under smirror.

**Adversary model**: anyone who can edit `config.yaml`. Per the SEC-C5 rule, service-mode requires admin-owned `config.yaml`, so this is contained for `service install`. However:
- **Per-user task mode** (`smirror task install`) and **foreground mode** (`smirror start`) read the user-writable config.
- Even in service mode, if a low-privilege user writes a fresh `config.yaml` at the per-user default path and then runs `smirror clean --self` followed by `service install` from an admin shell, the admin shell may import the user config (depending on flag order).

**Remediation**: introduce an allowlist or denylist for `rclone_extra_flags`. The denylist should at minimum cover `--rc*`, `--log-file`, `--config`, `--password-command`, and any flag that affects *what* rclone executes vs *how* a transfer behaves. Reject in `config.Validate()`.

**Severity**: Critical (privilege escalation in non-service modes; defense-in-depth violation in service mode).
**Confidence**: High (code path inspected, no validator exists).

---

### GAP-2 — `rclone_config` path not validated (High)

**Test**: [TestPanel_Config_RcloneConfigPathNotValidated](system-validation/panel_findings_test.go:251)
**Where**: [internal/config/config.go:Validate()](internal/config/config.go:338) does not stat `g.RcloneConfig`.

A bogus path is accepted (test confirmed). Combined with GAP-1, an attacker can point `rclone_config` at a user-writable file containing a malicious rclone remote definition (e.g. `[local]` rooted at `C:\Windows\System32`).

**Remediation**: in `Validate()`, if `g.RcloneConfig != ""`, stat it, require `S_ISREG`, and (in service mode) require admin ownership analogous to `IsAdminOwnedPath`.

---

### GAP-3 — Overlapping mirror local_paths accepted (Medium)

**Test**: [TestPanel_Config_OverlappingLocalPaths](system-validation/panel_findings_test.go:81) (passed, observation logged)
**Where**: [internal/config/config.go:Validate()](internal/config/config.go:338) does not check parent/child relationships among `Projects[i].LocalPath`.

Configuring `parent: C:\Project` and `child: C:\Project\Sub` causes:
- Both watchers fire on every file change under `Sub/`
- FairQueue gets two tasks, each routed to a different mirror — same source, different remote
- Quota burn 2×; remote diverges based on which task runs first

**Remediation**: in `Validate()`, after canonicalizing all `LocalPath` values, sort them and reject if any path is a strict prefix of another (with a final separator).

---

### GAP-4 — Drive-root `local_path` (e.g. `C:\`) silently accepted (Medium)

**Test**: [TestPanel_Config_DriveRootAsLocalPath](system-validation/panel_findings_test.go:121) (test took 30 s and was killed by timeout — `test-mirrors` started scanning the entire drive)
**Where**: same as GAP-3.

The 30-second scan time is itself diagnostic: the operation has no cap, so on a multi-TB volume `test-mirrors` would hang for tens of minutes before reporting drift. Watching `C:\` with fsnotify likely also exceeds Windows' per-handle ChangeNotification buffer almost immediately, dropping events.

**Remediation**: at validate time, reject `local_path` values that resolve to a drive root, `%SystemDrive%`, `%SystemRoot%`, `%ProgramFiles*%`, `%ProgramData%`. Print a friendly hint to choose a sub-directory.

---

### GAP-5 — `remote` accepts traversal-shaped values (Low)

**Test**: [TestPanel_Config_RemoteAcceptsTraversalSyntax](system-validation/panel_findings_test.go:163)

`remote: local:../../etc` passes validation; failure is deferred to first sync. `status` output shows the mirror as "OK" until then.

**Remediation**: a syntactic pre-check that rejects `..` path segments after the colon. Cheap defense-in-depth.

---

### GAP-6 — `--config` flag double-supplied: behavior unspecified (Low)

**Test**: [TestPanel_CLI_DoubleConfigFlag_LastWins](system-validation/panel_findings_test.go:838)

Passing `--config bogus --config good version` produced exit 1. The current arg-parsing loop in [main.go:144-155](cmd/smirror/main.go:144) breaks out of the loop on the FIRST `--config` it sees and never looks at later ones, so the bogus path "wins" if it comes first.

**Remediation**: pick a documented winner (last-wins is the convention in most CLIs) and assert it in a test.

---

### GAP-7 — Forward-dated state-DB schema: not blackbox-testable, manual repro recommended

**Test**: [TestPanel_StateDB_ForwardSchemaVersion_SilentDowngrade](system-validation/panel_findings_test.go:481)

The architect found that `internal/state/state.go` writes `schema_version = len(migrations)` unconditionally. A downgrade scenario (v0.9.17 → v0.9.12 → v0.9.17) leaves the DB at a high version number that the older binary considers up-to-date, skipping any migrations between 12 and 17.

The test logs the manual repro: `UPDATE meta SET value='999' WHERE key='schema_version';` followed by `smirror status`. If smirror runs without warning, the gap is real.

**Remediation**: when `schema_version > len(migrations)`, refuse to start (or fall through to read-only mode) with `"state DB schema version %d is newer than this binary supports (%d). Upgrade smirror or restore an older state DB."`.

---

### GAP-8 — Zero-byte / corrupted state DB tolerated silently

**Tests**: [TestPanel_StateDB_ZeroByteFileHandling](system-validation/panel_findings_test.go:399), [TestPanel_StateDB_CorruptedFileHandling](system-validation/panel_findings_test.go:436)

Both tests pass because no panic occurs and (for the corrupted case) the message contains DB-vocabulary. But the zero-byte case ran to exit 0 — smirror silently re-created the DB. That may be intentional ("be liberal on first run") but means a user who wipes their DB without realizing it loses sync history without warning.

**Remediation** (debatable): for `status` / `sync-now` (mutation-prone commands), require a non-zero existing DB or print a one-line "no state history found, starting fresh" warning. For `start` (long-running), behavior is fine.

---

### GAP-9 — Stale lock-file PID detection not implemented for read-only commands

**Test**: [TestPanel_Lock_StalePIDNotDetected](system-validation/panel_findings_test.go:355) (passed — `status` does not require the lock; the test confirms the read-only path is OK)

The senior-dev review's actual concern is that **mutating** commands (`start`, `sync-now`, `clean --self`) check `IsLocked` but do not verify the recorded PID is alive. A crashed previous instance leaves a stale lock that blocks restart until the OS releases the file handle (which it does, but the user-visible symptom is "exit 4" with no diagnostic).

This is **not** testable from a black-box harness without launching and crashing a real smirror — out of scope for system-validation. Recommend an internal-package unit test in `internal/lock/`.

**Remediation**: when acquiring the lock fails, read the PID from the file. If the PID is not alive (Windows `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, ...)` returns `ERROR_INVALID_PARAMETER`), warn and retry the lock.

---

## 3. Panel findings not converted to tests (qualitative, follow-up needed)

These are panel concerns that can't be black-box-tested without significant infrastructure (real network, admin elevation, multi-process orchestration) but are high-confidence enough to warrant a tracked action.

### High-confidence

| ID | Title | Source | Recommendation |
|----|-------|--------|----------------|
| PF-A1 | Webhook DNS rebinding: only static IP check at config load — at send time, DNS may resolve to a private IP. | Adversarial #3 | Replace stock `http.Client` with one that uses a custom `DialContext` re-checking IPs per dial; reject blocked IPs at dial time. |
| PF-A2 | Anomaly path sanitizer only redacts `os.UserHomeDir`. Service mode runs as LocalSystem; paths in `C:\Users\alice\...`, `C:\Orch`, `C:\HPL` are not redacted. | Adversarial #8 | Pass `cfg.Projects[i].LocalPath` and a list of known user homes to the sanitizer. |
| PF-A3 | SEC-H5 (audit 2026-04-18): default-reject symlink-to-file in service mode, **not yet implemented**. | Adversarial #9 | Add a service-mode check in the symlink-resolution path of `internal/sync/sync.go`. Keep foreground-mode behavior unchanged for monorepo use cases. |
| PF-A4 | Service mode rejects user-writable config (SEC-C5). Foreground mode does not — same `config.yaml` with `pre_sync_hook: calc.exe` runs as the foreground user. | Adversarial #16 | Document the intentional gap explicitly in SECURITY.md. |
| PF-A5 | Hook child processes are not in a Job Object — `cmd /C start /B notepad` orphans `notepad.exe` past the hook timeout. | Adversarial #12 | Wrap hook `exec.Command` in a Windows Job Object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. |
| PF-A6 | Architect: `cmd/smirror/main.go` imports 14 internal packages directly. CLI entry point is a god-object. | Architect #1 | Refactor `cmd/smirror/cmd_*.go` to a `cmd.Engine` type with explicit dependency wiring; main.go should orchestrate, not import every internal. |
| PF-A7 | Architect: `watcher.OnFilterChange` triggers an unbounded reconciliation goroutine. Rapid `.syncignore` edits → unbounded goroutines. | Architect #4 | Coalesce filter-change events into a single in-flight reconciliation per project, with a small queue. |
| PF-A8 | Architect: `Anomaly.OnRecord` callback runs synchronously in the calling goroutine; a slow webhook blocks the sync engine. | Architect #7 | Hand off to a bounded background channel; drop with a counter increment if the channel is full. |
| PF-D1 | Senior dev: `internal/sync/fairqueue.go` Dequeue spawns a `done`-channel goroutine that may outlive Dequeue under context cancel during cooldown waits. | Sr Dev #1 | Audit goroutine lifetime; close `done` first, then `mu.Unlock()`. |
| PF-D2 | Senior dev: `internal/filter/filter.go` Reload that fails (parse error) does not increment `generation`, so the next successful reload runs against tasks queued under TWO old generations rather than one. | Sr Dev #14 | Increment `generation` on failed-then-fixed transitions, OR explicitly drop queued tasks on filter-state regression. |

### Medium / lower

| ID | Title | Source | Recommendation |
|----|-------|--------|----------------|
| PF-E1 | Filter behavior under negation of a directory-excluded path is undocumented. Test confirmed `!.git/special-keep` does NOT re-include — that's gitignore-correct, but lock it down with a regression test. | Edge-case + my [TestPanel_Filter_NegateUnderExcludedDir](system-validation/panel_findings_test.go:566) | Promote the panel test to an asserting test once the spec is published. |
| PF-E2 | File grows during 200ms quiescence window — quiescence passes on size+mtime stability between two samples but the file may still be mid-write at sample-2. | Edge-case #5, Adversarial #15 | Add a "stable for N consecutive samples" rule (already partly there) AND a max-quiescence-attempts cap, OR require an `io.OpenFile` shared-read test (already done). The existing protection is reasonable; flag for review only. |
| PF-E3 | `lsjson` truncated output on connection reset → ghost-cleanup false-success. | Edge-case #20 | Validate JSON closes properly; treat parse errors as `Sync:LsJsonStale` anomaly, not a clean list. |
| PF-E4 | `RecordFailure`'s circuit-breaker state is keyed on mirror name. Renaming a mirror via `addmirror --rename` (if such flag exists) silently resets the breaker. | Adversarial #1 | Migrate breaker state on rename, or key on a stable mirror UUID stored in state DB. |
| PF-E5 | `report-bug --open` writes the diagnostic into a URL query string → browser history retains it. | Adversarial #10 | Print a clipboard-paste version with a `--no-open-browser` flag, OR use the `gh issue create -F -` flow if `gh` is on PATH. |

---

## 4. Test suite — final state

**File added**: [system-validation/panel_findings_test.go](system-validation/panel_findings_test.go)

| Section | Tests | Failures (real bugs) | Observations |
|---------|-------|----------------------|--------------|
| 1. Config validation gaps | 6 | 1 (case-only dup names) | 5 |
| 2. Report-bug output gaps | 1 | 0 (reclassified to test-stale) | 1 |
| 3. State DB & lock | 4 | 0 | 4 |
| 4. Filter & syncignore edges | 3 | 0 | 3 |
| 5. Filename edges | 3 | 0 | 3 |
| 6. File-size boundary | 1 | 0 | 1 |
| 7. Zero-byte / empty config | 2 | 0 | 0 |
| 8. CLI argument edges | 2 | 0 | 1 |
| 9. URL / webhook security | 1 (×7 sub-tests) | 0 | 0 |
| 10. Hook shell-interp | 1 | 0 | 1 |
| 11. Unknown YAML keys | 1 | 0 | 1 (positive: typo IS surfaced) |
| 12. rclone-error informativeness | 1 | 0 | 0 |

**Run command**:
```
go test ./system-validation/... -timeout 600s -count=1 -run "TestPanel_"
```

The suite is independent of the existing `cli_test.go` / `scenario_test.go` and contributes coverage on top of the existing 14 packages of unit tests (66.6% statement coverage per `docs/iso-compliance.md`).

---

## 5. Recommended priority order for fixing

1. **GAP-1** — `rclone_extra_flags` allowlist. **Critical**, ship before v1.0.
2. **GAP-2** — validate `rclone_config` path. **High**, ship before v1.0.
3. **PF-A3** — service-mode symlink-to-file reject (already in audit as SEC-H5). **High**.
4. **BUG-1** — case-only duplicate mirror names. **Medium**, but a one-line fix; ship as 0.9.x patch.
5. **PF-A1** — webhook DNS-rebind defense at dial time. **High**, but already partially mitigated by static-IP check; track for v1.0.
6. **GAP-3, GAP-4** — overlap and drive-root rejection. **Medium**, polish for v1.0.
7. **BUG-2** — update `cli_test.go` stale tests. **Low** (test code only), but must happen for the green CI gate.
8. **GAP-7** — schema-version-too-high refuse-to-run. **Medium**, defense-in-depth for downgrades.
9. **PF-A8** — async anomaly recorder callback. **Medium**, prevents webhook-stalls-engine.
10. Everything else as v1.1 backlog.

---

## 6. What was NOT covered

- **Race condition reproduction** — the panel surfaced ~5 race candidates (FairQueue, filter reload, state DB single-writer, hook child-process, anomaly callback). Each needs a `go test -race` harness inside its own internal package — not a black-box system-validation concern.
- **Real-network webhook tests** — DNS rebind, IPv6 dual-stack, connection pool reuse. Out of scope for the harness; recommend a fault-injection HTTP server in `internal/notify/` tests.
- **Real-rclone-backend behavior** — Drive's lack of MD5 for files >5 GB, OneDrive's quickXorHash, S3 etag-vs-md5 — `internal/sync` tests should cover these via mock rclone outputs; system-validation `TestBackend_*` already covers the connectivity surface.
- **Service / Task lifecycle** — installing/uninstalling under elevation. Already covered by `test/test_install.ps1` etc.; not duplicated here.
- **Performance / SLA** — `test/sla_smoke.ps1` is the right tier.

---

*Report generated 2026-04-28.*
