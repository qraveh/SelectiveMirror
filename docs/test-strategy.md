# SelectiveMirror — Organizational Test Strategy

**Standard alignment**: ISO/IEC/IEEE 29119-1:2022 §6 (Organizational Test Process), §7 (Test Strategy).
**Project**: SelectiveMirror (single-developer Windows-first selective near-real-time file mirror).
**Owner**: Raveh.
**Status**: v1.0 baseline.
**Closes**: `A-29119-01` (`docs/iso-compliance.md` §7).
**Last reviewed**: 2026-05-03 (v1.0.0 release prep).

---

## 1. Purpose

A single-page, project-level Test Strategy that satisfies the 29119-1 §6 *Organizational Test Process* requirement by **pointing at** the distributed strategy already in force across the repo, rather than duplicating it. The 29119 family does not require strategy content to live in a single artifact — it requires that the strategy *exist*, *be authored*, *be referenced*, and *be subject to organizational change control*. This document provides that integration point.

This is a **discipline document**, not an aspirational one. Every claim below references a tracked artifact in this repository. If a claim cannot point at a real file or a real CI workflow, it is wrong and the claim must be deleted, not promised.

---

## 2. Scope and risk profile

**Product**: a Go binary (`smirror.exe`) that watches local directories and mirrors changes to any rclone-supported backend, plus its installer (WiX MSI), its CLI surface, and its opt-in telemetry pipeline (Cloudflare Worker → Supabase rollups).

**Risk profile**: **moderate**. SelectiveMirror is not safety-critical (29119 §A.2 *Safety*: not applicable per `docs/iso-compliance.md` §10.6 / A-25010-09). The dominant risks are:

| Risk class | Mitigation tier |
|---|---|
| **Data loss** (false-positive deletion of remote files) | **Tier 1**: integration tests + adversarial reviewer + delete-policy gate (`docs/SRS.md` FR-DEL-01 / -02 / -03) |
| **Privacy contract violation** (telemetry leak, sanitizer regression) | **Tier 1**: CLAIMS-MAP gate + per-claim test enforcement (`system-validation/CLAIMS-MAP.md`) |
| **Privilege escalation** (admin-context process accepts user-writable config) | **Tier 1**: SEC-* hardening series with regression tests (see `docs/security-audit-2026-04-18.md`) |
| **Sync correctness** (TOCTOU, symlink, race conditions) | **Tier 2**: race-detector CI gates + targeted goroutine probes |
| **Performance regression** (latency, throughput, memory) | **Tier 2**: SLA-smoke workflow + per-release re-measurement ritual |
| **Operational visibility** (anomaly classification, status accuracy) | **Tier 3**: panel-found regression suite + Tier-2 backlog items |

Tier mapping drives test-investment intensity: Tier-1 risks must have a CI-gated regression test before any change to the surrounding code lands; Tier-2 risks have CI-runnable tests but may temporarily allowlist with a tracked deferral; Tier-3 risks may be probed periodically rather than per-commit.

---

## 3. Distributed strategy — pointers, not duplications

The actual strategy lives across these artifacts. Each row names *what* the artifact covers and *where* it lives:

| Strategy element (29119-1 §7) | Artifact | Location |
|---|---|---|
| **Test plan** (V&V Plan) | `docs/VV-Plan.md` | versioned in repo |
| **Requirements baseline** | `docs/SRS.md` | versioned in repo |
| **Test design techniques** | `docs/VV-Plan.md` §3 (techniques per requirement class) | versioned in repo |
| **Test data requirements** | `docs/VV-Plan.md` §4 (per-test fixture description); `system-validation/` for adversarial inputs | versioned in repo |
| **Risk-based prioritization** | this document §2 (risk profile + tier mapping) + `docs/iso-compliance.md` §10.6 (open-action priority list) | this document + iso-compliance.md |
| **Tooling** | Go 1.26+ test framework, race detector, GoReleaser, WiX, golangci-lint, MSI smoke harness, panel-test runner | `.github/workflows/`, `test/run_tests.ps1`, `installer/smoke-test.ps1` |
| **Environment** | GitHub Actions (Linux + Windows), local Windows dev tree (the production target) | `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/workflows/sla-smoke.yml`, `.github/workflows/release-dryrun.yml`, `.github/workflows/telemetry-emulation.yml` |
| **Pass/fail criteria** | `docs/iso-compliance.md` §3.1 + per-NFR target columns in `docs/SRS.md` §4.6 | versioned in repo |
| **Test Completion Reporting** | per-release CHANGELOG `[X.Y.Z]` block (Deferred-to subsections + Known-issues subsections) + `system-validation/CLAIMS-MAP.md` for the telemetry feature | `CHANGELOG.md`, `system-validation/CLAIMS-MAP.md` |
| **Reviews and audits** | panel reviews (`system-validation/PANEL-REVIEW-*.md`), validation memos (`system-validation/MEMO-TO-IMPL-*.md`), security audit (`docs/security-audit-2026-04-18.md`), ISO compliance audit (`docs/iso-compliance.md`) | versioned in repo |
| **Change control** | per-commit version bump in `cmd/smirror/main.go::version` (per `CLAUDE.md` Versioning rule); BugTracker dual-numbering (R-21 v1.1) | `CLAUDE.md`, `C:\BugTracker\projects\SelectiveMirror\` |

---

## 4. Test levels (29119-1 §6.2.4)

| Level | Scope | Typical artifact |
|---|---|---|
| **Unit** | All packages under `internal/` and `cmd/`, no I/O | `internal/<pkg>/<pkg>_test.go` |
| **Integration** | Real watcher + local-rclone backend; cross-package interactions | `test/run_tests.ps1`; `system-validation/*_test.go` (build tag: `integration` or default) |
| **System** | Whole binary (`smirror.exe`) end-to-end including telemetry uplink | `system-validation/`; `scripts/telemetry-v2-smoke-test.py`; `.github/workflows/telemetry-emulation.yml` |
| **Acceptance** | Per-feature claim-by-claim verification; CLAIMS-MAP gate is the contemporary instance for telemetry | `system-validation/CLAIMS-MAP.md` (telemetry); per-PR panel-finding regression suite for non-telemetry features |
| **SLA / non-functional** | Latency, throughput, memory, integrity | `test/sla_smoke.ps1`; `.github/workflows/sla-smoke.yml` |

---

## 5. Test types

| Type | Where it runs | Frequency |
|---|---|---|
| Static analysis | `go vet`, `golangci-lint` (8 linters, gocyclo 50) | every push (CI) |
| Unit | `go test ./internal/... ./cmd/...` | every push (CI) |
| Race detection | `go test -race` on selected packages | every push (CI), full sweep on release-dryrun |
| Integration | local rclone backend | every push (CI) |
| MSI smoke | WiX-built MSI install/uninstall round-trip on a real Windows host | release-dryrun + R-5 pre-tag operator gate |
| SLA smoke | latency / throughput / memory / integrity targets | scheduled (sla-smoke workflow); pre-tag refresh if > 48h stale |
| Telemetry CLAIMS-MAP gate | every privacy / architecture claim in `docs/PRIVACY.md` and `docs/telemetry-architecture-v2.md` mapped to a test | per-claim freshness check; gate at ≥ 90% non-deferred GREEN before tag |
| Telemetry emulation harness | mass-emulation against the live Worker | per-PR + nightly via `.github/workflows/telemetry-emulation.yml` |
| Live-Worker fingerprint probe | real Worker `cf-ray` + SM Worker fingerprint check | nightly + per-tag |
| Panel-found regression suite | every Tier-1 panel finding gets a regression test | every push (CI), gated in `release.yml` |

---

## 6. Acceptance criteria for v1.0 (29119-1 §6.2.5)

A v1.0 release ships when **all** of:

1. CHANGELOG `[X.Y.Z]` block authored, including a `Bugs known at tag` subsection enumerating any deferred items (with their target version) and any non-GREEN CLAIMS-MAP claim with its deferral rationale.
2. Aggregate `internal/` statement coverage **≥ 60%**.
3. Per-package floor **≥ 50%** with explicit waivers in `.github/workflows/ci.yml`.
4. Telemetry CLAIMS-MAP non-deferred GREEN ratio **≥ 90%**.
5. Zero open Tier-1 panel findings against the about-to-tag commit.
6. release-dryrun.yml green within 24h of tag.
7. R-5 MSI smoke (operator-side, elevated PowerShell) passes against the about-to-tag commit.
8. `docs/release-maturity.md` refreshed to reflect tag-day state (no 🔴 in the targeted audience tier).

These are the hard gates. Soft items (CHANGELOG cleanup, dashboard wording, etc.) are listed in the per-release operator checklist (`docs/operations/release-runbook.md`).

---

## 7. Continuous improvement (29119-1 §6.5)

The CLAIMS-MAP validation gate at 24/28 GREEN (92.3% non-deferred) for the telemetry feature is the project's de-facto **Test Completion Report** for v1.0. Per-feature gates similar in shape to the telemetry-v2 CLAIMS-MAP will be authored for future major features as part of the project's release ritual.

The lessons from the 0.9.66-dev forward-revert episode (premature source bump → forward-revert recovery) have been captured in BugTracker SM-NNN closure notes and in the sm-keeper agent's Mode A/B/C definitions (`.claude/agents/sm-keeper.md`); a permanent lesson-learned doc lives in `~/.claude/skills/qh-sw-developer/` skill memory.

This Test Strategy will be reviewed at every MAJOR release and at any cycle where a new feature class introduces a new risk tier (e.g., the telemetry feature in v0.9.x triggered the addition of CLAIMS-MAP-style gating).

---

## 8. Non-conformities by choice

The following 29119 elements are knowingly not implemented:

- **Independent verification body** (29119-1 §6.5.4): single-developer project; not feasible. ISO/IEC/IEEE 29148 §5.2.4 / §6.5 *Independent External Review* is treated as *Non-Conformity by Choice* (A-GOV-01) — see `docs/iso-compliance.md` §6.
- **Formal Test Manager role** (29119-1 §6.4.2): owner-developer fills both roles; documented here, not delegated.
- **Procurement-level test reporting** (29119-1 §10): not applicable; the project is not consumed via procurement contracts.

These choices are recorded so a future audit pass can confirm they remain deliberate rather than accidental.

---

*End of Organizational Test Strategy.*
