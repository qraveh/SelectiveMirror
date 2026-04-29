# SelectiveMirror Audit and Validation Report - 2026-04-29

## Scope

Codex audited `C:\SelectiveMirror` using the project documentation, existing unit and system-validation suites, and a BMad-style multi-role review panel. The audit prioritized data loss, silent corruption, privacy leaks, and false-positive release validation over cosmetic issues.

## Panel

- Winston, Architect: system contracts, destructive defaults, and policy invariants.
- Amelia, QA: validation matrix gaps and release-gate trustworthiness.
- Edge Case Hunter: state transitions, timestamp parsing, YAML escaping, sanitizer edge cases.
- Adversarial Reviewer: false-green validation, missing documented commands, telemetry privacy enforcement.

## Baseline Validation

Baseline command:

```powershell
go test ./internal/... ./cmd/... -count=1
```

Result: passed before adding the new bug-reproducing tests.

System-validation command:

```powershell
cd C:\SelectiveMirror\system-validation
go test -timeout 900s -count=1 ./...
```

Result: failed. The suite was already red before Round 14 additions. Notable failures included missing telemetry CLI command coverage, telemetry Worker/RLS privacy contract failures, live-sync burst/depth reliability failures, and a coverage report that still marked all goals as met despite failed tests.

## Validation Added

### System Validation Round 14

Added `system-validation/panel_findings_round14_test.go` with black-box tests for:

- Invalid global `delete_policy` values.
- `delete_policy: ignore` retention during `sync-now`.
- `delete_policy: quarantine` behavior for remote-only orphan cleanup.
- `smirror remote` YAML safety for apostrophe-containing local paths.

Focused command:

```powershell
cd C:\SelectiveMirror\system-validation
go test -timeout 300s -count=1 -run TestPanelR14_ ./...
```

Result: all four Round 14 tests failed, confirming the audit findings.

### Targeted Unit Validation

Added focused tests in:

- `internal/sync/sync_test.go`
- `internal/telemetry/sanitize_test.go`
- `internal/anomaly/sanitize_test.go`

Focused commands:

```powershell
go test ./internal/sync -count=1 -run 'TestSyncSingleFile_RemoteVerificationFailureDoesNotTrustStaleHash|TestParseExpiredQuarantineEntries_NanosecondSuffix'
go test ./internal/telemetry -count=1 -run TestSanitizeReport_RemoteURIRedactionMixedCase
go test ./internal/anomaly -count=1 -run TestSanitizeAnomaly_ExtraPrefixesCaseInsensitiveOnWindows
```

Result: each new reproducer failed on the current build.

## New Bugs Filed

| ID | Severity | Summary | Primary Reproducer |
| --- | --- | --- | --- |
| SM-190 | critical | Global `delete_policy` typos silently fall back to destructive delete. | `TestPanelR14_Config_GlobalInvalidDeletePolicyRejected` |
| SM-191 | critical | `sync-now` deletes retained remote files under `delete_policy: ignore`. | `TestPanelR14_DeletePolicyIgnore_SyncNowRetainsRemoteFile` |
| SM-192 | critical | Ghost cleanup bypasses quarantine and permanently deletes remote orphans. | `TestPanelR14_DeletePolicyQuarantine_GhostCleanupMovesOrphan` |
| SM-193 | major | Nanosecond quarantine names are never selected for expiry cleanup. | `TestParseExpiredQuarantineEntries_NanosecondSuffix` |
| SM-194 | major | `smirror remote` corrupts YAML when local path contains an apostrophe. | `TestPanelR14_RemoteCommand_ApostrophePathKeepsConfigLoadable` |
| SM-195 | major | Anomaly sanitizer leaks mirror paths with different casing on Windows. | `TestSanitizeAnomaly_ExtraPrefixesCaseInsensitiveOnWindows` |
| SM-196 | critical | Failed remote verification can leave stale `remote_hash` and skip required upload. | `TestSyncSingleFile_RemoteVerificationFailureDoesNotTrustStaleHash` |
| SM-197 | major | Release maturity dashboard marks system-validation green while the full suite is red. | Full system-validation vs `docs/release-maturity.md` |
| SM-198 | major | Live daemon burst validation misses 50-file and 200-file sync budgets. | `TestPanelR2_Daemon_LiveSync_BurstCreate`, `TestPanelR3_Queue_HighDepthGraceful` |

## Existing Bugs Reconfirmed

- `SM-157`: documented telemetry command is absent.
- `SM-161`, `SM-162`, `SM-163`: telemetry Worker/RLS privacy contracts are not enforced.
- `SM-176`: validation coverage report can mark failed goals as met.
- `SM-179`: report sanitizer misses mixed-case and one-character rclone remotes.

## Priority Order

1. Fix data-loss and silent-corruption risks: `SM-190`, `SM-191`, `SM-192`, `SM-196`.
2. Fix release-gate false confidence: `SM-176`, `SM-197`, plus the failing telemetry system-validation cluster.
3. Fix privacy leaks: `SM-179`, `SM-195`, and related telemetry bugs.
4. Fix operational correctness: `SM-193`, `SM-194`, and `SM-198`.

## Current State

The added tests are intentionally failing regression tests. They should remain in the suite as proof of the defects until the corresponding fixes are reviewed and implemented.
