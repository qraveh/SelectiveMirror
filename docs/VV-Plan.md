# Verification & Validation Plan

## SelectiveMirror — V&V per ISO/IEC/IEEE 29119 & ISO/IEC 25010

**Document Version**: 0.3
**Date**: 2026-04-06 (baseline); last status refresh 2026-04-18
**SRS Reference**: docs/SRS.md v1.0 (Baseline, status refreshed 2026-04-18)

---

## Table of Contents

1. [V&V Strategy](#1-vv-strategy)
2. [ISO Methodology](#2-iso-methodology)
3. [Static Analysis](#3-static-analysis)
4. [Code Coverage](#4-code-coverage)
5. [Current Coverage Analysis](#5-current-coverage-analysis)
6. [Test Plans by Feature](#6-test-plans-by-feature)
7. [Verification Coverage System](#7-verification-coverage-system)
8. [Random & Fuzz Testing](#8-random--fuzz-testing)
9. [Rules Already Coded in .claude](#9-rules-already-coded-in-claude)
10. [Implementation Plan](#10-implementation-plan)

---

## 1. V&V Strategy

### 1.1 Verification vs Validation

| Aspect | Verification | Validation |
|--------|-------------|-----------|
| Question | "Are we building the product right?" | "Are we building the right product?" |
| Method | Testing, static analysis, code review, coverage | User acceptance, integration tests, field testing |
| When | Every commit, CI pipeline | Per-release, user feedback cycles |
| Owner | Developer + CI | User + developer |

### 1.2 Test Tiers

| Tier | Scope | Trigger | Runtime | Coverage Target |
|------|-------|---------|---------|----------------|
| **T0: Static Analysis** | All source | Every commit (CI) | < 30s | All packages |
| **T1: Unit Tests** | Per-function, no I/O | Every commit (CI) | < 20s | > 80% statement coverage |
| **T2: Race Detection** | Concurrent code | Every commit (CI) | < 30s | All concurrent packages |
| **T3: Integration Tests** | Real watcher + local rclone | Pre-release | < 10 min | All user-facing commands |
| **T4: Stress Tests** | Multi-mirror, burst scenarios | Pre-release | < 15 min | Capacity and resilience |
| **T5: Fuzz Tests** | Config parsing, filter syntax, filenames | Nightly (planned) | < 30 min | Edge case discovery |
| **T6: Performance Benchmarks** | SLA validation | Per-release | < 5 min | All NFR-TB/RU/CA targets |
| **T7: E2E Backend Tests** | Real Google Drive / S3 | Manual, pre-release | < 30 min | Critical sync paths |

### 1.3 Test Pyramid

```
        /  T7: E2E  \           ← Few, slow, high-value
       / T4-T6: Stress \        ← Targeted, nightly
      / T3: Integration  \      ← Per-release
     / T2: Race Detection  \    ← Every commit
    / T1: Unit Tests (600+)  \  ← Every commit, fast
   / T0: Static Analysis (vet) \ ← Every commit, instant
  ──────────────────────────────
```

---

## 2. ISO Methodology

> **Compliance tracking**: gap analysis and remediation actions for ISO/IEC/IEEE 29119, ISO/IEC 25010, and ISO/IEC 25023 are maintained in `docs/iso-compliance.md`. This section defines how the standards are *applied*; the compliance doc records *how well*.

### 2.1 ISO/IEC/IEEE 29119 (Software Testing)

29119 defines test processes, documentation, and techniques. We apply:

| 29119 Part | Application |
|-----------|-------------|
| **Part 1: Concepts** | Test levels (unit, integration, system), test types (functional, non-functional) |
| **Part 2: Test Processes** | Test planning → design → execution → reporting. This document is the Test Plan. |
| **Part 3: Test Documentation** | Test plan (this doc), test design (per-feature tables), test reports (CI output + coverage) |
| **Part 4: Test Techniques** | Equivalence partitioning, boundary value analysis, decision table, state transition (applied per-feature below) |

### 2.2 ISO/IEC 25010 Quality Verification

Each 25010 quality characteristic maps to verification techniques:

| Quality Characteristic | Verification Technique | Tool |
|-----------------------|----------------------|------|
| Functional Suitability | Unit tests + integration tests | `go test`, PowerShell scripts |
| Performance Efficiency | Benchmarks + profiling | `go test -bench`, `pprof` |
| Compatibility | Co-existence tests (with AV, IDE, etc.) | Manual |
| Usability | Scenario walkthroughs | Manual (user feedback) |
| Reliability | Fault injection, stress tests | Stress scripts, panic injection |
| Security | Static analysis, credential scanning | `go vet`, `gosec`, `gitleaks` |
| Maintainability | Coverage metrics, cyclomatic complexity | `go test -cover`, `gocyclo` |
| Portability | Native-build + build tag verification (Windows-first; Linux/Darwin require native CGo toolchain since SM-148) | Native `go build` on target OS |

### 2.3 ISO/IEC 25023 Quality Measurement

Measurement functions for each NFR target:

| NFR | Measurement Function | Formula |
|-----|---------------------|---------|
| NFR-TB-01 (event latency) | Time behaviour - response time | p99(event_timestamp - queue_entry_timestamp) |
| NFR-TB-02 (sync latency) | Time behaviour - throughput time | p95(rclone_exit_timestamp - dequeue_timestamp) |
| NFR-RU-01 (memory idle) | Resource utilization - memory | max(RSS) during 5-min idle period |
| NFR-RU-03 (CPU idle) | Resource utilization - processor | avg(CPU%) during 5-min idle period |
| NFR-CA-01 (max mirrors) | Capacity - workload | max(mirrors) where latency < SLA |
| NFR-FT-01 (fault tolerance) | Maturity - failure rate | crashes / total_runtime_hours |

---

## 3. Static Analysis

### 3.1 Current State

| Tool | Scope | In CI | Status |
|------|-------|-------|--------|
| `go vet` | All packages | Yes | Active |
| `gofmt` | Formatting | Convention only | Not enforced in CI |
| `golangci-lint` | Multi-linter aggregator | No | Not configured |
| `gosec` | Security-focused analysis | No | Not configured |
| `staticcheck` | Bug detection, simplification | No | Not configured |
| `gocyclo` | Cyclomatic complexity | No | Not configured |

### 3.2 Recommended Static Analysis Pipeline

**Phase 1 (v0.5.0)**: Add `golangci-lint` with this configuration:

```yaml
# .golangci.yml
linters:
  enable:
    - govet           # Already in CI; subsume
    - staticcheck     # Bug detection, dead code, simplification
    - gosec           # Security: credential leaks, path traversal, SQL injection
    - errcheck        # Unchecked error returns
    - ineffassign     # Ineffectual assignments
    - gocritic        # Opinionated diagnostics
    - gocyclo         # Cyclomatic complexity (threshold: 15)
    - misspell        # Typos in comments and strings
    - unconvert       # Unnecessary type conversions

linters-settings:
  gocyclo:
    min-complexity: 15
  gosec:
    excludes:
      - G104          # Unhandled errors (covered by errcheck)

issues:
  exclude-dirs:
    - vendor
```

**Phase 2 (v0.6.0)**: Add `gitleaks` for credential scanning in CI.

### 3.3 Static Analysis Role in Workflow

```
Developer writes code
  → gofmt (editor auto-format)
  → go vet (editor on-save)
  → golangci-lint (pre-commit hook or CI)
  → gosec (CI)
  → gitleaks (CI, on PR)
  → Tests (CI)
  → Coverage report (CI)
  → Merge
```

Static analysis catches bugs that tests miss:
- **Unchecked errors** (`errcheck`) — a whole class of silent failures
- **SQL injection** (`gosec`) — relevant for state DB queries (SM-046)
- **Dead code** (`staticcheck`) — reduces maintenance surface
- **Complexity** (`gocyclo`) — flags functions that need refactoring

---

## 4. Code Coverage

### 4.1 Coverage Types in Go

| Coverage Type | Go Tool | What It Measures | Recommended |
|--------------|---------|-----------------|-------------|
| **Statement coverage** | `go test -cover` | % of statements executed | Yes — primary metric |
| **Function coverage** | `go tool cover -func` | Which functions have any coverage | Yes — gap detection |
| **Branch coverage** | `go test -covermode=count` | How many times each statement runs | Yes — hot path identification |
| **Condition coverage** | Not built-in | Each boolean sub-expression | No — use decision tables in test design |
| **Integration coverage** | `go test -coverpkg=./...` | Coverage from integration tests against all packages | Yes — for T3+ tiers |

### 4.2 Coverage Targets

| Package | Current | v0.5.0 Target | v1.0 Target | Notes |
|---------|---------|---------------|-------------|-------|
| config | 86.9% | 90% | 95% | Close; missing edge cases in Load() |
| filter | 89.4% | 92% | 95% | Good; add gitignore conformance |
| lock | 85.0% | 90% | 90% | Platform-specific; limited by test env |
| logging | 81.8% | 85% | 90% | Rotation edge cases |
| metrics | 79.2% | 85% | 90% | WriteStatusFile paths |
| rclone | 52.3% | 70% | 80% | Detect functions need more path coverage |
| state | 85.2% | 90% | 95% | Critical data layer |
| sync | 72.9% | 80% | 90% | Largest package; most complex |
| telemetry | 75.4% | 80% | 85% | Network-dependent paths hard to test |
| watcher | 16.6% | 40% | 60% | Hardest to unit-test (OS integration) |
| cmd/smirror | 2.1% | 10% | 30% | CLI glue; covered by integration tests |
| **TOTAL** | **35.8%** | **55%** | **70%** | Weighted by package size |

### 4.3 Coverage Tools & Commands

```bash
# Statement coverage with profile
go test ./internal/... ./cmd/... -coverprofile=coverage.out -covermode=count

# Per-function coverage report
go tool cover -func=coverage.out

# HTML visualization (opens browser)
go tool cover -html=coverage.out -o coverage.html

# Cross-package coverage (integration tests covering internal packages)
go test ./test/... -coverpkg=./internal/... -coverprofile=integration-coverage.out

# Merge coverage profiles (for aggregated reporting)
# Use: go install golang.org/x/tools/cmd/cover@latest
# Then combine unit + integration profiles

# Coverage diff (CI gate: coverage must not decrease)
# Compare coverage.out against main branch baseline
```

### 4.4 Coverage Gap Analysis Methodology

For each package, identify:
1. **Uncovered functions** (0.0% coverage) — are they dead code or untestable?
2. **Partially covered functions** (< 80%) — which branches are missed?
3. **Covered but not asserted** — function runs but return values aren't checked
4. **Missing scenarios** — equivalence classes not represented in tests

---

## 5. Current Coverage Analysis

### 5.1 Summary

| Metric | Value (baseline v0.5.0 → current v0.9.x) |
|--------|------------------------------------------|
| Total statement coverage (internal/) | 35.8% → **66.0%** (re-measured 2026-04-29 against 0.9.53-dev; was 66.4% against 0.9.39-dev) |
| Total functions | 184 → grown with anomaly/hooks/telemetry/fsutil packages |
| Functions at 0% | 137 (74.5%) at baseline; reduced materially but watcher still has 8 of 27 functions at 0% (NewManager, Start, Stop, eventLoop, healthMonitor, isLinkToDir, removeRecursive, WatchCount) |
| Unit test count | 530+ → **650+** (656 top-level Test/Fuzz across 16 packages; 871 incl. subtests) |
| System-validation tests (panel review rounds 2-13) | new artifact class; ~140 tests across 12 round files (+ Round 12 fuzz / rclone matrix, Round 13 cloud-backend) |
| Integration tests | 66 + 11 stress → 123 integration cases |
| Fuzz tests | 2 targets (filter, config); 30s × 2 targets, 18M+ execs clean |
| Test code lines | 6,798 (Go) + 1,150 (PowerShell) at baseline; grown with panel-review tests |

### 5.2 Per-Package Analysis

**Last re-measured**: 2026-04-29 against `master` HEAD `1e8eae9` (project version 0.9.39-dev). Action `A-29119-12` (per-release re-measurement) executed; release.yml will gate on this going forward.

| Package | Coverage | Δ vs prior baseline | Notes |
|---|---|---|---|
| anomaly | 72.8% | -0.3 | rotation paths exercised; FileWriter test gaps |
| config | **78.0%** | -8.9 from stale 86.9% | Test count grew faster than coverage; new validators (denylist, overlap, traversal-remote) added with their own tests but Validate's combinatoric paths are larger |
| filter | **78.7%** | -10.7 from stale 89.4% | Same drift — GenerateRcloneFilterFile error paths still untested |
| fsutil | 0.0% | new package | IsReparsePoint helper; trivial; Windows-only |
| hooks | 76.6% | -8.6 from 85.2% | **Regression**: PF-A5 Job Object code is Windows-only and not exercised on dev box; the Run path was also restructured (Start+Wait instead of CombinedOutput) |
| lock | 54.2% | -30.8 from stale 85.0% | New code paths (GAP-9 stale-PID detection, isProcessAlive) untested; need a multi-process integration test |
| logging | 81.8% | 0.0 | unchanged |
| metrics | 73.6% | -5.6 from 79.2% | New comment-only changes (RecordError SEC-L4 documentation) didn't add tests |
| notify | 63.2% | not previously measured | webhook code well-covered; notifier.New paths thinner |
| rclone | **34.1%** | -18.2 from stale 52.3% | Detect() paths still need mocks; this is the lowest-coverage real package |
| state | 75.5% | -9.7 from stale 85.2% | New GAP-7 forward-version refusal + GAP-8 fresh-open hint paths exercised; older meta paths thinner |
| sync | 70.1% | -2.8 from 72.9% | Stall supervisor + isUnsafeRelPath + RcloneInvocation added with tests; bytes-trimming for lsjson PF-E3 not yet directly tested |
| task | 72.7% | not previously measured | Per-user Scheduled Task install/uninstall happy path |
| telemetry | 76.8% | +1.4 from 75.4% | SanitizeReport + tier work raised coverage |
| watcher | **59.5%** | +42.9 from stale 16.6% | The "critical gap" was based on a stale baseline (SM-155); actual coverage was already ~59%. 8 of 27 functions still at 0% (NewManager, Start, Stop, eventLoop, healthMonitor, isLinkToDir, removeRecursive, WatchCount); these need dependency-injection refactor (X-04, P2). |
| **Total internal/** | **66.4%** | +30.6 from stale 35.8% | v1.0 target was 60% — exceeded. |
| cmd/smirror | (not aggregated) | — | Glue code; coverage comes from system-validation suite + panel-review rounds, not unit tests. |

#### Per-package narratives (updated)

- **config (78.0%)**: BUG-1 case-only dedup + GAP-1..5 validators added with tests. Validate's combinatoric paths (e.g., overlap × multiple-mirrors × symlink × admin-owner) grow faster than test count.
- **filter (78.7%)**: PF-E1 cross-layer negation pinned (no assertion). GenerateRcloneFilterFile error paths still the dominant gap.
- **hooks (76.6%, regressed)**: Job Object code is Windows-only. Skipped on dev-box test run. Real run on a Windows CI runner would lift this back to ~85%.
- **lock (54.2%)**: GAP-9 stale-PID code (proc_windows.go / proc_other.go) is untested; it requires a fake-process harness or a multi-process integration test.
- **rclone (34.1%, the worst)**: Detect() paths still require either a real rclone or an interface mock. Lowest-leverage to improve via unit tests; system-validation rounds 2/3/6 cover the runtime behavior.
- **sync (70.1%)**: Stall supervisor (Layer 2) added with a substantial test suite; isUnsafeRelPath table-tested; Live ListRemote PF-E3 truncation guard not yet directly unit-tested.
- **watcher (59.5%, formerly "critical")**: Reclassified P0→P2 in iso-compliance v0.4 because the original "16.6%" baseline was stale. Currently above v1.0 60% target floor for total internal/. 8 functions remain at 0%; X-04 (fsnotifier interface refactor) is the only path to >75%.

#### Newly-introduced 0% functions worth mentioning

- `internal/sync/proc_windows.go::readProcessIoCounters` — exercised by stall supervisor integration tests but not directly unit-tested. Falls under PF-E2-like "exercised at higher level".
- `internal/lock/proc_windows.go::isProcessAlive` — see lock package note.
- `internal/fsutil/IsReparsePoint` — trivial Windows attr check; tested transitively via watcher tests that exercise reparse-point rejection.

### 5.3 Critical Gaps Summary

| Priority | Gap | Impact | Effort |
|----------|-----|--------|--------|
| ~~**P0**~~ | ~~watcher package at 16.6%~~ | **Closed by re-measurement**: actual coverage was 59.3% at v0.4 baseline (stale data), 59.5% now. Reclassified P2 (X-04). | ~~Large~~ |
| ~~**P0**~~ | ~~137 functions at 0%~~ | **Substantially reduced**: anomaly + hooks + state + sync + watcher refactors brought 0%-functions count down meaningfully. Re-counting deferred until next re-measurement. | Medium |
| **P1** | No gitignore conformance suite | Filter correctness is a differentiator; PF-E1 cross-layer behavior pinned but full conformance not tested | Medium |
| **P1** | No fuzz testing for config/filter/filenames | Edge cases discovered by SM-036/041/046 suggest more lurk | Medium |
| **P1** | No performance benchmarks | SLA targets exist but aren't measured | Medium |
| **P2** | rclone package at 34.1% | Detect() paths require interface mocks | Medium |
| **P2** | watcher 8 functions at 0% (NewManager, Start, eventLoop, etc.) | Requires X-04 fsnotifier-interface refactor | Large |
| **P2** | Integration test coverage not aggregated | cmd/smirror coverage comes from system-validation, not unit tests | Small |
| ~~**P2**~~ | ~~No coverage gate in CI~~ | **Resolved**: CI has 35% coverage gate since v0.5.0 (ci.yml) | ~~Small~~ |
| **P3** | No mutation testing | High coverage doesn't guarantee test quality | Large |

---

## 6. Test Plans by Feature

### 6.1 Test Plan Template

Each feature test plan follows ISO/IEC/IEEE 29119 Part 4 test techniques:

- **EP**: Equivalence Partitioning (group inputs into classes)
- **BVA**: Boundary Value Analysis (test at boundaries)
- **DT**: Decision Table (combinatorial conditions)
- **ST**: State Transition (state machine testing)

### 6.2 Implemented Features

#### FR-WATCH: File Watching

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-WATCH-01 | EP | File create in watched dir | Event queued | Pass | — |
| T-WATCH-02 | EP | File modify in watched dir | Event queued | Pass | — |
| T-WATCH-03 | EP | File delete in watched dir | Delete event queued with priority | Pass | — |
| T-WATCH-04 | EP | File rename in watched dir | Remove old + create new | Pass | — |
| T-WATCH-05 | EP | New subdirectory created | Auto-add to watch | Pass | — |
| T-WATCH-06 | EP | Subdirectory deleted | Remove from watch, no crash | Pass | — |
| T-WATCH-07 | EP | Symlink-to-directory | Reject (prevent escape) | Pass | SM-041 |
| T-WATCH-08 | EP | Symlink-to-file (in tree) | Sync target content at symlink path | Pass | — |
| T-WATCH-09 | EP | Symlink-to-file (outside tree) | Sync on startup; no real-time watch | Not tested | — |
| T-WATCH-10 | BVA | Burst delete (50+ files in window) | Accelerated reconciliation triggered | Pass | SM-050 |
| T-WATCH-11 | EP | Event on excluded file | No event queued | Pass | — |
| T-WATCH-12 | EP | Multiple mirrors simultaneously | Events routed to correct mirror | Pass | — |
| T-WATCH-13 | ST | Watcher goroutine panic | safeGo catches, logs, continues | Pass | — |
| T-WATCH-14 | BVA | Max path length (260 chars) | Handled or logged | Not tested | — |
| T-WATCH-15 | EP | Renamed directory with children | All children re-queued | Not tested | — |
| T-WATCH-16 | EP | Case-only rename (File.txt → file.txt) | Handled correctly on case-insensitive FS | Not tested | — |

**Coverage**: 10/16 scenarios tested. Gaps: symlink edge cases, path limits, case-sensitivity.

#### FR-FILTER: Filtering

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-FILTER-01 | EP | Simple exclude (`*.log`) | File excluded | Pass | — |
| T-FILTER-02 | EP | Directory exclude (`node_modules/`) | Dir and contents excluded | Pass | — |
| T-FILTER-03 | EP | Negation (`!important.log`) | File re-included | Pass | — |
| T-FILTER-04 | DT | Global + project rules merged | Last-match-wins | Pass | SM-036 |
| T-FILTER-05 | EP | Double-star pattern (`**/foo`) | Matches at any depth | Pass | — |
| T-FILTER-06 | ST | .syncignore file modified | Hot-reload, new generation | Pass | SM-044 |
| T-FILTER-07 | EP | Stale sync task after reload | Task skipped (gen mismatch) | Pass | SM-044 |
| T-FILTER-08 | EP | rclone filter file generation | Valid rclone filter syntax | Pass | SM-037 |
| T-FILTER-09 | EP | Empty .syncignore | All files included | Pass | — |
| T-FILTER-10 | BVA | .syncignore with trailing whitespace | Trimmed | Not tested | — |
| T-FILTER-11 | EP | Pattern with character class `[abc]` | Matches correctly | Not tested | — |
| T-FILTER-12 | EP | Escaped hash `\#comment` | Treated as pattern, not comment | Not tested | — |
| T-FILTER-13 | BVA | .syncignore with 10,000 rules | Loads within 1s | Not tested | — |
| T-FILTER-14 | EP | Malformed .syncignore (FR-FILTER-11) | Reject, keep last-known-good | Pass (v0.5.0) | — |
| T-FILTER-15 | EP | Unicode pattern matching | Correct match | Not tested | — |

**Coverage**: 9/15 scenarios tested. Gaps: gitignore edge cases, performance, error handling.

#### FR-SYNC: Synchronization

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-SYNC-01 | EP | Single file sync (happy path) | rclone copyto success, state updated | Pass | — |
| T-SYNC-02 | EP | Batch sync on startup | rclone copy per mirror | Pass | — |
| T-SYNC-03 | ST | Quiescence check — file still changing | Sync deferred | Pass | — |
| T-SYNC-04 | EP | File exclusively locked | Sync skipped | Pass | — |
| T-SYNC-05 | BVA | File at exact max_file_size boundary | Skipped (≥ limit) | Pass | — |
| T-SYNC-06 | EP | File unchanged (same hash) | No upload (checksum match) | Pass | — |
| T-SYNC-07 | EP | Bandwidth limit applied | rclone --bwlimit flag set | Pass | — |
| T-SYNC-08 | EP | Reconciliation detects missed file | File synced | Pass | — |
| T-SYNC-09 | EP | sync-now command | All mirrors synced immediately | Pass | — |
| T-SYNC-10 | EP | dry-run command | Preview output, no rclone execution | Pass | — |
| T-SYNC-11 | EP | Sync failure recorded in state | Exit code + timestamp in DB | Pass | — |
| T-SYNC-12 | EP | Rename: delete old + sync new | Old remote deleted, new synced | Pass | — |
| T-SYNC-13 | EP | Mtime-only change (no content) | Re-sync (mtime is a signal) | Pass | — |
| T-SYNC-14 | EP | Adaptive cooldown (FR-SYNC-13) | Cooldown = f(frequency, duration) | Pass | — |
| T-SYNC-15 | EP | Adaptive reconciliation (FR-SYNC-09) | Interval extends when stable | Pass | — |
| T-SYNC-16 | EP | Transient failure retry (FR-SYNC-16) | Retry once before circuit breaker | Pass (v0.5.0 — rclone exit 1/5) | — |
| T-SYNC-17 | EP | Per-mirror rclone flags (FR-SYNC-14) | Flags passed to rclone | Pass | — |
| T-SYNC-18 | BVA | Zero-byte file sync | Synced correctly | Pass | — |
| T-SYNC-19 | BVA | File with special chars in name | Synced correctly | Pass | — |
| T-SYNC-20 | EP | rclone timeout (process hangs) | Killed after timeout, failure recorded | Not tested | — |

**Coverage**: 13/20 scenarios tested. Gaps: new features + edge cases.

#### FR-DEL: Delete Handling

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-DEL-01 | DT | delete_policy=ignore, file deleted | Remote preserved | Pass | — |
| T-DEL-02 | DT | delete_policy=delete (default), file deleted | Remote deleted | Pass | — |
| T-DEL-03 | DT | delete_policy=quarantine, file deleted | Remote moved to .quarantine/ | Pass | — |
| T-DEL-04 | EP | Delete event priority | Enqueued at head | Pass | — |
| T-DEL-05 | EP | Directory deleted (all children) | `rclone purge` on `delete`, per-file move on `quarantine` | Pass | — |
| T-DEL-06 | EP | Rename cleanup (force delete) | Old remote path deleted | Pass | — |
| T-DEL-07 | EP | Delete file never synced | No remote action (not in state) | Pass | — |
| T-DEL-08 | EP | Atomic directory purge (FR-DEL-07) | rclone purge instead of per-file | Pass (v0.5.0) | — |
| T-DEL-09 | EP | Quarantine auto-purge (FR-DEL-09) | Files > retention deleted | Pass (v0.5.0) | — |
| T-DEL-10 | BVA | Delete 10,000 files in one directory | Completes without timeout/OOM | Not tested | — |

#### FR-GHOST: Ghost Cleanup

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-GHOST-01 | EP | Orphan detected | Classified as ORPHAN | Pass | — |
| T-GHOST-02 | EP | Leak detected | Classified as LEAK | Pass | SM-053/055 |
| T-GHOST-03 | EP | sync-now cleans ghosts | Ghosts deleted after sync | Pass | — |
| T-GHOST-04 | EP | dry-run shows ghost preview | Listed but not deleted | Pass | — |
| T-GHOST-05 | EP | Quarantined files spared | .quarantine/ not touched | Pass | — |
| T-GHOST-06 | EP | Ghost scan during reconciliation | Ghosts detected, reported, not deleted | Pass | SM-054 |
| T-GHOST-07 | BVA | 1,000 ghosts on remote | All detected within timeout | Not tested | — |

#### FR-QUEUE: FairQueue

| Test ID | Technique | Scenario | Expected | Status | SM |
|---------|-----------|----------|----------|--------|-----|
| T-QUEUE-01 | EP | Duplicate event for same file | Old entry removed, new at tail | Pass | SM-058 |
| T-QUEUE-02 | EP | Hot file cycles to back | Other files advance | Pass | — |
| T-QUEUE-03 | EP | Delete event priority | Enqueued at head | Pass | — |
| T-QUEUE-04 | ST | Cooldown in effect | File skipped during cooldown | Pass | — |
| T-QUEUE-05 | ST | Circuit breaker: 3 failures | Exponential backoff begins | Pass | SM-059 |
| T-QUEUE-06 | EP | Circuit breaker: delete bypasses | Delete dequeued despite backoff | Pass | — |
| T-QUEUE-07 | ST | Circuit breaker: success resets | Counter zeroed, backoff cleared | Pass | SM-060 |
| T-QUEUE-08 | BVA | Queue at max capacity | Warning logged, reconciliation triggered | Pass (v0.5.0 — unbounded w/ 50K warning) | — |
| T-QUEUE-09 | EP | Full-project sync skips cooldown | Dequeued immediately | Pass | — |
| T-QUEUE-10 | BVA | Empty queue dequeue blocks | Blocks until item or context cancel | Pass | — |

### 6.3 Shipped-Feature Extended Test Plans

#### FR-ANOM: Anomaly Detection (shipped v0.6.0; pattern detection FR-ANOM-06 still pending)

| Test ID | Technique | Scenario | Expected | Req |
|---------|-----------|----------|----------|-----|
| T-ANOM-01 | EP | Orphan ghost detected | Anomaly recorded with Ghost:Orphan category | FR-ANOM-01/02 |
| T-ANOM-02 | EP | Circuit breaker activated | Anomaly recorded with context snapshot | FR-ANOM-02/03 |
| T-ANOM-03 | EP | Panic recovered in safeGo | Anomaly recorded with stack trace | FR-ANOM-02/03 |
| T-ANOM-04 | EP | State DB integrity check fails | Critical anomaly recorded | FR-ANOM-02 |
| T-ANOM-05 | EP | Same file fails 4 times | SyncFailure:Repeated pattern detected | FR-ANOM-06 |
| T-ANOM-06 | EP | Ghost count trending upward | Pattern anomaly generated | FR-ANOM-06 |
| T-ANOM-07 | EP | Anomaly report sanitization | No paths, no credentials in output | FR-ANOM-08 |
| T-ANOM-08 | DT | Outbound enabled + internet available | Report sent | FR-ANOM-11 |
| T-ANOM-09 | DT | Outbound disabled | Zero outbound traffic | FR-ANOM-11 |
| T-ANOM-10 | BVA | 1,000 anomalies in 24 hours | Report rotation triggers (size limit) | FR-ANOM-10 |
| T-ANOM-11 | EP | Causal hypothesis for orphan | "Was file renamed? Recently excluded?" | FR-ANOM-05 |
| T-ANOM-12 | EP | Anomaly summary in status output | Summary visible in `status` command | FR-ANOM-07 |
| T-ANOM-13 | EP | Reconciliation stale (2x interval) | Anomaly generated | FR-ANOM-02 |

#### FR-SYNC-13: Adaptive Cooldown (shipped v0.5.0)

| Test ID | Technique | Scenario | Expected | Req |
|---------|-----------|----------|----------|-----|
| T-COOL-01 | EP | Single event, no history | Minimal cooldown (base) | FR-SYNC-13 |
| T-COOL-02 | EP | 10 events in 60s (hot file) | Extended cooldown | FR-SYNC-13 |
| T-COOL-03 | EP | Large file (100 MB, 45s sync) | Cooldown ≥ 67s (1.5x sync duration) | FR-SYNC-13 |
| T-COOL-04 | EP | Small file (1 KB, 1s sync), low frequency | Cooldown ≈ base | FR-SYNC-13 |
| T-COOL-05 | BVA | File stops being hot (no events for 5 min) | Cooldown decays to base | FR-SYNC-13 |
| T-COOL-06 | EP | Full-project sync bypasses cooldown | Always dequeued | FR-SYNC-13 |
| T-COOL-07 | EP | Delete bypasses cooldown | Always dequeued | FR-SYNC-13 |
| T-COOL-08 | ST | File transitions: cold → hot → cold | Cooldown follows transitions | FR-SYNC-13 |

#### FR-ASP-17: Hook System (shipped v0.7.0; SEC-C5 hardening v0.8.x)

| Test ID | Technique | Scenario | Expected | Req |
|---------|-----------|----------|----------|-----|
| T-HOOK-01 | EP | Pre-sync hook succeeds | Sync proceeds | FR-ASP-17 |
| T-HOOK-02 | EP | Pre-sync hook fails (non-zero exit) | Sync skipped, error logged | FR-ASP-17 |
| T-HOOK-03 | EP | Post-sync hook on success | Hook invoked with file path and status | FR-ASP-17 |
| T-HOOK-04 | EP | Post-sync hook on failure | Hook invoked with error details | FR-ASP-17 |
| T-HOOK-05 | BVA | Hook exceeds timeout (30s default) | Killed, sync proceeds | FR-ASP-17 |
| T-HOOK-06 | EP | Hook receives environment variables | FILE, MIRROR, ACTION, STATUS available | FR-ASP-17 |
| T-HOOK-07 | EP | No hook configured | No overhead, no error | FR-ASP-17 |
| T-HOOK-08 | EP | Hook with special chars in file path | Path properly escaped/quoted | FR-ASP-17 |
| T-HOOK-09 | ST | Multiple hooks chained | Execute in order, stop on failure | FR-ASP-17 |
| T-HOOK-10 | EP | Hook inherits no credentials | No rclone config/tokens in env | FR-ASP-17 |

---

## 7. Verification Coverage System

### 7.1 Coverage Tracking Matrix

A machine-readable traceability matrix linking requirements → test cases → test results:

```
Requirement ID | Test IDs        | Tested? | Pass? | Last Run    | Coverage%
FR-WATCH-01    | T-WATCH-01..04  | Yes     | Pass  | 2026-04-01  | 100%
FR-WATCH-05    | T-WATCH-07      | Yes     | Pass  | 2026-04-01  | 100%
FR-WATCH-10    | —               | No      | —     | —           | 0% (Phase 3)
FR-WATCH-11    | —               | No      | —     | —           | 0% (Not impl)
FR-FILTER-11   | T-FILTER-14     | No      | —     | —           | 0% (Not impl)
FR-SYNC-13     | T-COOL-01..08   | No      | —     | —           | 0% (Not impl)
...
```

### 7.2 Automated Coverage Tracking

**Proposal**: Generate the coverage matrix automatically from test output + SRS requirement IDs.

Implementation:
1. **Test naming convention**: Test functions include requirement ID in name.
   - `TestFR_WATCH_01_FileCreate`
   - `TestFR_SYNC_13_AdaptiveCooldown_HotFile`

2. **Coverage report script**: Parse `go test -v` output, match test names to requirement IDs, produce JSON matrix.

3. **CI integration**: On each run, generate `coverage-matrix.json`. Compare against SRS requirement list. Report:
   - Requirements with zero test coverage
   - Requirements with failing tests
   - New requirements since last run (from SRS diff)

4. **Dashboard** (future): `status.json`-like file showing V&V status per requirement.

### 7.3 Metrics to Track

| Metric | Formula | Target |
|--------|---------|--------|
| **Requirement coverage** | requirements_with_tests / total_requirements | > 90% for v1.0 |
| **Test pass rate** | passing_tests / total_tests | 100% (zero tolerance) |
| **Statement coverage** | covered_statements / total_statements | > 70% for v1.0 |
| **Function coverage** | covered_functions / total_functions | > 80% for v1.0 |
| **Regression test ratio** | SM-xxx_tests / total_bugs_found | 100% (every bug gets a test) |
| **Integration test coverage** | commands_tested / total_commands | 100% for v1.0 |

---

## 8. Random & Fuzz Testing

### 8.1 Where Random Testing Adds Value

| Area | Why | Technique |
|------|-----|-----------|
| **Config parsing** | YAML is complex; malformed input could crash or expose defaults | Go native fuzzing (`go test -fuzz`) |
| **Filter patterns** | gitignore syntax has many edge cases; combinatorial explosion | Fuzz with random patterns + random filenames |
| **Filename handling** | Cross-FS validity is a combinatorial problem | Fuzz with Unicode, control chars, reserved names |
| **State DB queries** | SQL injection via file paths (SM-046 was exactly this) | Fuzz with SQL-special chars in filenames |
| **rclone argument construction** | Shell injection if paths aren't properly escaped | Fuzz with shell metacharacters in paths |

### 8.2 Go Native Fuzzing

Go 1.18+ has built-in fuzzing. SelectiveMirror should use it for:

```go
// Example: fuzz filter pattern matching
func FuzzFilterIsExcluded(f *testing.F) {
    // Seed corpus
    f.Add("*.log", "test.log")
    f.Add("!important.log", "important.log")
    f.Add("**/deep/path", "a/b/deep/path/file.txt")

    f.Fuzz(func(t *testing.T, pattern string, path string) {
        fe, err := filter.New([]string{pattern}, "", nil)
        if err != nil {
            return // invalid pattern is OK
        }
        // Must not panic
        _ = fe.IsExcluded(path)
    })
}

// Example: fuzz config parsing
func FuzzConfigLoad(f *testing.F) {
    f.Add([]byte("mirrors:\n  - name: test\n    local_path: /tmp\n    remote: local:/tmp"))

    f.Fuzz(func(t *testing.T, data []byte) {
        // Must not panic on any input
        _, _ = config.LoadFromBytes(data)
    })
}

// Example: fuzz filename safety
func FuzzFilenameSafety(f *testing.F) {
    f.Add("normal.txt")
    f.Add("file with spaces.doc")
    f.Add("日本語ファイル.txt")
    f.Add("CON.txt")  // Windows reserved

    f.Fuzz(func(t *testing.T, name string) {
        // Build rclone command - must not contain shell injection
        args := sync.BuildRcloneArgs("remote:dest", name)
        for _, arg := range args {
            if strings.ContainsAny(arg, "|;&$`") {
                t.Errorf("shell metachar in rclone arg: %q", arg)
            }
        }
    })
}
```

### 8.3 Where Random Testing is NOT Needed

- **State transitions** — finite, enumerable (use state transition testing instead)
- **CLI argument parsing** — well-defined grammar (use equivalence partitioning)
- **Service lifecycle** — deterministic (use scenario testing)

### 8.4 Implementation Plan

| Phase | Fuzz Targets | Effort |
|-------|-------------|--------|
| v0.5.0 | Config parsing, filter patterns | Small |
| v0.6.0 | Filename safety, state DB queries | Small |
| v1.0 | rclone argument construction, full YAML corpus | Small |

---

## 9. Rules Already Coded in .claude

### 9.1 Development Principles in CLAUDE.md

| Rule | Location | V&V Relevance |
|------|----------|---------------|
| "530+ tests across 14 packages" | CLAUDE.md: Testing | Test count baseline; regression gate |
| "Run all unit tests" command | CLAUDE.md: Testing | T1 execution procedure |
| "Run integration tests" command | CLAUDE.md: Testing | T3 execution procedure |
| Test tiers (Unit/Local/Backend) | CLAUDE.md: Testing | Test tier definitions |
| "rclone copy not sync" | CLAUDE.md: Design Decisions | Architectural constraint; tests must verify this |
| "Single rclone per backend" | CLAUDE.md: Design Decisions | Concurrency constraint |
| "Quiescence before sync" | CLAUDE.md: Design Decisions | FR-SYNC-03 rationale |
| "Filesystem-agnostic filename handling" | CLAUDE.md: Design Decisions | NFR-AD-02; cross-FS test requirement |

### 9.2 User-Level Feedback Rules (Memory)

| Rule | Source | V&V Impact |
|------|--------|-----------|
| **Exhaustive testing before moving forward** | feedback_working_style.md | Never ship without comprehensive tests; T1+T3 must pass |
| **Bug variant brainstorming (MANDATORY)** | feedback_working_style.md | When a bug is found, test ALL special characters, invalid inputs, cross-platform variants, boundary conditions |
| **Orphan analysis before deletion (MANDATORY)** | feedback_working_style.md | Never delete orphans without root-cause analysis (directly feeds FR-ANOM-05) |
| **Bug review process (MANDATORY)** | feedback_bug_review.md | File → present → explore flavors → reproduce → discuss fix → then implement. No fix without review. |
| **Observable facts over assumptions** | feedback_working_style.md | Prefer signals over hardcoded values; applies to cooldown design (FR-SYNC-13) |
| **Commit cadence (MANDATORY)** | policy_versioning.md | Every architecturally distinct change committed separately |
| **Release gating (MANDATORY)** | policy_versioning.md | Setting version to x.y.0 requires explicit user permission |

### 9.3 CI Workflow Rules (.github/workflows/ci.yml)

| Step | Gate | Failure Action |
|------|------|---------------|
| `go build` | Must succeed | Block merge |
| `go vet` | Must succeed | Block merge |
| `go test ./internal/...` | All pass | Block merge |
| `go test -race` (5 packages) | No races | Block merge |
| `smirror.exe version` | Must run | Block merge |
| `smirror.exe test-mirrors` | Continue on error | Informational |

### 9.4 Gap: Rules NOT Yet Coded

| Missing Rule | Impact | Recommendation |
|-------------|--------|----------------|
| ~~No coverage gate in CI~~ | **Resolved**: 35% gate in ci.yml since v0.5.0 | — |
| No lint beyond `go vet` | Bugs caught only by review | Add `golangci-lint` to CI |
| No fuzz test schedule | Edge cases found by chance | Add nightly fuzz runs |
| No performance benchmark gate | SLA drift undetected | Add benchmark comparison in CI |
| No integration test in CI | Integration breaks caught late | Add local rclone integration to CI |
| Test naming convention | Can't auto-trace req → test | Adopt `TestFR_XXX_YY_Scenario` naming |

---

## 10. Implementation Plan (V&V)

### 10.1 v0.5.0 V&V Work

| Work Item | Effort | Impact |
|-----------|--------|--------|
| Add `golangci-lint` to CI with recommended config | Small | Catches bug classes unit tests miss |
| Add coverage reporting to CI (`go test -cover`) | Small | Visibility |
| ~~Add coverage gate~~ | ~~Small~~ | **Done**: 35% gate in ci.yml |
| Add fuzz tests: config parsing, filter patterns | Medium | Edge case discovery |
| Refactor watcher for testability (extract pure logic) | Large | Raise watcher from 16.6% to 40%+ |
| Adopt test naming convention (`TestFR_XXX_YY`) | Medium | Automated traceability |
| Add performance benchmarks (`go test -bench`) | Medium | SLA baseline measurement |
| Generate coverage-matrix.json in CI | Medium | Requirement-to-test traceability |

### 10.2 v0.6.0 V&V Work

| Work Item | Effort | Impact |
|-----------|--------|--------|
| Gitignore conformance test suite | Medium | FR-FILTER-01 verification |
| Fuzz tests: filenames, state DB queries | Small | Cross-FS safety |
| Integration test coverage aggregation (`-coverpkg`) | Small | True coverage picture |
| Add `gitleaks` to CI | Small | Credential leak prevention |
| Performance SLA validation suite | Medium | NFR-TB/RU/CA verification |

### 10.3 v1.0 V&V Work

| Work Item | Effort | Impact |
|-----------|--------|--------|
| 32-mirror load test | Medium | NFR-CA-01 validation |
| 100K-file stress test | Medium | NFR-CA-02 validation |
| Coverage target validation (70% total) | Small | Release gate |
| Security audit (manual + gosec review) | Medium | NFR-SEC validation |
| Full requirement coverage matrix review | Medium | 29119 compliance |
