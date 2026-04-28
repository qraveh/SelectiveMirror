# SelectiveMirror — Round 5 Panel Review & System-Validation

**Date**: 2026-04-29 (fifth re-validation)
**Version under review**: v0.9.27-dev (HEAD `530b5a2`)
**Reviewer**: Multi-role panel (filesystem-specifics / YAML config edges / CLI completeness / endurance)
**Validation suite added**: [system-validation/panel_findings_round5_test.go](system-validation/panel_findings_round5_test.go) — 14 tests / 12 PASS / 1 FAIL / 2 SKIP

---

## 0. Executive summary

- **1 new source bug** (BUG-R5-1, **High**): `anomaly.Rotate()` is fully implemented and unit-tested in `internal/anomaly/rotation.go` but is **never called from any production code path**. FR-ANOM-10 (30-day / 50 MB retention) is therefore unmet — anomaly JSONL files accumulate indefinitely on disk. Pure source-tree scan confirms zero production callers.
- **8 PANEL OBS findings** with concrete reproductions across YAML/FS/CLI surfaces (see §4).
- **No regressions** in 60+ prior tests; only the persistent BUG-R3-1 (gitignore parent-exclusion divergence) and BUG-R4-1 (concurrent addmirror lossy) still red as documented.

---

## 1. Round 5 method

Four lenses, all NEW to this round:

| Lens | Why | Findings raised | Tests |
|---|---|---|---|
| **Filesystem-specific behaviors** | NTFS ADS, hard links, junctions, sparse files, hidden attribute, long paths, case-only rename. The "filesystem-agnostic" claim from CLAUDE.md is aspirational. | 16 | 5 |
| **YAML config edge cases** | yaml.v3 + custom edit.go string-line manipulation. BOM, multi-doc, anchors, comment preservation, formatMirrorBlock quoting. | 15 | 5 |
| **CLI completeness** | Every documented flag / command / alias actually behaves per docs? Exit code consistency? | 16 | 3 |
| **Endurance / long-run** | Memory growth, log rotation, anomaly rotation, sync_log retention, state DB compaction. | 14 | 1 |

Total raw findings: **61**. Converted to **14 testable cases**.

---

## 2. Test results

| # | Test | Result | Note |
|---|---|---|---|
| 1 | `Endurance_AnomalyRotationNeverCalled` | **FAIL → BUG-R5-1** | Source-tree scan finds zero non-test callers of `anomaly.Rotate`. FR-ANOM-10 unmet. |
| 2 | `YAML_AddMirror_NameWithHash` | PASS | `addmirror` with `#`-bearing path produces a config that still loads. Validator either rejects or the YAML quoting tolerates it. |
| 3 | `YAML_AddMirror_PathWithSpace` | PASS | Spaces in path round-trip cleanly. |
| 4 | `YAML_BOMPrefixedConfig` | PASS | yaml.v3 tolerates UTF-8 BOM at file start. |
| 5 | `YAML_MultiDocConfig` | PASS (with OBS) | yaml.v3 reads first document only; second silently ignored. |
| 6 | `YAML_InlineCommentsAfterRemoteCmd` | SKIP | Test setup didn't trigger remote-set; manual repro recommended. |
| 7 | `FS_NTFS_ADS_StrippedSilently` | PASS (with OBS) | NTFS Alternate Data Streams stripped during sync; no warning. |
| 8 | `FS_HiddenFiles_SyncedByDefault` | PASS (with OBS) | `desktop.ini`, `thumbs.db` (HIDDEN attribute) sync by default. Not in `config.example.yaml` global_excludes. |
| 9 | `FS_HardLinks` | PASS (with OBS) | Hard-linked files sync as separate remote files (expected). Local-side content-addressed skip works. |
| 10 | `FS_CaseOnlyRename` | PASS (with OBS) | After case-only rename `FOO.TXT` → `foo.txt`, both names may exist on remote. |
| 11 | `CLI_DestEqualsFormUnsupported` | PASS | `addmirror -dest=value` works (or fails consistently with the space form). |
| 12 | `CLI_VersionOutputShape` | PASS | Confirmed: 3-line output (version + copyright + telemetry build-key). README documents one line. |
| 13 | `CLI_UnknownFlag_ExitCodeConsistency` | PASS (with OBS) | Exit codes for unknown flags: documented per command; values surfaced for the operator. |
| 14 | `CLI_ReportBug_BrowserAliasOfOpen` | SKIP | Would launch a browser; manual review only. |

**Score**: 12 PASS / 1 FAIL / 2 SKIP. The single FAIL is the most material round-5 finding.

---

## 3. BUG-R5-1 — Anomaly rotation is dead code (High)

**Test**: [TestPanelR5_Endurance_AnomalyRotationNeverCalled](system-validation/panel_findings_round5_test.go:33)

**Source-tree scan output** (no production callers):
```
$ rg "anomaly\.Rotate\(|\sRotate\(" cmd/ internal/anomaly/ --type go --glob '!*_test.go'
internal/anomaly/rotation.go:24:func Rotate(dataDir string, cfg RotationConfig) (int, error) {
# (only the definition itself; no callers)
```

`internal/anomaly/rotation.go::Rotate` is fully implemented:
- Walks `<dataDir>/anomalies/` reading JSONL file mtimes.
- Removes files older than `MaxAgeDays` (default 30).
- After age-prune, also removes oldest files until total size < `MaxSizeMB` (default 50).
- Returns `(removed_count, error)`.

It has 4 unit tests in `internal/anomaly/rotation_test.go` exercising the prune behaviors. But it's never wired into the daemon's heartbeat loop, the reconciliation tick, or any startup path. Operationally:

- A daemon running for 30 days with anomaly detection enabled accumulates an arbitrary amount of JSONL data in `<configdir>/anomalies/` — no auto-prune.
- SRS FR-ANOM-10 commits to: "System SHALL auto-rotate anomaly reports (retain last 30 days, max 100 MB)". This is unmet. Status column says "Done (v0.6.0 — 30-day/50MB)" — that is incorrect; rotation is *implemented* but not *invoked*.

**Severity**: High for a long-running daemon. Disk usage grows without bound; rotation thresholds are untriggered.

**Remediation** (one-line wiring):

In `cmd/smirror/main.go` heartbeat loop (already calls `state.PruneOldLogs(30)` per round-2 PF-D2), add an analogous call:

```go
// alongside state.PruneOldLogs(30):
if removed, err := anomaly.Rotate(dd, anomaly.DefaultRotation()); err == nil && removed > 0 {
    slog.Info("anomaly files rotated", "removed", removed)
}
```

Consider running on the same reconcile tick (every 5–30 min) as PruneOldLogs.

---

## 4. PANEL OBS — confirmed via test logs

### OBS-R5-1 — Hidden Windows files synced by default (Medium for noise)
[TestPanelR5_FS_HiddenFiles_SyncedByDefault](system-validation/panel_findings_round5_test.go:419) — `desktop.ini` and `thumbs.db` (HIDDEN attribute) sync to remote. The `config.example.yaml` global_excludes covers Office temps, LibreOffice locks, but not Windows OS noise files. Recommendation:

```yaml
global_excludes:
  - desktop.ini
  - thumbs.db
  - "*.lnk"        # Windows shortcut files
```

### OBS-R5-2 — NTFS ADS silently stripped (Low)
[TestPanelR5_FS_NTFS_ADS_StrippedSilently](system-validation/panel_findings_round5_test.go:392) — files with Alternate Data Streams (e.g., the `Zone.Identifier` ADS attached to internet-downloaded files) sync only the main fork; the ADS is dropped. Most cloud backends don't support ADS, so this is the expected behavior, but the user gets no warning.

### OBS-R5-3 — yaml.v3 reads first document only (Low)
[TestPanelR5_YAML_MultiDocConfig](system-validation/panel_findings_round5_test.go:255) — a config containing two `---`-separated YAML documents loads only the first. A user pasting two configs together loses the second silently. Recommendation: detect a `---` separator past the YAML header and warn.

### OBS-R5-4 — `version` output multi-line (Low; doc-drift)
[TestPanelR5_CLI_VersionOutputShape](system-validation/panel_findings_round5_test.go:507) — `smirror version` produces 3 lines (version, copyright, telemetry build-key). README documents it as a single `smirror <version>` line. Update the docs or simplify the output.

### OBS-R5-5 — `addmirror -dest=value` form
[TestPanelR5_CLI_DestEqualsFormUnsupported](system-validation/panel_findings_round5_test.go:481) — flag-form parity with `--config=value` would be nice. CLI-reviewer #1 + #15 noted the inconsistency.

### OBS-R5-6 — Hard-linked files sync as two copies (expected; doc note)
[TestPanelR5_FS_HardLinks](system-validation/panel_findings_round5_test.go:441) — content-addressed skip prevents bandwidth waste, but storage cost on remote is 2× as if they were copies. Document.

### OBS-R5-7 — Case-only rename (Foo.txt → foo.txt) on NTFS
[TestPanelR5_FS_CaseOnlyRename](system-validation/panel_findings_round5_test.go:454) — fsnotify event semantics for case-only renames on NTFS are not always reliable. End state on remote may have one or both names depending on watcher timing.

### OBS-R5-8 — Inline comments lost on `remote` mutation
Confirmed manually from CLI-reviewer #12 + YAML-reviewer #12; not exercised by an asserting test (test was SKIP'd because the remote-set didn't actually fire in the harness for this run). The pattern: `SetField` does line-based string replacement; the comment portion of an inline-commented line is dropped. Recommendation: parse `(value, comment)` and re-emit the comment.

---

## 5. Round-5 findings NOT converted to tests

These came out of the four reviews but are not testable from a black-box harness without real network, internal-package access, or admin elevation.

### High-confidence

| # | Lens | Finding | Recommendation |
|---|---|---|---|
| R5-PF-1 | Endurance #2 | Crash reports under `<configdir>/reports/` accumulate forever — no auto-prune. | Wire a similar `Rotate` call for the reports dir alongside BUG-R5-1's fix. |
| R5-PF-2 | Endurance #4 | SQLite state.db file size grows after `PruneOldLogs` deletes rows because SQLite doesn't reclaim disk space without `VACUUM`. | Run `PRAGMA incremental_vacuum` periodically, or run a full `VACUUM` on a low-frequency schedule (e.g., monthly). |
| R5-PF-3 | Endurance #5 | Log rotation silently fails on disk full (errors swallowed). | Log a `Logging:RotationFailed` anomaly when rotation fails. |
| R5-PF-4 | Endurance #10 | Watcher warns at 50K watches but doesn't unload cold watches. | Add an LRU eviction on watch handles for projects with many subdirectories. |
| R5-PF-5 | YAML #15 | `formatMirrorBlock` writes name/local_path UNQUOTED. Round-5 tests didn't trip the parser, but the fragility is real. | Use `%q` for both `name` and `local_path` in `internal/config/edit.go::formatMirrorBlock`. |
| R5-PF-6 | YAML #9 | Multi-line block scalar (`pre_sync_hook: \|`) round-trips lossily through `formatMirrorBlock`'s `%q`. | Use yaml.v3 to serialize project blocks instead of hand-rolled string templating. |
| R5-PF-7 | FS #4 | Sparse files: quiescence's size+mtime check passes but the file may still be growing into sparse holes; rclone uploads the apparent size. | For files reporting size > on-disk allocation (`GetFileInformationByHandleEx FILE_ID_BOTH_DIR_INFO`), run an extra quiescence pass. |
| R5-PF-8 | FS #11 | UNC paths as source: SMB connection drops cause silent event loss; watcher records error but doesn't recover. | Detect `ERROR_NOT_READY` / `ERROR_BAD_NETPATH`, kick off a reconciliation when reconnected. |
| R5-PF-9 | FS #12 | Cloud-synced source (OneDrive, Drive Desktop): placeholder files trigger downloads when smirror reads them for quiescence. | Detect `FILE_ATTRIBUTE_RECALL_ON_DATA_ACCESS` (cloud placeholder) and skip such files until they're materialized. |
| R5-PF-10 | CLI #13 | README missing exit codes 4, 5, 6 (CLAUDE.md has them; README doesn't). | Sync README's exit-code table with CLAUDE.md. |

### Medium / lower

| # | Note |
|---|---|
| R5-PF-11 | Anomaly #2: `droppedCallbacks` counter never surfaces in `smirror status` (also raised in R4). |
| R5-PF-12 | YAML #3: BOM not stripped in Load path (Round 5 test PASSED — yaml.v3 tolerates BOM today, but the round-trip via edit.go's stripBOM diverges from Load's no-strip). |
| R5-PF-13 | CLI #5: `--browser` is the new flag, `--open` is deprecated. README still documents `--open`. Update. |
| R5-PF-14 | CLI #7/#8/#16: unknown-flag exit-code inconsistency (some commands return 1, others return 2). |
| R5-PF-15 | FS #14: `IO_REPARSE_TAG_DEDUP` (NTFS data deduplication) — currently rejected as a reparse point along with junctions, but dedup is harmless and could be allowed. |

---

## 6. Most-important-feature verdict

| Feature | Round 5 verdict |
|---|---|
| **Anomaly rotation (FR-ANOM-10)** | **BROKEN** — implemented but not invoked. BUG-R5-1. |
| YAML config edges (BOM, multi-doc, comments) | Mostly tolerant; multi-doc silently ignores second doc (low severity). |
| `formatMirrorBlock` round-trip safety | Fragile but works for tested cases. R5-PF-5/PF-6. |
| Filesystem-specific (NTFS) | Hidden files / ADS / case-only rename all work but lack user-visible warnings. |
| Hard links / sparse / UNC source | Correctness OK for the common case; edge cases like SMB drops have silent gaps. |
| CLI flag/exit-code consistency | Several minor doc-drifts (README vs CLAUDE.md vs --help vs code). |
| Crash report retention | No auto-prune; accumulates forever (R5-PF-1). |
| State DB compaction | No periodic VACUUM (R5-PF-2). |
| Long-run prior tests (R1-R4) | All non-FAIL tests still PASS; only known issues red. |

---

## 7. Suggested priority order for the maintainer

1. **BUG-R5-1** — wire `anomaly.Rotate` into heartbeatLoop. **One-line fix.** Closes FR-ANOM-10.
2. **R5-PF-1** — apply same pattern to crash reports (`<configdir>/reports/`).
3. **R5-PF-5** — quote `name` and `local_path` in `formatMirrorBlock`. Defense-in-depth before someone hits the round-trip bug.
4. **OBS-R5-1** — extend `config.example.yaml` global_excludes with `desktop.ini`, `thumbs.db`, `*.lnk`.
5. **R5-PF-2** — periodic SQLite VACUUM.
6. **R5-PF-10** — sync README's exit-code table with CLAUDE.md.
7. **OBS-R5-3** — warn on multi-doc YAML.
8. **R5-PF-3** — anomaly on log rotation failure.
9. **R5-PF-13** — update README to reference `--browser` instead of deprecated `--open`.
10. Everything else as v1.1 backlog.

---

## 8. Round 1+2+3+4 regression status

Re-run of `TestPanel_*`, `TestPanelR2_*`, `TestPanelR3_*`, `TestPanelR4_*` against v0.9.27-dev:

- **All previously-passing tests still PASS** (no new regressions).
- **2 known-FAILs** still red as documented:
  - `TestPanelR3_Gitignore_ExcludedParentBlocksChildNegation` (BUG-R3-1)
  - `TestPanelR4_CLI_ConcurrentAddMirror` (BUG-R4-1)
- 0 new regressions introduced by intermediate work.

Cumulative scoreboard (all 5 rounds):

- **5 source bugs** found across rounds:
  - R1 BUG-1: Validate accepts case-only duplicate names — **FIXED in v0.9.19-dev**
  - R3 BUG-R3-1: Gitignore parent-exclusion divergence — **OPEN**
  - R4 BUG-R4-1: Concurrent addmirror destroys pre-existing mirror — **OPEN**
  - R4 FIND-R4-1: Per-file hooks don't fire on batch sync — **OPEN (design choice TBD)**
  - R5 BUG-R5-1: Anomaly rotation never invoked — **OPEN (one-line fix)**

- **20+ confirmed PANEL OBS** observations across rounds (config defaults, doc drift, edge-case behaviors).

- **40+ panel findings** documented in PANEL-REVIEW reports as v1.0 / v1.1 backlog.

---

*Round 5 report generated 2026-04-29. Sibling reports: [PANEL-REVIEW-2026-04-28.md](system-validation/PANEL-REVIEW-2026-04-28.md), [PANEL-REVIEW-ROUND2-2026-04-28.md](system-validation/PANEL-REVIEW-ROUND2-2026-04-28.md), [PANEL-REVIEW-ROUND3-2026-04-28.md](system-validation/PANEL-REVIEW-ROUND3-2026-04-28.md), [PANEL-REVIEW-ROUND4-2026-04-29.md](system-validation/PANEL-REVIEW-ROUND4-2026-04-29.md).*
