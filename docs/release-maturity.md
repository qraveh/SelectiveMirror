# Release maturity dashboard

**Audience**: maintainer, release-day go/no-go, and the release-day operator workflow.
**Cadence**: updated on each pre-tag run, on each panel review, and on each weekly telemetry digest. Manually editable any time.

This file is a **live snapshot** of the indicators that decide whether SelectiveMirror is ready to widen its audience. Each row is a binary or trichotomous status; the bottom line tells you what the project's audience SHOULD be right now.

---

## Status board (refreshed 2026-05-25 for v1.0.1 prep — post SM-218/SM-219/SM-220 + Open Mediums close-out)

| Indicator | Target for "general public" | Current state | Color |
|---|---|---|---|
| **Code signing (Authenticode)** | Signed MSI + EXE under SignPath EV cert | Plan in SECURITY.md. SelectiveMirror is applying to SignPath Foundation concurrent with the v1.0.0 release (the foundation's gate is "released in the form to be signed"); first signed release follows once the cert is provisioned. Long pole for the wider-beta audience widening; not a blocker for the maintainer-only / small-tester audience that v1.0.0 ships to. | 🔴 |
| **GitHub build-provenance** | Both MSI + EXE attested per release | Wired in release.yml v0.9.27 cycle; first attested release lands with v1.0.0 tag (this tag). Flip to 🟢 after `gh attestation list` returns expected output post-tag. | 🟡 |
| **MSI consent UI** | Three-tier radio dialog visible during install | Property + registry wired since v0.9.4-dev. Dialog shipped during v0.9.x cycle. Pre-tag operator gate (R-5 MSI smoke) confirms eyes-on. | 🟡 |
| **winget submission** | Latest MSI submitted to microsoft/winget-pkgs | Manifest template up-to-date; CI auto-submission gated on `WINGET_SUBMIT_ENABLED=1` + `WINGET_SUBMIT_PAT`. First submission lands with v1.0.0 if gate flipped. | 🟡 |
| **Pre-release dryrun** | release-dryrun.yml green within 24h of tag | Operator-side gate per the pre-tag operator gate; required green within 24h of the v1.0.0 tag. | 🟡 |
| **system-validation gating** | All review-driven test files green at release time, OR allowlisted with CHANGELOG known-issues entry | Review-driven allowlist empty (all 5 Tier-1 findings closed). **Telemetry CLAIMS-MAP gate at 25/28 GREEN (89.3% total / 96.2% non-deferred — comfortably above the ≥ 90% gate)** — the strongest 29119-3 Test Completion Report evidence the project has produced. Two RED in active deferral: HMAC timing benchmark (v1.0.x), pg_stat_statements smoke (v1.0.x). Broader system-validation suite still has the historical separate failures outside review-driven scope (TestCLI_Status in-process schema-create race covered by retry loop; burst-file scenarios under load) — full-suite green remains a v1.0.x roadmap item. | 🟢 |
| **SLA smoke** | Latest scheduled run within 48h is green | Operator-side gate per the pre-tag operator gate; refresh required if > 48h stale at tag. | 🟡 |
| **Open Highs from latest panel review** | Zero open Highs against the about-to-tag commit | **0 open.** Concurrent-addmirror race closed in 0.9.44-dev. Batch-sync hooks gap closed by hooks deferral 2026-04-29 (see docs/RESOLUTION-2026-04-29-hooks-deferred.md). 22-commit telemetry-validation window added zero new Highs. | 🟢 |
| **Open Mediums** | ≤ 3 open, all with planned fix versions | **4 open** (2 closed in v1.0.1 prep): ~~addmirror fresh-config file mode~~ (closed; the SEC-H6 fix is in place + the regression test reasoning corrected to reflect Windows-NTFS ACL is the real protection — see CHANGELOG [Unreleased]); ~~foreground symlink-follow~~ (closed via `config.AllowSymlinks` default-reject; foreground now mirrors service-mode SEC-H5 default); remaining: CLAIMS-MAP HMAC timing benchmark (v1.0.x), CLAIMS-MAP pg_stat_statements smoke (v1.0.x), SM-082 items 3+4 (svc.Control inconsistency + Anomaly Detail stderr; v1.0.x), SM-057 (burst-delete reconcile sleep; v1.0.x). Approaching the ≤ 3 target; 🟡 until the remaining 4 close in v1.0.2 / v1.0.x. | 🟡 |
| **Telemetry health (continuous live measurement)** | Cloudflare Worker access-log probe daily-green; envelope rate steady; zero None-tier records over None-tier installs (NFR-PR-01 target = 0.000) | **Live + functional end-to-end as of 2026-05-21.** Major v1.0.1 discovery: SM-219 — HMAC master-key derivation mismatch between `verify_versioned_hmac` SQL and release.yml + binary — caused EVERY CI-built smirror's `first_seen` and `upgrade` event to be silently rejected by the Worker since the install-event pipeline shipped (0.9.102-dev). Rollup tables stayed at 0 rows through the entire v1.0.0 release cycle as a consequence. SM-219 SQL migration deployed to live Supabase 2026-05-21; a CI-style local-built v1.0.23 binary then landed the project's first real `first_seen` and upgrade events. Worker emits records, schema-validated daily by `.github/workflows/telemetry-emulation.yml`, CLAIMS-MAP gate at 25/28 GREEN, fingerprint probe (cf-ray + SM Worker custom header) verified per-tag. R-12 NFR-PR-01 first ratio for v1.0.1: `0/0` vacuous (the v1.0.0→v1.0.1 window had zero real-user contributions; the pre-SM-219 portion of the window had zero contributions period; maintainer-side validation rows from SM-219 / SM-220 verification have been removed). First quantified non-vacuous ratio will appear in v1.0.1→v1.0.2 release notes once real-user installs of v1.0.1 (the first telemetry-functional release) accumulate. | 🟢 |
| **Report-bug PII smoke** | Release-time smoke green every release | `scripts/check-pii-leak.ps1` wired into release.yml + dryrun. Plus `report-bug --submit` end-to-end (SM-158 ship 0.9.89-dev): bucketed payload only, no narrative columns ever stored server-side. | 🟢 |
| **HMAC master key** | Stored, rotation procedure documented + tested, absent-key fail-loud at release | Stored ✓; rotation documented (telemetry-ops.md); CI fail-loud landed v0.9.27 cycle. Build-key fingerprint visible in `smirror version`; CLAIMS-MAP HMAC build-key row GREEN. Rotation never actually executed — that drill is a v1.0.x item. | 🟡 |
| **Test-count delta** | No regression vs. previous release | **650+ unit + 80+ system-validation**; aggregate `internal/` coverage ~65.9%, telemetry 79.6% (+2.7pts). **State coverage regression**: 70.0% → 64.1% (5 metadata-write paths at 0% — VacuumIfStale, PruneOrphanedProjects, MarkRemoteVerificationStale, ClearStaleExitCodes, IncrementMetaCounter). Above the 50% per-package floor and 60% aggregate gate; tracked in CHANGELOG `[1.0.0]` "Bugs known at tag" for v1.0.x backlog. | 🟡 |
| **Docs vs. code drift** | All `docs/*.md` reference real file paths and current behaviors | Latest sweep 2026-05-25 (this v1.0.1-prep refresh). v1.0.1 R-16 audit-doc closure: `docs/iso-compliance.md` updated in 7 places to mark A-29119-01 closed (Test Strategy doc `docs/test-strategy.md` was authored 2026-05-03; the audit doc now consistently reflects that closure). Earlier 2026-05-14 path-move sweep (`C:\SelectiveMirror\` → `C:\mine\SelectiveMirror\`) landed via v1.0.1 housekeeping commit (12 files). | 🟢 |
| **Backups / rollback** | Documented rollback path that respects GAP-7 forward-only state DB | README "Compatibility & rollback" section in place. | 🟢 |
| **CI runner age** | windows-latest still supported, Go 1.26 still supported | Both current 2026-05. | 🟢 |
| **Branch CI (`ci.yml`) status** | Latest run on `master` is green | Lint debt cleanup landed: 13 findings closed (errcheck × 3, gosec G115 × 3, unconvert, unused field/funcs × 3, SA9003 × 2, SA5011 × 4 in tests, ineffassign × 2, gocyclo × 1). Local `golangci-lint run ./internal/... ./cmd/...` returns zero findings. **🟡 until the next master push triggers a green ci.yml run** (this row goes 🟢 then). | 🟡 |
| **External review** | One independent eye on a recent review batch | Closed by decision: external review NOT planned. Multi-role review pattern + telemetry CLAIMS-MAP gate are the substitutes. SELF-ASSESSMENT label retained on `docs/iso-compliance.md`. | ⚪ N/A by decision |

**Color key**: 🟢 ready · 🟡 partial / on-track but not closed · 🔴 blocker for the listed audience · ⚪ deliberate non-goal

---

## Audience matrix

The set of indicators above maps to a recommended audience.

| Audience | Hard requirements | Current verdict |
|---|---|---|
| **Maintainer-only** | Code signing not required. Indicators all 🟡 or better. No active red-state findings. | ✅ Ready. v0.9.x shipped at this level; v1.0.0 retains it. |
| **Maintainer + small group of testers** | Same as above + audience signaling in README + known-issues inventory in CHANGELOG. | ✅ Ready. **Current audience for v1.0.0** (recommended for the first 30 days post-tag while telemetry signature data accumulates and the SignPath cert lands). |
| **Wider beta (forum / Hacker News announcement)** | Above + winget submitted (🟢 row 4) + SLA smoke 🟢 + at most 1 open High with explicit user-facing call-out + first dryrun green. | ❌ Not yet. winget pending, SLA pending, **0 open Highs**. |
| **General public / "production"** | All rows 🟢 or ⚪ except optional. Code signing 🟢 (the row 1 blocker). MSI consent UI 🟢 dialog. Zero open Highs. | ❌ Not yet. Row 1 (signing) is the long pole. |

The maintainer signs off on which audience the next release targets. The release-runbook does not gate on this; it surfaces it. When in doubt: stay one rung lower than you think.

---

## Indicator detail and remediation owners

The status table above is the dashboard. This section is what backs up each row when it goes yellow or red.

### 🔴 Code signing (Authenticode)
- **Why it's red**: SignPath Foundation cert is not yet provisioned. SelectiveMirror is applying concurrent with the v1.0.0 release; foundation review wall-clock is open-ended (no published SLA). Every install triggers SmartScreen until the cert lands.
- **Remediation**: After foundation approval, integrate the SignPath GitHub Action between MSI build and upload (post-build, pre-attestation, pre-upload). README + SECURITY.md reference the plan; release.yml already has insertion-point comments.
- **Owner**: maintainer.
- **Target**: before the wider-beta audience widening. No specific date.

### 🟡 MSI consent UI
- **Why yellow**: Dialog (radio group: None / Standard / Reliability) was wired in v0.9.27-cycle. Default tier is `none` — silent installs do not enroll users in any tier — but this needs eyes-on test on a real MSI install before the green tick.
- **Remediation**: After v0.9.27 tag, do an interactive install on a clean Windows VM. Confirm dialog displays before "Custom Setup", radio group binds to `INSTALL_TELEMETRY_TIER`, registry value matches selection.
- **Owner**: maintainer (visual check) + release-day operator workflow (release-day playbook step).

### 🟡 winget submission
- **Why yellow**: Manifest template + CI generation wired in v0.9.27 cycle. CI auto-submission gated on repo variable `WINGET_SUBMIT_ENABLED=1` + secret `WINGET_SUBMIT_PAT` (a PAT with public_repo + workflow scopes, fork rights on microsoft/winget-pkgs).
- **Remediation**: Provision the PAT, set the variable to 1, push next tag. First successful winget-pkgs PR closes this.
- **Owner**: maintainer (PAT provisioning) + release pipeline (auto from then on).

### 🟢 Open Highs — none

- Concurrent `addmirror` race: closed in 0.9.44-dev via `lock.AcquirePath` + `withConfigLock` in `internal/config/edit.go`.
- Batch-sync hooks gap: closed 2026-04-29 by hooks deferral. Hooks are no longer counted toward v1.0 readiness; the integration use case (post-batch firing for AI-orchestration) is reachable via `alert_webhook_url` instead. See [RESOLUTION-2026-04-29-hooks-deferred.md](RESOLUTION-2026-04-29-hooks-deferred.md).

### 🟡 Telemetry health
- **Why yellow**: Telemetry is wired and deployed in v1.0.0 (consent registry has existed since v0.9.4-dev for development), but the audience is so small that "n is too small for analysis" is the read of the digest.
- **Remediation**: Just keeps maturing as audience widens. Once audience hits the wider-beta tier, weekly digest will start producing actionable signal.

### ⚪ External review
- **Status**: Not pursued by decision. SELF-ASSESSMENT label retained on `docs/iso-compliance.md`. Multi-role review pattern is the substitute.
- **Re-open trigger**: Audience widens to "general public / production" AND the maintainer chooses to revisit. Not a release blocker.

---

## How this file gets updated

- **Release-keeper subagent**: each pre-tag run, the agent compares the indicator targets to the current state and asks the maintainer to bless any color flips. Edits are PR-able like any other doc.
- **Reviews**: when a new review round closes findings, the rows for "Open Highs" / "Open Mediums" change color. The maintainer updates them in the review's commit alongside the test-fix commits.
- **Telemetry digest**: a yellow telemetry-health row flipping to green or red is the digest workflow's call; the maintainer reads the most recent digest and records the verdict here.

If a row's color does not match what the surrounding text says, the source-of-truth is the surrounding text — fix the table.
