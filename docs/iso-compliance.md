# ISO Standards Compliance Audit

## SelectiveMirror — Compliance with 29148:2018, 25010:2023, 25023:2016, 29119:2023

**Document Version**: 0.4 (Parallel-session integration)
**Date**: 2026-04-27
**Status**: **SELF-ASSESSMENT — retained for v1.0**. External independent review committed for v1.0.1 / v1.1 (action `A-GOV-01`). v1.0 ships with explicit "Partial ISO Compliance" disclosure in `README.md` and `CHANGELOG.md`.
**Release strategy**: Option β (ship 2026-05-01 with disclosure; close compliance gaps in v1.0.1 / v1.1).
**v0.4 integration**: incorporates 5 commits (0.9.8..0.9.12-dev) that landed in parallel after v0.3 was baselined. Several actions advanced; new bugs SM-155 and SM-156 filed; new action `A-29119-12` added (per-release VV-Plan §5.2 re-measurement ritual).
**Accountable owner**: Raveh (project lead). All actions in §9 are owned by Raveh; the *role-context* column names the BMad persona (John/Winston/Paige/Amelia) Raveh adopts when executing the action.
**Audit method**: BMad multi-role review — PM (John) for 29148, Architect (Winston) for 25010 / 25023, Tech-writer (Paige) for document attributes, Dev (Amelia) for 29119 process implementation. Adversarial Reviewer + Edge-Case Hunter ran independent passes on draft v0.1 (2026-04-27); this v0.2 incorporates their findings.
**Source documents audited**:
- `C:\SelectiveMirror\docs\SRS.md` (v1.0 baseline, refreshed 2026-04-18)
- `C:\SelectiveMirror\docs\VV-Plan.md` (v0.3, refreshed 2026-04-18)
- Supporting: `CLAUDE.md`, `CHANGELOG.md`, `.github/workflows/ci.yml`, `~/.claude/skills/qh-sw-developer/SKILL.md`, `docs/security-audit-2026-04-18.md`

---

## Table of Contents

1. [Purpose & Scope](#1-purpose--scope)
2. [Standards In Scope](#2-standards-in-scope)
3. [Compliance Summary](#3-compliance-summary)
4. [ISO/IEC/IEEE 29148:2018 — Requirements Engineering](#4-isoieceee-291482018--requirements-engineering)
5. [ISO/IEC 25010:2023 — Product Quality Model](#5-isoiec-250102023--product-quality-model)
6. [ISO/IEC 25023:2016 — Quality Measurement](#6-isoiec-250232016--quality-measurement)
7. [ISO/IEC/IEEE 29119:2023 — Software Testing](#7-isoieceee-291192023--software-testing)
8. [Cross-cutting Gaps](#8-cross-cutting-gaps)
9. [Action Register](#9-action-register)
10. [Maintenance & Re-audit Schedule](#10-maintenance--re-audit-schedule)

---

## 1. Purpose & Scope

This document is the **single source of truth** for SelectiveMirror's compliance with the four ISO standards adopted by the project. It exists to:

1. Map each standard's required clauses/processes to existing project artifacts (where evidence lives).
2. Identify gaps — missing clauses, partial implementations, unmeasured items.
3. Track remediation actions with accountable owner, role-context, priority, calendar date.
4. Survive re-audit cycles (quarterly or pre-release).

This is a **self-assessment** — authored by the same agent that authored the SRS and V&V Plan. The self-assessment label is retained permanently; third-party compliance is not claimed and is not planned.

### 1.1 Compliance levels used in this document

| Symbol | Meaning |
|---|---|
| ✅ | **Compliant** — clause is satisfied, evidence on file, current |
| ⚠️ | **Partial** — clause is partially satisfied; gap is identified and tracked |
| ❌ | **Non-compliant** — clause is required but not addressed (or addressed only by a hand-wave) |
| ➖ | **Not applicable** — clause exists in standard but not relevant to this product (justification given) |

### 1.2 Calibration policy (added in v0.2)

Per Adversarial Reviewer feedback: a passing reference, a parenthetical, or a single-line "approved" marker is **not** partial fulfillment of a 29148 clause. The bar for ⚠️ Partial is *substantive but incomplete coverage*. Items that are absent-with-a-handwave are ❌.

---

## 2. Standards In Scope

| Standard | Title | Year | Role in project |
|---|---|---|---|
| **ISO/IEC/IEEE 29148** | Systems and software engineering — Life cycle processes — Requirements engineering | 2018 | Structures `SRS.md`; defines requirement attributes & traceability |
| **ISO/IEC 25010** | SQuaRE — System and software quality models | 2023 | Organizes Non-Functional Requirements (`SRS.md` §4) |
| **ISO/IEC 25023** | SQuaRE — Measurement of system and software product quality | 2016 | Defines measurement functions for NFR targets (`VV-Plan.md` §2.3) |
| **ISO/IEC/IEEE 29119** | Software and systems engineering — Software testing | Parts 1, 2, 4 (2022); Part 3 (2021) — collectively cited here as "29119:2023 family" | Structures the V&V Plan, test processes, techniques |

---

## 3. Compliance Summary

### 3.1 Headline (revised v0.4)

| Standard | Overall | Compliant | Partial | Non-compliant | v0.4 delta |
|---|---|---|---|---|---|
| 29148:2018 | ⚠️ **Partial** | 11 / 19 | 3 | 5 | unchanged |
| 25010:2023 | ⚠️ **Partial** | 7 of 9 top-level characteristics | 0 | 1 (Usability schema mismatch) | Faultlessness now has substantive evidence (rclone stall detection); Analysability strengthened (new anomaly kinds Sync:Stalled / Sync:LsJsonSlow) |
| 25023:2016 | ⚠️ **Partial** | 6 functions defined / ~24 quantitative NFRs | many | most "Not Measured" | unchanged — but liveness.go thresholds (60s transfer flat-grace, 240s metadata) are *new* measurement targets that should be captured |
| 29119 family | ⚠️ **Partial** | Test Plan, test design, techniques | Reports, naming convention | Org Test Strategy ❌; **Test Monitoring & Control improved ⚠️→✅ via release.yml hardening** | release.yml runs `go vet` + `go test ./internal/... ./cmd/...` before GoReleaser; race detector now covers internal/sync + internal/state |

### 3.2 Why "Partial" not "Mostly compliant" (revised v0.2)

The v0.1 draft used "Mostly compliant" — the Adversarial Reviewer correctly flagged this as standards-gaming. Once three items in §4.1 are reclassified ❌, the 29148 row is 11/19 not 14/19. Document-attribute compliance is only halfway; **measurement evidence** and **test-process completeness** are the dominant gap classes — and several "Met at [looser target]" status entries in the SRS are **failure renamed**, not compliance.

### 3.3 Standards-gaming items to remediate

Three patterns identified that must be removed before the project claims ISO compliance externally:

| # | Pattern | Where | Remediation |
|---|---|---|---|
| 1 | "Met at looser target than spec" framing | NFR-TB-01, NFR-TB-02, NFR-RU-01, NFR-RU-03 (`SRS.md` §4.2) | Either revise SRS target (with rationale + version bump) or mark Status = **Not Met**. Action `A-25023-06`. |
| 2 | Persona-as-owner | v0.1 of this document | Replaced in v0.2: Raveh is accountable; persona is role-context. |
| 3 | "v1.0 SRS revision" deferral with no calendar date | 23 of 39 actions in v0.1 | v0.2 §9 requires a Target Date column for every P0 / P1 — see `A-GOV-02`. |

---

## 4. ISO/IEC/IEEE 29148:2018 — Requirements Engineering

**Role-context for this section**: John (PM persona). Tech-writer (Paige) co-owns document attributes.

### 4.1 SRS Document Attributes (29148 §9.5.5)

| 29148 attribute | Required | Evidence | Status |
|---|---|---|---|
| Document identification (title, version, date, author, status) | Required | `SRS.md` lines 1–9 | ✅ |
| Purpose | Required | `SRS.md` §1.1 | ✅ |
| Scope | Required | `SRS.md` §1.2 | ✅ |
| Definitions, acronyms, abbreviations | Required | `SRS.md` §1.3 | ✅ |
| **Glossary (separate from Definitions)** | Recommended | Not separated | ⚠️ — `A-29148-15` |
| **Stakeholder identification** | Required | Implicit (single developer); not enumerated | ❌ — `A-29148-15` |
| **Document conventions** | Recommended | Not declared (SHALL/SHOULD/MAY usage is implicit) | ⚠️ — `A-29148-15` |
| **Distribution list** | Required | Not present | ❌ — `A-29148-15` |
| References | Required | `SRS.md` §1.5 | ✅ |
| Document overview / TOC | Required | `SRS.md` §Table of Contents | ✅ |
| Product perspective | Required | `SRS.md` §2.1 | ✅ |
| Product functions | Required | `SRS.md` §2.2 | ✅ |
| User characteristics / classes | Required | `SRS.md` §2.3 | ✅ |
| Operating environment | Required | `SRS.md` §2.4 | ✅ |
| Design and implementation constraints | Required | `SRS.md` §7.1 | ✅ |
| **User documentation requirements** | Required | Bare reference to `docs/user-manual.md` exists; no SRS-internal declaration of audiences, formats, languages, delivery, completeness criteria | ❌ — `A-29148-17` |
| Assumptions and dependencies | Required | `SRS.md` §7.2 | ✅ |
| Apportioning of requirements (deferred to future versions) | Optional | `SRS.md` §8 (Future Requirements) | ✅ |
| Specific requirements (functional + non-functional) | Required | `SRS.md` §3 + §4 | ✅ |
| Interface requirements | Required | `SRS.md` §5 | ✅ |
| **Verification requirements per FR** | Required | NFRs have "Measurement" column; **most FRs have no explicit verification method** | ⚠️ — `A-29148-01` / `A-29148-05` |
| **Document change history** | Required | Single-line version on cover; no in-document change-history table; pre-baseline iterations only in git | ❌ — `A-29148-02` |
| **Approval / sign-off** (named approver + date) | Required | "Status: BASELINED — approved" (one line); no signatory, no date of approval, no review board | ❌ — `A-29148-03` |
| **V&V cross-reference table** | Recommended | Present indirectly via §10.4 (traceability); not labeled as V&V cross-reference | ⚠️ — `A-29148-13` |

**Tally**: 14 ✅ + 4 ⚠️ + 5 ❌ + (0 ➖) of 23 attributes audited = 11 / 19 substantive attributes compliant (excluding the 4 newly-tracked attributes for which no evidence exists yet).

### 4.2 Requirement Attributes (29148 §5.2.6, §9.4)

| Attribute | Coverage in SRS | Status |
|---|---|---|
| Unique identifier | All FR-*/NFR-* IDs unique | ✅ |
| Statement (SHALL/SHOULD/MAY) | Used consistently | ✅ |
| Rationale | Provided where non-obvious | ✅ |
| Source / stakeholder origin | Implicit; no explicit source column | ⚠️ — `A-29148-04` |
| Priority (MoSCoW) | Must / Should / Could / Won't | ✅ |
| Criticality | Folded into priority | ✅ |
| Status | Per `SRS.md` §10.3 | ✅ |
| Verification method | Defined for NFRs (Measurement column); **missing for most FRs** | ❌ — `A-29148-05` |
| Verification type per 29148 §6.4 (I/A/D/T) | Not tagged | ⚠️ — `A-29148-13` |
| Validation criteria (acceptance) | Implicit in target / SLA columns; not labeled "acceptance criteria" | ⚠️ — `A-29148-06` |

### 4.3 Document Hierarchy (29148 §6.2)

| Document type | Required | Present | Status |
|---|---|---|---|
| ConOps / Operational Concept | Recommended | Implicit in `SRS.md` §2.5 (Design Philosophy) — not labeled as ConOps; no operational-scenarios narrative | ⚠️ — `A-29148-07` |
| StRS (stakeholder needs) | Recommended for non-trivial systems | Not separately documented (single-developer project; stakeholder ≈ author) | ➖ acceptable for current scale; revisit if external stakeholders engage |
| SyRS / SRS | Required | `SRS.md` covers both as fused doc (acceptable for software-only product) | ✅ |

(v0.1 incorrectly cited `SRS.md` §9 "Competitive Gap Analysis" as ConOps evidence — corrected.)

### 4.4 Verification vs Validation discipline (29148 §A.2)

The Edge-Case Hunter pass identified that `VV-Plan.md` §1.1 places "integration tests" in the **Validation** column, which is a category error per 29148 §A.2 — integration tests are *verification*. Validation concerns operational/stakeholder fitness, not test scope. **Action `A-29148-14`**.

### 4.5 Traceability (29148 §5.2.8)

| Traceability dimension | Evidence | Status |
|---|---|---|
| Requirement → source code | `SRS.md` §10.4.2 (package map) | ✅ |
| Requirement → test | `SRS.md` §10.4.3 (SM-xxx markers); `VV-Plan.md` §6 test tables; **planned `TestFR_XXX_YY` naming not yet adopted** | ⚠️ — `A-29148-08` (joint with `A-29119-04`) |
| Requirement → version | `CHANGELOG.md` + Status column | ✅ |
| Requirement → roadmap phase | `SRS.md` §8 + §11 | ✅ |
| Bidirectional (test → requirement) | Manual via SM-xxx; no automated coverage matrix yet | ⚠️ — `A-29148-09` (joint with `A-29119-05`) |

### 4.6 Quality of Requirements (29148 §5.2.4)

| Quality | Audit observation | Status |
|---|---|---|
| Necessary | Removed-from-scope items documented (§8.5) | ✅ |
| Implementation-free | Some FRs prescribe technology ("via ReadDirectoryChangesW", "via SQLite"). Justified for Windows-first, but mixes WHAT and HOW | ⚠️ — `A-29148-10` |
| Unambiguous | SHALL/SHOULD usage consistent | ✅ |
| Complete | Major surface covered; "Open Questions" §10.6 remain | ⚠️ — track via §10.6 |
| Singular | Some compound requirements (e.g., FR-WATCH-10 chains "USN journal for fast restart AND for buffer overflow recovery") | ⚠️ — `A-29148-11` |
| Feasible | Status column reflects feasibility | ⚠️ — addressed via 25023 measurement gap |
| Traceable | See §4.5 | ✅ |
| Verifiable | NFRs measurable; some FRs ("graceful shutdown", "actionable errors") subjective | ❌ — `A-29148-12` (now demands a specific list) |

### 4.7 29148 Action Items

See §9. Tag prefix: **A-29148-NN**.

---

## 5. ISO/IEC 25010:2023 — Product Quality Model

**Role-context for this section**: Winston (architect persona).

### 5.1 Top-level Characteristic Coverage (25010:2023 §5.3)

25010:2023 defines **9 top-level characteristics**: Functional Suitability, Performance Efficiency, Compatibility, Interaction Capability, Reliability, Security, Maintainability, Flexibility, Safety. (Portability moved into Flexibility.)

| 25010:2023 characteristic | Addressed in SRS | Status |
|---|---|---|
| Functional Suitability | §4.1 | ✅ |
| Performance Efficiency | §4.2 | ✅ |
| Compatibility | §4.3 | ✅ |
| **Interaction Capability** (replaces 2011 Usability for interactive systems) | Excluded as "non-interactive (CLI + service)" — but `FR-ASP-05` (Web dashboard / tray icon, `SRS.md` §8.4 "Committed Post-v1.0") brings Interaction Capability back into scope at v1.5+ | ➖ **conditional**: acceptable for v1.0; mandatory re-audit when GUI work begins. `A-25010-11` |
| Reliability | §4.5 | ✅ |
| Security | §4.6 | ✅ |
| Maintainability | §4.7 | ✅ |
| **Flexibility** (new top-level in 2023; encompasses Adaptability, Scalability, Installability, Replaceability) | SRS still uses 2011 layout (Adaptability + Installability under Portability §4.8); Replaceability + Scalability not declared as separate NFRs | ⚠️ — `A-25010-01` |
| **Usability** (deprecated as top-level in 2023; folded into Interaction Capability) | Still present as `SRS.md` §4.4 — **schema mismatch with 2023** | ❌ for 25010:2023 — `A-25010-02` |
| **Safety** (new in 2023) | Excluded — "not a safety-critical system" | ⚠️ — data-loss scenarios (`FR-DEL-03` default `delete`, `FR-DEL-08` force-delete on rename) brush against Safety:Operational Constraint. Formal justification owed. `A-29148-16` |
| Portability (narrower in 2023) | `SRS.md` §4.8 | ⚠️ — re-classification needed (`A-25010-01`) |

### 5.2 Sub-characteristic Coverage

#### 5.2.1 Functional Suitability

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Functional Completeness | NFR-FS-01, FS-02 | ✅ |
| Functional Correctness | NFR-FC-01..03 | ✅ |
| **Functional Appropriateness** (25010:2023 §5.3.1) | Implicit evidence in `FR-DEL-01` three-policy design fits user-task variation | ⚠️ — `A-25010-03` (annotate existing FR rather than authoring new NFR) |

#### 5.2.2 Performance Efficiency

All three sub-characteristics covered: Time Behaviour, Resource Utilization, Capacity. ✅

#### 5.2.3 Compatibility

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Co-existence | NFR-CX-01..03 | ✅ |
| Interoperability | NFR-IO-01..03 | ✅ |

#### 5.2.4 Reliability

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Faultlessness (renamed in 2023; was Maturity) | Implicit in NFR-FT; no explicit faultlessness metric | ⚠️ — `A-25010-04` (P1, gates Reliability claim) |
| Availability | NFR-AV-01..02 | ✅ |
| Fault Tolerance | NFR-FT-01..04 | ✅ |
| Recoverability | NFR-RC-01..04 | ✅ |

#### 5.2.5 Security

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Confidentiality | NFR-CO-01..04 | ✅ |
| Integrity | NFR-IN-01..03 | ✅ |
| Non-repudiation | NFR-NR-01..02 | ✅ |
| Accountability | NFR-AC-01..02 | ✅ |
| **Authenticity** (new in 2023) | Not addressed; outbound-only via rclone limits exposure but doesn't justify omission | ⚠️ — `A-25010-05` (P1) |
| **Resistance** (new in 2023; resilience to attack) | Implicit in `docs/security-audit-2026-04-18.md`; not declared as NFR | ⚠️ — `A-25010-06` (P1; required for Security claim) |
| **Privacy / Data Protection** (cross-cutting; touches Confidentiality + Accountability) | Outbound paths exist (`FR-ANOM-11` anomaly endpoint, `FR-TEL-01/02` telemetry); no GDPR/CCPA-style lawful-basis / data-subject-rights / retention-on-server NFR | ❌ — `A-25010-10` (P1) |

#### 5.2.6 Maintainability

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Modularity | NFR-MO-01..02 | ✅ |
| Reusability | Closed-source product BUT `FR-ASP-17` Hook System is explicitly designed for third-party reuse | ⚠️ — `A-25010-07` (close with reasoned 2-sentence justification, declaring hook surface as Reusability evidence) |
| Analysability | Implicit in observability features (FR-DIAG, FR-ANOM); not declared as NFR | ⚠️ — `A-25010-08` |
| Modifiability | NFR-MD-01..04 | ✅ |
| Testability | NFR-TE-01..04 | ✅ |

#### 5.2.7 Flexibility (25010:2023 new top-level)

| Sub-characteristic | Addressed | Status |
|---|---|---|
| Adaptability | NFR-AD-01..03 (currently under Portability §4.8) | ⚠️ — `A-25010-01` |
| Scalability | Implicit in Capacity §4.2.3 | ⚠️ — `A-25010-01` |
| Installability | NFR-IS-01..03 (currently under Portability §4.8) | ⚠️ — `A-25010-01` |
| Replaceability | rclone-as-transport doctrine (`SRS.md` §2.5 item 2) is core architectural decision; effectively a Must-have NFR | ⚠️ — `A-25010-12` (P1, was P3) |

### 5.3 Schema-migration impact (A-25010-01 cascade)

The Edge-Case Hunter correctly noted that A-25010-01 v0.1 was under-specified. Migrating `SRS.md` §4 to 25010:2023 forces:

1. **NFR ID renames**: `NFR-IS-*` (Installability) and `NFR-AD-*` (Adaptability) currently under Portability move to Flexibility. Every traceability reference to those IDs (in code, tests, CHANGELOG, external docs) must be updated, OR the IDs preserved while only the section heading changes.
2. **Appendix `SRS.md` §10.2 regeneration** — the "ISO/IEC 25010 Quality Characteristics Key" table is 2011-shaped.
3. **`VV-Plan.md` §2.2 alignment** — its quality-characteristic-to-verification table currently lists 2011's 8 characteristics. Currently false claim that "VV-Plan applies 25010:2023". `A-29119-10`.
4. **This compliance document** — internal section anchors that point to SRS sections must remain valid post-restructure.

**Decision required (Raveh)**: Restructure SRS for 2023 schema, OR document the 2011 schema as the authoritative project layout with explicit deviation note.

**Recommendation (Winston-context)**: Defer to v1.0 SRS revision; document the deviation here. Justification: engineering content is complete; only taxonomy labels differ. Cost ≈ medium; benefit ≈ standards-purity, no functional impact. If deviation is chosen, downgrade A-25010-01 from "restructure" to "deviation note" — but A-25010-02 (Usability → Interaction Capability) is **not** waivable while still claiming 25010:2023.

### 5.4 25010 Action Items

Tag prefix: **A-25010-NN**. Aggregated in §9.

---

## 6. ISO/IEC 25023:2016 — Quality Measurement

**Role-context**: Winston (architect) defines functions; Amelia (dev) builds instrumentation.

### 6.1 Measurement Function Coverage

`VV-Plan.md` §2.3 defines **6 measurement functions** for ~24 quantitative NFRs.

| Quality area | NFRs in SRS | Measurement functions defined | Status |
|---|---|---|---|
| Functional Suitability | NFR-FS, NFR-FC (5 NFRs) | 0 explicit (regression tests implicit) | ⚠️ |
| Performance Efficiency – Time | NFR-TB-01..07 (7 NFRs) | 2 (NFR-TB-01, 02) | ⚠️ |
| Performance Efficiency – Resource | NFR-RU-01..05 (5 NFRs) | 2 (NFR-RU-01, 03) | ⚠️ |
| Performance Efficiency – Capacity | NFR-CA-01..03 (3 NFRs) | 1 (NFR-CA-01) | ⚠️ |
| Compatibility | NFR-CX, NFR-IO (6 NFRs) | 0 | ⚠️ |
| Usability | NFR-LN, NFR-OP, NFR-UE (~10 NFRs) | 0 (qualitative; reasonable) | ⚠️ |
| Reliability | NFR-FT, NFR-RC, NFR-AV (~10 NFRs) | 1 (NFR-FT-01) | ⚠️ |
| Security | NFR-CO, NFR-IN, NFR-NR, NFR-AC (11 NFRs) | 0 | ⚠️ |
| Maintainability | NFR-MO, NFR-TE, NFR-MD (~10 NFRs) | 0 (test count covered in NFR-TE-01) | ⚠️ |
| Portability | NFR-AD, NFR-IS (~6 NFRs) | 0 | ⚠️ |

**Action `A-25023-01`**: Author full measurement-function table for every quantitative NFR. For qualitative NFRs (e.g., NFR-OP-01 "actionable error messages"), declare measurement = peer review checklist.

### 6.2 Measurement Evidence — split into per-NFR P0 actions (revised v0.2)

The v0.1 draft used a single compound action `A-25023-02`. Per Adversarial Reviewer demand and Edge-Case Hunter reinforcement, that compound is split into 11 singular actions, each P0 for v1.0:

| Action ID | NFR | Target | v0.1 Status | Singular owner-action |
|---|---|---|---|---|
| **A-25023-02a** | NFR-TB-01 | < 50 ms p99 detection | "Met at 100 ms; 50 ms Not Measured" — see §3.3 standards-gaming flag | Measure or revise target |
| **A-25023-02b** | NFR-TB-02 | < 3 s p95 single-file sync | "Met at 5 s; 3 s Not Measured" — see §3.3 | Measure or revise target |
| **A-25023-02c** | NFR-TB-03 | < 60 s p95 large files | "Not Measured" | Measure |
| **A-25023-02d** | NFR-TB-04 | < 30 s startup reconciliation | "Met" — keep | Confirm methodology in measurement-function table |
| **A-25023-02e** | NFR-TB-06 | > 100 events/s queue throughput | "Not Measured" | Measure |
| **A-25023-02f** | NFR-TB-07 | < 40 s service restart | "Not Measured" | Measure |
| **A-25023-02g** | NFR-RU-01 | < 25 MB RSS idle | "Met at 30 MB" — see §3.3 | Measure or revise target |
| **A-25023-02h** | NFR-RU-02 | < 80 MB RSS loaded | "Not Measured" | Measure |
| **A-25023-02i** | NFR-RU-04 | < 10 IOPS / sync | "Not Measured" | Measure |
| **A-25023-02j** | NFR-CA-01 | 32 mirrors | "Not Tested" | Load test |
| **A-25023-02k** | NFR-CA-02 | 100 k files / mirror | "Not Tested" | Stress test |
| **A-25023-03** | NFR-AV-01 | 99.9 % uptime | "Not Measured" | Define availability-measurement methodology (telemetry / event-log based) |

### 6.3 Measurement Methodology Compliance (25023 §5.2)

25023 requires each measurement to declare: name, purpose, method of application, measurement function, **type of measure (Base / Derived / Indicator)**, **type of scale (nominal / ordinal / interval / ratio)**, source of data, audience.

`VV-Plan.md` §2.3 defines name + function + (implicitly) source for 6 measurements; the other attributes are missing. Some existing measurements are mis-typed: NFR-TE-01 "test count" is a Base Measure presented as Indicator; NFR-FT-01 "crashes / runtime hours" is a Derived Measure presented as raw measurement function.

**Actions**: `A-25023-04` (extend attribute table) and `A-25023-05` (Base / Derived / Indicator + Scale-type classification, fixing mis-typed entries).

### 6.4 Standards-gaming remediation (revised v0.2)

`A-25023-06` (new): Audit every "Met at [looser value]" Status entry in `SRS.md` §4.2. For each, decide: revise SRS target with documented rationale and version bump, OR mark Status = **Not Met**. Eliminate the rhetorical category.

### 6.5 Forward-look

A 25023 successor edition may be published; verification deferred. **Action `M-25023-01`**: review status of any post-2016 revision after v1.0 release; decide whether to migrate. (v0.1 incorrectly asserted "25023 was reissued in 2024" — that claim removed pending verification.)

---

## 7. ISO/IEC/IEEE 29119 family — Software Testing

**Role-context**: Amelia (dev) for test-process implementation, Edge-Case Hunter for test-design. John (PM) signs off on Strategy.

### 7.1 Document Compliance (29119-3 — Test Documentation)

| 29119-3 document | Required | Present | Status |
|---|---|---|---|
| **Organizational Test Strategy** | Required for non-trivial projects | A personal-discipline skill memory file in `~/.claude/skills/qh-sw-developer/` is **not** a project-level Strategy. Not under project version control, not approved by stakeholders, not tied to this product's risk profile. | ❌ — `A-29119-01` (P0) |
| **Project Test Plan** | Required | `VV-Plan.md` | ✅ |
| **Master Test Plan vs Level Test Plan** distinction | Required for multi-level | Not enumerated separately | ⚠️ — `A-29119-08` |
| **Test Sub-Plans** (per test level) | Optional | Test tables per FR-* in `VV-Plan.md` §6 | ✅ |
| **Test Design Specification** | Required | Per-feature tables in `VV-Plan.md` §6 with EP/BVA/DT/ST | ✅ |
| **Test Case Specification** | Required | Implicit in `*_test.go` files; not formally documented | ⚠️ — `A-29119-02` |
| **Test Procedure Specification** | Required | Implicit in test code | ⚠️ — `A-29119-02` |
| **Test Readiness Review checklist** | Recommended | Not present | ⚠️ — `A-29119-08` |
| **Regression Test Approach** | Recommended | Implicit (every commit runs all tests); not documented | ⚠️ — `A-29119-08` |
| **Test Data Requirements** | Recommended | Not documented | ⚠️ — `A-29119-08` |
| **Test Environment Readiness Report** | Recommended | Not produced | ⚠️ — `A-29119-06` |
| **Test Item Transmittal Report** (per release) | Required | Not produced | ❌ — `A-29119-03` |
| **Test Log** | Required | CI logs in GitHub Actions | ✅ |
| **Test Status Report** (per cycle) | Required | Not produced | ❌ — `A-29119-03` |
| **Test Completion Report** (per release) | Required | `docs/validation-report-2026-04-16.md` exists; one-off, not per release | ❌ — `A-29119-03` |
| **Anomaly Report** | Required | BugTracker (`C:\BugTracker\projects\Smirror\SM-NNN.md`) — unusually mature for solo project | ✅ |

### 7.2 Test Process Compliance (29119-2)

| Process | Required | Evidence | Status |
|---|---|---|---|
| Organizational Test Process (sec 6) | Required | `qh-sw-developer` skill memory + this audit — *insufficient as Org Process per 29119-1 §6* | ❌ — `A-29119-01` |
| Test Planning | Required | `VV-Plan.md` §1, §10 | ✅ |
| Test Monitoring & Control | Required | CI gates (`ci.yml`) **plus release.yml runs `go vet` + `go test` before GoReleaser** (added 2026-04-27, commit f264a3e); race detector covers all critical packages including internal/sync + internal/state. Per-release Test Status Report still missing. | ⚠️ — `A-29119-03` (Status Report only; CI gates compliant) |
| Test Completion | Required | One validation report; not per-release ritual | ❌ — `A-29119-03` |
| Test Design & Implementation | Required | `VV-Plan.md` §6 tables + Go tests | ✅ |
| Test Environment Set-Up & Maintenance | Required | `test/sla_smoke.ps1`, CI environment definitions; not documented as 29119 artifact | ⚠️ — `A-29119-06` |
| Test Execution | Required | `go test` + PowerShell harness | ✅ |
| Test Incident Reporting | Required | BugTracker (per `qh-sw-developer` §5) | ✅ |

### 7.3 Test Techniques (29119-4)

The project applies **EP, BVA, DT, ST** — correctly tagged in `VV-Plan.md` §6. Other 29119-4 techniques:

| Technique | Used? | Notes |
|---|---|---|
| Cause-Effect Graphing | ❌ | Could enrich anomaly hypothesis chains (advisory) |
| Combinatorial / pairwise | ❌ | Natural fit for delete-policy × ghost-mode × quarantine-retention matrix — `A-29119-09` |
| Random / Fuzz Testing | ✅ Partial | 2 fuzz targets; plan in `VV-Plan.md` §8 |
| Mutation Testing | ❌ | Listed as P3 gap in `VV-Plan.md` §5.3 |
| Use Case Testing | ⚠️ | Implicit in PowerShell scenarios; not formal |
| Risk-Based Testing | ⚠️ | `SRS.md` §11.9 + `VV-Plan.md` §11 risk registers; tests not explicitly risk-tagged. Note: the two registers may diverge — `A-GOV-03` |

### 7.4 Test Naming Convention

`VV-Plan.md` §7.2 specifies adopting `TestFR_XXX_YY_Scenario` for automated traceability — **not yet implemented**. **Action `A-29119-04`**.

### 7.5 Coverage Matrix

`VV-Plan.md` §7.1 specifies a requirement→test→result matrix — **not yet automated**. **Action `A-29119-05`**.

### 7.6 29119 vocabulary alignment (29119-1)

`VV-Plan.md` mixes "test level", "test type", and "test phase" without conformance to 29119-1 vocabulary. **Action `A-29119-11`** (low priority).

### 7.7 29119 Action Items

Tag prefix: **A-29119-NN**. Aggregated in §9.

---

## 8. Cross-cutting Gaps

| ID | Gap | Affects | Role-context |
|---|---|---|---|
| **X-01** | No formal Test Strategy document; `qh-sw-developer` skill is **not** a substitute (clarified v0.2) | 29119-3, 29148 §6 | John + Amelia |
| **X-02** | Measurement results not maintained over time (each measurement should record value + date) | 25023, 29119 | Winston + Amelia |
| **X-03** | Document change history embedded only in git, not in-doc | 29148 §9.5.5 | Paige |
| **X-04** | watcher package at 16.6% statement coverage; major requirement-coverage gap (FR-WATCH-*) | 25010 Maintainability:Testability, 29119 | Amelia |
| **X-05** | No Authenticity / Resistance / Privacy NFRs explicitly declared | 25010 Security (2023) | Winston |
| **X-06** | 25010:2023 schema mismatch in SRS §4 (uses 2011 Usability/Portability layout) | 25010 | Winston + Paige |
| **X-07** | No Test Item Transmittal / Test Status / Test Completion ritual per release | 29119-3 | Amelia |
| **X-08** | Verification vs Validation conflated in `VV-Plan.md` §1.1 (integration tests mis-placed under Validation) | 29148 §A.2 | John |
| ~~X-09~~ | ~~SRS §11.9 / VV-Plan §11 risk registers may diverge~~ — **withdrawn v0.3**: VV-Plan has no §11; only SRS §11.9 exists. Phantom finding from v0.1 audit. | — | — |
| **X-10** | Self-audit conflict of interest: same agent authored SRS, V&V Plan, and this audit | Governance / 29148 §6 | Raveh |
| **X-11** | `docs/security-audit-2026-04-18.md` findings closure not tracked | 25010 Security | Winston |

---

## 9. Action Register

**Format**: `ID | Description | Standard | Role-context (BMad persona) | Priority | Target date | Status`

**Accountability**: every action is owned by **Raveh**. The "Role-context" column names the BMad persona Raveh adopts when executing the action — it is *discipline framing*, not delegation.

**Priority key**:
- **P0** — blocks v1.0 release / required to claim ISO compliance externally
- **P1** — required for "Compliant" status on this audit
- **P2** — quality / hygiene
- **P3** — advisory / future

**Target date**: Calendar dates are required for all P0 / P1 items per Adversarial Reviewer demand. Items still showing "TBD" must be assigned dates by 2026-05-15 (action `A-GOV-02`).

### 9.1 ISO/IEC/IEEE 29148 actions

| ID | Description | Role | Priority | Target | Status |
|---|---|---|---|---|---|
| A-29148-01 | Add explicit Verification Method column to FR-* tables in `SRS.md` §3 | John | P0 | 2026-Q3 (TBD) | Open |
| A-29148-02 | Add in-document change-history table to `SRS.md` §1.6 | Paige | P2 | TBD | Open |
| A-29148-03 | Add named approval / sign-off block (signatory + date) to `SRS.md` cover | Paige | P2 | TBD | Open |
| A-29148-04 | Add "Source / Origin" column to NFR tables (or note origin in §10.4) | John | P3 | Post-v1.0 | Open |
| A-29148-05 | Define verification method for every FR (currently NFRs only) | John + Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-29148-06 | Re-label "Target / SLA" as "Acceptance Criteria" or add explicit acceptance criteria | John | P2 | TBD | Open |
| A-29148-07 | Add `docs/conops.md` (Concept of Operations) — 1–2 pages | Paige | P2 | TBD | Open |
| A-29148-08 | Adopt `TestFR_XXX_YY_Scenario` naming convention (joint with `A-29119-04`) | Amelia | P1 | 2026-Q2 (TBD) | Open |
| A-29148-09 | Generate automated requirement→test traceability matrix (joint with `A-29119-05`) | Amelia | P1 | 2026-Q2 (TBD) | Open |
| A-29148-10 | Audit FR-* statements for implementation leakage. **Acceptance**: produce a list of 3–10 specific FR rewordings; merge or document Windows-first deviation | John | P3 | Post-v1.0 | Open |
| A-29148-11 | Split compound requirements. **Acceptance**: list specific FR-IDs to split (FR-WATCH-10 confirmed), ship the split in next SRS revision | John | P3 | Post-v1.0 | Open |
| A-29148-12 | Convert subjective FRs to measurable form. **Acceptance**: list FR-IDs ("graceful shutdown" = `FR-SVC-05`; "actionable errors" = `NFR-OP-01`); define metric per-ID | John | P1 | 2026-Q3 (TBD) | Open |
| A-29148-13 | Tag every verification method as Inspection / Analysis / Demonstration / Test per 29148 §6.4 | John + Amelia | P2 | TBD | Open |
| A-29148-14 | Fix `VV-Plan.md` §1.1 V&V table: integration tests are *verification*, not validation | John | P1 | 2026-Q2 (TBD) | Open |
| A-29148-15 | Add Stakeholder list, Glossary (split from Definitions), Distribution list, Document conventions to SRS per 29148 §9.5.5 | Paige | P2 | TBD | Open |
| A-29148-16 | Provide formal Safety justification (data-loss scenarios reviewed and concluded out-of-scope) | Winston | P3 | Post-v1.0 | Open |
| A-29148-17 | Author User Documentation Requirements section in `SRS.md` (audiences, formats, delivery, completeness criteria) | Paige | P1 | 2026-Q3 (TBD) | Open |

### 9.2 ISO/IEC 25010 actions

| ID | Description | Role | Priority | Target | Status |
|---|---|---|---|---|---|
| A-25010-01 | ~~Decide: restructure or deviate~~ — **DECIDED 2026-04-27: Version B (deviation note) for v1.0; restructure to v1.1.** SRS §4.0 deviation note added; iso-compliance §5 anchors updated. Restructure (Version A) is now `A-25010-01b` for v1.1. | Winston | P1 | v1.0 (deviation): 2026-04-27 ✅ ; v1.1 (restructure): post-2026-08 | **Closed (v1.0 path)** |
| A-25010-01b | Restructure `SRS.md` §4 to full 25010:2023 layout (Flexibility as top-level; Adaptability/Installability/Replaceability/Scalability move there; Usability replaced by Interaction Capability or removed; Authenticity/Resistance added; Safety formal-justified). Includes cascade: NFR ID changes, Appendix §10.2 regen, VV-Plan §2.2 alignment | Winston | P2 | v1.1 | Open |
| A-25010-02 | Replace top-level "Usability" with "Interaction Capability" or document explicit deviation. **NOT waivable while claiming 25010:2023.** | Winston | P0 | 2026-Q3 (TBD) | Open |
| A-25010-03 | Annotate `FR-DEL-01` three-policy design as Functional Appropriateness evidence | Winston | P2 | TBD | Open |
| A-25010-04 | Add explicit Faultlessness metric (panics-per-runtime-hour); gates Reliability claim. **v0.4 update**: substantive evidence already shipped in 0.9.12-dev — `internal/sync/liveness.go` implements multi-signal stall detection (output / cpu_time / io_bytes), with measurable thresholds (10s tick × K=6 = 60s flat-grace transfer; 30s × K=8 = 240s metadata). Faultlessness model is now real engineering, not just policy. Need to formalize as NFR-FL-01 with these thresholds as targets. | Winston + Amelia | P1 | 2026-Q3 (TBD) | **Evidence shipped; NFR formalization pending** |
| A-25010-05 | Add Authenticity NFR (rclone-mediated; sub-process identity verification) | Winston | P1 | 2026-Q3 (TBD) | Open |
| A-25010-06 | Promote `docs/security-audit-2026-04-18.md` findings into formal Resistance NFR(s); required for Security claim | Winston | P1 | 2026-Q3 (TBD) | Open |
| A-25010-07 | Close Reusability evaluation: declare hook surface (`FR-ASP-17`) as Reusability evidence; finalize 2-sentence justification | Winston | P3 | 2026-05-15 | Open |
| A-25010-08 | Add Analysability NFR (link FR-DIAG, FR-ANOM observability features). **v0.4 update**: strengthened by 0.9.12-dev anomaly kinds (`Sync:Stalled` warning, `Sync:LsJsonSlow` info). Causal-hypothesis model from FR-ANOM-05 now extends to stall-class incidents. | Winston | P2 | TBD | Open (evidence accumulating) |
| A-25010-09 | (Deprecated; superseded by A-25010-12) | — | — | — | Closed |
| A-25010-10 | Add Privacy / Data Protection NFR family covering `FR-ANOM-11` outbound + `FR-TEL-*` (lawful basis, retention, redaction guarantee, opt-in/out symmetry, GDPR/CCPA references) | Winston | P1 | 2026-Q3 (TBD) | Open |
| A-25010-11 | Re-audit Interaction Capability when `FR-ASP-05` (GUI) reaches "in development". Re-audit trigger added to §10.1 | Winston | P3 (deferred) | When GUI work begins | Triggered |
| A-25010-12 | Reclassify Replaceability from advisory to architectural NFR; declare rclone-as-transport doctrine with explicit substitution test | Winston | P1 | 2026-Q3 (TBD) | Open |

### 9.3 ISO/IEC 25023 actions

| ID | Description | Role | Priority | Target | Status |
|---|---|---|---|---|---|
| A-25023-01 | Author measurement-function table covering every quantitative NFR (extend `VV-Plan.md` §2.3) | Winston + Amelia | P1 | 2026-Q3 (TBD) | Open |
| A-25023-02a | Measure or revise NFR-TB-01 (50 ms p99 detection) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02b | Measure or revise NFR-TB-02 (3 s p95 sync) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02c | Measure NFR-TB-03 (60 s p95 large files) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02d | Confirm NFR-TB-04 measurement methodology (already "Met") | Amelia | P1 | 2026-Q3 (TBD) | Open |
| A-25023-02e | Measure NFR-TB-06 (queue throughput > 100 events/s) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02f | Measure NFR-TB-07 (service restart < 40 s) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02g | Measure or revise NFR-RU-01 (25 MB RSS idle) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02h | Measure NFR-RU-02 (80 MB RSS loaded) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02i | Measure NFR-RU-04 (10 IOPS / sync) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02j | Load test NFR-CA-01 (32 mirrors) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-02k | Stress test NFR-CA-02 (100 k files / mirror) | Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-25023-03 | Define availability-measurement methodology (NFR-AV-01) — service-event-log-based | Amelia | P2 | TBD | Open |
| A-25023-04 | Extend `VV-Plan.md` §2.3 with 25023 §5.2 attributes (purpose, method, type, scale, audience) per measurement | Winston | P1 | 2026-Q3 (TBD) | Open |
| A-25023-05 | Classify each existing measurement as Base / Derived / Indicator and Scale type; correct mis-typed (NFR-TE-01, NFR-FT-01) | Winston | P2 | TBD | Open |
| A-25023-06 | Eliminate "Met at looser value" framing in `SRS.md` §4. For each, revise target with rationale + version bump OR mark Status = **Not Met** | Winston | P1 | 2026-Q2 (TBD) | Open |
| M-25023-01 | Verify whether 25023 successor edition exists; review and decide adoption | Winston | P3 | Post-v1.0 | Open |

### 9.4 ISO/IEC/IEEE 29119 actions

| ID | Description | Role | Priority | Target | Status |
|---|---|---|---|---|---|
| A-29119-01 | Author `docs/test-strategy.md` (project-level Organizational Test Strategy). **NOT waivable while claiming 29119 compliance.** | John + Amelia | P0 | 2026-Q3 (TBD) | Open |
| A-29119-02 | Decide whether per-test Test Case Specification is needed beyond Go test files. **Acceptance**: documented decision + rationale | Amelia | P3 | 2026-05-15 | Open |
| A-29119-03 | Adopt per-release ritual: Test Status Report + Test Item Transmittal + Test Completion Report — minimal templates in `docs/test-reports/`. **Split-able into 3 sub-actions if convenient** | Amelia | P1 | 2026-Q3 (TBD) | Open |
| A-29119-04 | Migrate Go test names to `TestFR_XXX_YY_Scenario` (joint A-29148-08) | Amelia | P1 | 2026-Q2 (TBD) | Open |
| A-29119-05 | Implement `tools/coverage-matrix.go` to generate `coverage-matrix.json` (joint A-29148-09) | Amelia | P1 | 2026-Q2 (TBD) | Open |
| A-29119-06 | Document test environments per 29119-2 §7.7. **Scope**: CI runner spec; `test/sla_smoke.ps1` env requirements; local-dev requirements; backend-test rclone configuration. Template: `docs/test-environments.md` | Amelia | P2 | TBD | Open |
| A-29119-07 | Adopt Risk-Based Testing — link test IDs to reconciled risk register entries (depends on `A-GOV-03`) | John + Amelia | P3 | Post-v1.0 | Open |
| A-29119-08 | Add Master Test Plan vs Level Test Plan structure; add Test Readiness Review checklist, Regression Approach, Test Data Requirements docs | Amelia | P2 | TBD | Open |
| A-29119-09 | Adopt Combinatorial / pairwise testing for delete-policy × ghost-mode × quarantine-retention matrix | Amelia | P3 | Post-v1.0 | Open |
| A-29119-10 | Update `VV-Plan.md` §2.2 quality-characteristic table to 25010:2023 layout (joint with A-25010-01) | Amelia | P1 | 2026-Q3 (TBD) | Open |
| A-29119-11 | Align `VV-Plan.md` vocabulary with 29119-1 (test level / test type / test phase usage) | Amelia | P3 | Post-v1.0 | Open |
| A-29119-12 | **NEW v0.4**: Add per-release VV-Plan §5.2 re-measurement to release ritual. SM-155 documents the gap: 0.9.11-dev claimed to refresh VV-Plan but only updated test count + version label; per-package coverage table was 9 days stale and propagated wrong baseline (16.6% watcher) into iso-compliance.md X-04 priority. Fix: add `go test ./internal/... -cover` step to release pipeline, update VV-Plan §5.2 atomically with version bump. | Amelia | P1 | v1.0 / 2026-04-30 | Open |

### 9.5 Governance / cross-cutting actions

| ID | Description | Role | Priority | Target | Status |
|---|---|---|---|---|---|
| A-GOV-01 | **DECIDED 2026-04-29**: SELF-ASSESSMENT label retained permanently. Third-party / independent compliance review is not planned and not claimed. **v0.6 reframe (2026-04-29)**: the iso-compliance-review report (`docs/iso-compliance-review-2026-04-29.md`) flagged that "Closed by decision" understates the structural consequence — 29148:2018 §6.5 (stakeholder validation) and §5.2.4 (peer review of requirements) become unfulfillable, not deferred. The honest classification is **Non-Conformity by Choice**: the project deliberately does not pursue this clause. No pretense of compliance is made on those clauses. | Raveh | — | — | **Non-Conformity by Choice (decision logged 2026-04-29; document records the deliberate non-pursuit)** |
| A-GOV-02 | Assign calendar dates to all P0 / P1 actions in this register | Raveh | P1 | 2026-05-15 | Open |
| ~~A-GOV-03~~ | ~~Reconcile SRS §11.9 and VV-Plan §11 risk registers~~ — **withdrawn 2026-04-27 v0.3**: VV-Plan has no §11 risk register; only SRS §11.9 exists. The audit doc invented a phantom reference. No action needed. | — | — | — | **Closed (invalid)** |
| A-GOV-04 | Track closure status of every `docs/security-audit-2026-04-18.md` finding with linked issue ID and date. **v0.4 update**: 0.9.9-dev (commit a0c5b3e "P1 security/correctness fixes from adversarial review") closed multiple findings: SEC-H6 (file-mode 0600→0644 invariant + ACL DACL walk), config.SetField column-0 match, path-traversal hardening on `deleteRemote*`, allowLoopbackWebhooks unexported, report-bug CWD, state.Open meta-write idempotency, emergency crash logs to safe paths. **v0.6 update**: panel-found bugs filed as GitHub issues SM-152..159 on 2026-04-29 (see §10.6 SM-NNN traceability table). Numbering note: pre-v0.5 CHANGELOG entries informally used "SM-155" / "SM-156" as internal tracking IDs for ISO-compliance work; those references are NOT the same as the GitHub issues filed under those numbers (SM-155 = alert_min_severity typo, SM-156 = BUG-R3-1 gitignore divergence). Going forward: SM-NNN unambiguously refers to a GitHub issue. Historical CHANGELOG references for ISO-tracking work that were never filed as bugs are not renumbered — they appear in their original entries with that limitation called out. | Winston | P1 | 2026-Q2 (in progress; SM-152..159 filed 2026-04-29) | **Partially closed; enumeration of SEC-* audit findings pending** |
| X-04 (raised) | Watcher coverage refactor for testability. **v0.4 update**: re-measured 2026-04-27 — actual coverage is **59.3%** (not 16.6% — VV-Plan §5.2 baseline was 9 days stale, see SM-155). Total internal/ coverage measured 66.6% (above v1.0 target 60%). The X-04 priority "P0" was anchored on the stale 16.6% figure. **Re-decision**: reduced from P0 to P2; v1.0 ships at 59.3% (within 0.7 points of the 60% target). Full refactor to ~75-80% (proper `fsnotifier` interface extraction, ~1 person-day) deferred to v1.0.1. Top-up tests (`isLinkToDir` + `WatchCount`, ~2 hours) to cross 60% optional for v1.0 — recommend scheduling if any spare cycles before 2026-05-01. | Amelia | **P2** (was P0) | v1.0.1 (full) / v1.0 optional top-up | **Mostly closed** |
| A-DOC-01 | Fix self-quality issues in this document | Paige | P2 | 2026-Q2 | **Resolved in v0.2 + v0.3** (phantom VV-Plan §11 removed; A-25010-01 closure recorded; release strategy documented) |

### 9.6 Action priority distribution (revised v0.2)

| Priority | Count | Notes |
|---|---|---|
| **P0** | 17 | A-29148-01, -05; A-25010-02; A-25023-02a..k (11); A-29119-01; X-04; (was 2 in v0.1) |
| **P1** | 21 | including A-GOV-01, -02, -04 |
| **P2** | 13 | |
| **P3** | 12 | |

Total: ~63 line items (post-split, post-additions). The compound count went from 39 → 63 because A-25023-02 split into 11 and 14 new actions were added per review findings.

### 9.7 Recommended sequencing (revised)

**Decision-only, by 2026-05-15**:
- A-25010-01 (restructure or deviate)
- A-25010-07 (Reusability close-out)
- A-29119-02 (Test Case Spec decision)
- A-GOV-02 (assign all calendar dates)

**v0.9 (foundation for v1.0 compliance)**:
- A-29148-08 / A-29119-04 (test naming)
- A-29148-09 / A-29119-05 (coverage matrix)
- A-29148-14 (V&V conflation fix)
- A-25023-06 (eliminate "Met at looser value")
- X-04 (watcher coverage)

**v1.0 release-gate**:
- All A-25023-02a..k (measurement execution)
- A-29119-01 (Test Strategy)
- A-29119-03 (Test Reports ritual)
- A-25023-01, A-25023-04 (measurement-function table)
- A-29148-01, A-29148-05, A-29148-12, A-29148-17 (29148 P0/P1 attributes)
- A-25010-02, -04, -05, -06, -10, -12 (25010 P0/P1)
- A-GOV-01 (self-assessment label retention — closed by decision)

**Post-v1.0**:
- All P3 items
- M-25023-01

---

## 10. Maintenance & Re-audit Schedule

### 10.1 When to re-audit

- **Pre-release** (every x.y.0 tag): refresh §3 summary; close/re-prioritize actions
- **Quarterly** (independent of releases)
- **On standard revision**: when ISO publishes a new edition of any in-scope standard, evaluate adoption (`M-*` actions)
- **On scope change**: e.g., when `FR-ASP-05` (GUI) work begins → trigger `A-25010-11` Interaction Capability re-audit

### 10.2 Audit owners (revised v0.2 — accountability column added)

| Role-context | Persona | Responsibility | Accountable human |
|---|---|---|---|
| Lead | — | Approve actions, prioritize, sign baseline | **Raveh** |
| Requirements | John (PM) | 29148 sections, FR/NFR attribute audits | Raveh (as John) |
| Architecture & Quality | Winston (architect) | 25010 / 25023 sections, NFR alignment | Raveh (as Winston) |
| Documentation | Paige (tech-writer) | Document structure, glossary, change history | Raveh (as Paige) |
| Implementation | Amelia (dev) | 29119 test-process, measurement instrumentation | Raveh (as Amelia) |
| Verification reviewer | Edge-Case Hunter | Independent orthogonal-coverage challenge (verification) | Raveh (or external) |
| Adversarial reviewer | Adversarial Reviewer | Cynical compliance-claim challenge | Raveh (or external) |
| External auditor | — | Independent third-party review (P1 — A-GOV-01) | TBD |

(v0.1 incorrectly assigned reviewers to "Validation"; corrected v0.2.)

### 10.3 Document-control metadata

| Field | Value |
|---|---|
| Document path | `C:\SelectiveMirror\docs\iso-compliance.md` |
| Linked from | `SRS.md` §1.4 ✅ (added v0.2) ; `VV-Plan.md` §2 ✅ (added v0.2) |
| Status field | **SELF-ASSESSMENT** (see §1) |
| Change log | Maintained inline below |

### 10.4 Change log

| Version | Date | Author | Change |
|---|---|---|---|
| 0.1 | 2026-04-27 | Raveh / Claude | Initial baseline. Self-assessment audit. |
| 0.2 | 2026-04-27 | Raveh / Claude (Adversarial Reviewer + Edge-Case Hunter incorporated) | Revisions: reclassified 4 items v0.1-Partial → v0.2-Non-compliant (User docs §4.1, Approval §4.1, Change history §4.1, Usability §5.1, Org Test Strategy §7.1); split compound `A-25023-02` into 11 singular P0 actions; added 17 new actions; replaced "Owner = persona" with "Accountable = Raveh; Role-context = persona"; corrected §4.3 ConOps reference; removed §10.3 cross-link TODO; removed unverified "25023:2024 reissue" claim. |
| 0.3 | 2026-04-27 | Raveh / Claude | Decisions baselined: (β) ship v1.0 on 2026-05-01 with partial-compliance disclosure; (A-GOV-01) SELF-ASSESSMENT label retained for v1.0, external review committed for v1.0.1; (A-25010-01) Version B (deviation note) chosen — SRS §4.0 added; full restructure deferred to v1.1 as new action `A-25010-01b`; (X-04) deferred to v1.0.1 with NFR-TE-01 status disclosure; SM-153 fixed via per-NFR decisions for NFR-TB-01 / TB-02 / RU-01 / RU-03; SM-152 + SM-154 filed in BugTracker. Withdrew phantom A-GOV-03 / X-09 (VV-Plan has no §11). Added §10.5 external-reviewer reading list. |
| 0.4 | 2026-04-27 | Raveh / Claude | Parallel-session integration. Five commits (0.9.8..0.9.12-dev) landed after v0.3 baseline. Re-measured coverage: total internal/ is **66.6%** (was 35.8% baseline; ~65% claimed in VV-Plan); watcher is **59.3%** (NOT 16.6% as VV-Plan §5.2 still says). X-04 reclassified from P0 to P2; ~~deferred to v1.0.1~~ → mostly closed. Test Monitoring & Control (29119-2) improved from ⚠️ → ✅ via release.yml hardening. A-25010-04 Faultlessness has substantive evidence shipped (`internal/sync/liveness.go` multi-signal stall detection with measurable thresholds). A-25010-08 Analysability strengthened by new anomaly kinds (Sync:Stalled, Sync:LsJsonSlow). A-GOV-04 now records partial closure of security-audit findings. New action `A-29119-12` added (per-release VV-Plan §5.2 re-measurement ritual). New bugs SM-155 (VV-Plan stale per-package coverage) and SM-156 (CHANGELOG SEC-C2 / SM-152 misattribution) filed. Methodology validation: parallel session used multi-role BMad design review (architect / senior dev / adversarial / edge-case hunter) on production design `docs/rclone-stall-design-for-review.md` — same pattern as this audit. |
| 0.5 | 2026-04-29 | Raveh / Claude | **A-GOV-01 closed by decision**: external/independent ISO compliance review is not planned. The SELF-ASSESSMENT label is retained permanently as the project's compliance posture. §10.5 (external-reviewer reading list) removed; A-GOV-01 status changed from "in progress (deferred path)" to "closed (decision: self-assessment is final)". §1 wording updated to remove "external review required" framing. Panel-review work (commits 0.9.19..0.9.22-dev) closed BUG-1 case-only mirror name dedup; GAP-1..5 config-validation hardening (rclone_extra_flags denylist, rclone_config validation, overlap rejection, drive-root rejection, traversal-remote rejection); PF-A3 (SEC-H5) service-mode default-rejects symlink-to-file; GAP-7 state DB refuses forward-version; GAP-6 --config last-wins; PF-A8 async OnRecord callback. |
| 0.6 | 2026-04-29 | Raveh / Claude | **Multi-role compliance delta review** (`docs/iso-compliance-review-2026-04-29.md`) integrated. Recommendations α + β + θ accepted: (α) **A-GOV-01 reframed** from "Closed (decision)" to **"Non-Conformity by Choice"** — the honest classification given 29148:2018 §6.5 / §5.2.4 are structurally unfulfillable; (β) **§10.5 restored as reference** (with a clear "not a commitment" header) — the reading list documents what compliance evidence looks like, useful even when external review isn't planned; (θ) this v0.6 entry. Recommendation ζ executed: VV-Plan §5.2 re-measured against HEAD `1e8eae9` (project version 0.9.39-dev) — total internal/ is **66.4%** (was stale-baselined as 35.8%); per-package table revised. Recommendation ε executed: A-29119-12 promoted from "ritual" to CI gate — `.github/workflows/ci.yml` coverage gate raised 35% → 60% (v1.0 target floor) and a per-package coverage report step added. Recommendation γ partially executed: SEC-H6 regression (panel R4 finding "fresh config.yaml created with mode 0644") closed at commit `93273d1` (cmdaddmirror.go + heartbeat.txt mode 0644 → 0600). Recommendation η executed: 11 panel-review docs (Rounds 2-11) and 10 panel_findings_round*_test.go files committed at `1e8eae9` — previously untracked compliance evidence now under document control. Recommendation δ (NFR-AU-* / NFR-RS-* / NFR-PR-* authoring in SRS §4.6) **deferred to v1.0.1** as too-large for a single turn — engineering evidence acknowledged in CHANGELOG and audit row remains ⚠️ in §3.1 until SRS catches up. |

### 10.5 ISO compliance evidence reading list (reference — not a commitment)

External / independent ISO compliance review is **NOT planned** for SelectiveMirror (A-GOV-01 closed as Non-Conformity by Choice). This list is preserved as a reference for what compliance evidence *looks like* — useful when explaining the project's compliance posture to a customer doing vendor due-diligence, or when authoring contributions that could later be cited as evidence.

**Primary documents** (~3 hours to read in full):
1. `docs/iso-compliance.md` — this document
2. `docs/SRS.md` (~950 lines)
3. `docs/VV-Plan.md` (~720 lines)

**Supporting documents** (~1 hour to skim):
4. `CHANGELOG.md` — per-version traceability, finding closure references
5. `CLAUDE.md` — codebase orientation
6. `docs/security-audit-2026-04-18.md` — security findings register
7. `docs/iso-compliance-review-2026-04-29.md` — most-recent multi-role compliance delta
8. `docs/validation-report-2026-04-16.md` — historical validation artifact
9. `docs/user-manual.md`, `docs/installation-manual.md`, `docs/developer-manual.md`
10. `.github/workflows/ci.yml` and `release.yml` — Test Monitoring & Control evidence

**Sample artifacts** (~1 hour to spot-check):
11. ~5 source files from `internal/` (e.g., `watcher/watcher.go`, `sync/sync.go`, `state/state.go`)
12. ~5 test files (`*_test.go`) — verify SM-NNN markers link to BugTracker entries
13. `system-validation/PANEL-REVIEW-ROUND{2..11}-2026-04-{28,29}.md` — multi-role review evidence
14. `test/sla_smoke.ps1` — performance smoke harness
15. SRS §11.9 risk register

**ISO standards** (paywalled — reader must have access):
16. ISO/IEC/IEEE 29148:2018 — focus §5.2, §6.2, §6.4, §9.5.5, Annex A
17. ISO/IEC 25010:2023 — §5.3, §6
18. ISO/IEC 25023:2016 — §5.2, §6
19. ISO/IEC/IEEE 29119-1:2022 (vocabulary), -2:2022 (processes), -3:2021 (documentation), -4:2022 (techniques)

**Note on auditability**: per A-GOV-01, this project is a **first-party self-assessment**. None of the above is a substitute for an actual third-party audit. The list is published only so that someone reading the compliance documentation has clear pointers to the underlying evidence.

### 10.6 SM-NNN traceability — panel-review bugs to GitHub issues

Recorded 2026-04-29 as part of A-GOV-04 progress. Maps each panel-review finding (Rounds 2–11, plus the SEC-H6 regression) to its GitHub issue number, severity, and current status.

| SM-NNN | GH # | Severity | Title | Source | Status | Closing commit |
|---|---|---|---|---|---|---|
| SM-152 | #155 | minor | SEC-H6 regression: fresh config.yaml + heartbeat.txt mode 0644 | Round 4 + ISO compliance review §4 #1 | **CLOSED** | `93273d1` (0.9.38-dev) |
| SM-153 | #156 | critical | BUG-R4-1: concurrent addmirror destroys pre-existing seed mirror | Round 4 §3 | OPEN | — |
| SM-154 | #157 | major | FIND-R4-1: per-file hooks do not fire on batch sync | Round 4 §4 | OPEN | — |
| SM-155 | #158 | minor | alert_min_severity typo accepted by Validate() | Round 4 §6 | OPEN | — |
| SM-156 | #159 | minor | BUG-R3-1: gitignore parent-exclusion + child negation divergence | Round 3 + Round 11 reconfirm | OPEN (in release.yml allowlist) | — |
| SM-157 | #160 | minor | BUG-R5-1: anomaly.Rotate is never invoked | Round 5 + Round 9/10 endurance | OPEN | — |
| SM-158 | #161 | minor | NEW-R10-1: failed sync-now cycles produce zero anomaly files | Round 10 + Round 11 reconfirm | OPEN | — |
| SM-159 | #162 | cosmetic | R12-OBS: rclone 2.x classified as Full Compatibility | Round 7 rclone-reviewer #8; Round 12 confirm | OPEN | — |

**Going forward**: any new panel-found bug gets a fresh SM-NNN (continuing from 160 onwards) and is filed as a GitHub issue at the same time as the panel-review document is committed. The CHANGELOG closure entry references the SM-NNN, the GitHub issue references the closing commit, and the panel review references the SM-NNN. Three-way cross-link.
