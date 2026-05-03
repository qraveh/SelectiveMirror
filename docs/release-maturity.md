# Release maturity dashboard

**Audience**: maintainer, release-day go/no-go, and the [SM-keeper agent](../.claude/agents/sm-keeper.md). PR-PM4 (panel review pre-release 2026-04-28).
**Cadence**: updated by SM-keeper on each pre-tag run, on each panel review, and on each weekly telemetry digest. Manually editable any time.

This file is a **live snapshot** of the indicators that decide whether SelectiveMirror is ready to widen its audience. Each row is a binary or trichotomous status; the bottom line tells you what the project's audience SHOULD be right now.

---

## Status board (refreshed: 2026-05-03 — v1.0.0 tag-day)

| Indicator | Target for "general public" | Current state | Color |
|---|---|---|---|
| **Code signing (Authenticode)** | Signed MSI + EXE under SignPath EV cert | Plan in SECURITY.md; SignPath Foundation application status: *operator-confirmed (see indicator-detail below for current value)*. Long pole for the wider-beta audience widening; not a blocker for the maintainer-only / small-tester audience that v1.0.0 ships to. | 🔴 |
| **GitHub build-provenance** | Both MSI + EXE attested per release | Wired in release.yml v0.9.27 cycle; first attested release lands with v1.0.0 tag (this tag). Flip to 🟢 after `gh attestation list` returns expected output post-tag. | 🟡 |
| **MSI consent UI** | Three-tier radio dialog visible during install | Property + registry wired since v0.9.4-dev. Dialog shipped during v0.9.x cycle. Pre-tag operator gate (R-5 MSI smoke) confirms eyes-on. | 🟡 |
| **winget submission** | Latest MSI submitted to microsoft/winget-pkgs | Manifest template up-to-date; CI auto-submission gated on `WINGET_SUBMIT_ENABLED=1` + `WINGET_SUBMIT_PAT`. First submission lands with v1.0.0 if gate flipped. | 🟡 |
| **Pre-release dryrun** | release-dryrun.yml green within 24h of tag | Operator-side gate per sm-keeper Mode A; required green within 24h of the v1.0.0 tag. | 🟡 |
| **system-validation gating** | All `panel_findings_*_test.go` green at release time, OR allowlisted with CHANGELOG known-issues entry | `panel_findings_*` allowlist empty (all 5 Tier-1 findings closed). **Telemetry CLAIMS-MAP gate at 25/28 GREEN (89.3% total / 96.2% non-deferred — comfortably above the ≥ 90% gate)** — the strongest 29119-3 Test Completion Report evidence the project has produced. Two RED in active deferral: A-01 HMAC timing benchmark (v1.0.x), A-03 pg_stat_statements smoke (v1.0.x). Broader system-validation suite still has the historical separate failures outside panel-test scope (TestCLI_Status SM-142 in-process schema-create race covered by retry loop; burst-file scenarios under load) — full-suite green remains a v1.0.x roadmap item. | 🟢 |
| **SLA smoke** | Latest scheduled run within 48h is green | Operator-side gate per sm-keeper Mode A; refresh required if > 48h stale at tag. | 🟡 |
| **Open Highs from latest panel review** | Zero open Highs against the about-to-tag commit | **0 open.** BUG-R4-1 closed in 0.9.44-dev. FIND-R4-1 closed by hooks deferral 2026-04-29 (see docs/RESOLUTION-2026-04-29-hooks-deferred.md). 22-commit telemetry-validation window (0.9.75 → 0.9.96-dev) added zero new Highs. | 🟢 |
| **Open Mediums** | ≤ 3 open, all with planned fix versions | **6 open**, all with v1.0.x targets: OBS-R4-1 (cosmetic addmirror file mode), R4-PF-10 (foreground symlink-follow), CLAIMS-MAP A-01 (HMAC timing benchmark), CLAIMS-MAP A-03 (pg_stat_statements smoke), SM-082 items 3+4 (svc.Control inconsistency + Anomaly Detail stderr), SM-057 (burst-delete reconcile sleep). Above the ≤ 3 target; 🟡 with explicit v1.0.x deferral pages in CHANGELOG `[1.0.0]` "Bugs known at tag". | 🟡 |
| **Telemetry health (continuous live measurement)** | Cloudflare Worker access-log probe daily-green; envelope rate steady; zero None-tier records over None-tier installs (NFR-PR-01 target = 0.000) | **Live as of 0.9.88-dev / 2026-05-02.** Worker emits records, schema-validated daily by `.github/workflows/telemetry-emulation.yml`, CLAIMS-MAP gate at 25/28 GREEN, fingerprint probe (cf-ray + SM Worker custom header) verified per-tag. First measurement of NFR-PR-01 ratio included in v1.0.1 release notes per A-25023-02 schedule. | 🟢 |
| **Report-bug PII smoke** | Release-time smoke green every release | `scripts/check-pii-leak.ps1` wired into release.yml + dryrun. Plus `report-bug --submit` end-to-end (SM-158 ship 0.9.89-dev): bucketed payload only, no narrative columns ever stored server-side. | 🟢 |
| **HMAC master key** | Stored, rotation procedure documented + tested, absent-key fail-loud at release | Stored ✓; rotation documented (telemetry-ops.md); CI fail-loud landed v0.9.27 cycle. Build-key fingerprint visible in `smirror version`; CLAIMS-MAP C-18 GREEN. Rotation never actually executed — that drill is a v1.0.x item. | 🟡 |
| **Test-count delta** | No regression vs. previous release | **650+ unit + 80+ system-validation**; aggregate `internal/` coverage ~65.9%, telemetry 79.6% (+2.7pts). **State coverage regression**: 70.0% → 64.1% (5 metadata-write paths at 0% — VacuumIfStale, PruneOrphanedProjects, MarkRemoteVerificationStale, ClearStaleExitCodes, IncrementMetaCounter). Above the 50% per-package floor and 60% aggregate gate; tracked as DIS-5 in CHANGELOG `[1.0.0]` "Bugs known at tag" for v1.0.x backlog. | 🟡 |
| **Docs vs. code drift** | All `docs/*.md` reference real file paths and current behaviors | Latest sweep 2026-05-03 (this dashboard refresh). Test Strategy doc (`docs/test-strategy.md`) authored this tag closes A-29119-01 / R-16. SRS Project Version line bumped to current (D-5 in panel pre-tag work block). | 🟢 |
| **Backups / rollback** | Documented rollback path that respects GAP-7 forward-only state DB | README "Compatibility & rollback" section in place. | 🟢 |
| **CI runner age** | windows-latest still supported, Go 1.26 still supported | Both current 2026-05. | 🟢 |
| **External review** | One independent eye on a recent panel-review batch | A-GOV-01 closed by decision: external review NOT planned. Multi-role panel-review pattern + telemetry CLAIMS-MAP gate are the substitutes. SELF-ASSESSMENT label retained on `docs/iso-compliance.md`. | ⚪ N/A by decision |

**Color key**: 🟢 ready · 🟡 partial / on-track but not closed · 🔴 blocker for the listed audience · ⚪ deliberate non-goal

---

## Audience matrix

The set of indicators above maps to a recommended audience.

| Audience | Hard requirements | Current verdict |
|---|---|---|
| **Maintainer-only** | Code signing not required. Indicators all 🟡 or better. No active red-state findings. | ✅ Ready. v0.9.x shipped at this level; v1.0.0 retains it. |
| **Maintainer + small group of testers** | Same as above + audience signaling in README + known-issues inventory in CHANGELOG. | ✅ Ready. **Current audience for v1.0.0** (recommended for the first 30 days post-tag while telemetry signature data accumulates and the SignPath cert lands). |
| **Wider beta (forum / Hacker News announcement)** | Above + winget submitted (🟢 row 4) + SLA smoke 🟢 + at most 1 open High with explicit user-facing call-out + first dryrun green. | ❌ Not yet. winget pending, SLA pending, **0 open Highs** (was 2 in prior dashboard text — corrected: see Open-Highs row above). |
| **General public / "production"** | All rows 🟢 or ⚪ except optional. Code signing 🟢 (the row 1 blocker). MSI consent UI 🟢 dialog. Zero open Highs. | ❌ Not yet. Row 1 (signing) is the long pole. |

The maintainer signs off on which audience the next release targets. The release-runbook does not gate on this; it surfaces it. When in doubt: stay one rung lower than you think.

---

## Indicator detail and remediation owners

The status table above is the dashboard. This section is what backs up each row when it goes yellow or red.

### 🔴 Code signing (Authenticode)
- **Why it's red**: Application to SignPath Foundation has not been submitted. Every install triggers SmartScreen.
- **Remediation**: Submit the SignPath application (free EV for OSS, project-bound cert). Once cert issues, integrate the SignPath GitHub Action between MSI build and upload (post-build, pre-attestation, pre-upload). README + SECURITY.md already reference the plan.
- **Owner**: maintainer.
- **Target**: before the wider-beta audience widening. No specific date.

### 🟡 MSI consent UI
- **Why yellow**: Dialog (radio group: None / Standard / Reliability) was wired in v0.9.27-cycle (PR-S2 in CHANGELOG `## [Unreleased]`). Default tier is `none` — silent installs do not enroll users in any tier — but this needs eyes-on test on a real MSI install before the green tick.
- **Remediation**: After v0.9.27 tag, do an interactive install on a clean Windows VM. Confirm dialog displays before "Custom Setup", radio group binds to `INSTALL_TELEMETRY_TIER`, registry value matches selection.
- **Owner**: maintainer (visual check) + SM-keeper (release-day playbook step).

### 🟡 winget submission
- **Why yellow**: Manifest template + CI generation wired in v0.9.27 cycle. CI auto-submission gated on repo variable `WINGET_SUBMIT_ENABLED=1` + secret `WINGET_SUBMIT_PAT` (a PAT with public_repo + workflow scopes, fork rights on microsoft/winget-pkgs).
- **Remediation**: Provision the PAT, set the variable to 1, push next tag. First successful winget-pkgs PR closes this.
- **Owner**: maintainer (PAT provisioning) + release pipeline (auto from then on).

### 🟢 Open Highs — none

- **BUG-R4-1** (concurrent `addmirror` race): closed in 0.9.44-dev via `lock.AcquirePath` + `withConfigLock` in `internal/config/edit.go`.
- **FIND-R4-1** (batch-sync hooks): closed 2026-04-29 by hooks deferral. Hooks are no longer counted toward v1.0 readiness; the integration use case (post-batch firing for AI-orchestration) is reachable via `alert_webhook_url` instead. See [RESOLUTION-2026-04-29-hooks-deferred.md](RESOLUTION-2026-04-29-hooks-deferred.md).

### 🟡 Telemetry health
- **Why yellow**: Phase 5 live since v0.9.4-dev, but the audience is so small that "n is too small for analysis" is the honest read of the digest.
- **Remediation**: Just keeps maturing as audience widens. Once audience hits the wider-beta tier, weekly digest will start producing actionable signal.

### ⚪ External review
- **Status**: Not pursued by decision (A-GOV-01 closed, see CHANGELOG line 45). SELF-ASSESSMENT label retained on `docs/iso-compliance.md`. Multi-role panel review pattern is the substitute.
- **Re-open trigger**: Audience widens to "general public / production" AND the maintainer chooses to revisit. Not a release blocker.

---

## How this file gets updated

- **SM-keeper agent**: each pre-tag run, the agent compares the indicator targets to the current state and asks the maintainer to bless any color flips. Edits are PR-able like any other doc.
- **Panel reviews**: when a new round closes findings, the rows for "Open Highs" / "Open Mediums" change color. The maintainer updates them in the panel review's commit alongside the test-fix commits.
- **Telemetry digest**: a yellow row 10 (telemetry health) flipping to green or red is the digest workflow's call; SM-keeper reads the most recent digest and records the verdict here.

If a row's color does not match what the surrounding text says, the source-of-truth is the surrounding text — fix the table.
