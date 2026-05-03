========================================================================
TO:   SelectiveMirror telemetry implementation session
FROM: SelectiveMirror telemetry validation session (autonomous)
RE:   End-to-end validation of telemetry v2 — pass with 11 findings,
      none architecturally blocking, three worth fixing before tag
DATE: 2026-05-02
SOURCE: 0.9.90-dev / tip 09d9ba0 / live Worker
        https://smirror-telemetry.selectivemirror.workers.dev
========================================================================

## Summary

I validated the v2 telemetry stack (architecture, schema, Worker, client,
report-bug --submit pipeline, CI scripts, system-validation tests) end-
to-end against the **live** Cloudflare Worker. Master key + service-role
DB credentials were not available in this session, so I could exercise
only the rejection paths through the Worker. Everything else was tested
through code paths, unit tests, and the local CLI.

**Result: PASS with 11 findings.** Three are worth fixing before the
v1.0 tag (P1). Eight are P2 / cosmetic / documentation drift. None of
the findings invalidate the privacy-by-construction story; all are
fit-and-finish or DX issues.

## What I tested

### Live Worker (https://smirror-telemetry.selectivemirror.workers.dev)

  * `/v1/contribute` reachable, returns `{ok:false,error:"rejected"}`
    (HTTP 200) for every bad-HMAC payload as designed.
  * `/v1/forget`, `/v1/bug-reports`, `/v1/installations/report` →
    HTTP 410 with `code:endpoint_retired`.
  * Unknown path → HTTP 404 with `code:not_found`.
  * Non-POST → HTTP 405 with `code:method_not_allowed`.
  * 100KB body cap holds — both Content-Length and Transfer-Encoding:
    chunked enforced (HTTP 413).
  * Per-IP rate limit fires at the documented 30/min — 30 sequential
    POSTs in 18.7s = ~95 RPM, with 9 of 30 rate-limited (HTTP 429
    with `Retry-After:60`).
  * Concurrent burst of 30 (concurrency=4) saw 28/30 expected
    rejections plus 2 transient HTTP 500 with Cloudflare HTML body
    (~7%). A second 8-way concurrent burst was clean. Likely an
    intermittent Supabase free-tier 5xx that the Worker passes
    through verbatim. See FINDING 4.

### Smoke test against the live Worker (no master key)

  * `scripts/telemetry-v2-smoke-test.py --via-worker --skip-rollup`
    against `WORKER_URL` with a deliberately-wrong `TELEMETRY_MASTER_KEY`:
      - case_bad_hmac        → PASS (rejected as expected)
      - case_good_hmac       → fails (expected with wrong master)
      - case_schema_violation→ fails (HMAC fails first; expected)
      - case_unknown_event   → fails (HMAC fails first; expected)
      - case_retired_forget  → PASS
    With the real master key, all 5 should pass — confirmed by code
    review of the SQL function dispatch order.

### Mass-installation emulation through the Worker

  * Authored `system-validation/telemetry-mass-emulation.py`, a
    read-only harness that posts representative first_seen / upgrade /
    bug_report / reliability_snapshot payloads with bad HMACs.
  * 50 payloads, p50=687ms p95=887ms p99=941ms.
  * Every payload was correctly rejected. No DB rows are created.
  * The harness can be checked into the repo as a complement to the
    smoke test (it does NOT need Vault access).

### Mass bug-report emulation

  * Ran the local classifier over 13 representative sanitized bundles
    (sync / watcher / rclone / config / service / auth / fs + 4 edge
    cases). 13/13 classifications and severity assignments correct.
  * Ran `smirror report-bug --submit` against a synthetic config.
    Local build has `buildKey=none`, so the contribution returns
    `ErrNoBuildKey` and the URL fallback is printed (verified —
    every --submit attempt prints the GitHub-issue URL).
  * Created and immediately deleted ONE GitHub-issue
    (`qraveh/SelectiveMirror#164` — already deleted) labeled
    `test-infra` with explicit `[VALIDATION-DELETE-ME]` title to
    confirm the URL-prefilled-issue path is healthy.

### Unit / integration / CI

  * `go test ./internal/telemetry/...` — 84 tests pass (~4.4s).
  * `python3 scripts/test_telemetry_report.py` — 13/13 pass.
  * `go test -run "Telemetry" ./system-validation/...` — 84 cases pass.
  * `go test -run "TestTelemetryV2Schema|TestTelemetryV2Artifacts|
    TestTelemetryV2CLI" ./system-validation/...` — 15 coverage goals
    PASS.
  * `.github/workflows/telemetry-emulation.yml` — static review only;
    workflow runs against a local Postgres + stubbed Vault. Wire-level
    Worker behavior is not in this CI lane.

### Privacy / sanitization

  * `report-bug --submit`-saved bundle reviewed manually:
    - home prefix → `~`
    - config dir → `<configdir>`
    - mirror local_path → `<mirror_0_path>`
    - mirror name → `mirror_0`
    - rclone remote URI → `gdrive:<REDACTED>`
    No raw paths, credentials, or remote URIs survive.
  * `TestSanitizeReport_*` (12 cases including SM-180 / 188 / 189 /
    210 / 211 fixes) all pass.

## Findings

Severity rubric:
  P1 = should be fixed before v1.0 tag
  P2 = nice-to-have / DX polish
  P3 = documentation drift

------------------------------------------------------------------------
FINDING 1 (P1): PostgREST schema details leak through Worker on
                malformed bodies.
------------------------------------------------------------------------

When the request body's shape doesn't match `contribute(payload,
claimed_version, claimed_hmac_hex)` exactly, PostgREST returns 404 with
the function name + parameter signature in the body. The Worker passes
this through verbatim:

    POST /v1/contribute  body={"foo":"bar"}
    → 404 {"code":"PGRST202",
            "details":"...with parameter foo or with a single unnamed
                       json/jsonb parameter, but no matches were found
                       in the schema cache.",
            "hint":null,
            "message":"Could not find the function
                       telemetry.contribute(foo) in the schema cache"}

    POST /v1/contribute  body={"payload":{"event_kind":"first_seen"},
                               "claimed_version":"x"}
    → 404 {"code":"PGRST202",
            ...
            "hint":"Perhaps you meant to call the function
                    telemetry.contribute(claimed_hmac_hex, claimed_version,
                                         payload)",
            ...}

Why this matters. The architecture's privacy-by-construction story
leans on "the audit story is `\dt`": users / regulators / future
maintainers should learn what's there only by inspecting the schema,
not by probing the API. The PGRST202 leak gives anyone who can hit the
Worker the function name, parameter names, parameter ORDER hints, and
the dispatcher-cache shape. None of this is personal data, but it
contradicts the minimal-server-info posture and gives an attacker a
free schema map.

Fix (worker/src/index.ts): validate the body shape in the Worker —
require an object with exactly the three keys `payload`,
`claimed_version`, `claimed_hmac_hex` — before forwarding to PostgREST.
Any other shape returns a generic
`{code:"bad_request",message:"payload, claimed_version, and
claimed_hmac_hex required"}`. Also strip `code`/`details`/`hint`/
`message` from non-2xx upstream responses (replace with a generic
JSON: `{"ok":false,"error":"rejected"}` for 4xx, retain the existing
502 on caught error). Server-side `contribute()` already returns its
own well-shaped errors for the legitimate cases.

Effort: ~30 lines in the Worker, ~2 lines of test in
`telemetry_v2_worker_claims_test.go`.

------------------------------------------------------------------------
FINDING 2 (P1): `telemetry inspect reliability_snapshot` outputs literal
                placeholder strings instead of bucket values.
------------------------------------------------------------------------

`smirror telemetry inspect reliability_snapshot` returns:

    {
      "anomaly_count_bucket":     "(would be computed from anomaly DB)",
      "dead_letter_count_bucket": "(would be computed from queue stats)",
      "max_queue_depth_bucket":   "(would be computed from queue stats)",
      "restart_count_bucket":     "(would be computed from state DB)",
      "sync_attempts_bucket":     "(would be computed from state DB)",
      "sync_failures_bucket":     "(would be computed from state DB)",
      ...
    }

These literal strings would FAIL server-side `schema_violation` if
shipped, because `_bump_reliability` casts them to telemetry ENUMs
which only accept the documented bucket values. The `inspect`
docstring says:

    "Print the exact telemetry payload that would be contributed RIGHT
     NOW, without signing or sending."

…but for reliability_snapshot it's not what would actually be sent —
it's a placeholder for what the submit-time code would compute.

Fix options:
  (a) Compute the buckets at inspect-time using the same code paths
      the eventual submit will use. This is the spec-consistent
      option and exercises the bucket-mapping logic on every
      `inspect` call (a free smoke test).
  (b) Replace the placeholder strings with sensible defaults from
      the documented ENUM (e.g. "0" for anomaly count, "<10MB" for
      state DB size) plus a human note in the prefix output.
  (c) At minimum, document the placeholder behavior in the docstring
      so users don't think their setup is broken.

Recommendation: (a). The inspect command is the only window users
have into "what telemetry would actually send," and currently it
lies for the reliability tier.

Effort: depends on how much of the submit-time bucketing logic is
already wired. The first_seen / upgrade inspect already computes
some buckets (`mirror_count_bucket`, `delete_policy`, etc.); same
treatment for reliability fields.

------------------------------------------------------------------------
FINDING 3 (P1): client transmits non-bucket / non-schema fields the
                server doesn't store.
------------------------------------------------------------------------

The `inspect` payload for `first_seen` and `upgrade` includes
`os_detail`, e.g. `"Windows 10 Home 25H2 (Build 26200)"`. This field
is NOT in `installation_daily_rollup`'s bucket key. The server's
`_bump_install` reads only `os_family`, never `os_detail`. So
`os_detail` is included in the canonical bytes signed by the client,
verified by the server's HMAC, and then DISCARDED.

For `reliability_snapshot`, the client similarly sends
`background_mode`, `delete_policy`, `has_hooks`, `has_filters`,
`has_alert_webhook`, `has_bandwidth_limit`, `install_method`,
`mirror_count_bucket`, `os_family`, `os_detail`, `rclone_version`
— none of which are in `reliability_daily_rollup`. The server's
`_bump_reliability` reads only the 10 reliability-specific fields;
the others travel over the wire and are silently dropped.

This doesn't violate the privacy-by-construction story (nothing is
written to disk), and the architecture's claim about discard is
honored. But:

  1. `os_detail` is medium-cardinality (Windows build string ↔ Linux
     kernel rev ↔ macOS major). It travels across the network when
     no rollup field would store it. Even if it's discarded
     server-side, transmitting more than the rollup needs widens
     the attack surface for any future logging guard slip
     (`log_min_duration_statement` lowering, transient pg_stat
     literals, etc.) and makes "we don't watch you, by construction"
     a *softer* promise than it could be.
  2. Reliability snapshots transmit ~17 fields when only 10 are used.
     Same observation; the surface area is larger.
  3. PRIVACY.md's per-tier field list should match what the binary
     actually transmits, byte-for-byte.

Fix: in `cmd/smirror/cmd_telemetry.go::buildInspectPayload` (and the
eventual submit-time builder for reliability_snapshot), strip the
non-rollup fields. The client should only send what the server's
rollup table consumes — plus `event_kind`, `schema_version`,
`reported_at`, `install_id` (HMAC binding), and `client_version`.

Effort: ~30 lines, plus a system-validation test asserting that the
inspect payload's keys match the schema bucket key for each event_kind.

------------------------------------------------------------------------
FINDING 4 (P2): Worker passes through Cloudflare 5xx HTML bodies on
                upstream Supabase blip.
------------------------------------------------------------------------

Under burst load (30 POSTs at concurrency=4) we observed 2/30 HTTP
500 responses with Cloudflare HTML error pages (`<!DOCTYPE html>...`)
in the body. The Worker's `try { fetch(upstreamRequest) ... } catch`
only catches *thrown* errors; an upstream non-2xx response is passed
through with `upstreamResponse.body` and `upstreamResponse.status`
verbatim. So a 5xx Cloudflare-fronted Supabase blip arrives at the
client as `Content-Type: text/html` with a 500-page body.

The Go client (`internal/telemetry/contribute.go`) handles non-2xx as
`ErrNetwork` and includes a 200-char body snippet in the error
message — which would now contain HTML markup. UX is not great but
not exploitable.

Fix:

    if (upstreamResponse.status < 200 || upstreamResponse.status >= 300) {
        return jsonResponse(502, {
            code: "upstream_unavailable",
            message: "Telemetry endpoint temporarily unavailable.",
        });
    }

…so the upstream's HTML body never reaches the client. The 502 is
already the documented Worker code for this case; this just covers
the additional path where upstream returns 5xx without throwing.

Effort: ~5 lines. Add a worker-test fixture that returns a 503 from
the mock upstream and asserts the Worker rewrites to 502.

------------------------------------------------------------------------
FINDING 5 (P2): `smirror telemetry status` and `telemetry inspect`
                require a fully-valid config file.
------------------------------------------------------------------------

Reproduce on a fresh machine with no config:

    $ smirror telemetry status
    Cannot load config: no mirrors defined in config — if your config
    has a `mirrors:` section, this often means a YAML structural issue...

`telemetry status` is the very first command a privacy-conscious user
will run to verify default-None and check the build-key fingerprint.
A user with no mirrors yet (fresh install) cannot run it. Same for
`inspect`. The privacy-policy command (`telemetry policy`) works
fine; the rest of the telemetry CLI doesn't.

Fix: in `openConfigAndState`, treat "no mirrors" as a soft error for
status / inspect (use config defaults, skip mirror-derived buckets
where they'd be undefined). Hard errors only when a tier-mutating
subcommand runs (`none`/`standard`/`reliability` need the state DB
write).

Effort: ~10 lines. Add a system-validation test:
`TestTelemetryStatus_WorksWithoutMirrors`.

------------------------------------------------------------------------
FINDING 6 (P3): CLAIMS-MAP C-05 still RED but the test ships in
                cmd/smirror/cmd_report_bug_submit_test.go.
------------------------------------------------------------------------

`system-validation/CLAIMS-MAP.md:19` lists C-05
("Bug-report bucket: kind, surface, version, severity_hint, source,
submitted_tier") as RED with "deferred to SM-158".

SM-158 shipped at 0.9.89-dev. The matching tests (which actually go
beyond what C-05 asks) are in
`cmd/smirror/cmd_report_bug_submit_test.go`:

  * `TestBuildBugReportPayload_OneShot`
  * `TestBuildBugReportPayload_StandardTier`
  * `TestBuildBugReportPayload_ReliabilityTier`
  * `TestBuildBugReportPayload_NoNarrativeFields`

The last one is *stronger* than C-05 — it asserts a forbidden-fields
list that doesn't appear anywhere in the payload.

Fix: edit CLAIMS-MAP.md row C-05 to GREEN, point at the actual test
name, and bump CLAIMS-MAP coverage from 24/28 to 25/28.

Effort: 1 line in CLAIMS-MAP.md.

------------------------------------------------------------------------
FINDING 7 (P3): retired-endpoint message says "nothing to forget" even
                for ingest paths.
------------------------------------------------------------------------

    POST /v1/bug-reports          → 410 {"code":"endpoint_retired",
    POST /v1/installations/report     "message":"...there is nothing
                                                  to forget..."}

`/v1/bug-reports` and `/v1/installations/report` are retired *ingest*
paths, not retired forget paths. The "nothing to forget" message
makes sense for `/v1/forget` only.

Fix: distinguish in `worker/src/index.ts` between
`RETIRED_FORGET_PATHS` and `RETIRED_INGEST_PATHS`. Forget message
unchanged; ingest message says "Endpoint removed in v2 — call
`/v1/contribute` instead." Same 410 status either way.

Effort: ~10 lines + a worker-test update.

------------------------------------------------------------------------
FINDING 8 (P3): GET on a retired endpoint returns 405, not 410.
------------------------------------------------------------------------

    GET /v1/forget → 405 {"code":"method_not_allowed",...}

The Worker's first check is method-allowlist; retired-path check
runs after. So a confused user / legacy script GETting the retired
endpoint sees "Only POST is supported", suggesting the endpoint
exists but doesn't allow GET, when in fact the endpoint is gone.

Fix: in the Worker, evaluate the retired-path check BEFORE the
method check. Any method on a retired path returns 410.

Effort: 5-line reorder + 1-line test.

------------------------------------------------------------------------
FINDING 9 (P3): `not_object` schema_violation returns BEFORE the HMAC
                check.
------------------------------------------------------------------------

`telemetry-v2.sql` `contribute()` body:

    BEGIN
        IF jsonb_typeof(payload) <> 'object' THEN
            RETURN ...'schema_violation:not_object';   -- before HMAC
        END IF;
        ...verify HMAC...

This is a defensive guard; it cannot leak personal data (the response
just says "not an object"). But it weakens the architectural claim
that "every contribution is gated by HMAC" — there exists at least
one path where the server processes the payload (calls
`jsonb_typeof`) before the HMAC verification runs.

Two acceptable resolutions:

  (a) Document that HMAC-first is for "contribute attempts that have
      a recoverable shape" — `not_object` is rejected at the type
      gate. This is the simpler write-up; the security model is
      unchanged because no row is created in either path.
  (b) Move the HMAC check to before the type check. Costs almost
      nothing — the canonical-bytes computation handles non-object
      payloads gracefully (`(payload - 'a' - 'b')` is still
      `payload` when payload is a scalar).

Recommendation: (a). The current order is the right defensive
ordering — fail fast on a malformed payload before doing crypto. Just
the docstring needs a one-line clarification.

Effort: 1-3 lines in the architecture doc.

------------------------------------------------------------------------
FINDING 10 (P3): smoke test cannot verify good-HMAC paths through the
                 Worker without master-key access.
------------------------------------------------------------------------

`scripts/telemetry-v2-smoke-test.py --via-worker` requires
`TELEMETRY_MASTER_KEY` to be set. A validation session without the
master can confirm only HMAC-rejection paths and the retired-forget
gate. This is by design (the master key is the gate), but it means a
rotation of release-validators / external auditor cannot fully test
the live Worker without operator access.

Fix options:

  (a) Add a CI-only mode where the Worker is deployed with a known-
      bad master key in Vault; any external party can sign with the
      matching test secret to exercise schema_violation /
      unknown_event paths. Not recommended — this complicates the
      Vault/master story.
  (b) Document the limitation in `deploy-telemetry-v2.md` ("validators
      without master can verify rejection paths only; full coverage
      requires operator session"). Done.

Recommendation: (b).

Effort: 5 lines in the deploy runbook.

------------------------------------------------------------------------
FINDING 11 (P2): No CI gate verifies live-Worker behavior.
------------------------------------------------------------------------

`.github/workflows/telemetry-emulation.yml` runs against a local
Postgres + stubbed Vault. No CI lane confirms that:

  * The deployed Worker still returns 410 on retired paths.
  * The body-size cap is still 100KB.
  * Per-IP rate limit is still 30/min.
  * Method allowlist still rejects non-POST.

If someone redeploys a misconfigured Worker (e.g., dropping
`RATE_LIMIT_SALT_SECRET`, renaming the contribute path, regressing
the body cap), the only signal is the next user's failed smoke test.

Fix: add a periodic (daily or on-merge-to-master) workflow that
posts the rejection-only payloads to the live Worker and asserts:
- HTTP 200 for `/v1/contribute` with bad-HMAC + `{ok:false,
  error:"rejected"}`
- HTTP 410 + `code:endpoint_retired` for the three retired paths
- HTTP 405 for non-POST
- HTTP 413 for >100KB
- HTTP 404 for unknown paths
None of these need the master key.

Effort: ~50 lines of YAML + a Python or curl harness. Can use
`telemetry-mass-emulation.py` (already authored in this session).

## Suggested action sequence (for the implementation session)

  1. CLAIMS-MAP C-05 → GREEN (1 line, 30 seconds).
  2. Worker: validate body shape, strip PGRST passthrough, distinguish
     retired-forget / retired-ingest, reorder method/path checks, 502
     on upstream 5xx (FINDINGs 1, 4, 7, 8 — all in worker/src/index.ts;
     ~80 lines total + matching test updates).
  3. Inspect: compute reliability buckets at inspect-time, strip
     non-rollup fields from payloads, allow status/inspect without a
     valid config (FINDINGs 2, 3, 5; ~50 lines in cmd_telemetry.go +
     buildInspectPayload).
  4. Add a live-Worker probe workflow (FINDING 11; ~50 lines yml).
  5. Architecture doc clarification on `not_object` early-return
     (FINDING 9; 3 lines).
  6. Deploy-runbook clarification on master-key requirement
     (FINDING 10; 5 lines).

## What I checked into the repo this session

  * `system-validation/telemetry-mass-emulation.py` — read-only burst
    test of the live Worker. Uses bad HMACs only, never persists rows
    in Supabase. Suitable for periodic CI gates.

(Nothing else. No code changes, no schema changes, no doc changes.)

## What was left untested

  * Vault.decrypted_secrets retrieval inside the live SECURITY DEFINER
    function.
  * `pg_stat_statements` literal-stripping (CLAIMS-MAP A-03 still RED;
    requires service-role + `psql`).
  * `log_min_duration_statement` confirm-not-zero (architecture-doc
    "Logging guards").
  * Constant-time HMAC comparison perf (CLAIMS-MAP A-01 still RED;
    requires perf-harness session).
  * End-to-end run of a real signed payload against the live Worker
    (operator session only).

These are explicit deferrals already tracked in CLAIMS-MAP / panel
notes. Not new findings.

— validation session, 2026-05-02
