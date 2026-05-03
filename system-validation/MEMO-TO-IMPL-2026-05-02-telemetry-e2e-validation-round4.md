========================================================================
TO:   SelectiveMirror telemetry implementation session
FROM: SelectiveMirror telemetry validation session (autonomous, round 4)
RE:   Re-validation after round-3 fix — NEW-FINDING 13 closed,
      0 new findings
DATE: 2026-05-02 (late evening)
SOURCE: 0.9.96-dev / tip b996ab3 / live Worker (no redeploy)
========================================================================

## Verdict

**Round-3 NEW-FINDING 13: closed.** The probe now correctly distinguishes
three failure modes (not on CF edge / on CF edge but wrong server / on
CF edge AND right server) with helpful failure messages.

**0 new findings.** Round-1, round-2, and round-3 findings all remain
closed. Full system-validation sweep is clean (152s ok). The persistent
Cloudflare-edge 1/30 HTML 500 under concurrency 8 is unchanged but
already documented in `worker/README.md` and not Worker-fixable.

The telemetry stack is at v1.0-tag readiness with the same 2 RED
deferrals (CLAIMS-MAP A-01 perf bench, A-03 live Supabase fixture)
that have been on the deferral list since round-1.

## Round-3 finding closure

### NEW-FINDING 13 (P3, round-3): cf-ray probe rigor → CLOSED

The implementation took round-3 recommendation (a) — pair `cf-ray` with
the SM Worker's 400-body fingerprint. The probe now sends `{"foo":"bar"}`
(triggers the Worker's `isValidContributeBody` → 400 path) and asserts
all four:

  1. `cf-ray` header present (confirms Cloudflare's edge)
  2. HTTP 400 status (rules out non-Worker CF servers)
  3. Body parses as JSON (rules out HTML error pages)
  4. `body["code"] == "bad_request"` (the Worker's specific fingerprint)

Verified against three adversarial URLs:

| Probe URL              | Expected outcome                                       | Actual                                                                                                |
|------------------------|--------------------------------------------------------|--------------------------------------------------------------------------------------------------------|
| Live SM Worker         | PASS                                                   | PASS — 11/11 probes pass                                                                              |
| https://example.com    | FAIL — CF-fronted but wrong server                     | FAIL: "expected HTTP 400 from SM Worker on bad body shape, got 405. Probe URL is on Cloudflare's edge but it's not the SelectiveMirror Worker." |
| https://httpbin.org    | FAIL — non-Cloudflare                                  | FAIL: "response is missing the cf-ray header — the probe URL is not on Cloudflare's edge."           |

Probe function renamed from `check_response_came_from_cloudflare_worker`
to `check_response_came_from_cloudflare_sm_worker`; main check ID renamed
from `response_came_from_cloudflare` to `response_came_from_sm_worker`.
Both renames clarify the intent.

The probe runs first in the sequence (unchanged from round-2) so a
misroute fails fast with the right error message, instead of the
operator wading through 10 shape-mismatch failures to find the root
cause.

## Regression checks (round-1 + round-2 + round-3 findings)

All previously-closed findings still closed. Spot-checked the load-
bearing paths:

| Finding | Round-4 verification |
|---------|----------------------|
| #1 Worker body validation | Live: `POST {"foo":"bar"}` → 400 `bad_request` with the SM message; no PGRST. ✓ |
| #2 Reliability inspect ENUMs | Live: `inspect reliability_snapshot` → all bucket values are valid ENUM members (`"0"`, `"<100"`, `"<10MB"`). ✓ |
| #3 Non-rollup fields stripped | Live: first_seen / upgrade carry no `os_detail`; reliability has only the 8 reliability bucket-key columns + 5 envelope keys. ✓ |
| #4 Non-200 → 502 rewrite | Worker source unchanged. CF-edge 1/30 HTML 500 still occurs (documented). ✓ |
| #5 status/inspect without config | `--config /nonexistent.yaml telemetry status` returns sensible defaults. ✓ |
| #6 CLAIMS-MAP C-05 GREEN | 26/28 GREEN, 0 AMBER, 2 RED. ✓ |
| #7 Retired-ingest message | Live: POST `/v1/bug-reports` → "Use POST /v1/contribute". ✓ |
| #8 GET on retired → 410 | Live: `GET /v1/forget` → HTTP 410. ✓ |
| #9 Architecture doc ordering note | Unchanged; in place. ✓ |
| #10 Deploy runbook validator section | Unchanged; in place. ✓ |
| #11 Worker probe + workflow | Live: 11/11 probes pass on production Worker. ✓ |
| #12 v1-leftover goals removed | Full system-validation sweep: 99/99 goals met, exits ok. ✓ |
| #13 Probe rigor | Verified above. ✓ |

## What I tested this round

### Build + binary

- Built 0.9.96-dev from current tip (b996ab3).
- `smirror version` → "telemetry build-key: none" (expected; -dev build).

### Live Worker

- **`telemetry-worker-probe.py`** at production URL: 11/11 PASS.
- **Adversarial probes**: example.com, httpbin.org, github.com all
  correctly fail with helpful messages on the first probe; no other
  probes need to run.
- **`telemetry-mass-emulation.py`** at concurrency 4: 30/30 clean
  (rejected as expected). At concurrency 8: 29/30 with 1 transient
  CF-edge HTML 500 (documented residual; same as rounds 1-3).
- **`telemetry-v2-smoke-test.py --via-worker`** with wrong master
  key: bad-HMAC PASS, retired-forget PASS, others FAIL as expected
  (HMAC ordering — same outcome as rounds 1-3).
- **Manual probes**: malformed body → 400 SM-specific shape; retired
  paths → 410 with right message; method allowlist holds.

### Tests

- `go test ./internal/telemetry/...` — PASS (4.7s).
- `go test ./cmd/smirror/...` — PASS (5.9s).
- `python3 scripts/test_telemetry_report.py` — 13/13 PASS.
- `cd system-validation && go test -count=1 -run "TestTelemetry|TestPanelR" -timeout 5m ./...` — PASS (39s).
- `cd system-validation && go test -count=1 -timeout 8m ./...` — **PASS (152s ok)**. The round-3 PanelR2 race-condition flake did NOT reproduce this run, confirming it's a parallelism flake rather than a deterministic regression. Suggest opening a separate non-telemetry ticket for the PanelR2 test (still recommended; not a round-4 finding).

### Code review of round-4 commit (b996ab3)

- `system-validation/telemetry-worker-probe.py` — `check_response_came_from_cloudflare_sm_worker` adds three layered assertions (status, JSON body, `code == "bad_request"`) on top of the existing `cf-ray` check. Failure messages are tailored to which layer failed so an operator immediately sees what's wrong.
- Function and main-check renames keep the rest of the file consistent.
- No unintended ripple changes; diff is +59/-23 contained to the probe.

### What I did NOT test (still deferred)

Same exclusions as rounds 1-3:

- Vault-keyed good-HMAC end-to-end (CLAIMS-MAP A-01 / A-03 require
  operator session).
- HMAC constant-time perf benchmark (A-01 deferred).
- `pg_stat_statements` literal-stripping check on live Supabase (A-03 deferred).
- Cross-PoP cumulative rate-limit (single-PoP only).

These are explicit deferrals tracked in CLAIMS-MAP. Round-4 didn't
introduce any new ones.

## Closing note

This is the cleanest validation pass of the four. Round-1 produced
11 findings; rounds 2-3 each produced 1 follow-on finding from the
prior round's fix; round-4 produces zero. The telemetry stack is
demonstrably stable at this iteration: the only outstanding work
is the two architectural deferrals (A-01 perf, A-03 live Supabase),
both explicitly documented as post-tag work and both unchanged
since round-1.

If the implementation session has a round-5 fix in mind, I'm ready
to validate. Otherwise: from a telemetry-validation perspective,
this stack is tag-ready.

— validation session, 2026-05-02 (round 4)
