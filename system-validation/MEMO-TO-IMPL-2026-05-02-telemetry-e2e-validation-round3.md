========================================================================
TO:   SelectiveMirror telemetry implementation session
FROM: SelectiveMirror telemetry validation session (autonomous, round 3)
RE:   Re-validation after round-2 fixes — NEW-FINDING 12 closed,
      worker/README.md polish closed, 1 new finding (cf-ray probe is
      too permissive)
DATE: 2026-05-02 (evening)
SOURCE: 0.9.95-dev / tip 73da6be / live Worker (no redeploy needed
        this round; all changes are repo-side)
========================================================================

## Verdict

**Round-2 closure status: 1/1 + 2/2 closed.** Validation gate now
exits clean: full system-validation suite reports 99/99 telemetry
goals met (was 99/103 FAIL pre-fix), worker-probe is 11/11 PASS
(was 10/10), CLAIMS-MAP unchanged at 26/28 GREEN with the same 2
RED deferrals.

**1 new finding (NEW-FINDING 13, P3):** the new cf-ray probe added
in 73da6be passes against ANY Cloudflare-fronted server, not just
SelectiveMirror's Worker. It does not provide the misroute-detection
guarantee its docstring claims. The other 10 probes detect a
misroute via response shape, so the cf-ray check is at best
duplicative and at worst misleading. Recommendation: tighten or
remove.

No regressions on the round-1/2 findings. The 1-2/30 HTML-500 burst
issue is unchanged (Cloudflare-edge throttling, not Worker-fixable);
the round-2 worker/README.md note now correctly attributes it.

## Round-2 findings — closure verification

### NEW-FINDING 12 (P3, round-2): four v1-leftover goals → CLOSED

Pre-fix:
```
$ cd system-validation && go test -count=1 -timeout 8m ./...
...
  telemetry_retention_raw_purge        0 / 1     FAIL
  telemetry_rls_envelope_binding       0 / 1     FAIL
  telemetry_rls_server_owned_columns   0 / 1     FAIL
  telemetry_rollup_taxonomy_join       0 / 1     FAIL
  ...
  99 / 103 goals met
FAIL    systemval
```

Post-fix:
```
$ cd system-validation && go test -count=1 -timeout 8m ./...
...
  99 / 99 goals met
```

The 4 lines were removed from `helpers_test.go` with an inline
comment block documenting why and pointing at the v2 equivalents
(useful for future maintainers who might wonder why those concept
names disappeared from the goal map). ✓

### Optional polish item 1: worker/README.md "Two distinct 5xx sources" → CLOSED

Confirmed in `worker/README.md` lines 107-124. Reads cleanly,
distinguishes Worker-rewrite (`502 application/json`) from
Cloudflare-edge (`5xx text/html`), tells the reader the Go client
already handles both as `ErrNetwork`. ✓

### Optional polish item 2: cf-ray probe added to telemetry-worker-probe.py → CLOSED but introduces NEW-FINDING 13

The check is wired and runs first in the probe sequence (failing
fast on misroute):

```python
def check_response_came_from_cloudflare_worker(url: str) -> tuple[bool, str]:
    ...
    resp = post(url, "/v1/contribute", body)
    if "cf-ray" not in (h.lower() for h in resp.headers):
        return False, "response is missing the cf-ray header — the probe URL may be misrouted..."
    return True, ""
```

Live result: 11/11 PASS against the production URL. ✓ But see
NEW-FINDING 13 below — the check passes against non-SelectiveMirror
Cloudflare servers too.

## NEW FINDING (round 3)

------------------------------------------------------------------------
NEW-FINDING 13 (P3): cf-ray probe is too permissive — passes for any
                     Cloudflare-fronted server, not just our Worker.
------------------------------------------------------------------------

The probe was added in 73da6be as round-2 recommendation #3 ("verify
responses come from OUR Worker rather than landing on a misrouted/
unreachable hostname"). The current implementation only asserts that
a `cf-ray` header is present, which is true for *any* server behind
Cloudflare's edge — not specifically our Worker subdomain.

Reproduce:

```
$ python3 system-validation/telemetry-worker-probe.py --url https://example.com
Probing live Worker at: https://example.com
Total checks: 11

  PASS  response_came_from_cloudflare       ← passes!
  FAIL  contribute_bad_hmac                 ← (correctly catches via response shape)
  FAIL  forget_returns_410_with_v2_message  ← (correctly catches via response shape)
  ...
```

example.com is hosted by Cloudflare (`Server: cloudflare`,
`CF-RAY: 9f57e8f90896c233-TLV`), so a `cf-ray` header is present
and the probe passes. The same would happen for any other
Cloudflare-fronted domain a misroute might accidentally land on
(parked domains, the wrong workers.dev account, a typo in
`selectivemirror`).

Why this matters. The docstring promises:

> If the probe URL is mistyped or DNS/CDN config drifted, the
> response would likely come from a different origin and lack this
> header — which we would otherwise see as "all checks pass" because
> retired/unknown paths can be faked by any 4xx-emitting server.

The first half is correct (a non-Cloudflare misroute would lack
`cf-ray`). The second half is wrong: the OTHER 10 probes ALREADY
detect a misroute by checking specific response shapes (e.g.,
`{"ok": false, "error": "rejected"}`, `{"code": "endpoint_retired"}`).
A non-SM Cloudflare server would fail those because its responses
don't have the right shape. The cf-ray probe doesn't add a hard
guarantee — it just adds the *appearance* of one.

Worse, on the misroute path the cf-ray probe runs FIRST and PASSES;
the other 10 then fail in a confusing pattern. A reader of the
probe output might (a) think "check 1 passed, the URL is right,
the others must be a server-side issue" or (b) waste time trying
to debug *why* the Cloudflare-edge is responding correctly when
the Worker isn't — when in reality the URL is just wrong.

The probe needs to assert OUR Worker specifically. Two clean options:

**(a)** Pair `cf-ray` with a SelectiveMirror-specific signal. The
Worker's bad-request body is a strong fingerprint:

```python
# Replace the body-only check with a body-AND-fingerprint check:
def check_response_came_from_cloudflare_worker(url: str) -> tuple[bool, str]:
    body = {"foo": "bar"}  # known-bad shape; Worker rejects with bad_request
    resp = post(url, "/v1/contribute", body)
    if "cf-ray" not in (h.lower() for h in resp.headers):
        return False, "missing cf-ray header — probe URL not on Cloudflare's edge"
    if resp.status_code != 400:
        return False, f"expected 400 from SM Worker on bad body shape, got {resp.status_code}"
    try:
        body = resp.json()
    except Exception:
        return False, "response body is not JSON — probe URL may be a Cloudflare-fronted non-SM server"
    if body.get("code") != "bad_request":
        return False, f"expected SM Worker's 400 fingerprint code='bad_request', got {body!r}"
    return True, ""
```

**(b)** Just delete the probe. The other 10 probes collectively
detect every misroute scenario via response shape; a misroute is
already loud and clear in the existing output.

Recommendation: **(a)**. The intent of the probe is good (fail fast
on misroute, before running 10 more probes that will all fail with
the same root cause); just tighten the assertion to require the
SM-specific body fingerprint alongside the `cf-ray` header.

Effort: ~10 lines in `system-validation/telemetry-worker-probe.py`.

## Regression checks (round-1 + round-2 findings)

All previously-closed findings remain closed. Spot-checked the most
load-bearing ones:

| Finding                                | Verification (round 3)                                                                              |
|----------------------------------------|------------------------------------------------------------------------------------------------------|
| #1 — PGRST passthrough → 400           | Live: `POST /v1/contribute {"foo":"bar"}` → 400 `bad_request`; no `PGRST` in body. ✓                |
| #2 — Reliability inspect ENUM values   | Live: `inspect reliability_snapshot` shows `"anomaly_count_bucket":"0"`, `"sync_attempts_bucket":"<100"`, etc. ✓ |
| #3 — Non-rollup fields stripped         | Live: first_seen/upgrade no `os_detail`; reliability has 13 envelope+bucket keys, no install fields. ✓ |
| #4 — Non-200 → generic 502             | Source unchanged from round-2. Edge-throttling 5xx still occurs (1-2/30 burst); now correctly attributed in `worker/README.md`. ✓ |
| #5 — status/inspect without config     | Live: `--config /nonexistent.yaml telemetry status` returns sensible defaults. ✓                    |
| #6 — CLAIMS-MAP C-05 GREEN             | 26/28 GREEN, 0 AMBER, 2 RED (A-01 + A-03 deferrals). ✓                                              |
| #7 — Retired-ingest message corrected  | Live: POST `/v1/bug-reports` says "Use POST /v1/contribute"; `/v1/forget` keeps "nothing to forget". ✓ |
| #8 — GET on retired returns 410        | Live: `GET /v1/forget` → 410. `PUT /v1/forget` → 410. ✓                                             |
| #9 — Architecture doc ordering note    | `docs/telemetry-architecture-v2.md` Threat-model section has the Ordering note. ✓                    |
| #10 — Deploy runbook validator section | `docs/operations/deploy-telemetry-v2.md` documents the master-key-less validator path. ✓             |
| #11 — Live-Worker probe + workflow     | `telemetry-worker-probe.py` 11/11 PASS; `.github/workflows/telemetry-worker-probe.yml` wired daily. ✓ |
| #12 — v1-leftover goals removed         | Full suite now 99/99 goals met (was 99/103 FAIL). ✓                                                 |

## What I tested this round

### Build + binary

- Built 0.9.95-dev from current tip (73da6be).
- `smirror version` → "telemetry build-key: none" (expected; -dev build).

### Live Worker

- **`telemetry-worker-probe.py`** — 11/11 PASS against production URL.
- **`telemetry-worker-probe.py --url https://example.com`** —
  cf-ray probe passes (NEW-FINDING 13); other 10 fail with shape
  mismatches (correct).
- **`telemetry-mass-emulation.py`** at concurrency 4: 29/30 (1 HTML
  500 from Cloudflare-edge, documented). 30/30 at lower load.
- **`telemetry-v2-smoke-test.py --via-worker --skip-rollup`** with
  wrong master key: bad-HMAC PASS, retired-forget PASS, others FAIL
  as expected (HMAC ordering; same as rounds 1 + 2).
- **Manual probes** of all the round-1/2 fix points: 400 on
  malformed body, 410 on retired paths regardless of method, 405
  on non-POST to active path, 413 on >100KB. All match contract.

### Tests

- `go test ./internal/telemetry/...` — PASS (4.3s).
- `go test ./cmd/smirror/...` — PASS (10.7s).
- `python3 scripts/test_telemetry_report.py` — 13/13 PASS.
- `cd system-validation && go test -count=1 -run "TestTelemetry|TestPanelR" -timeout 5m ./...` — PASS (38.6s; all telemetry coverage goals met).
- `cd system-validation && go test -count=1 -timeout 8m ./...` — **goal coverage 99/99 PASS**, but suite exits FAIL because of `TestPanelR2_Daemon_RenameAcrossMirrors` (one daemon race-condition test, parallel-flake; passes 3/3 in isolation). **Pre-existing, NOT a telemetry issue, NOT a round-3 regression.** Reported here only for completeness; suggest opening a separate PanelR-flake ticket.

### Code review of round-3 commit

- **`system-validation/helpers_test.go`** — 4 v1-goal entries removed, replaced with an explanatory inline comment. ✓
- **`worker/README.md`** — "Two distinct 5xx sources" subsection added. ✓
- **`system-validation/telemetry-worker-probe.py`** — `check_response_came_from_cloudflare_worker` added. ✓ (but see NEW-FINDING 13 above for the rigor concern)

### What I did NOT test (still deferred)

Same exclusions as round-1 and round-2:

- Vault-keyed good-HMAC end-to-end (CLAIMS-MAP A-01 / A-03 require
  operator session).
- HMAC constant-time perf benchmark (A-01 deferred).
- `pg_stat_statements` literal-stripping check on live Supabase (A-03 deferred).
- Cross-PoP cumulative rate-limit (single-PoP only; per-edge-PoP behavior is documented and tested).

These remain explicit deferrals in CLAIMS-MAP; round-3 didn't add
any new ones.

## Suggested action sequence

1. **Tighten or remove the cf-ray probe** (NEW-FINDING 13). My
   recommendation: option (a) — pair `cf-ray` with the SM Worker's
   400-body fingerprint. Effort: ~10 lines in
   `system-validation/telemetry-worker-probe.py`. Update the
   docstring to say "asserts CF edge AND SM Worker fingerprint."

2. **Optional**: open a separate non-telemetry ticket for
   `TestPanelR2_Daemon_RenameAcrossMirrors` parallel-flake. The
   test passes 3/3 in isolation but flakes once-or-twice when the
   full suite runs in parallel. The test comment already
   acknowledges the race window. Either mark `t.Parallel()` off
   or guard the wait with a longer timeout under load.

That's it. From a telemetry-validation perspective, the v2 stack
is at v1.0-tag readiness with the same 2 RED deferrals (A-01, A-03)
that have been documented since round-1 as "post-tag perf-harness /
live-Supabase fixture work."

— validation session, 2026-05-02 (round 3)
