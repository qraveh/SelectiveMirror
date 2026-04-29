# Required actions for SelectiveMirror development session

**To:** SelectiveMirror implementation session (`C:\SelectiveMirror`)
**From:** ISO compliance audit session (`C:\BMadClaude`)
**Date:** 2026-04-29 (late evening)
**Subject:** Required-actions list for v1.0.0 cut on 2026-05-01
**Anchor:** master @ `f0c3bd0` (0.9.63-dev) plus uncommitted BugTracker modifications to `SM-{152,153,154,155,156}.md`
**Standing authorization (recorded 2026-04-29 evening):** Raveh grants standing permission for end-of-code-changing-session version increments, **including bumps to x.y.0**. This relaxes the `qh-sw-developer` skill §1 rule *"Setting it to x.y.0 requires explicit user permission — never autonomously bump"* for the duration of the v1.0 release work. R-3 below is therefore pre-authorized.

Each action carries: **ID** · file/section · acceptance criterion · BMad role-context · effort.

---

## A. MUST do before v1.0 tag (blocking)

| ID | File/section | Acceptance | Owner | Effort |
|---|---|---|---|---|
| **R-1** | Commit the 5 BugTracker collision-cross-link edits to `C:\BugTracker\projects\SelectiveMirror\SM-{152,153,154,155,156}.md` | `cd C:\BugTracker && git status --short projects/SelectiveMirror/SM-15{2..6}.md` returns clean. | Paige | 5 min |
| **R-2** | Promote `CHANGELOG.md` `[Unreleased]` block to `[1.0.0] — 2026-05-01` | Section header rename; preserves all existing entries unchanged. | Paige | 5 min |
| **R-3** | `cmd/smirror/main.go::version` bump 0.9.63-dev → 1.0.0 in the same commit as R-2. **Pre-authorized** per the standing-authorization note above. | `grep "^var version" cmd/smirror/main.go` returns `"1.0.0"`. | Amelia | 5 min |
| **R-4** | Pre-tag CI verification: `go build ./...` + `go vet ./...` + `go test ./internal/... ./cmd/...` + `go test ./internal/... -cover` against the per-package 50% floor + waivers + 60% aggregate gate | All gates green. Per-package floor passes against current waivers (fsutil/service/rclone). | Amelia | 10 min |
| **R-5** | Pre-tag MSI smoke test: `installer/smoke-test.ps1` returns 16/16 invariants | Same as 0.9.26 smoke pass; no regressions from WiX 6 fix `bbb0d3c`. | Amelia | 10 min |
| **R-6** | Tighten `README.md` ISO disclosure language to align with v0.6 audit posture (replace "External independent review committed for v1.0.1" wording — currently contradicts v0.6 NCC framing) | README disclosure block uses the language proposed in the audit's prior §6: *"Self-assessment retained as deliberate Non-Conformity by Choice on ISO/IEC/IEEE 29148:2018 §5.2.4 / §6.5..."* | Paige | 10 min |
| **R-7** | Verify `release.yml $allowed = @()` empty array survives the tag | `grep "\$allowed = @()" .github/workflows/release.yml` returns the literal empty-array form. | Amelia | 1 min |

**Pre-tag MUST total: ~45 min of mechanical work plus the tag push itself.**

---

## B. SHOULD do before v1.0 tag (cheap, audit-quality wins)

| ID | File/section | Acceptance | Owner | Effort |
|---|---|---|---|---|
| **R-8** | Fix VV-Plan §1.1 V&V conflation (BugTracker SM-152 — the local-file content). Move "integration tests" from the Validation/Method column to the Verification/Method column. Mark BugTracker SM-152 status `open → fixed`. | `grep -A 5 "Validation" docs/VV-Plan.md` shows "User acceptance, field testing, beta feedback" under Validation and `grep "integration tests"` shows them under Verification. | John | 10 min |
| **R-9** | Refresh `docs/iso-compliance.md` lines 12-15 `Source documents audited` block to current revisions: `SRS.md` v1.1 and `VV-Plan.md` post-ζ re-measurement | Block reads the current versions; date stamp 2026-04-29. | Paige | 5 min |
| **R-10** | Add a row to `docs/iso-compliance.md` §10.4 Change log: *"v1.0 baseline (2026-05-01): Audit baselined for v1.0.0 release; all F-1..F-6 closed; A-GOV-01 NCC; NFR stubs landed in SRS §4.6.5/.6/.7; full measurement-function elaboration deferred to v1.0.1."* | New row in §10.4 table. | Paige | 5 min |
| **R-11** | Verify SM-152 BugTracker entry's `status: open` is updated post-R-8 to `status: fixed` with `fixed_at: 2026-04-29T...` and `fixed_in_version: 1.0.0` | YAML frontmatter reflects fix. | Paige | 2 min |

**Pre-tag SHOULD total: ~22 min. Recommended to bundle into a single "v1.0 tag prep" commit before R-3.**

---

## C. MUST declare-as-committed for v1.0.1 (post-tag deferrals to record before tag)

These should appear in the `[1.0.0]` section of `CHANGELOG.md` under a "Deferred to v1.0.1" subsection, OR in `docs/iso-compliance.md` §9 with explicit target dates. Either form is acceptable; the requirement is that they're written down before tag.

| ID | Description | Acceptance | Reference |
|---|---|---|---|
| **R-12** | NFR-PR-01 measurement-function elaboration: Cloudflare Worker access-log analysis; ratio of None-tier records / None-tier installs over the v1.0.0 release window; target = 0.000 | First measurement at v1.0.1 cut; included in v1.0.1 release notes | Audit prior message §4 |
| **R-13** | `internal/lock::isProcessAlive` multi-process test harness | Coverage rises above 50% without waiver | F-6 acknowledgment in commit `b0d7505` |
| **R-14** | `internal/rclone::Detect` interface extraction + mocks | Waiver removed; coverage rises above 50% | ci.yml waiver target |
| **R-15** | `internal/fsutil` direct unit tests | Waiver removed; coverage > 50% | ci.yml waiver target ("v1.0.x no-op fix") |
| **R-16** | `docs/test-strategy.md` single-page consolidating VV-Plan + PANEL-REVIEW + CLAUDE.md + run_tests.ps1 references | A-29119-01 closed; iso-compliance §3.1 29119 row promotable to ✅ | A-29119-01 |
| **R-17** | `docs/security-audit-2026-04-18.md` finding-closure cross-reference table populated for SEC-C1..C5, SEC-H2..H11, SEC-M1..M14, SEC-L1..L5 closed in v1.0.0 cycle | A-GOV-04 enumeration closed | A-GOV-04 |

---

## D. MUST declare-as-committed for v1.1 (post-tag deferrals to record before tag)

| ID | Description | Acceptance | Reference |
|---|---|---|---|
| **R-18** | Full ISO/IEC 25023 §5.2 measurement-function elaboration for NFR-AU-01..03 / NFR-RS-01..03 / NFR-PR-02..03 | Each NFR carries: purpose, method-of-application, type-of-measure (Base/Derived/Indicator), scale type, audience, measurement function | δ recommendation; A-25023-04 |
| **R-19** | A-25023-02a..k full measurement campaign (11 NFR-TB / NFR-RU / NFR-CA targets currently "Not Measured") | Each target has a recorded measurement value with date | A-25023-02a..k (originally P0; deferred) |
| **R-20** | A-25010-01b — restructure SRS §4 to ISO/IEC 25010:2023 layout (Flexibility as top-level; Adaptability/Installability/Replaceability/Scalability move there; full Authenticity/Resistance/Privacy elaboration in §4.6) | SRS §4.0 deviation note removed; full 2023 schema in place | A-25010-01b |
| **R-21** | SM-NNN single-source-of-truth migration (BugTracker ↔ GitHub Issues namespace reconciliation per A-GOV-04) | One canonical numbering stream; collision-acknowledgment block in §10.6 simplified to historical note | F-2 v1.1+ scope |
| **R-22** | Address remaining 29148:2018 §9.5.5 doc-attribute gaps: A-29148-02 (in-doc Change History) / A-29148-03 (named Approval/sign-off block) / A-29148-07 (ConOps doc) / A-29148-15 (Stakeholder list, Glossary, Distribution list, Doc Conventions) / A-29148-17 (User Documentation Requirements section) | iso-compliance §4.1 row count moves from 11/19 to 16/19+ | 29148 P1 backlog |

---

## E. Post-tag (audit chain closure)

| ID | Description | Owner |
|---|---|---|
| **R-23** | Forward the v1.0.0 tag commit + CHANGELOG `[1.0.0]` block to the audit chain for closure re-evaluation. The audit will re-measure aggregate + per-package coverage against the tag, verify CHANGELOG release-notes ISO disclosure language, and document any issues found between this hand-off and the tag. | Implementation session forwards; audit session executes |

---

## Discipline checks (per `qh-sw-developer` skill)

- **§1 Commit cadence**: R-1 stands alone (BugTracker repo). R-2 + R-3 + R-6 + R-8 + R-9 + R-10 + R-11 land in **one commit** per "version bump in the same commit as the code/doc it describes." That commit is `1.0.0: v1.0.0 release prep`. R-4 + R-5 are pre-commit verification, not commits themselves. R-7 is a check.
- **§5 Bug-before-fix**: R-8 fix is performed against existing SM-152 (no new file needed; status update only). No new bug needs filing.
- **§1 Release rule (RELAXED)**: Raveh's standing-authorization note above pre-authorizes the bump to `1.0.0`. R-3 may proceed without further sign-off. The relaxation applies for the duration of the v1.0 release work; should be re-tightened (or formalized in `qh-sw-developer` skill itself) post-v1.0 if Raveh wants the rule restored.
- **§13 End-of-answer taxonomy**: required at the end of the v1.0 prep commit if files changed.

---

## Decision matrix for the tag operator

| Section | Total effort | Skip impact |
|---|---|---|
| **A** (must) | ~45 min | Cannot tag |
| **B** (should) | ~22 min | Tag ships with mild doc inconsistency; fixable in v1.0.0.1 patch |
| **C** (declare for v1.0.1) | 0 min if just declared in CHANGELOG | Audit chain cannot close cleanly without the deferral declarations |
| **D** (declare for v1.1) | 0 min if just declared | Same |
| **E** (post-tag) | one message back to audit chain | Audit chain stays open indefinitely |

**Minimum viable v1.0 path**: A + R-12..R-22 declared as-text in CHANGELOG `[1.0.0]` Deferred section. ~50 min total.

---

## Standing authorization (record-of-decision)

Recorded 2026-04-29 evening from Raveh: *"I already authorize on the level of the rule to increment the version at the end of the code changing session."*

Interpretation:
- Scope: end-of-code-changing-session version increments.
- Includes: bumps to x.y.0 (the rule's previous explicit-permission requirement is relaxed).
- Duration: the duration of the v1.0 release work; not implicitly extended past v1.0 unless restated.
- Mechanism: standing pre-authorization, no per-bump confirmation needed.

Under this authorization, R-3 (`var version = "0.9.63-dev"` → `"1.0.0"`) is pre-cleared. Any subsequent x.y.0 bump (e.g., a hypothetical v1.1.0) within the v1.0 release work window is also pre-cleared. Bumps after the v1.0 release window should re-confirm authorization.

Recommendation: if Raveh wants this authorization to persist permanently, the cleanest path is to edit `~/.claude/skills/qh-sw-developer/SKILL.md` §1 to remove or qualify the "explicit user permission" language. That edit is out of scope for this required-actions list.

— ISO compliance audit session, 2026-04-29 evening
  source: response to dev session memo `docs/iso-audit-response-2026-04-29.md` (2026-04-29 evening) plus standing authorization received same turn
