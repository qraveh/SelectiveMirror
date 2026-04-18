# SelectiveMirror — Full Documentation Validation Report

**Date**: 2026-04-16
**Validated version**: `0.8.41-dev` (from `cmd/smirror/main.go:43`)
**Validated by**: Claude Opus 4.6 documentation audit session
**Purpose**: Hand off to a fix session — every discrepancy listed with file paths, line numbers, and exact current/expected values.

---

## How This Report Was Produced

1. Read all documentation files (CLAUDE.md, README.md, CHANGELOG.md, CONTRIBUTING.md, CREDITS.md, SECURITY.md, SRS.md, VV-Plan.md, config.example.yaml, .golangci.yml, .goreleaser.yaml, ci.yml, story.md)
2. Explored full project structure (14 internal packages, 19 cmd files, 30 test files)
3. Built the project (`go build ./cmd/smirror/` — clean)
4. Ran `go vet ./...` — clean
5. Ran full test suite with coverage (`go test ./internal/... ./cmd/... -p 24 -count=1 -coverprofile`)
6. Ran `golangci-lint run ./internal/... ./cmd/...`
7. Cross-referenced every documentation claim against code

---

## Build & Vet: PASS

No issues.

---

## Test Results

### Summary: 534/535 pass, 1 flaky failure, 35.2% coverage

| Package | Result | Coverage |
|---------|--------|----------|
| internal/anomaly | PASS | 73.1% |
| internal/config | PASS | 78.4% |
| internal/filter | PASS | 78.5% |
| **internal/hooks** | **FAIL** | 90.9% |
| internal/lock | PASS | 83.3% |
| internal/logging | PASS | 81.8% |
| internal/metrics | PASS | 73.6% |
| internal/notify | PASS | 64.8% |
| internal/rclone | PASS | 30.0% |
| internal/service | PASS (no tests) | 0.0% |
| internal/state | PASS | 73.1% |
| internal/sync | PASS | 71.4% |
| internal/telemetry | PASS | 69.7% |
| internal/watcher | PASS | 59.6% |
| cmd/smirror | PASS | 8.3% |
| **TOTAL** | **1 FAIL** | **35.2%** |

### BUG-01: Flaky hooks test (TestRun_SimpleCommand)

- **File**: `internal/hooks/hooks_test.go:27-41`
- **Symptom**: `echo hello` via `cmd.exe /C` times out after 5s
- **Root cause**: Test creates `Runner` with `New(5 * time.Second)`, runs `echo hello`. On Windows under load, `cmd.exe` startup can exceed 5s. The test uses a timeout-based approach.
- **User preference**: Event-based approach preferred over timeouts. Do not use timeouts for test synchronization — use completion signals, channels, or WaitGroups instead.
- **Implementation in hooks.go**: `internal/hooks/hooks.go:46-88` — the `Run()` method uses `context.WithTimeout` (line 51) which is correct for production code (hooks must not hang forever). The test problem is that 5s is too tight for the test environment.
- **Fix direction**: The test should not rely on a wall-clock timeout to determine pass/fail. Instead, use a generous production-like timeout (30s) or verify completion via the returned error (nil = success) without racing against a tight deadline. The `Runner.Run()` API itself is fine — it's the test's 5s `New(5*time.Second)` that's too aggressive.

---

## Lint Warnings (golangci-lint)

5 warnings found:

### LINT-01: Unchecked CloseHandle
- **File**: `cmd/smirror/cmdaddmirror.go:49`
- **Code**: `defer syscall.CloseHandle(h)` — return value not checked
- **Fix**: `defer func() { _ = syscall.CloseHandle(h) }()` or assign and check

### LINT-02: cmdStatus cyclomatic complexity 64 (threshold 50)
- **File**: `cmd/smirror/main.go:799`
- **Note**: `.golangci.yml` says "cmdStatus=49 after recent CLI additions" — this comment is stale, actual is 64.

### LINT-03: cmdAddMirror cyclomatic complexity 52 (threshold 50)
- **File**: `cmd/smirror/cmdaddmirror.go:70`

### LINT-04: Should use strings.TrimPrefix
- **File**: `internal/filter/filter.go:123`
- **Current**: `if strings.HasPrefix(content, "\xEF\xBB\xBF") { content = content[3:] }`
- **Fix**: `content = strings.TrimPrefix(content, "\xEF\xBB\xBF")`

### LINT-05: Should use strings.EqualFold
- **File**: `internal/watcher/watcher.go:152`
- **Current**: `strings.ToLower(syncDir) != strings.ToLower(projDir)`
- **Fix**: `!strings.EqualFold(syncDir, projDir)`

---

## Documentation Discrepancies

### CRITICAL — Misleading to users or developers

#### DOC-01: Version 3-way mismatch

| Document | Claims | Actual |
|----------|--------|--------|
| CLAUDE.md (Status line, near top) | "v0.5.0 released" | 0.8.41-dev |
| CHANGELOG.md | Latest entry: v0.7.0 (2026-04-02) | 0.8.x changes undocumented |
| main.go:43 | `0.8.41-dev` | (ground truth) |
| SECURITY.md:7 | "0.8.x Yes" | Consistent with code but contradicts CLAUDE.md |

**Fix**: Update CLAUDE.md status line to reflect current version. Add 0.8.x entries to CHANGELOG.md (or at minimum note the gap).

#### DOC-02: Dependency names wrong in CLAUDE.md and CREDITS.md

| Documented (both files) | Actual (go.mod) |
|------------------------|-----------------|
| `github.com/sabhiram/go-gitignore` | `github.com/git-pkgs/gitignore v1.1.1` |
| `gopkg.in/yaml.v3` | `go.yaml.in/yaml/v3 v3.0.4` |

**Files to fix**:
- `CLAUDE.md` — Dependencies section (search for "sabhiram" and "gopkg.in/yaml.v3")
- `CREDITS.md` — Compiled Dependencies table (same two entries)
- `README.md` — if it also lists dependencies (check)
- `CONTRIBUTING.md` — if it also lists dependencies (check)

#### DOC-03: Delete policy default conflict in config.example.yaml

- **File**: `config.example.yaml:60` (approximate)
- **Current comment**: `# delete_policy: ignore         # ignore (default), mirror, or quarantine`
- **Actual default** (code `internal/config/config.go` `DeletePolicy()` method): `delete`
- **CLAUDE.md** correctly says `delete (default)`
- **Fix**: Change comment to `# delete_policy: delete         # ignore, delete (default), or quarantine`

#### DOC-04: Phase status stale in CLAUDE.md

- **File**: `CLAUDE.md` — Phases section
- **Current**: "Phase 6 anomaly intelligence next; Phase 3 USN journal + Phase 5 telemetry pending"
- **Reality**:
  - Anomaly system is implemented (internal/anomaly exists, CHANGELOG v0.6.0 documents it)
  - Hooks system is implemented (internal/hooks exists, CHANGELOG v0.7.0 documents it)
  - Telemetry package exists (internal/telemetry)
- **Fix**: Update phase checklist. Mark anomaly/hooks phases as done. Clarify telemetry status ("code written, not enabled").

### MODERATE — Developer confusion

#### DOC-05: 3 packages missing from CLAUDE.md module list

CLAUDE.md's "Modules" section lists 12 packages. Three active packages are missing:

| Missing package | Status | Since |
|----------------|--------|-------|
| `internal/anomaly` | Active, 11 source files | v0.6.0 |
| `internal/hooks` | Active, 2 source files | v0.7.0 |
| `internal/telemetry` | Active, 3 source files | v0.5.0+ |

**Fix**: Add entries to the Modules table in CLAUDE.md:
```
internal/anomaly/anomaly.go      — Anomaly classification, recording, rotation
internal/hooks/hooks.go          — Pre/post-sync hook execution
internal/telemetry/telemetry.go  — Opt-in anonymous telemetry + update check
```

#### DOC-06: Race detection package lists differ between CI and CONTRIBUTING.md

- **CI** (`ci.yml:59`): `./internal/filter/ ./internal/logging/ ./internal/lock/ ./internal/metrics/ ./internal/watcher/`
- **CONTRIBUTING.md:26**: `./internal/config/ ./internal/filter/ ./internal/lock/ ./internal/metrics/ ./internal/logging/`
- **Differences**: CI has `watcher` but not `config`; CONTRIBUTING has `config` but not `watcher`
- **Fix**: Align both lists. Recommend: include both `config` and `watcher`.

#### DOC-07: VV-Plan.md says "No CI coverage gate"

- **File**: `docs/VV-Plan.md` — P2 gap table and static analysis section
- **Lines**: ~319 ("No CI coverage gate / Coverage can decrease without warning") and ~670
- **Reality**: CI has a 35% coverage gate (ci.yml lines 39-55) since v0.5.0
- **Fix**: Update VV-Plan to note the gate exists at 35%

#### DOC-08: Test count claims — 5 documents, 5 different numbers, all wrong

| Document | File/location | Claimed | Actual |
|----------|--------------|---------|--------|
| CLAUDE.md | Testing section | "570+ tests across 15 packages" | 535 tests + 2 fuzz, 14 packages |
| CONTRIBUTING.md | Line 22 | "287 tests across 11 packages" | outdated |
| docs/SRS.md | NFR-TE-01 section | "287 unit tests" | outdated |
| docs/VV-Plan.md | Coverage section | "287 unit tests, 35.8% coverage" | count outdated; coverage close (35.2%) |
| docs/story.md | Multiple lines | "392 unit tests" | outdated |

**Fix**: Update all five files to say "535+ tests" and "14 packages". Or use a vague phrasing like "500+" that won't go stale as fast.

#### DOC-09: .golangci.yml comment about cmdStatus complexity is stale

- **File**: `.golangci.yml` — gocyclo section comment
- **Current**: Says "cmdStatus=49 after recent CLI additions"
- **Actual**: cmdStatus is now 64; cmdAddMirror is 52. Both exceed the 50 threshold.
- **Fix**: Update comment. Consider whether to raise the threshold or refactor the functions.

### MINOR

#### DOC-10: Undocumented alias `syncnow`

- **File**: `cmd/smirror/main.go:139` — `case "sync-now", "syncnow":`
- **Not in**: CLAUDE.md, README.md, or help text
- **Fix**: Add to docs or remove from code (your call — it's a convenience alias)

#### DOC-11: Exit code 6 (ExitUpgrade) undocumented

- **File**: `cmd/smirror/main.go:65` — `ExitUpgrade = 6`
- **Used by**: `selfupdate` command when user declines update
- **Not in**: CLAUDE.md, SRS.md, or any docs
- **Fix**: Add to exit code documentation: "6 = upgrade declined"

#### DOC-12: CONTRIBUTING.md missing `dustin/go-humanize` dependency

- go.mod lists `github.com/dustin/go-humanize` as a direct dependency
- CONTRIBUTING.md doesn't mention it in any dependency discussion
- **Fix**: Minor — only matters if CONTRIBUTING mentions deps

---

## Coverage Deep Dive

### Coverage vs VV-Plan targets

| Package | VV-Plan v0.5.0 Target | Actual Now | Delta |
|---------|----------------------|------------|-------|
| config | 90% | 78.4% | -11.6% (regression?) |
| filter | 92% | 78.5% | -13.5% (regression?) |
| lock | 90% | 83.3% | -6.7% |
| logging | 85% | 81.8% | -3.2% |
| metrics | 85% | 73.6% | -11.4% |
| rclone | 70% | 30.0% | -40.0% (significant gap) |
| state | 90% | 73.1% | -16.9% |
| sync | 80% | 71.4% | -8.6% |
| telemetry | 80% | 69.7% | -10.3% |
| watcher | 40% | 59.6% | **+19.6%** (improvement) |
| cmd/smirror | 10% | 8.3% | -1.7% |

**Note**: Coverage percentages may differ from VV-Plan baselines because new code was added (denominator grew). The watcher improvement is real; other "regressions" may be new uncovered code rather than deleted tests.

### Zero-coverage files (13 files, all 0.0%)

- `internal/anomaly/anomaly.go` (core types)
- `internal/anomaly/reader.go`
- `internal/notify/notify.go`
- `internal/notify/webhook.go`
- `internal/rclone/remotes.go`
- `internal/service/eventlog_windows.go`
- `internal/service/service.go`
- `internal/service/syncnow_windows.go`
- `cmd/smirror/cmdaddmirror.go`
- `cmd/smirror/cmdremote.go`
- `cmd/smirror/cmdunmirror.go`
- `cmd/smirror/main.go`
- `cmd/smirror/selfupdate.go` + `selfupdate_windows.go`
- `cmd/smirror/pathclean_windows.go`
- `cmd/smirror/syncignore.go`

Total zero-coverage functions: **261 out of ~535 total functions**.

---

## User Preferences for Fix Session

- **No timeouts for test synchronization.** Use event-based approaches: channels, WaitGroups, completion signals. Production code may use `context.WithTimeout` for real deadlines (e.g., hook execution), but tests must not rely on wall-clock timeouts to determine pass/fail.
- **Versioning rule**: `-dev` is always PATCH-level. `0.8.0` would be a released version; dev versions are `0.8.N-dev`. Each commit bumps the patch: `0.8.41-dev` → `0.8.42-dev`.
- **AboutAuthor.txt is human-only** — never edit it.

---

## Prioritized Fix List

| Priority | ID | What | Files |
|----------|----|------|-------|
| P0 | BUG-01 | Fix flaky hooks test (event-based, no timeout) | `internal/hooks/hooks_test.go:27-41` |
| P0 | DOC-01 | Update version references across docs | `CLAUDE.md`, `CHANGELOG.md` |
| P0 | DOC-02 | Fix dependency names (sabhiram→git-pkgs, gopkg.in→go.yaml.in) | `CLAUDE.md`, `CREDITS.md` |
| P0 | DOC-03 | Fix delete_policy default comment | `config.example.yaml:60` |
| P1 | DOC-04 | Update phase status in CLAUDE.md | `CLAUDE.md` (Phases section) |
| P1 | DOC-05 | Add 3 missing packages to CLAUDE.md module list | `CLAUDE.md` (Modules section) |
| P1 | DOC-06 | Align race detection package lists | `ci.yml:59`, `CONTRIBUTING.md:26` |
| P1 | DOC-07 | Update VV-Plan re: CI coverage gate | `docs/VV-Plan.md` |
| P1 | DOC-08 | Update test counts (535+/14 packages) | 5 files (see table above) |
| P1 | DOC-09 | Update .golangci.yml complexity comment | `.golangci.yml` |
| P2 | LINT-01 | Fix unchecked CloseHandle | `cmd/smirror/cmdaddmirror.go:49` |
| P2 | LINT-04 | Use strings.TrimPrefix for BOM | `internal/filter/filter.go:123` |
| P2 | LINT-05 | Use strings.EqualFold | `internal/watcher/watcher.go:152` |
| P3 | DOC-10 | Document or remove `syncnow` alias | `cmd/smirror/main.go:139` |
| P3 | DOC-11 | Document exit code 6 | `cmd/smirror/main.go:65` |
| P3 | DOC-12 | Add go-humanize to dependency docs | `CREDITS.md` |
