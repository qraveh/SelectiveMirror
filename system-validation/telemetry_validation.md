# Telemetry System Validation Report

Date: 2026-04-28  
Project root: `C:\SelectiveMirror`  
Validation module: `C:\SelectiveMirror\system-validation`  
BugTracker project: `C:\BugTracker\projects\SelectiveMirror`

## Executive Summary

This is the second telemetry validation pass. It checked the user's claimed fixes, re-ran the telemetry validation suite, used a BMad-style multirole review panel, corrected stale validation checks, and filed additional bugs.

The "all fixed" claim is not true for the current checkout. Some important fixes did land:

- `SM-160` appears fixed in active code: the legacy broad usage telemetry payload is removed.
- `SM-167` appears fixed: canonical JSON disables Go HTML escaping and has strong `<`, `>`, `&` regression coverage.
- The primary `SM-159` startup update-check path is now tier-gated in `cmd/smirror/selfupdate.go`.

However, major telemetry functionality and privacy/security blockers remain. The executable still has no `smirror telemetry` command, `report-bug --submit` is missing, `report-bug` and crash reports still leak sensitive log data, the Worker/RLS contract is unsafe/incomplete, the MSI build path still omits the telemetry signing key, public digest privacy is still incomplete, and new SQL/docs/validation bugs were found.

This turn fixed validation-suite false positives only. Product telemetry code was not fixed.

## Method

I used BMad skills from `C:\BMadClaude` as a multirole panel and then converted the findings into system-validation tests and BugTracker records.

Panel roles:

- System architect: architecture, service boundaries, worker/schema flow.
- QA/acceptance: CLI contract, release build path, executable validation.
- Privacy/security edge-case reviewer: sanitization, consent, RLS/HMAC, rate limiting.
- Documentation/release reviewer: docs, installer, runbook, BugTracker alignment.

## Scope Reviewed

Primary telemetry surfaces reviewed:

- User docs and privacy contract.
- CLI command dispatch and help text.
- `report-bug` and crash-report diagnostics.
- Startup update-check behavior.
- Legacy telemetry client and consent tier code.
- Cloudflare Worker ingress behavior.
- Supabase schema, RLS, worker SQL, rollups, and views.
- MSI installer consent/build-key path.
- Weekly digest script.
- Canonical JSON/HMAC compatibility.
- System-validation harness behavior.

Primary files reviewed included:

- `C:\SelectiveMirror\cmd\smirror\main.go`
- `C:\SelectiveMirror\cmd\smirror\crashreport.go`
- `C:\SelectiveMirror\cmd\smirror\selfupdate.go`
- `C:\SelectiveMirror\internal\telemetry\telemetry.go`
- `C:\SelectiveMirror\internal\telemetry\tier.go`
- `C:\SelectiveMirror\internal\telemetry\canonical.go`
- `C:\SelectiveMirror\internal\telemetry\canonical_test.go`
- `C:\SelectiveMirror\worker\src\index.ts`
- `C:\SelectiveMirror\docs\PRIVACY.md`
- `C:\SelectiveMirror\docs\cli-telemetry-command.md`
- `C:\SelectiveMirror\docs\telemetry-microserver.sql`
- `C:\SelectiveMirror\docs\telemetry-rls.sql`
- `C:\SelectiveMirror\docs\telemetry-worker.sql`
- `C:\SelectiveMirror\docs\telemetry-views.sql`
- `C:\SelectiveMirror\docs\operations\telemetry-ops.md`
- `C:\SelectiveMirror\installer\TelemetryConsent.wxi`
- `C:\SelectiveMirror\installer\build-msi.ps1`
- `C:\SelectiveMirror\.github\workflows\release.yml`
- `C:\SelectiveMirror\scripts\telemetry-report.py`
- `C:\SelectiveMirror\system-validation\*.go`

## Validation Suite Updates

Updated:

- `C:\SelectiveMirror\system-validation\telemetry_test.go`
- `C:\SelectiveMirror\system-validation\telemetry_security_test.go`
- `C:\SelectiveMirror\system-validation\helpers_test.go`
- `C:\SelectiveMirror\system-validation\telemetry_validation.md`

Important validation corrections:

- `SM-170`: fixed stale tests that reported fixed `SM-159`, `SM-160`, and `SM-167` behavior as failing.
- Legacy telemetry checks now parse active Go AST instead of scanning comments.
- Startup update checks now inspect `checkForUpdateOnStartup` for the tier gate before `client.CheckForUpdate`.
- Canonical JSON checks now accept the new stronger no-HTML-escape fixtures and require `SetEscapeHTML(false)`.

New validation probes added:

- Crash-report privacy and explicit consent.
- Retention janitor coverage for normalized raw report fields.
- Tier fail-closed behavior when state DB metadata cannot be read.
- Bounded GitHub token lookup.
- Ops runbook references to defined SQL views.
- Rollup taxonomy join cross-products.
- Coverage report masking failed tests.
- Global `rclone` preflight blocking static telemetry checks.

## How To Run

Focused telemetry validation:

```powershell
cd C:\SelectiveMirror\system-validation
$env:PATH = "C:\SelectiveMirror\bin;$env:PATH"
go test . -run Telemetry -count=1
```

Focused false-positive verification:

```powershell
cd C:\SelectiveMirror\system-validation
$env:PATH = "C:\SelectiveMirror\bin;$env:PATH"
go test . -run '^TestTelemetryPrivacyContract_NoStartupUpdatePingAtDefaultNone$|^TestTelemetryPrivacyContract_NoLegacyUsageReportShape$|^TestTelemetryCanonicalJSON_DoesNotHTMLEscapeStrings$' -count=1
```

## Verification Results

Passing checks from this pass:

- `go test ./internal/telemetry -count=1`
- `go test ./cmd/smirror -run 'TestExtractSearchKeywords|TestSearchSimilarIssues' -count=1`
- Focused false-positive verification subset above.
- BugTracker validation for `SM-170` through `SM-178`.

Expected failing checks:

- `go test . -run Telemetry -count=1` from `C:\SelectiveMirror\system-validation`
- Focused new-bug subset:
  - `TestTelemetryDigest_PrivacyAndMarkdownEscaping`
  - `TestTelemetryRetention_PurgesNormalizedRawText`
  - `TestTelemetryCrashReport_SanitizationAndConsent`
  - `TestTelemetryGithubToken_HasTimeoutOrCallerContext`
  - `TestTelemetryTierGate_FailsClosedOnStateReadError`
  - `TestTelemetryDocs_OperationsViewsExist`
  - `TestTelemetryRollup_TaxonomyJoinsDoNotCrossProduct`
  - `TestTelemetryValidationHarness_CoverageDoesNotMaskFailedTests`
  - `TestTelemetryValidationHarness_StaticChecksDoNotRequireRclone`

## Bug Inventory

| Bug | Severity | Status | Area | Summary |
| --- | --- | --- | --- | --- |
| SM-157 | major | open | CLI | Documented `smirror telemetry` command is still not implemented. |
| SM-158 | major | open | CLI | `report-bug --submit`, `--one-shot`, and `--browser` remain missing. |
| SM-159 | major | partly fixed | Privacy | Primary startup update path is now tier-gated; residual fail-open risk tracked as `SM-173`. |
| SM-160 | major | appears fixed | Client privacy | Active legacy broad usage telemetry payload is removed; validation false positive fixed in `SM-170`. |
| SM-161 | major | open | Worker/schema | Worker still proxies raw envelopes instead of documented ingest/normalization flow. |
| SM-162 | critical | open | SQL/RLS | RLS HMAC still does not bind envelope fields or protect server-owned ingest state. |
| SM-163 | major | open | Worker security | Worker edge limits remain bypassable; raw IPs are stored in rate-limit keys. |
| SM-164 | critical | open | Diagnostics privacy | `report-bug --stdout` still leaks filenames, remotes, and secrets from logs. |
| SM-165 | major | open | Documentation | Docs still disagree about telemetry status, installer UI, and live/deferred behavior. |
| SM-166 | major | partly fixed | Digest privacy | Markdown escaping appears fixed; install prefixes and low-n per-report rows still leak. |
| SM-167 | major | appears fixed | HMAC | Canonical JSON no-HTML-escape implementation/tests are now present. |
| SM-168 | major | open | Installer/release | MSI build still omits telemetry signing key and installer consent path remains incomplete. |
| SM-169 | minor | fixed | Test infra | Focused `-run` validation no longer fails unrelated coverage goals. |
| SM-170 | minor | fixed | Test infra | Stale telemetry validation false positives were corrected. |
| SM-171 | critical | open | Crash privacy | Crash reports bypass telemetry sanitization and default to submit. |
| SM-172 | major | open | Retention | Retention janitor strips only ingest envelopes, not normalized raw report fields. |
| SM-173 | major | open | Consent | Tier gate can fail open to registry when state DB tier read errors. |
| SM-174 | major | open | Update/GitHub | `GithubToken` can hang on `gh auth token` before HTTP timeout starts. |
| SM-175 | minor | open | Ops docs | Runbook references nonexistent `version_dist` view. |
| SM-176 | major | open | Test infra | Coverage report marks goals as met even when selected tests fail. |
| SM-177 | major | open | Test infra | Static telemetry validation is blocked by global `rclone` preflight. |
| SM-178 | major | open | Analytics SQL | Rollup taxonomy joins can duplicate reports across bogus segments. |

## Current Highest-Risk Findings

1. `SM-164` and `SM-171`: diagnostics and crash submission can leak filenames, remotes, tokens, and workload labels.
2. `SM-162`: RLS does not authenticate envelope metadata or prevent client-set server-owned state.
3. `SM-163`: Worker rate limiting stores raw IPs and is bypassable with chunked bodies/concurrency.
4. `SM-157` and `SM-158`: documented consent and submit flows are still absent from the executable.
5. `SM-168`: MSI release artifacts can ship without telemetry signing capability.

## What Was Fixed This Turn

Only validation-suite issues were fixed:

- `SM-170` filed and fixed.
- Stale tests for `SM-159`, `SM-160`, and `SM-167` were corrected.
- New system-validation probes were added for `SM-171` through `SM-178`.

Product telemetry implementation was not changed in this turn.

## Follow-Up

Recommended fix order:

1. Fix sanitization and explicit consent across `report-bug` and crash reporting: `SM-164`, `SM-171`.
2. Fix RLS/worker security boundary: `SM-162`, `SM-163`, `SM-161`.
3. Implement real consent CLI and submit flow: `SM-157`, `SM-158`.
4. Resolve installer/release readiness: `SM-168`.
5. Close remaining privacy/ops analytics issues: `SM-172`, `SM-173`, `SM-174`, `SM-175`, `SM-178`.
6. Improve validation trustworthiness: `SM-176`, `SM-177`.
