# SelectiveMirror — Round 8 Panel Review & System-Validation

**Date**: 2026-04-29 (eighth re-validation)
**Version under review**: v0.9.32-dev (HEAD `023a3d8`)
**Reviewer**: Multi-role panel (internal test quality / ancillary code / CI pipeline / cumulative meta-review)
**Validation suite added**: [system-validation/panel_findings_round8_test.go](system-validation/panel_findings_round8_test.go) — 10 tests / 10 PASS / 0 FAIL

---

## 0. Executive summary

- **Round 8 is a CONSOLIDATION round, not a discovery round.** All four lenses targeted previously-unaudited surfaces (internal Go test quality, Cloudflare Worker / SQL / Python / PowerShell ancillary code, CI/release pipeline, and a cumulative meta-review across all 8 rounds).
- **Zero new source bugs surfaced via the test suite.** All 10 tests PASS-with-OBS.
- **The 4 OPEN bugs continue to reproduce in v0.9.32-dev** — none has been picked up between rounds 7 and 8.
- **3 prior PANEL findings shipped between rounds 7 and 8**: GAP-8 (zero-byte DB warn), GAP-9 (stale-lock PID detection), PF-D1 (FairQueue cooldown timer leak).
- The headline meta-finding: **after 8 rounds and ~108 black-box tests, NFR-CA-01 (32-mirror deployment) and disk-full fault injection are STILL untested**. These are the two largest untested surface areas heading into v1.0.

---

## 1. Round 8 method

Four lenses NEW to this round:

| Lens | Why | Findings raised |
|---|---|---|
| **Internal Go test quality** | The 640+ internal tests are the daily-driver safety net. If they have weak assertions, hidden production bugs slip through. Sample for false-positive patterns. | 15 |
| **Ancillary code** | Cloudflare Worker, Supabase SQL, Python validation script, PowerShell test runners — none touched in 7 prior rounds. | 23 |
| **CI/release pipeline** | .github/workflows/, .goreleaser.yaml, MSI installer, winget — all untouched. | 14 |
| **Cumulative meta-review** | After 7 rounds and ~98 tests, what's STILL under-tested? Where would a new bug class live? | structured cumulative gap analysis |

Total raw: **~52 findings + meta-analysis**. Converted to **10 verifiable tests** (file-inspection style; the auditor findings deeper than file-inspection get tracked as PANEL OBS without their own asserting test).

---

## 2. Test results

```
go test -timeout 600s -count=1 -run "TestPanelR8_" ./system-validation/...
```

| # | Test | Result | Note |
|---|---|---|---|
| 1 | `CI_NoStaticSecurityScanning` | PASS (with OBS) | No `gosec` / `gitleaks` / `codeql` references in any workflow file. |
| 2 | `CI_NoDependabotConfig` | PASS (with OBS) | No `.github/dependabot.yml`, no `renovate.json`. Dependencies never auto-updated. |
| 3 | `CI_ChecksumsNotSigned` | PASS (with OBS) | No `minisign` / `cosign` / GPG-sign step in `release.yml` or `.goreleaser.yaml`. checksums.txt is SHA256-only. |
| 4 | `CI_WingetManifestPlaceholder` | PASS (with OBS) | winget yaml has `PLACEHOLDER` / TODO marker; no automation computes the MSI hash. |
| 5 | `PowerShell_NoStrictMode` | PASS (with OBS) | None of `run_tests.ps1`, `verify.ps1`, `stress_test.ps1`, `sla_smoke.ps1` invoke `Set-StrictMode`. |
| 6 | `PowerShell_HardcodedPaths` | PASS (with OBS) | Hardcoded `C:\Program Files\Go` and `C:\SelectiveMirror` found in test scripts. |
| 7 | `InternalTests_WeakWhitespaceOnlyTest` | PASS (with OBS) | `TestToRcloneFilter_WhitespaceOnly` body has no `t.Errorf` / `t.Fatal` — log-only. |
| 8 | `OpenBugsScoreboard` | PASS | Documents the 4 OPEN bugs and the 9 newly-shipped fixes from rounds 6-7. |
| 9 | `Meta_NFR_CA_01_StillUntested` | PASS (with OBS) | After 8 rounds, the 32-mirror deployment claim is still not exercised. |
| 10 | `Meta_DiskFullNeverTested` | PASS (with OBS) | No round has injected disk-full fault. With BUG-R5-1 (anomaly rotation dead code) + 30+ LogAction error-suppression sites, this is the highest-risk untested surface. |

**Score**: 10 PASS / 0 FAIL. All findings are documented OBS for the maintainer's backlog.

---

## 3. Newly-CLOSED items (between rounds 7 and 8)

| Finding | Origin | Closed In |
|---|---|---|
| **GAP-8** (zero-byte state.db warn) | R5 OBS | 0.9.31-dev |
| **GAP-9** (stale-lock PID detection) | R2 PF + R4 OBS | 0.9.31-dev |
| **PF-D1** (FairQueue cooldown timer goroutine leak) | R2 senior-dev #1 | 0.9.32-dev |
| **SEC-M3** (closed by GAP-1) | audit + R1 GAP-1 | 0.9.30-dev |
| **SEC-M12 + SEC-M13** | audit | 0.9.32-dev |

---

## 4. Status of the 4 OPEN bugs against v0.9.32-dev

| Finding | Rounds Open | Re-confirmation |
|---|---|---|
| BUG-R3-1 (gitignore parent-exclusion divergence) | 5 (since R3) | Failed 1/1 against v0.9.32-dev |
| BUG-R4-1 (concurrent addmirror destroys data) | 4 (since R4) | Failed 1/1; SEC-M6 atomic writes did not close it |
| BUG-R5-1 (anomaly.Rotate dead code) | 3 (since R5) | Failed 1/1; source-tree scan still finds zero callers |
| FIND-R4-1 (per-file hooks skip batch sync) | 4 (since R4) | Failed 1/1 |

---

## 5. Round-8 PANEL OBS — confirmed via tests

### CI/Release pipeline (4 OBS)
- **OBS-R8-1**: no static security scanning in CI (gosec / gitleaks / CodeQL)
- **OBS-R8-2**: no Dependabot / Renovate config — Go modules never auto-updated
- **OBS-R8-3**: checksums.txt unsigned — SHA256-only integrity, no signature for authenticity
- **OBS-R8-4**: winget manifest InstallerSha256 = `PLACEHOLDER`; computed manually post-release

### PowerShell test scripts (2 OBS)
- **OBS-R8-5**: no `Set-StrictMode` in run_tests.ps1, verify.ps1, stress_test.ps1, sla_smoke.ps1 — undefined-variable typos silently `$null`
- **OBS-R8-6**: hardcoded `C:\Program Files\Go` and `C:\SelectiveMirror` — breaks portability

### Internal test quality (1 OBS)
- **OBS-R8-7**: `TestToRcloneFilter_WhitespaceOnly` is log-only (no asserting `t.Errorf`/`t.Fatal`)

### Meta — long-standing untested surfaces (2 OBS)
- **OBS-R8-8**: NFR-CA-01 (32 mirrors) still untested after 8 rounds. Round 3 tested 5; Round 5 tested 2-vs-8 startup. 32-mirror claim is unverified.
- **OBS-R8-9**: disk-full (ENOSPC) fault injection NEVER performed. Combined with BUG-R5-1 (anomaly rotation dead) and 30+ LogAction error-suppression sites (R7), this is the highest-risk untested scenario class.

---

## 6. Round-8 panel findings NOT converted to tests

Many of the round-8 findings require infrastructure (Cloudflare KV state, Supabase test DB, network fault injection, large-scale stress runs) that the black-box harness can't directly exercise. Track for backlog.

### Ancillary code (Cloudflare Worker / SQL / Python) — high-value backlog

| # | Source | Finding | Severity |
|---|---|---|---|
| R8-PF-1 | Worker | Non-atomic `kv.get()`/`kv.put()` in rate limiter — bursts can exceed claimed 30/min | Medium |
| R8-PF-2 | Worker | Body-size cap based on `Content-Length` only; chunked-transfer with omitted Content-Length bypasses 100KB cap | Low |
| R8-PF-3 | SQL / Worker integration | Canonical JSON length-first key ordering not enforced at ingest. Other-language clients (Go's `encoding/json`, Python's `sort_keys=True`) silently fail HMAC. | **High** (footgun for future clients) |
| R8-PF-4 | SQL | `install_id` stored plaintext in 3 tables. Privacy doc claims anonymity but trivial cross-correlation if SQL access is ever obtained. | Medium |
| R8-PF-5 | Python | `md_cell_escape` doesn't block markdown links (`[text](https://...)`). A malicious signature could phish via the public digest. | Low |
| R8-PF-6 | Python | K-anonymity floor (`<5`) applied inconsistently — bug section shows `<K`, recurrence section silently filters | Low |

### Internal Go test quality (3 high-confidence)

| # | Test | Issue | Severity |
|---|---|---|---|
| R8-PF-7 | `TestSupervisor_ProbeErrorDoesNotKillIfOutputMoves` | Removed (per code comment) — the supervisor's probe-error tolerance has no direct regression guard | High |
| R8-PF-8 | `TestFairQueue_ConcurrentEnqueueDequeue` | Missing `t.Parallel()` — concurrency stress serializes when run with the rest of the suite | High |
| R8-PF-9 | `TestTrackDeleteBurst_AtThreshold_TriggersReconciliation` | No assertion that the reconciliation goroutine was actually launched — counter arithmetic checked, side effect not | **Critical** (FR-WATCH-07 mitigation has no proof it fires) |

### CI / release pipeline (5 medium-impact)

| # | Source | Finding | Recommendation |
|---|---|---|---|
| R8-PF-10 | ci.yml race step | Race detector runs on 8/11 packages; missing internal/cmd, internal/engine, internal/http | Extend race detector list to `./internal/...` |
| R8-PF-11 | ci.yml race step | No `-count=1` on race tests — Go cache may skip re-runs | Add `-count=1` |
| R8-PF-12 | release.yml | Doesn't cross-link to ci.yml (GHA `needs:` doesn't support cross-workflow). Mitigated by re-running tests in release.yml | Document the structural weakness in release.yml comment |
| R8-PF-13 | .goreleaser.yaml | `draft: true` + `prerelease: auto` — release visibility is ambiguous | Pick one; document explicitly |
| R8-PF-14 | release.yml | telemetry buildKey embedded via LDFLAGS; no runtime validation of key format/length | Add a one-line check in `internal/telemetry.init()` and warn if the key is malformed |

---

## 7. Cumulative meta-review — top untested surfaces

The meta-reviewer's structured gap analysis surfaces these as the highest-value items the suite has NOT covered after 8 rounds:

### NFRs marked "Not Measured" / "Not Tested" (highest leverage)

- **NFR-CA-01**: 32 mirrors without degradation
- **NFR-CA-02**: 100K files per mirror
- **NFR-TB-03**: < 60s p95 sync for 100 MB files (large-file)
- **NFR-TB-06**: > 100 events/sec queue throughput
- **NFR-TB-07**: < 40s service restart total
- **NFR-RU-02**: < 80 MB RSS at 10K queued events
- **NFR-RU-04**: < 10 IOPS per file sync

### System-level scenarios never simulated

- 32-mirror deployment under load
- 100K files per mirror (state DB performance)
- Daemon running uninterrupted for 30 days
- Disk-full (ENOSPC) during sync / anomaly write / state DB write
- Multi-day filter churn (rapid `.syncignore` reload over hours)
- 32-mirror + 10K files + concurrent CLI mutations (combines BUG-R4-1 + scale)
- Real cloud-backend integration (Drive, S3, Dropbox) — only manual today
- Rclone version compatibility matrix (1.73 → hypothetical 2.0)

### Operational scenarios missed

- Backup / restore of state.db
- Cross-machine config migration
- Service upgrade from v0.9.x to v1.0 under load
- Rollback after a bad upgrade (state-DB forward-version refusal helps; not tested)

### Cross-platform untested

- POSIX symlink-to-dir reject (R2-PF-1; never implemented for POSIX)
- Sparse files (R5-PF-7)
- Cloud-placeholder files (OneDrive Files-On-Demand, Drive Desktop) — R5-PF-9

---

## 8. The most-actionable finding for v1.0

The meta-reviewer's prioritized top-5:

1. **BUG-R5-1** — wire `anomaly.Rotate()`. **One-line fix. 3 rounds open.**
2. **NFR-CA-01** — 32-mirror stress test (highest-leverage untested surface)
3. **BUG-R4-1** — file-level lock around config.yaml writes
4. **Disk-full fault injection** (R7-PF-8/PF-9 + R8 OBS-R8-9)
5. **R8-PF-9** — convert `TestTrackDeleteBurst_AtThreshold_TriggersReconciliation` from counter-only to side-effect assertion

If those five land before v1.0, the largest gap classes (durability + scale + atomicity + audit-trail + integration-completeness) are eliminated.

---

## 9. Cumulative scoreboard (rounds 1-8)

- **32 panel review runs** across 8 rounds × 4 lenses
- **~108 black-box tests** authored across 8 round files
- **5 source bugs found**: 1 fixed (BUG-1, R1, fixed in v0.9.19), 4 OPEN
- **8 prior PANEL findings shipped** during the run-cycle:
  - BUG-1 (R1): case-only duplicate names → fixed v0.9.19
  - GAP-1..GAP-7 (R1): config validation hardening → fixed v0.9.20-21
  - PF-A3, PF-A7, PF-A8 (R1-R2): service-mode + concurrency → fixed v0.9.21-22
  - GAP-8 (R5): zero-byte DB warn → fixed v0.9.31
  - GAP-9 (R2/R4): stale-lock PID detection → fixed v0.9.31
  - PF-D1 (R2): FairQueue cooldown timer leak → fixed v0.9.32
- **~150 documented OBS / PF backlog items** across all rounds
- **Zero regressions** introduced by maintainer commits during the 8-round series

---

## 10. Verdict — should the maintainer keep running these rounds?

After 8 rounds of multi-role review with diminishing returns:

**Round-by-round bug discovery rate**:
- R1: 1 bug + many config gaps (mostly fixed)
- R2: 0 bugs, 1 OBS
- R3: 1 bug (gitignore parent-exclusion)
- R4: 1 bug + 1 finding
- R5: 1 bug
- R6: 0 new bugs (all 4 OPEN re-confirmed)
- R7: 0 new bugs (all 4 OPEN re-confirmed)
- R8: 0 new bugs (all 4 OPEN re-confirmed)

**Recommendation**: instead of continuing identical "multi-role panel review" rounds, the next valuable iteration would be **a STRESS / SCALE round**: implement the 32-mirror NFR-CA-01 stress test, the disk-full fault injection, and the 30-day endurance simulation. These would surface a new class of bugs (or confirm the system holds at scale).

The diminishing returns of further panel reviews are real. The remaining backlog is well-defined and actionable. The 4 OPEN bugs all have specific remediations of size "one line" to "small." The biggest untested area is now SCALE — which needs a different test methodology, not more panel reviews.

---

*Round 8 report generated 2026-04-29. Sibling reports: rounds 1-7 PANEL-REVIEW-*.md.*
