# CLAIMS-MAP — every PRIVACY.md / architecture-v2 claim mapped to a test ID

**Origin**: Quincy (System Validation), telemetry round-3 panel, 2026-04-30.
**Owner**: Raveh.
**Rule**: every assertion in `docs/PRIVACY.md` and `docs/telemetry-architecture-v2.md` containing the words *"never"*, *"only"*, *"anonymous"*, or a numeric quantity must appear in this table with a test ID. **If a claim has no test, either delete the claim or write the test.** Do not ship v1.0 with unverified marketing prose.

A claim is **GREEN** when the linked test exists, runs in CI (or the smoke harness), and was last seen passing within the freshness window. **AMBER** = test exists but not yet wired to CI. **RED** = no test (deletion candidate or work item).

---

## Privacy contract claims (`docs/PRIVACY.md`)

| ID | Claim (verbatim or paraphrased) | Doc anchor | Test file | Test ID | Status | Last green |
|----|---------------------------------|------------|-----------|---------|--------|-----------|
| **C-01** | "If you do nothing, nothing leaves your machine." (default tier = None; no startup pings) | PRIVACY.md "Default: None" | `system-validation/telemetry_test.go` | `TestTelemetryPrivacyContract_NoStartupUpdatePingAtDefaultNone` | GREEN | 2026-04-29 |
| **C-02** | "We never store personal data." (schema-provable: only rollup tables exist) | PRIVACY.md "The shape of the promise" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_OnlyRollupTablesExist` + `TestTelemetryV2Schema_NoNarrativeColumns` | GREEN | 2026-04-30 |
| **C-03** | "9 structural fields per first_seen / upgrade event" (mirror_count_bucket, background_mode, delete_policy, has_hooks, has_filters, has_alert_webhook, has_bandwidth_limit, rclone_version, plus version + install_method + os_family) | PRIVACY.md "Tier 2 — Standard" | `cmd/smirror/cmd_telemetry_test.go` + `system-validation/telemetry_v2_cli_claims_test.go` | `TestTelemetryInspect_FirstSeen_ProducesValidJSON` + `TestTelemetryV2CLI_InspectProducesJSONForFirstSeen` | GREEN | 2026-04-30 |
| **C-04** | "Reliability snapshot: 7 bucketed dimensions" (anomaly_count_bucket, most_common_anomaly_kind, sync_attempts_bucket, sync_failures_bucket, restart_count_bucket, max_queue_depth_bucket, dead_letter_count_bucket, state_db_size_bucket — note PRIVACY.md says 7, schema has 8 dimensions; reconcile in next pass) | PRIVACY.md "Tier 3 — Reliability" | `cmd/smirror/cmd_telemetry_test.go` | `TestTelemetryInspect_Reliability_AddsReliabilityFields` | GREEN | 2026-04-30 |
| **C-05** | "Bug-report bucket: kind, surface, version, severity_hint, source, submitted_tier" | PRIVACY.md "bug_report event" | `cmd/smirror/cmd_report_bug_submit_test.go` | `TestBuildBugReportPayload_OneShot` + `TestBuildBugReportPayload_StandardTier` + `TestBuildBugReportPayload_ReliabilityTier` + `TestBuildBugReportPayload_NoNarrativeFields` (this last one asserts a forbidden-fields list — stronger than the claim) | GREEN | 2026-05-02 |
| **C-06** | "k-anonymity floor of 5 in published digests" (cells with count < 5 suppressed) | PRIVACY.md "Forward commitment" | `scripts/test_telemetry_report.py` | `test_k_anon_filter_*` (5 cases: below floor, missing field, empty input, all-below, k_anon_guard) | GREEN | 2026-04-30 |
| **C-07** | "No heartbeats, ever." (only `first_seen` and `upgrade` events on install-telemetry channel) | PRIVACY.md "Forward commitment" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_EventKindEnumNoHeartbeat` | GREEN | 2026-04-30 |
| **C-08** | "No accumulated counts." (no bytes-mirrored, files-synced, uptime, error-counts continuous metrics in any rollup) | PRIVACY.md "Forward commitment" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoAccumulatedCountColumns` | GREEN | 2026-04-30 |
| **C-09** | "No geography." (no timezone, locale, language, IP-derived data anywhere in payload or storage) | PRIVACY.md "Forward commitment" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoGeoFields` | GREEN | 2026-04-30 |
| **C-10** | "No hardware fingerprint." (no CPU/memory/disk-class fields) | PRIVACY.md "Forward commitment" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoHardwareFingerprintFields` | GREEN | 2026-04-30 |
| **C-11** | "Bucketization mandatory for any numeric field." | PRIVACY.md "Forward commitment" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoUnbucketedNumerics` | GREEN | 2026-04-30 |
| **C-12** | "install_id is verified for HMAC and discarded the same millisecond." | PRIVACY.md "How a contribution works" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoInstallIDColumn` | GREEN | 2026-04-30 |
| **C-13** | "IP addresses are hashed with daily-rotating salt; never raw in storage." | PRIVACY.md "Where the data lives" | `system-validation/telemetry_v2_worker_claims_test.go` | `TestTelemetryV2Worker_IPNeverInKVKey` | GREEN | 2026-04-30 |
| **C-14** | "Same IP within UTC day → same KV key (counter accumulates); across days → different keys (linkability broken at 24h)." | PRIVACY.md "How a contribution works" | `system-validation/telemetry_v2_worker_claims_test.go` | `TestTelemetryV2Worker_RateLimitKeyDateInMessage` (asserts utcDate-in-message + HMAC-SHA-256 + 16-byte truncation) | GREEN | 2026-04-30 |
| **C-15** | "Bug-report narratives stay on GitHub." (no copies in changelogs, digests, READMEs) | PRIVACY.md "What is no longer telemetry" | `system-validation/telemetry_v2_cli_claims_test.go` | `TestTelemetryV2Artifacts_NoQuotedNarrativeFragments` | GREEN | 2026-04-30 |
| **C-16** | "Worker exposes only /v1/contribute; /v1/forget and legacy paths return 410 Gone." | PRIVACY.md "Where the data lives" + worker README | `system-validation/telemetry_v2_worker_claims_test.go` | `TestTelemetryV2Worker_RetiredPathsCoverLegacyAndForget` + `TestTelemetryV2Worker_AllowedPathsExactlyContribute` | GREEN | 2026-04-30 |
| **C-17** | "There is nothing to delete (under v2). `forget` is not a command." | PRIVACY.md "Your rights" + cli-telemetry-command.md | `system-validation/telemetry_v2_cli_claims_test.go` | `TestTelemetryV2CLI_ForgetSubcommandRejected` | GREEN | 2026-04-30 |
| **C-18** | "Build-key fingerprint visible in `smirror version` so users can confirm telemetry signing is enabled." | (implicit from BuildKeyFingerprint() existence) | `system-validation/telemetry_test.go` + `system-validation/telemetry_v2_cli_claims_test.go` | `TestTelemetryVersionReportsBuildKeyFingerprint` + `TestTelemetryV2CLI_VersionReportsBuildKeyFingerprint` | GREEN | 2026-04-30 |

## Architecture-v2 claims (`docs/telemetry-architecture-v2.md`)

| ID | Claim | Doc anchor | Test file | Test ID | Status | Last green |
|----|-------|------------|-----------|---------|--------|-----------|
| **A-01** | "HMAC verify is constant-time enough that there's no useful timing attack." | architecture-v2 "Threat model" | (PENDING) | `TestVerifyHmac_TimingBoundedWithinThreshold` (benchmark, p99 deviation < 5%) | RED | (deferred — requires perf-harness session) |
| **A-02** | "Replay can only over-count, not exfiltrate." (architectural — no row created) | architecture-v2 "Threat model" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoInsertOutsideRollups` | GREEN | 2026-04-30 |
| **A-03** | "pg_stat_statements does NOT see payload literals." (PostgREST normalizes parameters to $1, $2) | architecture-v2 "Logging guards" | (PENDING) | `TestPgStatStatements_NoPayloadLiterals` (smoke contribute, query stat_statements, assert no JSON) | RED | (deferred — requires live-Supabase fixture) |
| **A-04** | "telemetry.contribute() returns 200 with `{ok:false, error:...}` on rejection (so client can read the reason)." | architecture-v2 "telemetry.contribute() pseudocode" | `scripts/telemetry-v2-smoke-test.py` | `case_bad_hmac` | GREEN | (smoke test against fresh harness) |
| **A-05** | "Function dispatches by `event_kind`; unknown kind rejected with `unknown_event` error code." | architecture-v2 "Event kinds" | `scripts/telemetry-v2-smoke-test.py` | `case_unknown_event` | GREEN | (smoke test) |
| **A-06** | "Schema violation (bad enum value) rejected with `schema_violation*` error code." | architecture-v2 "telemetry.contribute() pseudocode" | `scripts/telemetry-v2-smoke-test.py` | `case_schema_violation` | GREEN | (smoke test) |
| **A-07** | "Aggregate counters are monotonic — counts only go up." (UPSERT increments, never decrements) | architecture-v2 "Threat model: replay" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_CountersMonotonic` | GREEN | 2026-04-30 |
| **A-08** | "Bug-report narratives are NOT a telemetry event." (only categorical bucket, no narrative column) | architecture-v2 "What we moved off the telemetry path" | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_NoNarrativeColumns` | GREEN | 2026-04-30 |
| **A-09** | "5 acceptance cases pass: bad HMAC, good HMAC, schema violation, unknown event, retired forget." | architecture-v2 "Logging guards" + smoke test | `scripts/telemetry-v2-smoke-test.py` | all five `case_*` | GREEN | (smoke test wired in `.github/workflows/telemetry-emulation.yml`, 2026-04-30) |
| **A-10** | "Bug-kind drift detection: per-version unknown share is queryable." | (new — Mary's round-3 panel fix) | `system-validation/telemetry_schema_claims_test.go` | `TestTelemetryV2Schema_HasBugUnknownShareView` | GREEN | 2026-04-30 |

---

## Status summary (as of 0.9.91-dev — post-validation-pass closures)

| Bucket | Count | % |
|--------|-------|---|
| GREEN | 25 | 89% |
| AMBER | 0 | 0% |
| RED | 3 | 11% |
| **Total claims** | 28 | 100% |

Four-day deltas:
- 2026-04-30 morning (commit db309a6): 6 GREEN → 19 GREEN (+13 from schema-claims + CLI tests).
- 2026-04-30 afternoon, items 1-3 (commit d088cd7): 19 GREEN → 21 GREEN (+1 C-06 AMBER→GREEN; +1 NEW A-10).
- 2026-04-30 evening, Worker structural claims (commit 5e9500a): 21 GREEN → 24 GREEN (C-13 AMBER→GREEN; C-14 + C-16 RED→GREEN).
- 2026-05-02 SM-158 ship + validation pass: 24 GREEN → 25 GREEN (C-05 RED→GREEN — `TestBuildBugReportPayload_*` ship in `cmd/smirror/cmd_report_bug_submit_test.go`).

Remaining RED (both deferred):

1. **A-01** — HMAC timing benchmark. **Deferred** — needs perf-harness session.
2. **A-03** — pg_stat_statements payload-literal absence. **Deferred** — needs live-Supabase fixture not currently available in CI.
3. (only two RED in active state — both pre-deferred)

There are no AMBER claims remaining.

---

## v1.0 validation gate (Quincy's recommendation)

Before tag, the percentage of GREEN claims must be **≥ 90%** of all *non-deferred* claims (excluding A-01 which requires a benchmark harness, and C-15 which is a humans-don't-quote-issues social rule that the CI scan only catches accidents of). Deferred claims must be explicitly listed below with a target version.

**Deferred to v1.0.x:**
- A-01 (HMAC timing benchmark) — needs a separate perf-harness session.
- A-03 (pg_stat_statements payload-literal absence) — needs a live-Supabase test fixture that we can't currently provision in CI.

**Must be GREEN before v1.0:**
- (none — C-05 closed 2026-05-02 with the SM-158 ship.)

**Current state**: 25/28 GREEN = **89.3% total**. With A-01 + A-03 deferred (denominator 26), 25/26 = **96.2%** — **comfortably above Quincy's ≥ 90% gate**.

There are no non-deferred RED claims. Both remaining RED are explicit deferrals to v1.0.x with target work documented above.

---

## How to add a claim

When PRIVACY.md or telemetry-architecture-v2.md adds a new commitment (a "never," "only," "anonymous," or numeric assertion), the same commit MUST add a row to this table with at minimum:
- ID (next free C-NN or A-NN)
- Verbatim or paraphrased claim text
- Doc anchor (file + section)
- Test file path (or `(PENDING)`) and test ID (or `(PENDING)`)
- Status (RED if no test yet; AMBER if test exists but not in CI; GREEN once CI-runnable)

The CI workflow `.github/workflows/telemetry-claims-check.yml` (PENDING) will eventually grep the doc files for new instances of the trigger words and fail if any sentence isn't represented in this table.

---

## How to verify the map is current

Manual checklist (run quarterly):

1. `git log --oneline docs/PRIVACY.md docs/telemetry-architecture-v2.md` since this file's last update.
2. For each diff, check whether it added/changed a claim. If yes, ensure CLAIMS-MAP has a corresponding row.
3. For each GREEN row, run the linked test (or trigger CI) and update "Last green" date.
4. For any AMBER row that's been AMBER > 30 days, escalate (either green it or explain).
5. For any RED row that's been RED > 90 days, escalate to either greening or removing the claim from PRIVACY.md.

If the doc says it but no test backs it, the doc is wrong or the test set is wrong. The map is the forcing function.
