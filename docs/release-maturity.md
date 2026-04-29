# Release maturity dashboard

**Audience**: maintainer, release-day go/no-go, and the [SM-keeper agent](../.claude/agents/sm-keeper.md). PR-PM4 (panel review pre-release 2026-04-28).
**Cadence**: updated by SM-keeper on each pre-tag run, on each panel review, and on each weekly telemetry digest. Manually editable any time.

This file is a **live snapshot** of the indicators that decide whether SelectiveMirror is ready to widen its audience. Each row is a binary or trichotomous status; the bottom line tells you what the project's audience SHOULD be right now.

---

## Status board (refreshed: 2026-04-28)

| Indicator | Target for "general public" | Current state | Color |
|---|---|---|---|
| **Code signing (Authenticode)** | Signed MSI + EXE under SignPath EV cert | Plan in SECURITY.md; SignPath Foundation application not yet submitted | 🔴 |
| **GitHub build-provenance** | Both MSI + EXE attested per release | Wired in release.yml v0.9.27 cycle; first attested release will be v0.9.27 | 🟡 |
| **MSI consent UI** | Three-tier radio dialog visible during install | Property + registry wired since v0.9.4-dev; dialog landing in v0.9.27 cycle (PR-S2) | 🟡 |
| **winget submission** | Latest MSI submitted to microsoft/winget-pkgs | Manifest template up-to-date; CI auto-submission gated on `WINGET_SUBMIT_ENABLED=1`; first auto-submission with v0.9.27 | 🟡 |
| **Pre-release dryrun** | release-dryrun.yml green within 24h of tag | Workflow added in v0.9.27 cycle; one full successful run required before next tag | 🟡 |
| **system-validation gating** | All `panel_findings_*_test.go` green at release time, OR allowlisted with CHANGELOG known-issues entry | Wired in release.yml v0.9.27 cycle. Allowlist for `panel_findings_*` is empty after the Phase A-G + Round 14 batch (all five Tier-1 findings closed). **However, the broader `system-validation/` suite has separate failures outside the panel-test scope** — telemetry server-contract / Worker / RLS tests, TestCLI_Status SQLITE_BUSY parallel-load flake (SM-142), TestScenario_BurstFileCreation / High-depth queue under load. SM-197 (Codex audit 2026-04-29) flagged the dashboard as overstated. Honest read: 🟡. The release.yml gate passes because it scopes to panel-found tests; full-suite green is a v1.0.x roadmap item. | 🟡 |
| **SLA smoke** | Latest scheduled run within 48h is green | Workflow added in v0.9.27 cycle; first scheduled run pending | 🟡 |
| **Open Highs from latest panel review** | Zero open Highs against the about-to-tag commit | 0 open. BUG-R4-1 closed in 0.9.44-dev. FIND-R4-1 closed by hooks deferral 2026-04-29 (see docs/RESOLUTION-2026-04-29-hooks-deferred.md). | 🟢 |
| **Open Mediums** | ≤ 3 open, all with planned fix versions | 4 open (OBS-R4-1..5, R4-PF-10). Planned for v1.0.x patches. | 🟡 |
| **Telemetry health (last 7 days)** | Envelope rate steady, no recurring signature on the latest released version | First Phase-5-live release (v0.9.4-dev) data ingested; weekly digest sample-size still small | 🟡 |
| **Report-bug PII smoke** | Release-time smoke green every release | `scripts/check-pii-leak.ps1` wired into release.yml + dryrun in v0.9.27 cycle | 🟢 |
| **HMAC master key** | Stored, rotation procedure documented + tested, absent-key fail-loud at release | Stored ✓; rotation documented (telemetry-ops.md); CI fail-loud added in v0.9.27 cycle (PR-S3); rotation never actually executed | 🟡 |
| **Test-count delta** | No regression vs. previous release | 640+ unit + 70+ system-validation; growing per cycle | 🟢 |
| **Docs vs. code drift** | All `docs/*.md` reference real file paths and current behaviors | Latest sweep 2026-04-29 (CHANGELOG line 42-47); story.md properly framed as historical snapshot | 🟢 |
| **Backups / rollback** | Documented rollback path that respects GAP-7 forward-only state DB | README "Compatibility & rollback" section added in v0.9.27 cycle (PR-W2) | 🟢 |
| **CI runner age** | windows-latest still supported, Go 1.26 still supported | Both current 2026-04 | 🟢 |
| **External review** | One independent eye on a recent panel-review batch | A-GOV-01 closed by decision: external review NOT planned (per CHANGELOG line 45). SELF-ASSESSMENT label retained. | ⚪ N/A by decision |

**Color key**: 🟢 ready · 🟡 partial / on-track but not closed · 🔴 blocker for the listed audience · ⚪ deliberate non-goal

---

## Audience matrix

The set of indicators above maps to a recommended audience.

| Audience | Hard requirements | Current verdict |
|---|---|---|
| **Maintainer-only** | Code signing not required. Indicators all 🟡 or better. No active red-state findings. | ✅ Ready. v0.9.x shipped at this level; v1.0.0 retains it. |
| **Maintainer + small group of testers** | Same as above + audience signaling in README + known-issues inventory in CHANGELOG. | ✅ Ready. **Current audience for v1.0.0** (recommended for the first 30 days post-tag while telemetry signature data accumulates and the SignPath cert lands). |
| **Wider beta (forum / Hacker News announcement)** | Above + winget submitted (🟢 row 4) + SLA smoke 🟢 + at most 1 open High with explicit user-facing call-out + first dryrun green. | ❌ Not yet. winget pending, SLA pending, 2 open Highs. |
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
