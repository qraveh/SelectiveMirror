# SelectiveMirror — Comprehensive Test Plan

**Version**: 1.0.0
**Created**: 2026-03-27
**Author**: Claude (code audit) + Raveh (review)

---

## 1. Test Architecture Overview

### Test Tiers

| Tier | Scope | Runner | Prerequisites |
|------|-------|--------|---------------|
| **Unit** | Single package, no I/O | `go test ./internal/...` | Go compiler |
| **Integration** | Real watcher + local rclone | `test/run_tests.ps1` | Go + rclone |
| **Install** | MSI package verification | `test/test_install.ps1` | Built MSI |
| **Uninstall** | Cleanup verification | `test/test_uninstall.ps1` | Installed MSI |
| **E2E** | Real cloud backend | Manual | rclone + credentials |

### Test Infrastructure

- **RcloneRunner injection**: `sync.Engine` accepts a `func(ctx, args) int` replacement for rclone subprocess
- **In-memory SQLite**: `modernc.org/sqlite` allows real DB testing without disk I/O overhead
- **Temp directories**: All unit tests use `t.TempDir()` for isolation
- **Local rclone remote**: Integration tests use `rclone config create testlocal local` (no network)

---

## 2. Existing Tests Inventory

### 2.1 Unit Tests (Go)

#### `internal/config/config_test.go` — 12 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestLoadValidConfig | Parses valid YAML, projects populated |
| 2 | TestLoadInvalidNoProjects | Rejects config with empty projects list |
| 3 | TestLoadInvalidDuplicateNames | Detects duplicate project names |
| 4 | TestLoadMissingLocalPath | Rejects nonexistent local_path |
| 5 | TestLoadMissingName | Rejects missing project name |
| 6 | TestProjectDefaults | Default debounce=5s, max_file_size=100MB |
| 7 | TestProjectCustomValues | Custom debounce and max_file_size honored |
| 8 | TestSyncIgnoreFile | .syncignore path resolution (custom + default) |
| 9 | TestDeletePolicy | Parses "ignore", "mirror", "quarantine" strings |
| 10 | TestQuarantineRetention | Default quarantine_days=30 |
| 11 | TestFindProject | Lookup project by name |
| 12 | TestProjectNames | Extract project name list |

#### `internal/filter/filter_test.go` — 13 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestGlobalExcludes | .git/, *.pyc, *.tmp, *.log, __pycache__/ excluded |
| 2 | TestProjectSyncIgnore | Project .syncignore rules + global rules |
| 3 | TestNoFilters | Nothing excluded with empty filters |
| 4 | TestNonexistentSyncIgnore | Graceful handling of missing .syncignore |
| 5 | TestEffectiveRules | Returns merged global + project rules |
| 6 | TestGenerateRcloneFilterFile | Creates rclone filter file with +/- syntax |
| 7 | TestUnicodePaths | Unicode filenames handled correctly |
| 8 | TestReloadSyncIgnore | Hot-reload detects changes, returns changed=true |
| 9 | TestReloadNoChange | Returns changed=false when content unchanged |
| 10 | TestReloadDeletedSyncIgnore | Handles .syncignore deletion gracefully |
| 11 | TestReloadNewSyncIgnore | Handles .syncignore creation |
| 12 | TestSyncIgnorePath | Returns configured path |
| 13 | TestConcurrentReloadAndRead | 100 reloads + 1000 reads, no panics |

#### `internal/state/state_test.go` — 11 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestOpenAndClose | Opens DB, verifies schema_version=2 |
| 2 | TestUpdateAndGetFileState | Store/retrieve hash, size, mtime, exit code |
| 3 | TestUpsertFileState | Updates existing file state (idempotent) |
| 4 | TestGetFileStateNotFound | Returns nil for nonexistent file |
| 5 | TestLogAction | Logs sync action with latency |
| 6 | TestGetAllSyncedPaths | Retrieves all synced paths for project |
| 7 | TestGetPendingFiles | Gets files with non-zero exit codes |
| 8 | TestGetLastSyncTime | Returns latest sync time or zero |
| 9 | TestMetaSetGet | Key-value metadata CRUD |
| 10 | TestHashFile | MD5 hash correctness ("hello world\n" = 6f5902ac...) |
| 11 | TestHashFileNotFound | Error for missing file |

#### `internal/lock/lock_test.go` — 5 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestAcquireAndRelease | Acquire + release cycle works |
| 2 | TestDoubleAcquireFails | Second acquire returns ErrAlreadyRunning |
| 3 | TestReleaseAllowsReacquire | Can re-acquire after release |
| 4 | TestIsLockedWhenNotLocked | IsLocked returns false when unlocked |
| 5 | TestSplitLines | Parses CRLF, LF, single-line content |

#### `internal/metrics/metrics_test.go` — 8 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestRecordSync | Records file sync with bytes + latency |
| 2 | TestRecordError | Records error per project |
| 3 | TestQueueDepth | Sets and reads queue depth |
| 4 | TestRecordScanComplete | Marks scan completion timestamp |
| 5 | TestSnapshotVersion | Version included in snapshot |
| 6 | TestWriteStatusFile | Atomic write of status.json |
| 7 | TestFormatHuman | Human-readable metrics output |
| 8 | TestEmptySnapshot | Zero-state snapshot is valid |

#### `internal/sync/sync_test.go` — 19 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestLockKey_SingleFile | Lock key = "project:relpath" |
| 2 | TestLockKey_FullProject | Lock key = "project" (no relpath) |
| 3 | TestAcquireRelease_NoDeadlock | Sequential acquire/release completes |
| 4 | TestAcquireFileLock_Concurrent_SameKey | Same key: only 1 concurrent holder |
| 5 | TestAcquireFileLock_DifferentKeys_Parallel | Different keys: parallel access |
| 6 | TestQuiesceFile_StableFile | Returns stat for stable file |
| 7 | TestQuiesceFile_Nonexistent | Error for missing file |
| 8 | TestQuiesceFile_Directory | Error for directory |
| 9 | TestProcessTask_PanicRecovery | Panic caught, no crash |
| 10 | TestRun_ContextCancel | Run() exits on context cancel |
| 11 | TestRun_ChannelClose | Run() exits on TaskChan close |
| 12 | TestSyncFullProject_MirrorPolicy | Uses "sync" verb for mirror |
| 13 | TestSyncFullProject_IgnorePolicy | Uses "copy" verb for ignore |
| 14 | TestDeleteRemoteFile_IgnorePolicy | Delete skipped for ignore |
| 15 | TestDeleteRemoteFile_MirrorPolicy | Delete called for mirror |
| 16 | TestDeleteRemoteFile_ForceDelete_OverridesIgnorePolicy | Force overrides ignore |
| 17 | TestSyncSingleFile_HashUnchanged | Skips sync when hash+mtime match |
| 18 | TestCommonFlags_ContainsSkipLinks | --skip-links in args |
| 19 | TestCommonFlags_BandwidthLimit | --bwlimit included when set |

#### `internal/rclone/detect_test.go` — 9 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1 | TestParseVersionOutput_Standard | Parses "rclone v1.68.2" full output |
| 2 | TestParseVersionOutput_NoPrefix | Parses "rclone 1.73.0" (no "v") |
| 3 | TestParseVersionOutput_Empty | Returns 0.0.0 for empty |
| 4 | TestVersion_AtLeast | Version comparison (>=) |
| 5 | TestCompatCheck_Full | v1.73+ = CompatFull |
| 6 | TestCompatCheck_Partial | v1.68 = CompatPartial |
| 7 | TestCompatCheck_None | v1.40 = CompatNone |
| 8 | TestDetect_SystemRclone | Detects installed rclone (skip if missing) |
| 9 | TestSearchDescription | Help text is non-empty |

#### `internal/watcher/watcher_test.go` — 16 tests
| # | Test | What It Verifies |
|---|------|-----------------|
| 1-4 | TestIsSubPath_* | Path containment (child, parent, not-child, similar-prefix) |
| 5-11 | TestIsRelSubPath_* | Relative path containment (child, deep, dot, empty, not-child, similar-prefix, exact) |
| 12 | TestFindProject_Match | Finds project for file under project dir |
| 13 | TestFindProject_NoMatch | Returns nil for file outside all projects |
| 14 | TestSafeGo_PanicRecovery | Panic caught, recorded in HealthErrors |
| 15 | TestHealthErrors_RecordAndRetrieve | Errors retrievable, returns copy |
| 16 | TestHealthErrors_CappedAt100 | Cap at 100 entries |

**Unit test total: 93 tests across 8 packages**

### 2.2 Integration Tests (`test/run_tests.ps1`) — 40 tests

| # | Test Function | Category | What It Verifies |
|---|--------------|----------|-----------------|
| 1 | Test-BasicFileSync | Core | hello.txt syncs to remote |
| 2 | Test-ExcludedFilesNotSynced | Filter | .pyc, .tmp, .log excluded |
| 3 | Test-ExcludedDirectoryNotSynced | Filter | .git/ tree excluded |
| 4 | Test-SubdirectorySync | Watcher | New subdirs watched, files synced |
| 5 | Test-DebounceRapidWrites | Debounce | 10 rapid writes → last version synced |
| 6 | Test-FileModification | Core | v1→v2 content propagates |
| 7 | Test-LargeFileSkip | Config | 2MB over 1MB limit skipped |
| 8 | Test-EmptyFile | Edge | empty.txt syncs |
| 9 | Test-UnicodeFilename | Edge | テスト.txt syncs |
| 10 | Test-SpacesInFilename | Edge | "my file (copy).txt" syncs |
| 11 | Test-DeeplyNestedPath | Edge | a/b/c/d/e/f/g/deep.txt syncs |
| 12 | Test-SingleInstanceLock | Lock | Second instance rejected |
| 13 | Test-DoctorWhileRunning | CLI | Doctor detects running instance |
| 14 | Test-ExplainCommand | CLI | Explain shows INCLUDED + remote path |
| 15 | Test-ExplainExcludedFile | CLI | Explain shows EXCLUDED + rule |
| 16 | Test-BurstFileCreation | Stress | 50 files burst, ≥48 synced |
| 17 | Test-FileDeletedBeforeSync | Edge | Ephemeral file doesn't crash |
| 18 | Test-RenameStorm | Stress | 5 renames, final name synced |
| 19 | Test-SimultaneousWriteToSameFile | Concurrency | 5×10 writes to same file |
| 20 | Test-StopAndRestart | Reconciliation | Files created while stopped sync on restart |
| 21 | Test-VerifyCommand | CLI | Verify runs, no drift detected |
| 22 | Test-VerifyDetectsDrift | CLI | Verify detects orphan remote file |
| 23 | Test-StatusCommand | CLI | Status shows project + policy |
| 24 | Test-ProcessKillRecovery | Resilience | Hard kill → clean restart |
| 25 | Test-SpecialCharactersInContent | Edge | CRLF, tab, null byte content |
| 26 | Test-DotFiles | Edge | .editorconfig syncs, .git doesn't |
| 27 | Test-FileWithNoExtension | Edge | Makefile, LICENSE sync |
| 28 | Test-FileRenameSync | Rename | before→after rename propagates |
| 29 | Test-FileMoveToSubdir | Rename | movable.txt → moved_here/movable.txt |
| 30 | Test-DirectoryRename | Rename | orig_dir/ → renamed_dir/ |
| 31 | Test-DirectoryDelete | Delete | Dir deletion doesn't crash |
| 32 | Test-MoveFileOutOfProject | Boundary | File leaves project, smirror survives |
| 33 | Test-MoveFileIntoProject | Boundary | File enters project, syncs |
| 34 | Test-NestedDirRename | Rename | Deep nested rename propagates |
| 35 | Test-SymlinkFileNotSynced | Security | Symlink NOT followed |
| 36 | Test-SymlinkDirNotFollowed | Security | Symlink dir NOT followed |
| 37 | Test-JunctionNotFollowed | Security | Junction NOT followed |
| 38 | Test-NamedPipeIgnored | Edge | System files don't crash |
| 39 | Test-SyncIgnoreHotReload | Filter | .syncignore change excludes new patterns |
| 40 | (Additional edge cases) | Various | Debug variant in run_tests_debug.ps1 |

### 2.3 Installation Tests

#### `test/test_install.ps1` — 9 checks
| # | Check | What It Verifies |
|---|-------|-----------------|
| 1 | Install directory exists | C:\Program Files\SelectiveMirror |
| 2 | smirror.exe in install dir | Binary present |
| 3 | PATH contains install dir | Environment variable set |
| 4 | smirror version exits 0 | Binary runs |
| 5 | smirror version output matches | Version regex correct |
| 6 | smirror doctor exits cleanly | Basic health check |
| 7 | README.txt present | Documentation installed |
| 8 | LICENSE present | License file installed |
| 9 | PDF manuals present | docs/ subdirectory populated |

#### `test/test_uninstall.ps1` — 4 checks
| # | Check | What It Verifies |
|---|-------|-----------------|
| 1 | Install dir removed | Clean uninstall |
| 2 | smirror.exe not on PATH | PATH cleaned |
| 3 | PATH entry removed | No stale entry |
| 4 | User config preserved | ~/.selectivemirror/ survives |

### 2.4 CI Pipeline (`.github/workflows/ci.yml`)

| Step | What It Runs |
|------|-------------|
| Build | `go build -v ./cmd/smirror/` |
| Vet | `go vet ./...` |
| Unit tests | `go test -v ./internal/...` |
| Install rclone | `choco install rclone -y` |
| smirror version | Built binary reports version |
| smirror doctor | Basic health check (continue-on-error) |

---

## 3. Coverage Gap Analysis

### 3.1 ZERO Test Coverage (No Tests Exist)

| Package/Area | Functions Missing Tests | Risk |
|-------------|----------------------|------|
| **`internal/logging/`** | `newRotatingWriter`, `Write`, `rotate`, `openFile`, `Close`, `Setup` | **HIGH** — rotation bugs SM-038/SM-043 are untestable without unit tests |
| **`cmd/smirror/main.go`** | All 11 commands, `loadConfig`, `buildFilters`, `dataDir`, `hashFile`, `reconcileAll`, `heartbeatLoop` | **HIGH** — CLI is the user-facing surface, untested at unit level |

### 3.2 Undertested Functionality (Tests Exist But Incomplete)

| Area | What's Missing | Risk |
|------|---------------|------|
| **sync: deleteRemoteFile failure path** | No test for rclone exit≠0 → state should be preserved (SM-036) | HIGH |
| **sync: deleteRemoteDir** | No unit test at all — only covered indirectly by integration Test-DirectoryDelete | HIGH |
| **sync: quarantine policy** | No unit test for DeleteQuarantine path (moveto + timestamp) | MEDIUM |
| **sync: syncMtime** | No unit test — metadata-only sync path untested | MEDIUM |
| **sync: rclone timeout** | No test for 5-minute timeout behavior (`defaultRunRclone`) | MEDIUM |
| **sync: Validate** | No test for remote connectivity checking | LOW |
| **sync: DryRun** | No unit test | LOW |
| **sync: ListRemote** | No unit test for `rclone lsjson` parsing | MEDIUM |
| **filter: negation across layers** | No test for project `!pattern` overriding global exclude (SM-037) | HIGH |
| **filter: toRcloneFilter** | Only tested via GenerateRcloneFilterFile — no isolated test | LOW |
| **state: GetFilesUnderDir** | No unit test — used by deleteRemoteDir | MEDIUM |
| **state: CountFiles** | No unit test | LOW |
| **state: UpdateMtimeOnly** | No unit test | LOW |
| **state: concurrent writes** | No test for multiple goroutines writing simultaneously | MEDIUM |
| **state: migration error handling** | No test for ALTER TABLE failure (SM-039) | MEDIUM |
| **watcher: event handling** | `eventLoop`, `handleEvent`, `handleRemove`, `handleRename` — no unit tests (covered by integration) | MEDIUM |
| **watcher: debounceLoop** | No unit test — only integration Test-DebounceRapidWrites | MEDIUM |
| **watcher: addRecursive/removeRecursive** | No unit test | MEDIUM |
| **watcher: reloadFilter** | No unit test — only integration Test-SyncIgnoreHotReload | LOW |
| **config: Workers cap** | No test that workers > 16 is capped to 16 | LOW |
| **config: HeartbeatInterval** | No unit test | LOW |
| **config: ReconcileInterval** | No unit test | LOW |
| **config: expandHome** | No unit test for ~ expansion | LOW |
| **rclone: resolve()** | No test for Windows search paths fallback | LOW |
| **lock: IsLocked with stale PID** | No test for IsLocked when PID in lock file is dead | LOW |
| **metrics: RecordMetadataSync** | No unit test | LOW |

### 3.3 Missing Scenario Coverage (Integration)

| Scenario | Why It's Missing | Risk |
|----------|-----------------|------|
| **Debounce invocation count** (SM-042) | Test checks end state, not rclone call count | MEDIUM |
| **Quarantine delete policy E2E** | No integration test with `delete_policy: quarantine` | MEDIUM |
| **Reconciliation interval** | No test for periodic reconciliation (every 5min) | LOW |
| **Heartbeat writes** | No test for status.json periodic update | LOW |
| **Multi-project isolation** | No test with 2+ projects verifying changes in A don't trigger B | MEDIUM |
| **Max file size boundary** | Test-LargeFileSkip uses 2MB vs 1MB limit — no test at exact boundary | LOW |
| **Config hot-reload** | No test for config file changes during running daemon | LOW |
| **Bandwidth limiting** | No test verifying --bwlimit flag effect | LOW |
| **Rclone CompatPartial** | No test for behavior with rclone < 1.73 (missing --skip-links) | LOW |
| **Concurrent full-project sync** | sync-now during active watcher sync | MEDIUM |
| **WSL cross-filesystem** | No automated test for ext4↔NTFS sync (manual only) | LOW |

### 3.4 CI Pipeline Gaps

| Gap | Impact | Bug |
|-----|--------|-----|
| Missing `-race` flag | Race conditions undetected | SM-040 |
| No integration tests in CI | Only unit tests run | MEDIUM risk |
| Go version 1.22 in CI vs 1.26 in dev | Version mismatch | LOW |

---

## 4. Planned New Tests

### 4.1 Priority 1 — Critical Gaps (SM bug reproduction)

| ID | Package | Test Name | What It Verifies |
|----|---------|-----------|-----------------|
| P1-01 | sync | TestDeleteRemoteFile_RcloneFailure_StatePreserved | SM-036: state NOT deleted when rclone exits non-zero |
| P1-02 | sync | TestDeleteRemoteDir_DoubleDeleteState | SM-036: deleteRemoteDir doesn't double-call DeleteFileState |
| P1-03 | filter | TestNegationOverridesGlobalExclude | SM-037: project `!important.log` overrides global `*.log` |
| P1-04 | filter | TestNegationWithinSingleLayer | SM-037: negation within same layer still works |
| P1-05 | logging | TestRotate_OpenFileFailure_NoPanic | SM-038: rotate handles openFile error gracefully |
| P1-06 | logging | TestRotate_RenameFailure_Graceful | SM-038: rotate handles os.Rename error |
| P1-07 | state | TestMigration_ReadOnlyDB_ReturnsError | SM-039: ALTER TABLE error not swallowed on read-only DB |
| P1-08 | state | TestMigration_ColumnExists_NoError | SM-039: idempotent when column exists |
| P1-09 | logging | TestRotate_BackupNaming_DoubleDigit | SM-043: backup names correct for maxBackups > 10 |

### 4.2 Priority 2 — Missing Unit Tests for Core Paths

| ID | Package | Test Name | What It Verifies |
|----|---------|-----------|-----------------|
| P2-01 | logging | TestNewRotatingWriter_CreatesFile | Writer creates log file and dir |
| P2-02 | logging | TestWrite_TriggersRotation | Write exceeding maxBytes rotates |
| P2-03 | logging | TestWrite_BelowThreshold_NoRotation | Small writes don't rotate |
| P2-04 | logging | TestClose_ClosesFile | Close releases file handle |
| P2-05 | logging | TestSetup_DebugLevel | Setup with "debug" enables debug |
| P2-06 | logging | TestSetup_FileAndConsole | Dual output to file + stderr |
| P2-07 | sync | TestDeleteRemoteDir_AllFilesDeleted | All files under dir cleaned from remote |
| P2-08 | sync | TestDeleteRemoteDir_EmptyDir_Noop | No-op for dir with no synced files |
| P2-09 | sync | TestDeleteRemoteDir_IgnorePolicy_Skipped | Skipped when policy=ignore |
| P2-10 | sync | TestSyncSingleFile_QuiesceFailure | Handles unstable file gracefully |
| P2-11 | sync | TestDeleteRemoteFile_QuarantinePolicy | Quarantine path includes timestamp |
| P2-12 | sync | TestDeleteRemoteFile_QuarantineFailure | State preserved on quarantine rclone failure |
| P2-13 | sync | TestSyncMtime_Success | Metadata-only sync updates mtime on remote |
| P2-14 | sync | TestSyncMtime_RcloneFailure | Handles rclone touch failure |
| P2-15 | sync | TestListRemote_ParsesLsjson | Parses rclone lsjson JSON output |
| P2-16 | sync | TestListRemote_EmptyRemote | Returns empty list for empty remote |
| P2-17 | sync | TestRcloneTimeout_Enforced | defaultRunRclone kills after 5 min |
| P2-18 | state | TestGetFilesUnderDir_ReturnsChildren | Returns all files under directory prefix |
| P2-19 | state | TestGetFilesUnderDir_NoMatch | Returns empty for unknown prefix |
| P2-20 | state | TestCountFiles_Correct | Returns accurate count |
| P2-21 | state | TestUpdateMtimeOnly_UpdatesMtime | Updates mtime without changing hash |
| P2-22 | state | TestConcurrentWrites_NoCorruption | Multiple goroutines writing simultaneously |

### 4.3 Priority 3 — Strengthened Integration Tests

| ID | Test Name | What It Verifies |
|----|-----------|-----------------|
| P3-01 | Test-DebounceInvocationCount | SM-042: 10 writes → exactly 1 rclone call (parse log) |
| P3-02 | Test-QuarantineDeletePolicy | File deleted locally → quarantined remotely with timestamp |
| P3-03 | Test-MultiProjectIsolation | Changes in project A don't trigger sync of project B |
| P3-04 | Test-ReconciliationAfterGap | Files created externally detected on reconciliation |
| P3-05 | Test-MaxFileSizeBoundary | File at exact limit syncs; 1 byte over is skipped |
| P3-06 | Test-ConcurrentSyncNowAndWatcher | sync-now during active watch doesn't corrupt |
| P3-07 | Test-GracefulShutdownFlushes | SIGINT flushes pending syncs before exit |
| P3-08 | Test-FilterNegationE2E | Project .syncignore `!keep.log` overrides global `*.log` |

### 4.4 Priority 4 — CI Improvements

| ID | Change | What It Adds |
|----|--------|-------------|
| P4-01 | Add `-race` to CI | SM-040: enable race detector |
| P4-02 | Update Go version | 1.22 → 1.26 to match dev environment |
| P4-03 | Add integration tests to CI | Run subset of run_tests.ps1 in CI |
| P4-04 | Add `go test -count=10` | Detect flaky tests via repeated runs |

---

## 5. Test Execution Matrix

### Running All Tests

```powershell
# Unit tests (fast, no prerequisites)
cd C:\SelectiveMirror
go test -v ./internal/...

# Unit tests with race detector (requires CGO_ENABLED=1 + gcc)
CGO_ENABLED=1 go test -v -race ./internal/...

# Integration tests (requires rclone)
powershell -ExecutionPolicy Bypass -File test/run_tests.ps1

# Integration tests with debug logging
powershell -ExecutionPolicy Bypass -File test/run_tests_debug.ps1

# Post-install smoke test (requires MSI installed)
powershell -ExecutionPolicy Bypass -File test/test_install.ps1

# Post-uninstall verification
powershell -ExecutionPolicy Bypass -File test/test_uninstall.ps1

# Validate all bug reports
cd C:\BugTracker
powershell -ExecutionPolicy Bypass -File scripts/validate-bugs.ps1
```

### Coverage Report

```powershell
cd C:\SelectiveMirror
go test -coverprofile=coverage.out ./internal/...
go tool cover -html=coverage.out -o coverage.html
```

---

## 6. Test Counts Summary

| Category | Existing | Planned | Total |
|----------|----------|---------|-------|
| Unit tests | 93 | 31 | 124 |
| Integration tests | 40 | 8 | 48 |
| Install/Uninstall | 13 | 0 | 13 |
| CI improvements | 6 steps | 4 changes | 10 |
| **Total** | **152** | **43** | **195** |

### Coverage by Package (Current → Target)

| Package | Existing Tests | Functions Untested | Planned Tests | Target |
|---------|---------------|-------------------|---------------|--------|
| config | 12 | 4 minor | 0 | Good |
| filter | 13 | 1 critical (negation) | 2 | Complete |
| state | 11 | 5 medium | 5 | Complete |
| lock | 5 | 1 minor | 0 | Good |
| metrics | 8 | 1 minor | 0 | Good |
| sync | 19 | 8 critical+medium | 12 | Complete |
| rclone | 9 | 1 minor | 0 | Good |
| watcher | 16 | event handling (integration-covered) | 0 | Adequate |
| **logging** | **0** | **ALL (6 functions)** | **9** | **Complete** |
| **main.go** | **0** | ALL (11 commands) | — | Integration-covered |

---

## 7. Acceptance Criteria

A test run is considered **green** when:
1. `go test ./internal/...` — 0 failures
2. `test/run_tests.ps1` — all tests pass
3. `scripts/validate-bugs.ps1` — 0 errors, 0 warnings
4. No test takes longer than 30s (unit) or 5min (integration)
5. No test leaves artifacts in temp directories

A bug is considered **verified** when:
1. A reproducing test exists that fails on buggy code
2. The fix makes the test pass
3. Validation tests cover related scenarios
4. All existing tests still pass after the fix
