# ISO Compliance Delta Report — v0.4 → current

## SelectiveMirror — Multi-role review of v0.9.26 / iso-compliance v0.5

**Document Version**: 1.0 (Initial)
**Date**: 2026-04-29
**Author**: Raveh / Claude (multi-role BMad analysis)
**Status**: REPORT ONLY — no edits / commits applied per request
**Scope**: Compliance delta between iso-compliance.md v0.4 (2026-04-27, commit `277e6df`) and current state (v0.9.26 published 2026-04-29; iso-compliance.md v0.5; project version 0.9.27-dev).
**Method**: Six-role analysis — John (PM, 29148) / Winston (Architect, 25010 + 25023) / Paige (Tech-writer, document attributes) / Amelia (Dev, 29119) + Edge-Case Hunter + Adversarial Reviewer.
**Source data**:
- 14 commits 0.9.13-dev..0.9.27-dev (`git log 277e6df..HEAD`)
- `CHANGELOG.md` [Unreleased] + [0.9.26]
- `docs/iso-compliance.md` v0.5
- `docs/SRS.md` v1.1
- `docs/VV-Plan.md` v0.3
- `system-validation/PANEL-REVIEW-{2026-04-28, ROUND2, ROUND3, ROUND4}.md`
- Live coverage measurement 2026-04-29 (`go test ./internal/... -cover`)

---

## Table of Contents

0. [Executive verdict](#0-executive-verdict)
1. [What shipped between v0.4 baseline and now](#1-what-shipped-between-v04-baseline-2026-04-27-and-now-2026-04-29)
2. [Per-role analysis](#2-per-role-analysis-multi-role-panel)
3. [Updated headline (advisory)](#3-updated-headline-advisory--no-edits-applied-per-user-request)
4. [New findings](#4-new-findings-not-filed-ranked-by-severity)
5. [Recommended decisions for next turn](#5-recommended-decisions-for-next-turn-if-user-approves)
6. [Bottom-line answer](#6-bottom-line-answer-to-more-or-less-compliant)

---

## 0. Executive verdict

**Net compliance: substantially MORE compliant on engineering substance; modestly LESS compliant on auditability posture; documentation lag has WIDENED.**

| Dimension | v0.4 baseline | Current | Delta |
|---|---|---|---|
| **Engineering substance** (does the product behave per the standards' quality model?) | Mostly compliant | Substantially compliant | **+** |
| **Documentation completeness** (do the docs say what the standards require them to say?) | Partial | Partial+ (CHANGELOG richer; SRS/VV-Plan stale) | ≈ |
| **Auditability posture** (could a third party verify the claim?) | Self-assessment, external review committed | **Self-assessment, external review explicitly declined** | **−** |
| **Test-process formalism** (29119-3 documents present per release?) | Partial | Partial (panel-review docs partially substitute Test Completion Reports) | + |
| **Measurement evidence** (25023 §5.2) | 6 / 24+ NFRs measured | 6 / 24+ NFRs measured | 0 |
| **Action register** | 63 actions, 17 P0 | 63 actions, A-GOV-01 closed by punt; A-25010-04 evidence shipped; new advisory P0s emerging | mixed |

**Headline reframe**: SelectiveMirror is now a **MORE** compliant *product* than it was at v0.4 (security-hardened, faultlessness-engineered, privacy-modeled), but a **LESS** compliant *audit subject* (no path to independent verification, several SRS/VV-Plan gaps growing rather than shrinking).

---

## 1. What shipped between v0.4 baseline (2026-04-27) and now (2026-04-29)

**14 commits, project version 0.9.13-dev → 0.9.27-dev (after v0.9.26 published 2026-04-29):**

| Commit batch | Theme | ISO relevance |
|---|---|---|
| 0.9.14-dev (`983fd4d`) | Cloudflare Worker subdomain | A-25010-10 Privacy infrastructure |
| 0.9.15-dev (`e37ce7b`) | Telemetry multi-role panel review — implementations | 29119 Test Process evidence (panel reviews) |
| 0.9.16-dev (`8f5c277`) | **Three-tier telemetry consent (None/Standard/Reliability)** | A-25010-10 Privacy NFR materially advanced |
| 0.9.17-dev (`8cb2605`) | Telemetry privacy bug batch (SM-159, 160, 164–167) | A-25010-10 + A-GOV-04 |
| 0.9.18-dev (`cf0660c`) | Telemetry privacy round-2 (SM-158, 165, 166, 171–178) | A-25010-10 |
| 0.9.19-dev (`0c5cff6`) | Panel-review quick wins (BUG-1 case-only mirror dedup, BUG-2 test-fix) | A-25010-03 Functional Appropriateness; 29119 evidence |
| 0.9.20-dev (`0cae424`) | **Panel-review config hardening (GAP-1..GAP-5)** | A-25010-06 Resistance — substantial NFR-class evidence |
| 0.9.21-dev (`3afaabe`) | Panel-review service-mode hardening (PF-A3 / SEC-H5, GAP-7) | A-25010-05 Authenticity, A-25010-06 Resistance |
| 0.9.22-dev (`460b03e`) | Panel-review polish (GAP-6, PF-A8 async OnRecord) | NFR-FT-01 Fault Tolerance, A-25010-08 Analysability |
| **v0.9.22 mid-cycle tag** | First successful 0.9.x tag attempt; pulled before publish (MSI build issue) | 29119-2 Test Monitoring & Control (release-pipeline regression) |
| `4ac9b5a` | docs + hygiene: SignPath plan, **ISO self-assessment final**, gitignore | **A-GOV-01 closed by decision** |
| 0.9.24-dev (`b7e7b6b`) | SEC-H2 + P2 batch | A-GOV-04 advance |
| 0.9.25-dev (`0c8144c`) | **SEC-H batch (H2/H3/H4/H7), SEC-M batch (M1/M8/M9), PF batch** | A-25010-05/06, A-GOV-04 |
| 0.9.26-dev (`bbb0d3c`) | MSI build fix (WiX 6 Fragment wrapper) | 29119-2 Test Monitoring & Control validated |
| **v0.9.26** (`530b5a2`) | Promoted [Unreleased] → [0.9.26], tagged | **First successful published 0.9.x release since v0.9.0** |

**Coverage delta** (re-measured 2026-04-29):

| Package | v0.4 (2026-04-27) | Current (2026-04-29) | Δ |
|---|---|---|---|
| anomaly | 73.1% | 75.8% | **+2.7** |
| config | 75.1% | 77.9% | **+2.8** |
| filter | 78.7% | 78.7% | 0 |
| hooks | 85.2% | 85.2% | 0 |
| state | 74.4% | 75.1% | +0.7 |
| sync | 71.5% | 71.0% | -0.5 |
| telemetry | 77.4% | 76.8% | -0.6 |
| watcher | 59.3% | 59.6% | +0.3 |
| **Total internal/** | **66.6%** | **67.3%** | **+0.7** |

Test count: 608 → 640+ (+32 tests in 2 days).

**4 panel-review docs** added (`system-validation/PANEL-REVIEW-2026-04-28.md` and Round2/Round3/Round4) — multi-role review pattern (Architect / Senior dev / Edge-case hunter / Adversarial) applied four times, with corresponding `panel_findings_round[234]_test.go` system-validation harnesses.

---

## 2. Per-role analysis (multi-role panel)

### 2.1 John (PM) — ISO/IEC/IEEE 29148:2018 Requirements Engineering

**Net delta: ≈ neutral (slight regression in posture; engineering didn't touch SRS attributes).**

| Finding | v0.4 baseline | Current | Δ |
|---|---|---|---|
| SRS §1.4 Applicable Standards | 4 standards listed | unchanged | 0 |
| SRS §4.0 Schema deviation note | added v0.4 | unchanged | 0 |
| FR verification methods (A-29148-05 P0) | open | open | 0 |
| Subjective FR conversion (A-29148-12) | open | open | 0 |
| User Documentation Requirements section (A-29148-17) | open | open | 0 |
| 29148 §6.5 *Validation of requirements with stakeholders* | "Self-assessment + external review committed" (v0.4) | "Self-assessment is final; no external validation planned" (v0.5) | **− (downgrade)** |
| Stakeholder identification (A-29148-15) | ❌ | ❌ | 0 |
| Approval / sign-off (A-29148-03) | ❌ | ❌ | 0 |
| Document change history (A-29148-02) | ❌ | ❌ | 0 |

**John's verdict**: 29148 row of headline (11/19 compliant) is **structurally unchanged** but **weakened in spirit**. The closure of A-GOV-01 means 29148 §5.2.4 (peer review of requirements) and §6.5 (stakeholder validation) are explicitly *unfulfillable* now — not deferred. This is an honest framing but a stricter compliance reading would say "29148:2018 §6.5 cannot be satisfied" rather than "self-assessment retained". Recommend re-classifying this as a deliberate **Non-Conformity by Choice** rather than a closed item.

**29148 fresh findings (advisory)**:
- *Round 4 panel-review found `BUG-R4-1`* (concurrent addmirror destroys seed mirror) — that's a missing-requirement gap on the `addmirror` command; the SRS doesn't require concurrency safety on the command surface (FR-CLI-* set is silent on concurrent CLI invocations). Add `FR-CLI-08: Concurrent addmirror invocations SHALL preserve all pre-existing mirror entries`.
- *Round 4* found `alert_min_severity: erro` typo passes `Validate()` — that's a missing-validation requirement; add `FR-CFG-NN: Enum-typed config fields SHALL reject typos with the list of accepted values`.

### 2.2 Winston (Architect) — ISO/IEC 25010:2023 + ISO/IEC 25023:2016

**Net delta: SUBSTANTIALLY MORE compliant on 25010; UNCHANGED on 25023 measurement coverage.**

#### 25010:2023 quality-model compliance

| Quality area | v0.4 status | Current evidence | Δ |
|---|---|---|---|
| Functional Suitability:Functional Appropriateness | ⚠️ implicit only | **BUG-1 closes case-only-duplicate dedup; GAP-3 rejects overlapping `local_paths`; GAP-4 rejects drive-root** — the product now refuses configurations that don't fit the user task. Strong Appropriateness evidence. | **+** |
| Reliability:Faultlessness | "Evidence shipped, NFR pending" | unchanged | 0 |
| Reliability:Fault Tolerance (NFR-FT) | rclone-stall detection in v0.9.12 | extended: PF-A8 async OnRecord prevents anomaly-callback blocking sync engine; PF-D2 enqueues full-project sync after failed `.syncignore` reload | **+** |
| Security:Authenticity (A-25010-05 P1) | not declared | **SEC-H3 copyto TOCTOU defense; SEC-H4 NTFS reparse-point detection; SEC-H7 state DB symlink rejection; PF-A3/SEC-H5 service-mode default-rejects symlinks-to-files** — multiple authenticity-class fixes shipped without a corresponding NFR | **+ engineering / 0 doc** |
| Security:Resistance (A-25010-06 P1) | not declared | **GAP-1 `--rc*` denylist; GAP-2 rclone_config validation; GAP-5 traversal-shaped remote rejection; SEC-M1 openBrowserURL hardening; SEC-M9 selfupdate rate-limit visibility** — substantial Resistance-class engineering | **+ engineering / 0 doc** |
| Security:Privacy / Data Protection (A-25010-10 P1) | not declared | **Three-tier telemetry consent model (None / Standard / Reliability); SM-159..178 privacy-honest output; report-bug placeholder labels** — Privacy NFR has now ~10 commits of implementation evidence | **++ engineering / 0 doc** |
| Maintainability:Analysability (A-25010-08 P2) | "evidence accumulating" | **PF-A8 async OnRecord makes anomaly stream non-blocking, increasing operational visibility; per-anomaly DroppedCallbacks counter; SEC-M9 selfupdate errors warn-logged** — Analysability further strengthened | **+** |
| Flexibility:Replaceability (A-25010-12 P1) | not declared | unchanged | 0 |

**25010 fresh findings**:
- The shipped engineering for **Authenticity / Resistance / Privacy** dwarfs the v0.4 audit description. The audit register has 5 separate "Open" actions (A-25010-05, -06, -10) for content that's now been engineered into 30+ commits. **The audit doc is materially understating what the product actually does for Security:Authenticity, Security:Resistance, and Security:Privacy.**
- **Decision required**: do we *retroactively author* NFR-AU-* / NFR-RS-* / NFR-PR-* covering the shipped evidence, or do we accept the v0.4 framing that these stay "evidence shipped, NFR pending" until v1.1?

#### 25023:2016 measurement compliance

| Item | v0.4 | Current |
|---|---|---|
| Quantitative NFRs with measurement function defined | 6 / 24+ | 6 / 24+ |
| `A-25023-02a..k` (11 measurement campaigns) | all open P0 | all open P0 |
| `A-25023-04` (extend §2.3 attributes) | open P1 | open P1 |
| `A-25023-05` (Base/Derived/Indicator typing) | open P2 | open P2 |
| `A-25023-06` (eliminate "Met at looser") | applied via SM-153 in v0.4 | ✅ retained |
| **New measurement targets** introduced | rclone stall thresholds (60 s, 240 s flat-grace) | unchanged — still not formalized as NFR-FL-01 |
| **New measurement targets in 0.9.x batch** | — | telemetry consent tier changes, anomaly callback drop-rate, alert_min_severity validation — all currently *implicit* metrics |

**Winston's verdict on 25010**: net **+1 step** — engineering pulled the product two steps closer to 25010:2023 compliance on Security; documentation didn't follow. **Cannot honestly claim "Authenticity addressed" until NFR-AU-* exists in SRS §4.6.**

**Winston's verdict on 25023**: **net 0** — the engineering shipped *more measurable behavior* (anomaly drop rate, consent tier, stall thresholds) but the measurement-function table didn't follow. This is the most dangerous gap to leave open: 25023 §6 demands recorded measurement *results*, and every release that doesn't measure-and-record drifts further from the standard.

### 2.3 Paige (Tech-writer) — Document attributes

**Net delta: REGRESSION in documentation honesty / consistency.**

| Document | v0.4 status | Current | Δ |
|---|---|---|---|
| `CHANGELOG.md` | rich, well-structured | even richer (P0..P6 priority categorization; per-finding rationale; multi-role review attribution) | **+** |
| `docs/iso-compliance.md` | v0.4 (63 actions) | v0.5 (A-GOV-01 closed by decision; §10.5 reading list **removed**) | **mixed** |
| `docs/SRS.md` | v1.1 — NFR-TE-01 corrected, NFR-FT-01 annotated with rclone-stall | unchanged content (Project Version 0.9.7-dev still listed despite project at 0.9.27-dev) | **− stale version pointer** |
| `docs/VV-Plan.md` | §5.2 stale (16.6% watcher; 35.8%→~65% claim) | **STILL stale** (config 86.9% / filter 89.4% / watcher 16.6% — current measurements are 77.9 / 78.7 / 59.6); test count 530+→600+ stated, actual 640+ | **− widening drift** |
| Panel review docs (`system-validation/PANEL-REVIEW-*.md`) | none | 4 docs (Round 1–4); structured, dated, multi-role attributed | **+** new artifact class |
| `SECURITY.md` | minimal | Code Signing section added (SignPath plan) | **+** |
| `CLAUDE.md` | "608 tests" | "640+ tests" (CHANGELOG entry); but check actual file | + |
| `README.md` | ISO partial-compliance disclosure | unchanged | 0 |
| In-doc Change History (A-29148-02) | absent | absent | 0 |
| Approval / sign-off block (A-29148-03) | absent | absent | 0 |
| Distribution List / Stakeholder list / Glossary / Doc Conventions (A-29148-15) | absent | absent | 0 |
| ConOps (A-29148-07) | absent | absent | 0 |

**Paige's verdict**:

1. **CHANGELOG quality is exemplary**. Best-in-class for a solo project. Could cite this as 29119-3 Test Completion Report partial substitute.
2. **SRS Project Version line is stale** (says "0.9.7-dev"; project is "0.9.27-dev"). Document Version 1.1 not bumped despite content being modified.
3. **VV-Plan §5.2 is GROWING stale, not shrinking** — config drift +9.0pts, filter drift +10.7pts, watcher drift -43.0pts. **A-29119-12 was not executed** despite the v0.9.26 release happening on its target date.
4. **`docs/iso-compliance.md` v0.5 removed §10.5 (external-reviewer reading list)**. This was useful audit-evidence content — even if external review isn't planned, the reading list documents *what compliance evidence looks like*. Removal is a regression in audit-completeness.
5. **No Document-attribute action (A-29148-02/-03/-07/-15) advanced.** All seven ❌ items in §4.1 of the audit remain ❌.

### 2.4 Amelia (Dev) — ISO/IEC/IEEE 29119 Software Testing

**Net delta: MORE compliant on test execution & monitoring; UNCHANGED on test documentation formalism.**

| Item | v0.4 status | Current | Δ |
|---|---|---|---|
| Coverage (internal/) | 66.6% | 67.3% | **+0.7** |
| Test count | 608 | 640+ | **+32** |
| Race detector coverage | extended to internal/sync, internal/state | unchanged | 0 |
| Release.yml hardening (29119-2 Test Monitoring & Control) | go vet + go test before GoReleaser | extended: `installer/smoke-test.ps1` integrated | **+** |
| MSI smoke test (16 invariants) | existed | unchanged | 0 |
| Panel-review test artifacts | none formal | **3 new: panel_findings_round[234]_test.go** (system-validation) | **+ new artifact class** |
| Org Test Strategy doc (A-29119-01 P0) | ❌ | ❌ | 0 |
| Per-release Test Status / Item Transmittal / Completion Report (A-29119-03 P1) | ❌ | ❌ — but Round-N panel-review docs partially substitute the Completion Report | partial + |
| Test naming convention (A-29119-04 P1) | not adopted | not adopted | 0 |
| Coverage matrix (A-29119-05 P1) | not automated | not automated | 0 |
| `A-29119-12` per-release VV-Plan §5.2 re-measurement | new action P1 / 2026-04-30 | **MISSED — v0.9.26 released without re-measurement** | **− P1 missed** |
| 29119-4 techniques used | EP, BVA, DT, ST | + Cause-Effect Graphing implicit in panel-review hypothesis chains | + |

**Amelia's verdict**:
1. The four panel-review documents are **the strongest 29119 evidence the project has ever produced**. They are dated, multi-role, attributed, with conversion of qualitative findings into automated tests (`panel_findings_round[234]_test.go`). This is closer to a Test Completion Report than `docs/validation-report-2026-04-16.md` was.
2. **A-29119-12 was missed** — this is a *new P1 missed at deadline*. The release ritual didn't include re-measurement; VV-Plan §5.2 is growing stale.
3. **A-29119-01 (Test Strategy) remains the largest 29119 gap**. The panel-review process *is* the de-facto Test Strategy; making it explicit (`docs/test-strategy.md` documenting "we run multi-role panel reviews per release with Round-N notation") would close it.
4. **Round-4 BUG-R4-1 (concurrent addmirror destroys seed mirror) is a P0-class new bug found via Test Process**. 29119-2 §8 (Test Incident Reporting) is functioning correctly.

### 2.5 Edge-Case Hunter — verification pass on the v0.4 → v0.5 delta

| # | Edge case | Severity |
|---|---|---|
| 1 | `iso-compliance.md` §10.5 was removed without filing a doc bug for the removal. The reading list was useful even sans external review. | minor |
| 2 | A-GOV-01 closed by decision but the disclosure still says "External independent review committed for v1.0.1" in the same document's §1 header (see line 7) — *internal contradiction with the v0.5 §1 wording on line 43 and §9.5 line 537.* | minor |
| 3 | A-GOV-04 status says "Partially closed; enumeration pending" — but enumeration *should be possible now*: SEC-C1..C5 closed in 0.9.0; SEC-H2/H3/H4/H6/H7 + SEC-M1/M8/M9 + PF-A3/SEC-H5 closed in 0.9.x. That's 13 audit findings closed without explicit cross-reference to `docs/security-audit-2026-04-18.md` line items. | important |
| 4 | The CHANGELOG says "16 MEDIUM / 5 LOW deferred to post-0.9.0" (line 202) but only 3 SEC-M (M1/M8/M9) closed visibly — gap of 13 MEDIUM + 5 LOW = 18 items still in deferral with no enumeration. | important |
| 5 | Round-4 panel review found `BUG-R4-1` (concurrent addmirror destroys seed mirror) — was filed in panel review doc but **does it have a SM-NNN BugTracker entry?** Need to check. | important |
| 6 | Round-4 found `alert_min_severity: erro` typo passes `Validate()` — same question: filed as SM-NNN? | minor |
| 7 | Panel-review docs use the "Round 1 / Round 2 / Round 3 / Round 4" notation but SRS §11.7 mentions "self-audit + adversarial multi-role panel reviews" — the rounds aren't anchored to release versions in either the SRS or VV-Plan. **A 29119-3 Test Completion Report convention would tie Round-N to a specific release tag.** | minor |
| 8 | Coverage results are not in the repo as a per-release artifact. (Same gap as v0.4 — but now exacerbated by stale VV-Plan §5.2.) | minor |
| 9 | The new `system-validation/panel_findings_round*.go` files are **untracked in git** as of this session — they exist on disk but aren't committed. (Confirmed via `git status --short` — they're under "??".) | important — see below |
| 10 | Coverage of `internal/state` dropped 85.2% → 75.1% over the period; `internal/rclone` is 34.1% (was 52.3% per VV-Plan §5.2). These regressions are *real*, not stale-baseline artifacts. The CHANGELOG never acknowledged them. | minor |
| 11 | iso-compliance v0.5 change-log entry says "panel-review work (commits 0.9.19..0.9.22-dev) closed BUG-1, GAP-1..5, etc." — but doesn't enumerate by SM-NNN ID. If those panel findings became SM bugs, the audit doc loses traceability. | important |
| 12 | iso-compliance v0.5 references `docs/security-audit-2026-04-18.md` without confirming whether the audit doc itself was updated with closure markers. | minor |
| 13 | Three-tier telemetry consent (None/Standard/Reliability) is shipped as engineering but no NFR-PR-* (Privacy) entry added to SRS §4.6. **A-25010-10 status should be at minimum "Evidence shipped; NFR pending" — currently still "Open"**. | important |
| 14 | A-29119-12 deadline 2026-04-30 was set by a previous me; v0.9.26 released 2026-04-29 (one day before deadline) without satisfying the action. Need to decide: extend deadline, fail action, or accept the bake-in lag. | important |
| 15 | Round-4 found "Fresh `config.yaml` created with mode 0666 (Go reports 0666; expected 0600 per SEC-C5)". This is a **regression of the SEC-H6 fix** — file mode 0600 was supposed to be enforced in 0.9.9-dev, but the *creation* path is still defaulting to 0666. SEC-H6 closure may be premature. | critical |

### 2.6 Adversarial Reviewer — challenge of compliance claims

#### Claim 1: "More compliant after parallel-session work"

**Counter**: True for engineering substance, but the auditability posture *contracts* in v0.5. A-GOV-01 was closed not by fulfillment but by abandonment. This is the same standards-gaming pattern (renaming failure as compliance) that the v0.2 review forbade. The honest classification is **A-GOV-01 = Permanently Open / Not Pursued**, not **Closed**.

#### Claim 2: "Self-assessment is final — third-party compliance is not claimed and not planned"

**Counter on auditability**: This wording (line 43) is fine internally — but if anyone external ever reads `docs/iso-compliance.md` (e.g., a customer doing vendor due-diligence on rclone-based file mirroring), they will read the §3.1 headline saying "ISO/IEC 25010:2023 Partial" and the §1 wording saying "self-assessment is final". The product is then either **claiming ISO compliance based on self-assessment** (weak claim — but a claim) or **not claiming compliance** (in which case why have the audit at all?). Recommend re-stating §1 to: *"This document records how the project chooses to apply four ISO standards as engineering scaffolding. It is not a compliance claim."*

#### Claim 3: "29119 Test Monitoring & Control compliance improved"

**Counter**: Yes — release.yml + smoke-test.ps1 are real defense-in-depth. **But A-29119-12 (per-release re-measurement of VV-Plan §5.2) was missed at deadline 2026-04-30 by the same release cycle that demonstrates the rest of Test M&C improvements**. The release ritual doesn't include the action it was supposed to enforce. Demand a process fix: A-29119-12 should be added to `release.yml` *as a CI step* — not just as a written ritual.

#### Claim 4: "Watcher coverage refactor X-04 mostly closed"

**Counter**: Watcher is at 59.6% (was 59.3% in v0.4). **A 0.3 percentage-point change in 14 commits is not "closure" — it's stagnation.** Eight of 27 functions still at 0% includes `NewManager`, `Start`, `Stop`, `eventLoop`, `healthMonitor`, `WatchCount` — the entire watcher lifecycle is structurally untested. The X-04 reduction P0 → P2 was justified at v0.4 by the stale-baseline finding (16.6 → 59.3); but **the actual refactor (interface extraction, fakeFsnotifier) has not been done**. Reclassify as P2 *but* note that the reduction is conditional on accepting "60% percent target met" rather than "structural testability achieved."

#### Claim 5: "A-25010 Privacy / Authenticity / Resistance now have shipping evidence"

**Counter**: True, but **shipping evidence without an NFR is not 25010 compliance** — it's behavior. The 25010 model demands that the quality characteristic be *declared as a requirement* with a target, not just exhibited as runtime behavior. Until NFR-AU-* / NFR-RS-* / NFR-PR-* exist in SRS §4.6, the audit cannot upgrade those rows from ⚠️ to ✅. **The engineering-doc lag is now 30+ commits behind**.

#### Claim 6: "Panel-review docs are 29119 evidence"

**Counter**: They are good evidence — but they're not committed to the repository (per git status). 4 review docs + 3 test files are untracked: `?? system-validation/PANEL-REVIEW-*.md`, `?? system-validation/panel_findings_round[234]_test.go`. **Evidence not under version control is not evidence**. This is a 29119-3 §6.3 Test Documentation control failure.

#### Claim 7: "Coverage is 67.3% (above 60% v1.0 target)"

**Counter**: Aggregate coverage masks regressions. `internal/state` dropped to 75.1% (-10pts vs VV-Plan §5.2 baseline); `internal/rclone` dropped to 34.1% (-18pts). Aggregate 67.3% is above target but the trajectory of two critical packages is downward. **The standards-gaming pattern in v0.5 is "headline coverage met"** — same family of issue as "Met at looser value" the v0.2 review excised from SRS §4.

#### Adversarial demand list (6 items)

1. **Re-classify A-GOV-01** from "Closed (decision)" to **"Non-Conformity by Choice"** with clear acknowledgement that 29148 §6.5 is structurally unfulfillable for this project.
2. **Restore §10.5** external-reviewer reading list — even with A-GOV-01 closed, the list is useful as a "what compliance evidence looks like" reference.
3. **File SM-NNN bugs** for: (a) `BUG-R4-1` concurrent addmirror, (b) `FIND-R4-1` per-file hooks don't fire on batch sync, (c) `alert_min_severity: erro` typo, (d) Fresh config.yaml mode 0666 (= **SEC-H6 regression**).
4. **Commit the panel-review artifacts** (`system-validation/PANEL-REVIEW-*.md`, `panel_findings_round[234]_test.go`) as a single commit — they're compliance evidence, not local scratch.
5. **Either author NFR-AU-* / NFR-RS-* / NFR-PR-*** in SRS §4.6 (to make 25010 Authenticity / Resistance / Privacy genuinely *requirements*) **OR keep the audit row at ⚠️**. Don't claim improvement on engineering alone.
6. **Add A-29119-12 to release.yml as a CI step** — promote the per-release VV-Plan §5.2 re-measurement from ritual to enforcement.

---

## 3. Updated headline (advisory — no edits applied per user request)

| Standard | v0.4 headline | Proposed v0.6 headline | Rationale |
|---|---|---|---|
| **29148:2018** | ⚠️ Partial — 11/19 compliant, 5 gaps | ⚠️ Partial — 11/19 compliant, **5 gaps + 1 deliberate non-conformity (A-GOV-01)** | A-GOV-01 closure is structural, not actional |
| **25010:2023** | ⚠️ Partial — 7/9 top-level; Authenticity/Resistance/Privacy under-addressed | ⚠️ Partial — 7/9 top-level; **Authenticity/Resistance/Privacy have engineering evidence, NFR formalization pending** | 30+ commits closed engineering gap; doc lag |
| **25023:2016** | ⚠️ Partial — 6/24+ measurement functions | ⚠️ Partial — 6/24+ (unchanged); **A-29119-12 missed at v0.9.26** | Per-release ritual didn't fire |
| **29119 family** | ⚠️ Partial — Test M&C ✅, Strategy ❌, Reports ❌ | ⚠️ Partial — Test M&C ✅, **Strategy still ❌, but Round-N panel-review docs partially substitute Completion Report** | Panel-review docs are a real new artifact class |
| **Auditability posture** | Self-assessment, external review committed for v1.0.1 | **Self-assessment, external review explicitly declined** | A-GOV-01 punted — recommend re-stating as Non-Conformity by Choice |

---

## 4. New findings (not filed; ranked by severity)

| # | Finding | Severity | Recommended bug ID |
|---|---|---|---|
| 1 | **Fresh `config.yaml` created with mode 0666 instead of 0600** — regression of SEC-H6 fix on the *creation* path | **critical** (security regression) | SM-NEXT |
| 2 | `A-29119-12` deadline 2026-04-30 missed; v0.9.26 released without VV-Plan §5.2 re-measurement | high (P1 missed) | A-29119-12 (existing action) — extend deadline + add CI enforcement |
| 3 | Untracked panel-review evidence (`system-validation/PANEL-REVIEW-*.md`, `panel_findings_round[234]_test.go`) | high (29119-3 doc-control failure) | SM-NEXT |
| 4 | iso-compliance.md §1 contradicts itself (line 7 says "external review committed", line 43 says "not planned"); §10.5 was removed without an audit-doc bug | high (internal inconsistency) | SM-NEXT |
| 5 | SRS Project Version line is stale (0.9.7-dev) despite project at 0.9.27-dev | medium (doc drift) | SM-NEXT |
| 6 | Round-4 `BUG-R4-1` concurrent-addmirror destroys seed mirror | high (data loss) — likely already filed | verify SM-NNN |
| 7 | Round-4 `FIND-R4-1` per-file hooks don't fire on batch sync | medium (behavioral gap, AI-orchestration impact) | verify SM-NNN |
| 8 | `alert_min_severity: erro` typo passes `Validate()` | medium | verify SM-NNN |
| 9 | A-25010-05 / -06 / -10 still "Open" despite shipping evidence | medium (audit doc lag) | update A-25010-* status |
| 10 | A-GOV-04 enumeration pending — 13+ SEC-* findings closed without `docs/security-audit-2026-04-18.md` cross-reference | medium | enumerate, mark closed/open |
| 11 | `internal/state` coverage regression -10pts; `internal/rclone` -18pts | medium | per-package investigation |
| 12 | VV-Plan §5.2 still stale (config 86.9 → 77.9; filter 89.4 → 78.7; watcher 16.6 → 59.6); test count claim 600+ vs actual 640+ | medium | execute A-29119-12 |
| 13 | A-GOV-01 closed by punt rather than fulfillment | minor (semantic) | re-classify to Non-Conformity by Choice |
| 14 | Panel-review docs not anchored to release tags | minor | add convention "Round-N → vX.Y.Z" |

---

## 5. Recommended decisions for next turn (if user approves)

| # | Decision | My recommendation |
|---|---|---|
| α | Re-classify A-GOV-01 from "Closed by decision" to **"Non-Conformity by Choice"** in iso-compliance.md | yes |
| β | Restore §10.5 external-reviewer reading list as a *reference* (not a commitment) | yes |
| γ | File 4 new bugs (SEC-H6 regression / panel-evidence not committed / iso-compliance internal contradiction / SRS stale Project Version) | yes |
| δ | Author NFR-AU-* / NFR-RS-* / NFR-PR-* in SRS §4.6 to formalize Privacy / Authenticity / Resistance — close the doc lag for 30+ commits of engineering | yes (P1 — deferable to v1.1 if scope-limited for v1.0) |
| ε | Add A-29119-12 to release.yml as a CI step | yes |
| ζ | Execute A-29119-12 NOW (re-measure VV-Plan §5.2 with current data, atomic commit) | yes |
| η | Commit the four panel-review docs + 3 test files as compliance evidence | **yes — most urgent** |
| θ | Bump iso-compliance.md to v0.6 with all of the above | yes |

---

## 6. Bottom-line answer to "more or less compliant?"

**More compliant on engineering substance** — the product genuinely behaves more like an ISO 25010:2023 model implementation today than 48 hours ago. Authenticity, Resistance, Privacy, Faultlessness, Analysability, Functional Appropriateness all received material code/test commits.

**Less compliant on auditability posture** — the path to claiming compliance externally is now closed by decision, and several documentation artifacts that would support such a claim (Round-N evidence, NFR formalization, VV-Plan §5.2 freshness) lag behind the engineering by 30+ commits and 2+ days.

**The risk going into v1.0**: shipping with this gap means the README.md disclosure ("Partial ISO compliance — see iso-compliance.md") points readers to a document that *understates* the engineering substance and *overstates* the documentation completeness. That is a fixable inconsistency, not a structural problem.

---

## Document control

| Field | Value |
|---|---|
| Document path | `C:\SelectiveMirror\docs\iso-compliance-review-2026-04-29.md` |
| Status | REPORT ONLY — not an audit-baseline document; advisory only |
| Author role | Multi-role BMad analysis (John / Winston / Paige / Amelia + Edge-Case Hunter + Adversarial Reviewer) |
| Source documents at time of review | `iso-compliance.md` v0.5 / `SRS.md` v1.1 / `VV-Plan.md` v0.3 / `CHANGELOG.md` [Unreleased] + [0.9.26] / `system-validation/PANEL-REVIEW-{2026-04-28, ROUND2, ROUND3, ROUND4}.md` |
| Repository state at time of review | SelectiveMirror master @ `530b5a2` (CHANGELOG promoted [Unreleased] to [0.9.26]) — 14 commits past the v0.4 audit baseline `277e6df` |
| Next action | Pick from §5 to direct next turn, or interrupt for redirect |
