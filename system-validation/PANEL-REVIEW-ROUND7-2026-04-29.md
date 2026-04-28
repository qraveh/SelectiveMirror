# SelectiveMirror — Round 7 Panel Review & System-Validation

**Date**: 2026-04-29 (seventh re-validation)
**Version under review**: v0.9.31-dev (HEAD `7725cd6`; project moved 0.9.30 → 0.9.31 mid-round)
**Reviewer**: Multi-role panel (rclone subprocess depth / state DB migration / error handling / security claims final pass)
**Validation suite added**: [system-validation/panel_findings_round7_test.go](system-validation/panel_findings_round7_test.go) — 10 tests / 7 PASS / 0 FAIL / 3 SKIP

---

## 0. Executive summary

- **Zero new source bugs** surfaced via the test suite this round.
- **Two prior PANEL findings became maintainer-shipped fixes between rounds 6 and 7**:
  - GAP-8 (zero-byte state.db warn) — round 5 R5 OBS
  - GAP-9 (stale-lock PID detection) — round 2 PF + round 4 OBS
- **The 4 remaining OPEN bugs all still reproduce** against v0.9.31-dev:
  - BUG-R3-1 (gitignore parent-exclusion divergence)
  - BUG-R4-1 (concurrent `addmirror` destroys data; SEC-M6 atomic writes did NOT close it)
  - BUG-R5-1 (`anomaly.Rotate` dead code; carried 2 rounds without action)
  - FIND-R4-1 (per-file hooks don't fire on batch sync)
- **53 raw round-7 findings** distributed: rclone-subprocess 15, state-DB 15, error-handling 16, security-claims 27 (with 4 Mismatches). Most testable converted to 10 tests (3 SKIP for fault-injection cases, 7 PASS-with-OBS).
- **Largest backlog: error-handling**. 30+ sites where `state.LogAction()` errors are silently discarded (audit-trail loss potential), plus 5+ places where `state.DeleteFileState()` errors are ignored after rclone success (state vs remote divergence on disk-full).

---

## 1. Round 7 method

Four lenses NEW to this round:

| Lens | Why | Findings raised |
|---|---|---|
| **rclone subprocess depth** | The entire system rests on driving rclone correctly. Argument quoting, env passing, exit-code interpretation, output bounds. | 15 |
| **State DB migration & schema** | Migrations are not transactional; no integrity check at Open; no VACUUM after prune; FK contracts loose. | 15 |
| **Error handling completeness** | NFR-OP-01 says "actionable" with status Partial. Audited 30+ specific sites for silent swallow / wrong level / missing anomaly. | 16 |
| **Security claims final pass** | 27-row matrix of every NFR / SEC / PRIVACY claim against the implementation. Status: Verified / Partial / Unverified / Mismatch. | 27 (incl. 4 Mismatches) |

---

## 2. Test results

```
go test -timeout 600s -count=1 -run "TestPanelR7_" ./system-validation/...
```

| # | Test | Result | Note |
|---|---|---|---|
| 1 | `Reconfirm_AllOpenBugsStillOpen` | PASS | Documents the cumulative status of the 4 OPEN bugs against v0.9.31-dev. |
| 2 | `StateDB_NoIntegrityCheckOnOpen` | PASS (with OBS) | Corrupted state.db page-4 data did NOT trigger a corruption hint to `smirror status`. `Open()` does not run `PRAGMA integrity_check`; only `test-mirrors` does. |
| 3 | `StateDB_NoVacuumAfterPrune` | PASS (with OBS) | Source-tree scan finds zero VACUUM call sites. PruneOldLogs deletes rows; SQLite never reclaims disk space. |
| 4 | `StateDB_MigrationsNotTransactional` | PASS (with OBS) | `state.go` does not wrap migrations in `db.Begin/Commit`. Power-loss recovery relies on error-string matching. |
| 5 | `Rclone_RemoteWithSpacesAccepted` | PASS | Go's `exec.Command` quoting handles spaces correctly on Windows; rclone reviewer #15's concern doesn't manifest in practice for this case. |
| 6 | `Rclone_EnvVarPassthrough` | SKIP | Requires fault-injection on env; recommend internal-package test. |
| 7 | `Errors_DeleteFileStateErrorIgnored` | SKIP | Requires fault-injection on state.DeleteFileState; recommend internal-package test. |
| 8 | `Security_TelemetryBackendTypesLeaksRemoteNames` | PASS (with OBS) | Source scan checks for the leak pattern; flags if telemetry's BackendTypes extraction uses remote-name (left-of-colon) directly. |
| 9 | `Security_StatusJsonSanitizationScope` | SKIP | status.json not produced when sync fails fast against a bogus remote; needs a successful-then-failing scenario. |
| 10 | `Security_BinaryNotSigned` | PASS | Documents that source-built binary is unsigned (as expected). MSI builds need to land SignPath. |

**Score**: 7 PASS / 0 FAIL / 3 SKIP. All findings are OBS — no new source-bug claims; the round's value is in the documented gaps + the security-claims matrix in §4.

---

## 3. Status of the 4 OPEN bugs against v0.9.31-dev

| Finding | Status | Evidence |
|---|---|---|
| BUG-R3-1 (gitignore parent-exclusion) | **STILL OPEN** | `TestPanelR3_Gitignore_ExcludedParentBlocksChildNegation` fails 1/1 |
| BUG-R4-1 (concurrent addmirror destroys data) | **STILL OPEN** | `TestPanelR4_CLI_ConcurrentAddMirror` fails 1/1. SEC-M6 atomic writes alone aren't enough; the read-modify-write window still races. |
| BUG-R5-1 (anomaly.Rotate dead code) | **STILL OPEN** | `TestPanelR5_Endurance_AnomalyRotationNeverCalled` fails. Source-tree scan against v0.9.31-dev confirms zero production callers. **Carried 2 rounds without action.** |
| FIND-R4-1 (per-file hooks skip batch sync) | **STILL OPEN** | `TestPanelR4_Hooks_EnvVarsActuallyExported` fails; `TestPanelR6_Adv_SyncNowSkipsPerFileHooks` confirms with focused canary. |

### Newly-CLOSED items (between rounds 6 and 7)

| Finding | Origin | Resolution |
|---|---|---|
| GAP-8 (zero-byte state.db) | R5 OBS | 0.9.31-dev: warn-on-zero-byte added |
| GAP-9 (stale-lock PID detection for read-only commands) | R2 + R4 OBS | 0.9.31-dev: PID liveness check added to lock-file Acquire path |

---

## 4. Security claims matrix (round-7 final pass)

The full 27-entry table is in the panel reviewer's transcript. Headline:

- **14 Verified** — implementation matches claim (e.g., NFR-CO-01 credentials never logged, NFR-IN-01 checksums used, SEC-C2 perMachine MSI, SEC-H3 TOCTOU re-Lstat, SEC-H4 reparse detection, SEC-H7 state-DB symlink reject)
- **7 Partial** — implementation exists but has gaps:
  - **NFR-CO-03** (sanitize diagnostic reports) — `report-bug` sanitizes home-dir; `status.json` LastError includes raw paths; log lines may contain `--config` and rclone stderr
  - **NFR-NR-01** (every action in sync_log) — batch sync logs ONE row per project, not N per file
  - **SEC-C4** (webhook DNS-rebind defense at dial time) — claimed but not independently verifiable from black-box
  - **SEC-C5** (hook injection hardening) — admin-config gate works; metachar filter "skips hook silently" without surfacing
  - **SEC-H1** (rclone_extra_flags allowlist) — denylist exists for `--rc*`, `--config`, `--log-file`; subtle indirect flags (`--drive-impersonate`) may slip
- **2 Unverified**:
  - **SEC-H5** (service-mode symlink reject) — code field exists but execution path not directly confirmed
  - **SEC-H10** (rclone stderr redaction) — audit flagged 2026-04-18; CHANGELOG since does not list a fix
- **4 Mismatches** — the actionable items:

### Mismatches — top 4 actionable

#### M-1 — Telemetry BackendTypes leaks user-defined remote names (High)

PRIVACY.md claims "only backend type" (e.g., `gdrive` vs `s3`). Implementation transmits user-defined rclone remote NAMES (e.g., `acmecorp-prod-drive`, `client-ABCD-bucket`). Combined with stable install-id + OS + arch, enables behavioral fingerprinting and de-anonymization.

**Remediation**: in `internal/telemetry/telemetry.go::ExtractBackendTypes`, parse `rclone.conf` to look up the actual `type` field for each remote name; emit only the type. Or hash names with a per-install salt. Or drop the field entirely if the privacy-vs-debug tradeoff favors privacy.

#### M-2 — `go mod verify` not a release gate (Medium)

CI runs `go mod verify` (per ci.yml) but `release.yml` does not depend on `ci.yml`. A tag pushed onto a commit whose CI failed would still publish.

**Remediation**: add `needs: [ci]` to release job, or re-run `go mod verify` in release.yml before build. (The CHANGELOG already documents that `release.yml` runs `go vet` + `go test` directly — extend with `go mod verify`.)

#### M-3 — Code signing claim vs implementation (Medium)

SECURITY.md describes SignPath Foundation plan as "post-v1.0". Currently, binaries are SHA256-only (integrity, not authenticity). v1.0 selfupdate path verifies SHA but cannot detect a swap-and-resign attack on the release pipeline.

**Remediation**: as a stopgap, sign with `minisign` or `cosign` keyless until SignPath comes through. Document in SECURITY.md whether v1.0 ships unsigned or with a stopgap.

#### M-4 — Sanitization scope inconsistency (Medium, NFR-CO-03)

`report-bug` redacts home-dir; `status.json` LastError does not; log lines may contain `--config` paths and rclone stderr (which can carry OAuth tokens, signed URLs).

**Remediation**: factor a single `sanitize.Redact` helper out of `internal/telemetry/sanitize.go` and use it in:
- `internal/metrics/metrics.go::WriteStatusFile` for the LastError field
- `internal/logging/logging.go` for log lines containing rclone subprocess output
- `internal/anomaly/sanitize.go` already does this

---

## 5. Round-7 panel findings NOT converted to tests

These are the most-actionable backlog items from the four reviews.

### High-confidence

| # | Lens | Finding | Recommendation |
|---|---|---|---|
| R7-PF-1 | rclone #1 | `cmd.Output()` in `ListRemote` and `PurgeExpiredQuarantine` captures unbounded output. 50K-file lsjson can be hundreds of MB in heap. | Stream-parse via `cmd.StdoutPipe()` + `json.Decoder.Token()`; don't buffer the whole output. |
| R7-PF-2 | rclone #3 | No timeout context on metadata `cmd.Output()` calls (ListRemote, PurgeExpiredQuarantine, Validate). A hung rclone blocks reconciliation indefinitely. | Wrap in `exec.CommandContext` with a `context.WithTimeout` matching the liveness supervisor's metadata-bucket grace (240s). |
| R7-PF-3 | rclone #11 | "Single rclone per backend" claimed in CLAUDE.md but not enforced. Two workers can spawn rclone concurrently against the same remote, causing thundering-herd. | Per-backend semaphore (keyed on `proj.Remote` prefix or a hash of the remote-config stanza). |
| R7-PF-4 | rclone #12 | `stderrBuf` (strings.Builder) is unbounded. With user-configured `--verbose --verbose`, a single failure can dump 100+ MB of debug into the buffer. | Cap at 64 KB and append `[... truncated ...]` marker. |
| R7-PF-5 | state-DB #2/#4 | Migrations not in `db.Begin/Commit`. A power loss mid-ALTER leaves the DB partially-migrated; idempotency relies on error-string match. | Wrap each migration step in a transaction. SQLite supports DDL inside transactions. |
| R7-PF-6 | state-DB #5 | No `PRAGMA integrity_check` at Open. Corruption discovered only by `test-mirrors`. | Run integrity_check on Open; refuse to start if status != "ok". |
| R7-PF-7 | state-DB #7 | No VACUUM after PruneOldLogs. DB file size grows unbounded as rows are deleted but space isn't reclaimed. | Schedule weekly `db.Exec("VACUUM")`, or after PruneOldLogs deletes more than N rows. |
| R7-PF-8 | error-handling #16 | 30+ call sites of `state.LogAction()` ignore returned errors. Disk-full silently loses audit trail. | Wrap LogAction calls in a helper that records `Anomaly.Record(KindStateDBIntegrity, ...)` on error. |
| R7-PF-9 | error-handling #2/#5 | `state.DeleteFileState` errors ignored after rclone deletefile success. State vs remote divergence on disk-full. | Same pattern: anomaly + visible failure log. |
| R7-PF-10 | security M-1 | Telemetry BackendTypes leaks user-defined remote names. | Parse rclone.conf for actual type; or hash; or drop. |
| R7-PF-11 | security M-2 | `go mod verify` not a release gate. | Add `needs:` to release job. |
| R7-PF-12 | security M-4 | Sanitization scope: status.json LastError + log lines with rclone stderr include un-redacted paths/tokens. | Centralize sanitizer; apply uniformly. |

### Medium / lower

| # | Note |
|---|---|
| R7-PF-13 | rclone #2: lsjson truncation not distinguished from parse error. Operator can't tell "rclone crashed" from "JSON parse failure". |
| R7-PF-14 | state-DB #10: missing indexes on `(project)` and `(project, rel_path)`. 1M-file state DB scans become slow. |
| R7-PF-15 | state-DB #14: schema not documented anywhere besides CREATE TABLE statements. |
| R7-PF-16 | error-handling #14: watcher healthErrors slice is bounded-but-lossy at 100 entries. Early errors fall off. |
| R7-PF-17 | rclone #6: `RCLONE_CONFIG_PASS` env var inherited by subprocess. Mostly OK (intentional for encrypted configs), but no explicit allowlist. |
| R7-PF-18 | rclone #14: user `--config` in `rclone_extra_flags` last-wins over global. Fragile if rclone's flag-precedence ever changes. |

---

## 6. Most-important-feature verdict

| Feature | Round 7 verdict |
|---|---|
| Per-file sync correctness | Solid — extensively tested across rounds. |
| Per-file hooks (live watcher) | Solid. |
| Per-file hooks (batch sync) | **Still missing** (FIND-R4-1, OPEN). |
| Anomaly retention | **Still broken** (BUG-R5-1, OPEN). Files accumulate forever. |
| State DB integrity | Documented robustness gaps: no integrity_check on Open, no VACUUM, migrations not transactional. None observed-in-the-wild yet but combine power-loss + several months of operation and they'll surface. |
| CLI mutation atomicity | Per-write atomic (SEC-M6); read-modify-write race still open (BUG-R4-1). |
| Filter accuracy (gitignore conformance) | 11/12 cases pass; **parent-exclusion divergence** still open (BUG-R3-1). |
| Telemetry privacy claims | M-1 (backend-name leakage), M-4 (sanitization scope), M-2 (release gate), M-3 (code signing) are gaps in the published claims. |
| rclone subprocess robustness | Layer-2 stall detection is solid; metadata-path timeouts and per-backend serialization are NOT. |
| Error handling completeness | Systematic LogAction-error-suppression is the broadest gap; ~30 sites. |

---

## 7. Suggested priority order (rolled forward from prior rounds)

The list of priorities has not materially shifted from rounds 5/6. The most-shippable items are:

1. **BUG-R5-1** — wire `anomaly.Rotate()`. **One-line fix. Carried 2 rounds.**
2. **BUG-R4-1** — file-level lock around config.yaml writes (SEC-M6 atomic writes alone don't close it).
3. **R7-PF-7** — VACUUM after PruneOldLogs. Single-line `db.Exec("VACUUM")` once per week.
4. **R7-PF-6** — `PRAGMA integrity_check` at Open.
5. **R7-PF-10 / SEC-M-1** — telemetry BackendTypes name-leakage. PRIVACY.md mismatch. Important before public 1.0.
6. **R7-PF-3** — per-backend semaphore for rclone subprocess. Closes the documented "single rclone per backend" claim.
7. **R7-PF-12 / SEC-M-4** — unify sanitization across status.json, log file, report-bug.
8. **R7-PF-8** — anomaly on LogAction error (defense for the audit-trail-loss gap).
9. **BUG-R3-1** — gitignore parent-exclusion (or document the divergence in SRS).
10. **FIND-R4-1** — decide hook semantics for batch sync.

---

## 8. Cumulative scoreboard (rounds 1-7)

- **28 panel review runs** across 7 rounds × 4 lenses
- **~98 black-box tests** authored
- **5 source bugs found** (1 fixed, 4 OPEN — all 4 confirmed reproducing in v0.9.31-dev)
- **2 prior PANEL findings shipped between rounds 6 and 7**: GAP-8 (zero-byte DB) + GAP-9 (stale-lock PID)
- **~110 documented findings** across PANEL OBS / PF / Mismatch backlog
- **Zero regressions** introduced by any maintainer commit during the 7-round series

The system is increasingly mature. The 4 still-OPEN bugs are now well-defined and have one-line-to-small remediations. After those land, v1.0 readiness is dominated by the larger backlog items (per-backend rclone semaphore, sanitization unification, telemetry leakage, state-DB durability hardening).

---

*Round 7 report generated 2026-04-29. Sibling reports: rounds 1-6 PANEL-REVIEW-*.md.*
