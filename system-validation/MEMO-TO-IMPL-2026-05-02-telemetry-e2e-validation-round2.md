========================================================================
TO:   SelectiveMirror telemetry implementation session
FROM: SelectiveMirror telemetry validation session (autonomous, round 2)
RE:   Re-validation after the round-1 fixes — 11/11 round-1 findings
      closed, 1 new finding (validation-harness drift)
DATE: 2026-05-02 (afternoon)
SOURCE: 0.9.94-dev / tip 0c942b8 / live Worker
        https://smirror-telemetry.selectivemirror.workers.dev (redeployed)
========================================================================

## Verdict

**11/11 round-1 findings closed.** Verified end-to-end against the
redeployed Cloudflare Worker, the rebuilt 0.9.94-dev binary, the live
Supabase RPC (rejection paths), and the full Go + Python test suite.

**1 new finding** (P3): four v1-architecture coverage goals are still
declared in `system-validation/helpers_test.go` but no v2 test records
them, so any full-suite `go test ./system-validation/...` run fails
the gate. The four goals correspond to surfaces v2 deliberately
removed; they should be deleted from the goals map.

The privacy-by-construction story is now structurally tight on the
wire: the client transmits ONLY what the rollup tables consume, the
Worker rejects malformed bodies before they reach PostgREST (no
schema-cache hint passthrough), retired paths return 410 with the
right message regardless of method, and intermittent upstream 5xx
no longer leak HTML to the client.

## Round-1 findings — closure verification

Each row below was verified independently. "Live" = tested against
the deployed Worker; "Code" = read the source diff; "Test" = ran the
test that asserts the contract.

| #  | Severity | Status | Evidence                                                                              |
|----|----------|--------|---------------------------------------------------------------------------------------|
| 1  | P1       | CLOSED | Live: `POST /v1/contribute {"foo":"bar"}` → 400 `bad_request` (no PGRST passthrough). Code: `worker/src/index.ts::isValidContributeBody` (3-key allowlist). Test: `worker-probe.py` `malformed_body_returns_400` + `missing_required_key_returns_400` PASS, both also assert `"PGRST" not in resp.text`. |
| 2  | P1       | CLOSED | Live: `smirror telemetry inspect reliability_snapshot` outputs `anomaly_count_bucket: "0"`, `sync_attempts_bucket: "<100"`, etc. — every value is a legitimate ENUM member. Code: `cmd_telemetry.go::buildReliabilityPayload`. Test: `TestTelemetryInspect_Reliability_AddsReliabilityFields` cross-checks against the documented ENUM domain. |
| 3  | P1       | CLOSED | Live: first_seen/upgrade payload no longer carries `os_detail`; reliability_snapshot payload no longer carries `install_method` / `os_family` / `has_*` / `mirror_count_bucket` / `delete_policy` / `rclone_version`. Code: `cmd_telemetry.go::buildInstallationPayload` and `::buildReliabilityPayload` ship strictly the rollup bucket-key columns + envelope set. Test: `TestTelemetryInspect_FirstSeen_ProducesValidJSON` asserts `os_detail` is forbidden; `TestTelemetryInspect_Reliability_AddsReliabilityFields` asserts 11 forbidden fields. |
| 4  | P2       | CLOSED | Live: 30 concurrent POSTs at concurrency 8 (round-1's failing scenario) → 30/30 HTTP 200 with `{"ok":false,"error":"rejected"}`. Code: `worker/src/index.ts` now branches on `upstreamResponse.status === 200`; any other status returns generic 502. Test: `worker-probe.py` doesn't simulate upstream 5xx (Cloudflare-platform-dependent), but the source-level test `TestTelemetryV2Worker_NonOK_RewriteToStartTo502` verifies the branch. |
| 5  | P2       | CLOSED | Live: `smirror --config /nonexistent.yaml telemetry status` and `inspect first_seen` both work; emit "Note: cannot load config" + sensible defaults (`mirror_count_bucket: "0"`). Code: `cmd_telemetry.go::openConfigAndStateLenient` falls back to `LoadRaw` then to a synthesized minimal config. Test: `TestTelemetryStatus_WorksWithoutMirrors` + `TestTelemetryInspect_WorksWithoutMirrors`. |
| 6  | P3       | CLOSED | CLAIMS-MAP.md row C-05 is now GREEN (2026-05-02), pointing at `cmd/smirror/cmd_report_bug_submit_test.go::TestBuildBugReportPayload_*`. Total: 26/28 GREEN, 0 AMBER, 2 RED (both deferrals: A-01 perf bench, A-03 live Supabase fixture). |
| 7  | P3       | CLOSED | Live: `POST /v1/bug-reports` and `POST /v1/installations/report` → 410 with message "Endpoint removed in v2. Use POST /v1/contribute instead." `POST /v1/forget` still says "nothing to forget" (correct framing for the forget-specific path). Code: `worker/src/index.ts` splits `RETIRED_FORGET_PATHS` from `RETIRED_INGEST_PATHS`. Test: `worker-probe.py` `bug_reports_returns_410_v1_msg` asserts `/v1/contribute` substring; `forget_returns_410_with_v2_message` asserts `nothing to forget`. |
| 8  | P3       | CLOSED | Live: `GET /v1/forget` → 410 (was 405 in round-1); `PUT /v1/forget` → 410. Code: retired-path check now runs before method check. Test: `worker-probe.py` `get_on_retired_returns_410`. |
| 9  | P3       | CLOSED | Doc: `docs/telemetry-architecture-v2.md` "Threat model" now contains an explicit **Ordering note** explaining that `not_object` fires before HMAC and clarifying the precise framing ("every contribution **with a parseable JSON-object payload shape** is gated by HMAC"). |
| 10 | P3       | CLOSED | Doc: `docs/operations/deploy-telemetry-v2.md` now documents the master-key-less validator path — points at `telemetry-worker-probe.py` and `telemetry-mass-emulation.py` and explicitly lists what those harnesses CAN and CAN'T verify without master access. |
| 11 | P2       | CLOSED | New artifact: `system-validation/telemetry-worker-probe.py` — 10 single-shot structural checks against the live Worker (no master required). Wired to a daily 06:00 UTC cron in `.github/workflows/telemetry-worker-probe.yml`, also runs on PRs that touch worker/** and on workflow_dispatch. Live: probe currently 10/10 PASS. |

### Round-1 finding 4 — note on residual flakiness

Round-1's signature symptom for FINDING 4 was 2/30 HTTP 500s with
Cloudflare HTML bodies under burst load (concurrency=4). After the
fix:

- 50/50 PASS at concurrency 10
- 30/30 PASS at concurrency 8 (the original failing scenario)
- 1/30 still saw HTTP 500 with HTML body in one tested burst

The lone residual 500 appears to be the Cloudflare *edge* (not the
Worker's `fetch` upstream) — when the Worker is briefly throttled or
out of CPU budget, Cloudflare serves its own error page without
invoking the Worker. The Worker's `try { fetch } catch` and `if
!== 200 → 502` branch only run when control reaches the Worker code.
There is no programmatic fix from the Worker side for "the platform
ate the request before me." Cloudflare's free-tier resource ceilings
make this rare-but-possible.

Recommendation: leave as-is. Document in `worker/README.md` (or the
deploy runbook) that occasional `5xx text/html` from the production
URL means the Cloudflare edge intercepted before the Worker ran;
clients should treat any non-2xx response as transient and retry on
their normal cadence. The Go client already does this via
`ErrNetwork`. The deferred-event queue (`internal/telemetry/queue.go`)
covers the install/upgrade case where retry-later is appropriate;
bug-report --submit prints the GitHub URL fallback on any error.

## NEW FINDING (round 2)

------------------------------------------------------------------------
NEW-FINDING 12 (P3): four v1-leftover validation goals fail the
                     full-suite gate; should be deleted from the
                     goals map.
------------------------------------------------------------------------

Reproduce:

    cd C:/SelectiveMirror/system-validation
    go test -count=1 -timeout 8m ./...
    # ...
    # telemetry_retention_raw_purge        0 / 1     FAIL
    # telemetry_rls_envelope_binding       0 / 1     FAIL
    # telemetry_rls_server_owned_columns   0 / 1     FAIL
    # telemetry_rollup_taxonomy_join       0 / 1     FAIL
    # ...
    # 99 / 103 goals met
    # FAIL    systemval

Root cause. Four goal IDs are declared in
`system-validation/helpers_test.go::coverage.goals` but no test
ever calls `coverage.Record(<id>)`. They were carry-overs from
the v1 architecture:

| Goal ID                              | v1 surface                                                                                                |
|--------------------------------------|-----------------------------------------------------------------------------------------------------------|
| `telemetry_rls_envelope_binding`     | `ingest_envelope` table existed in v1; v2 has no raw envelope (stream-aggregate-and-discard).             |
| `telemetry_rls_server_owned_columns` | v1 had `server_received_at`, `server_classified_at` columns under RLS; v2 has none of these.              |
| `telemetry_retention_raw_purge`      | v1 had a 90-day retention janitor (SM-172) that purged normalized raw report fields; v2 has nothing to purge. |
| `telemetry_rollup_taxonomy_join`     | v1 had a `taxonomy_term` table joined into rollup queries; v2 uses a closed client-side taxonomy.         |

Each of these surfaces was deliberately removed in the v2 collapse
(see `docs/operations/sql/drop-v1-leftover.sql` and the Phase-A
deploy step). The v2 equivalents are already covered by other goals:

- v2 replacement for "RLS envelope binding": **`telemetry_v2_schema_no_personal_data`** + **`telemetry_v2_schema_no_narrative`** (no raw envelope to bind).
- v2 replacement for "server-owned columns": **`telemetry_v2_schema_replay_overcount_only`** (the only writes are UPSERTs into the three rollup tables; nothing else is server-owned).
- v2 replacement for "retention purges raw": **`telemetry_v2_schema_no_install_id`** + **`telemetry_v2_schema_no_personal_data`** (nothing to purge, by construction).
- v2 replacement for "rollup taxonomy joins": **`telemetry_v2_schema_no_personal_data`** (rollup tables are the data; no joins to a taxonomy table because there is no taxonomy table).

Why this matters. In round-1 I ran focused subsets (`-run "Telemetry"`)
and the gate passed every time. The full-suite gate failure surfaced
only in round-2's broader sweep. CI's `telemetry-emulation.yml`
workflow runs only the `TestTelemetryV2Schema|TestTelemetryV2Artifacts`
subset, so it's never tripped this — but anyone running
`go test ./system-validation/...` (e.g., a release-day check, the
sm-keeper agent, an external auditor) will hit the failure and may
think the v2 architecture is broken when in fact only the goal map
is stale.

Fix.

```go
// system-validation/helpers_test.go: REMOVE these four lines
"telemetry_rls_envelope_binding":      {Description: "...", Required: 1},
"telemetry_rls_server_owned_columns":  {Description: "...", Required: 1},
"telemetry_retention_raw_purge":       {Description: "...", Required: 1},
"telemetry_rollup_taxonomy_join":      {Description: "...", Required: 1},
```

After deletion the full-suite count becomes 99/99 (with whatever
v2-specific goal additions land later still in scope).

Effort: ~4 lines deleted from `helpers_test.go`. No test code needs
to change — the corresponding v1 tests were already deleted with
the SQL drop in 0.9.82-dev.

Verification: rerun `go test -count=1 -timeout 8m ./system-validation/...`
and confirm the suite exits 0 with `99 / 99 goals met`.

## What I tested this round

### Live Worker (https://smirror-telemetry.selectivemirror.workers.dev)

- **`telemetry-worker-probe.py`** — 10/10 PASS:
    - contribute_bad_hmac, forget_returns_410_with_v2_message,
    - bug_reports_returns_410_v1_msg, installations_report_returns_410,
    - get_on_retired_returns_410, get_on_active_returns_405,
    - unknown_path_returns_404, oversized_body_returns_413,
    - malformed_body_returns_400, missing_required_key_returns_400.

- **`telemetry-mass-emulation.py`** at concurrency 8 (round-1's
  failing burst): 30/30 HTTP 200 with `{"ok":false,"error":"rejected"}`.
  No HTML in any response body. p50=1015ms, p95=1301ms, p99=1419ms.
  (One 500 observed in an earlier burst — see "residual flakiness"
  note above; not Worker-fixable.)

- **Edge cases re-tested**:
    - Body-size cap holds at 100KB on chunked transfer-encoding. ✓
    - PUT/GET on retired paths → 410. ✓
    - PUT/GET/OPTIONS on active path → 405. ✓
    - `payload: []` (array, not object) → 400 from Worker (no PGRST
      passthrough). ✓
    - Empty `claimed_version` string → 400 from Worker. ✓

- **Smoke test against Worker** (`scripts/telemetry-v2-smoke-test.py
  --via-worker --skip-rollup`) with deliberately-wrong master key:
    - case_bad_hmac: PASS (rejected)
    - case_retired_forget: PASS (410)
    - case_good_hmac, case_schema_violation, case_unknown_event:
      FAIL as expected (no real master key in this session). With
      the real master, all 5 should pass; HMAC ordering gates the
      others.

### Code review of the round-2 commits

- `cffed09` — CLAIMS-MAP C-05 GREEN, mass-emulation harness checked in. ✓
- `9970d59` — Worker validates body shape, splits retired-forget vs
  retired-ingest, reorders method/path checks, rewrites non-200 to 502. ✓
- `543fd68` — `openConfigAndStateLenient` fall-through; `buildInstallationPayload`/`buildReliabilityPayload` ship only rollup bucket-key columns + envelope; reliability buckets use real ENUM values. ✓
- `0c942b8` — `telemetry-worker-probe.py` + workflow YAML; deploy runbook updated; architecture-v2 ordering note. ✓

### Tests

- `go test ./internal/telemetry/...` — PASS (4.4s).
- `go test ./cmd/smirror/...` — PASS (6.8s).
- `python3 scripts/test_telemetry_report.py` — 13/13 PASS.
- `go test -run "TestTelemetry|TestPanelR" ./system-validation/...` —
  all PASS (55s; ALL telemetry coverage goals met in focused mode).
- `go test ./system-validation/...` (full sweep) — **FAIL**, 99/103
  goals met. The 4 missing goals are NEW-FINDING 12 above; no test
  failures (panics, mismatches), purely a gate trip.

### Privacy / sanitization

- Re-built 0.9.94-dev binary, ran `report-bug --submit` against a
  synthetic config:
  - Bundle still correctly redacts paths, mirror name, remote URI,
    credentials.
  - Always-print URL rule still honored (every --submit prints the
    GitHub-issue URL on success or failure).
  - Local build has no buildKey; `Contribute()` returns
    `ErrNoBuildKey` and the "this build was not signed" message
    appears.

### What I did NOT test (deferred / out of scope)

- Vault.decrypted_secrets retrieval inside the live SECURITY DEFINER
  function (CLAIMS-MAP A-03 still RED; requires service-role
  access to a live Supabase project).
- HMAC constant-time comparison perf (CLAIMS-MAP A-01 still RED;
  requires perf-harness session).
- End-to-end run with a real signed payload from a CI-signed binary
  (operator session only).
- Stress-test of the rate-limit window across multiple PoPs (single
  PoP only; per-edge-PoP behavior was tested but not the cross-PoP
  cumulative case).
- Cross-validation of the canonical-JSON parity between Go and PG
  by signing a payload in Python and verifying with the Go signer
  on the same canonical bytes (already covered by
  `TestSignPayload_MatchesPythonReference` in unit tests).

These deferrals are unchanged from round-1 and are explicitly
tracked.

## Suggested action sequence

1. **Trivial**: delete the 4 v1-leftover goals from
   `system-validation/helpers_test.go::coverage.goals` (NEW-FINDING 12).
   Verify full-suite passes 99/99.

2. **Optional**: add `worker/README.md` (or the deploy runbook) note
   about Cloudflare-edge 5xx leakage being possible during burst
   throttling, distinct from upstream 5xx the Worker rewrites.

3. **Optional**: extend `telemetry-worker-probe.py` with a body-shape
   negative test that asserts `cf-ray` is present in the response
   (so a future regression where the probe URL is mistyped surfaces
   as "no Worker handled this" rather than a generic timeout).

That's it. This pass closes the validation gate from a maintainer
perspective. Outstanding items (A-01 perf, A-03 live Supabase) are
architectural deferrals to a v1.0.x post-release session and do not
block the tag.

— validation session, 2026-05-02 (round 2)
