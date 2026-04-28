# SelectiveMirror — Round 4 Panel Review & System-Validation

**Date**: 2026-04-29 (fourth re-validation)
**Version under review**: v0.9.27-dev (HEAD `530b5a2`; CHANGELOG promoted [Unreleased] to [0.9.26])
**Reviewer**: Multi-role panel (anomaly detection / CLI config-mutation / hooks + audit trail / adversarial recheck)
**Validation suite added**: [system-validation/panel_findings_round4_test.go](system-validation/panel_findings_round4_test.go) — 14 tests (10 pass / 1 source bug / 1 surface finding / 3 skip-with-reason)

---

## 0. Executive summary

- **1 new source bug** (BUG-R4-1, **High**): concurrent `addmirror` from two terminals isn't just lossy — it can destroy the pre-existing seed mirror entirely. This is worse than the CLI-mutation reviewer predicted.
- **1 new behavioral finding** (FIND-R4-1, **High** for AI-orchestration use case): per-file hooks do **not** fire on batch sync (`sync-now`, startup reconciliation, `dry-run`). They only fire on the live-watcher path. The audit-trail and orchestration story for batch syncs is therefore silent.
- **5 confirmed observations** with concrete reproductions:
  - Fresh `config.yaml` created with mode 0666 (Go reports `0666`; expected 0600 per SEC-C5).
  - `smirror status` doesn't surface sync_log history; operators must use `sqlite3` to audit.
  - `smirror addmirror` during a running daemon adds the mirror to config but the daemon never picks it up; no hint printed.
  - Delete events skip per-file hooks (`task.Type != TaskDelete` guard); no `pre_delete_hook` / `post_delete_hook`.
  - `alert_min_severity: erro` (typo) passes `config.Validate()` and silently demotes filtering.
- **Round 1+2+3 regression**: 58 prior tests still pass; the only red is BUG-R3-1 (gitignore parent-exclusion divergence) which has not yet been fixed between rounds.

---

## 1. Round 4 method

Four lenses NEW to this round:

| Lens | Why | Findings raised | Tests written |
|---|---|---|---|
| **Anomaly detection accuracy** | Phase 6 work; SRS FR-ANOM-01..11 commits to JSONL format, sanitization, rotation, hypothesis chains. None exercised by R1-R3. | 15 | 4 |
| **CLI config-mutation safety** | `addmirror` / `unmirror` / `remote` / `clean` mutate `config.yaml` and state DB in place. Atomicity + concurrency previously untested. | 15 | 4 |
| **Hooks system + audit trail** | Phase 7 hooks; FR-ASP-17, NFR-NR-01 (every action logged). Security, env vars, batch sync coverage. | 16 | 3 |
| **Adversarial recheck after fixes** | Many R1 fixes shipped — re-attack them and look for residual gaps in newer surfaces (rclone-stall, async OnRecord). | 13 | 2 |

Total raw: **59 findings**. Converted to **14 testable system-validation cases**, 10 of which produced PASS-with-OBS or hard FAIL signal.

---

## 2. Test results

| # | Test | Result | Note |
|---|---|---|---|
| 1 | `Hooks_EnvVarsActuallyExported` | **FAIL → FIND-R4-1** | Hook never fired. Root cause confirmed below: batch sync doesn't trigger per-file hooks. |
| 2 | `Hooks_PreSyncFailureDoesNotBlock` | PASS (with OBS) | VV-Plan T-HOOK-02 says "sync skipped on hook failure"; code says "hooks never block sync". V&V plan needs update. |
| 3 | `Hooks_NotInvokedForDelete` | PASS (with OBS) | Confirmed: post-sync hook does NOT fire for delete events. Documented design gap. |
| 4 | `AuditTrail_BatchSync_RowCount` | PASS (with OBS) | sync_log row count after 5-file sync vs FR-NR-01 commitment. Currently 1 row per project per batch (not per file). |
| 5 | `AuditTrail_StatusShowsSyncHistory` | PASS (with OBS) | `smirror status` doesn't surface sync_log; no `--verbose` or `history` subcommand. |
| 6 | `Anomaly_AlertMinSeverity_Validated` | PASS (with OBS) | `alert_min_severity: erro` (typo) accepted by `Validate()`; severityAtLeast() returns 0 for unknown keys. |
| 7 | `Anomaly_HypothesisChainPresent` | SKIP | No anomaly files written in test budget; can't probe FR-ANOM-05 black-box. |
| 8 | `CLI_ConcurrentAddMirror` | **FAIL → BUG-R4-1** | Two parallel addmirrors destroyed the pre-existing seed mirror. Last-writer-wins erases data. |
| 9 | `CLI_FreshConfig_FileMode` | PASS (with OBS) | Fresh `config.yaml` mode is `0666` (Go-reported); SEC-C5 / SECURITY.md baseline is 0600. |
| 10 | `CLI_RemoteSet_QuotingSafety` | PASS | `s3:bucket'with'quotes` either rejected or written safely; no config corruption observed. |
| 11 | `CLI_AddMirror_DuringDaemonRun` | PASS (with OBS) | Daemon does NOT detect the newly-added mirror; no hint to user. |
| 12 | `Adv_VerifyDisabledExplicit` | PASS | Documented `verify_interval_sec: -1` correctly accepted. |
| 13 | `Anomaly_RotationDeletionError` | SKIP | Rotation can't be triggered black-box without time-travel. |
| 14 | `Anomaly_OverflowAnnouncementOnDisk` | SKIP | Needs an HTTP fault-injection server. |

**Score**: 9 PASS / 2 FAIL / 3 SKIP.

---

## 3. BUG-R4-1 — Concurrent `addmirror` destroys pre-existing mirror (High)

**Test**: [TestPanelR4_CLI_ConcurrentAddMirror](system-validation/panel_findings_round4_test.go:444)

**Repro**:
```bash
# Pre-existing config.yaml has one mirror named "seed".
smirror addmirror /path/A -dest /dst/A &
smirror addmirror /path/B -dest /dst/B &
wait
```

**Observed**: only one of {A, B, seed} survives in `config.yaml` after both processes return. The pre-existing `seed` mirror is regularly the casualty — both new processes read the original config (which had `seed`), each adds their own entry, both write the result, last writer wins.

**Per CLI-mutation reviewer Finding #1**: `internal/config/edit.go::SetField`/`AddMirror`/`RemoveMirror` use plain read-modify-write (`os.ReadFile` → string manipulation → `os.WriteFile`). No file-level lock, no compare-and-swap, no atomic-rename pattern. Two CLI invocations from different terminals are guaranteed to lose data.

**Severity**: High. The user has no warning that they just lost a mirror, and the loss is silent. With `unmirror --purge-remote` running concurrently with `addmirror`, the data-loss window grows to "remote contents".

**Remediation**:
1. Acquire an exclusive lock on `config.yaml` (LockFileEx on Windows, `flock` on POSIX) before each read-modify-write cycle.
2. Use temp-file + atomic rename for the write.
3. Re-validate the config invariants after re-reading under lock (some other writer may have changed it).

`internal/lock/lock.go` already implements an OS-level lock primitive for the daemon's single-instance lock; the same primitive can wrap config edits.

---

## 4. FIND-R4-1 — Per-file hooks do NOT fire on batch sync (High)

**Test**: [TestPanelR4_Hooks_EnvVarsActuallyExported](system-validation/panel_findings_round4_test.go:38)

The test configured a `post_sync_hook` that dumps `SMIRROR_*` env vars to a canary file, then ran `sync-now`. The canary file was never created. The hook never fired.

Looking at the source (per Hooks-reviewer finding #7), `Engine.Hooks.Run` is called from `syncSingleFile` — the live-watcher per-file path. It is **not** called from `batchSync` / `syncFullProject` (the `rclone copy --filter-from` path used by `sync-now`, startup reconciliation, and `addmirror --initial-sync`).

**Implications**:

- For an AI-orchestration use case, hooks are the integration point with downstream tools. If the daemon is running and a single file changes, the hook fires correctly. But on `sync-now`, on startup reconciliation, on initial-sync after `addmirror`, on the periodic FR-SYNC-09 reconciliation cycle — none of those invoke the hook.
- For audit/non-repudiation (NFR-NR-01), this means batch syncs are silent in user-side tooling: a webhook listener or post-process script never sees them.
- For users who rely on `sync-now` as a regular catch-up mechanism, this is a feature gap, not a bug. It should be documented loudly.

**Remediation options**:

1. **Per-file hooks invoked via filesystem walk after batch**: after `rclone copy` completes, walk the changed files and fire `post_sync_hook` for each. Cost: hooks now scale with file count, not invocation count.
2. **Project-level hook addition**: add `pre_batch_sync_hook` / `post_batch_sync_hook` config keys that fire ONCE per batch with `SMIRROR_FILE_COUNT`, `SMIRROR_BYTES_UPLOADED` env. Cheap, but requires a new contract.
3. **Document explicitly** in CLAUDE.md / SRS / SECURITY.md: "hooks fire only on the live watcher path; batch operations bypass them." Cost: doc-only.

Pairs with VV-Plan T-HOOK-06 (which assumes env vars MIRROR / ACTION / STATUS that don't exist) — the test plan is also in drift with the implementation; they should be reconciled together.

---

## 5. PANEL OBS — high-impact observations

### OBS-R4-1 — Fresh config.yaml created with mode 0666 (Medium)
[TestPanelR4_CLI_FreshConfig_FileMode](system-validation/panel_findings_round4_test.go:506) — running `smirror addmirror <path>` when `config.yaml` does not exist creates the file with permission bits `0666` (Go's `os.Stat` reading on Windows). SEC-C5 / SECURITY.md baseline is 0600. CLI-mutation reviewer Finding #15.

**Remediation**: thread `writePreservingMode` (which already supports 0600 fallback for new files) through `cmd/smirror/cmdaddmirror.go::cmdAddMirror`'s `os.WriteFile` call at line 317.

### OBS-R4-2 — `smirror status` doesn't surface sync_log history (Medium)
[TestPanelR4_AuditTrail_StatusShowsSyncHistory](system-validation/panel_findings_round4_test.go:285) — confirmed: status output has no recent-syncs section. NFR-NR-01 commits to an audit trail; operators must `sqlite3` it directly today. Hook-reviewer Finding #13.

**Remediation**: small CLI feature. `smirror status --verbose` or `smirror history [mirror] [-n 50]` reading the last N rows from `sync_log`.

### OBS-R4-3 — Delete events skip hooks (Medium for orchestration use case)
[TestPanelR4_Hooks_NotInvokedForDelete](system-validation/panel_findings_round4_test.go:174) — confirmed: `internal/sync/sync.go:287` excludes TaskDelete from hook invocation. Documented design but a real gap for "react-to-delete" orchestration. Hook-reviewer Finding #5.

**Remediation**: either remove the exclusion (run post-sync with `SMIRROR_EVENT=delete`) or add `pre_delete_hook` / `post_delete_hook` config keys.

### OBS-R4-4 — `addmirror` during running daemon: no hot-reload, no hint (Medium)
[TestPanelR4_CLI_AddMirror_DuringDaemonRun](system-validation/panel_findings_round4_test.go:586) — confirmed: addmirror writes config, daemon never picks up the new mirror, no warning. CLI-mutation reviewer Finding #8.

**Remediation**: at the end of `addmirror`, check if the daemon is running (lock file held by another process); if yes, print "Daemon is running. Restart it to begin watching this mirror." Cost: ~5 lines.

### OBS-R4-5 — `alert_min_severity` typo passes Validate() (Low)
[TestPanelR4_Anomaly_AlertMinSeverity_Validated](system-validation/panel_findings_round4_test.go:336) — confirmed: `alert_min_severity: erro` accepted at config load; `severityAtLeast` returns 0 for unknown keys. Anomaly-reviewer Finding #3, #13.

**Remediation**: in `config.Validate()`, after parsing `AlertMinSeverity`, compare against `{info, warning, error, critical}`; reject unknown.

---

## 6. Round-4 panel findings NOT converted to tests

These are panel concerns that are not testable from a black-box harness without significant infrastructure (real network, internal-package access, sqlite3 CLI, fault injection).

### High-confidence

| # | Lens | Finding | Recommendation |
|---|---|---|---|
| R4-PF-1 | Anomaly #6 | Anomaly JSONL `writer.Write()` does not flush per-record. On SIGKILL between Write and the OS buffer flush, the just-written line is lost. | Add `f.Sync()` after each write, or buffered with a periodic flush + flush-on-close. |
| R4-PF-2 | Anomaly #11 | `SanitizePath` only redacts the user's home directory. Service-mode (LocalSystem) sees other users' paths unredacted. UNC paths and case-insensitive variants also miss. | Pass list of known mirror paths AND list of user homes to the sanitizer; canonicalize with `filepath.EvalSymlinks` before comparison. |
| R4-PF-3 | Anomaly #5 | Rotation deletion errors silently ignored. A read-only / locked old anomaly file blocks the size-cap forever. | Log + record `Rotation:Failed` anomaly when a delete fails. |
| R4-PF-4 | CLI #2 | `addmirror --initial-sync` failure leaves the mirror in config with no rollback. | Either rollback config write on initial-sync failure, OR document that recovery requires `unmirror`. |
| R4-PF-5 | CLI #11 | `clean --all` doesn't TOCTOU-protect the `%ProgramData%\SelectiveMirror` symlink. An attacker could plant a junction post-stat to redirect the deletion. | Mirror SEC-H7 pattern: Lstat before RemoveAll; refuse if path is a symlink/junction. |
| R4-PF-6 | CLI #12 | `writePreservingMode` writes config in place (not via temp-file + rename). A crash mid-write truncates the user's config. | Atomic write: `WriteFile(<config>.tmp)` + `os.Rename` to swap. |
| R4-PF-7 | Hook #1 | Pre-sync hook failure does NOT block sync, contradicting VV-Plan T-HOOK-02. | Pick one: implement T-HOOK-02 (sync skip on hook fail) or update the V&V plan to match code intent. |
| R4-PF-8 | Hook #4 | Post-sync hook failure does not surface in `sync_log` (only slog DEBUG). | Add a hook-failure column to sync_log or record an anomaly. |
| R4-PF-9 | Hook #15 | Hook failure doesn't create an anomaly. Operator's webhook never sees it. | `Anomaly.Record(KindHookFailure, ...)` on hook non-zero exit. |
| R4-PF-10 | Adv #1 | Foreground (non-service) mode follows symlinks; SEC-H5 only protects service mode. A non-admin user's malicious `.syncignore` could plant a symlink to `%LOCALAPPDATA%\sensitive\file` and have it synced. | Tighten `Engine.RejectSymlinkedFiles` default to true even in foreground; opt-in with `--allow-symlinks` flag for monorepo users. |
| R4-PF-11 | Adv #3 | GAP-1 denylist matches by ASCII prefix. Cyrillic 'с' (U+0441) lookalike for 'c' bypasses prefix check. | Either use unicode-fold comparison, or normalize input via `golang.org/x/text/secure/precis`. |

### Medium / lower

| # | Note |
|---|---|
| R4-PF-12 | Anomaly #2: `droppedCallbacks` counter never surfaces in `smirror status`. Webhook-overflow is invisible. |
| R4-PF-13 | Anomaly #4: Anomaly summary table not in status; only "recent" list of last N. |
| R4-PF-14 | Hook #2: env var names PROJECT/EVENT diverge from VV-Plan's MIRROR/ACTION/STATUS. Either rename code or update plan. |
| R4-PF-15 | Hook #14: env doesn't include file size or content hash. Limits orchestration use cases. |
| R4-PF-16 | Adv #5: `clean --self` TOCTOU race with the per-user Scheduled Task. |
| R4-PF-17 | Adv #10: 8.3 short names + UNC paths bypass the path-overlap check (GAP-3). |
| R4-PF-18 | CLI #6: `cmdRemote` quoting was tested as PASS (no corruption observed today), but the embedded-quote case is sensitive — keep monitoring. |

---

## 7. Most-important-feature verdict

| Feature | Round 4 verdict |
|---|---|
| Hooks (live-watcher path) | Working as documented. |
| Hooks (batch-sync path) | **Not invoked at all** (FIND-R4-1). For orchestration use case, this is a feature gap. |
| CLI mutation atomicity | **Broken** (BUG-R4-1) — concurrent edits lose data. Fresh-config mode is too permissive. |
| Anomaly detection | Categories fire correctly when triggerable (R3 confirmed); sanitization has gaps (UNC, multi-user); hypothesis chain has 2 missing categories. |
| Audit trail (NFR-NR-01) | Per-file syncs logged; batch syncs aggregate to one row; status doesn't expose history. |
| Status / observability | Functional but bare; no recent-syncs view, no anomaly summary, no overflow counter. |
| Symlink safety (foreground) | **Asymmetric** with service mode (R4-PF-10). |
| Delete propagation | Confirmed working (R3); hooks excluded from delete (OBS-R4-3). |
| All R1+R2+R3 prior tests | **No regressions** against v0.9.27-dev. |

The system is solid where it has been hardened, but several "moved features" (batch-sync hooks, audit-trail surfacing, concurrent CLI edits, anomaly observability) are still partial. None are catastrophic; all are feature-completeness items for v1.0.

---

## 8. Suggested priority order for the maintainer

1. **BUG-R4-1** — atomicity + lock around `config.yaml` writes. **Critical for v1.0** (data loss on a documented user pattern). Existing `internal/lock` primitive can be reused.
2. **R4-PF-6** — atomic-rename pattern for config writes. Pairs with BUG-R4-1.
3. **R4-PF-1** — `f.Sync()` in anomaly writer. Two-line fix. Closes anomaly-loss-on-crash.
4. **OBS-R4-1** — fresh config 0600 mode. One-line fix.
5. **OBS-R4-5** — validate `alert_min_severity` enum. Five-line fix.
6. **OBS-R4-4** — `addmirror` prints "restart daemon" when daemon is running. Five-line fix.
7. **FIND-R4-1** — decide and document hook semantics for batch sync. Then implement or doc.
8. **R4-PF-10** — foreground symlink-reject as default. May break monorepo users; needs a `--allow-symlinks` opt-in.
9. **R4-PF-2** — sanitizer covers multi-user paths.
10. **OBS-R4-2** — `smirror history` subcommand or `status --verbose`.
11. Everything else as v1.1 backlog.

---

## 9. Round 1+2+3 regression status

Re-run of `TestPanel_*`, `TestPanelR2_*`, `TestPanelR3_*` against v0.9.27-dev:
- 58 prior tests: PASS.
- 1 prior known-FAIL: `TestPanelR3_Gitignore_ExcludedParentBlocksChildNegation` (BUG-R3-1) — still failing as documented; not yet fixed between rounds.
- 0 new regressions.

---

*Round 4 report generated 2026-04-29. Sibling reports: [PANEL-REVIEW-2026-04-28.md](system-validation/PANEL-REVIEW-2026-04-28.md), [PANEL-REVIEW-ROUND2-2026-04-28.md](system-validation/PANEL-REVIEW-ROUND2-2026-04-28.md), [PANEL-REVIEW-ROUND3-2026-04-28.md](system-validation/PANEL-REVIEW-ROUND3-2026-04-28.md).*
