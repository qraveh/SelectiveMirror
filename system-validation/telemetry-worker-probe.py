#!/usr/bin/env python3
"""
Live-Worker probe for the telemetry v2 architecture.

FINDING 11 from the 2026-05-02 validation memo: there was no CI gate
that verified the deployed Cloudflare Worker still honors the
documented contract. A misconfigured redeploy (dropped salt secret,
renamed contribute path, regressed body cap, missing retired path)
would only surface when the next user's smoke test failed.

This script is the gate. It hits each of the Worker's documented
behaviors once, asserts the response, and exits non-zero on any
failure. Designed to run from CI — no master key required.

Checks (each maps to a documented contract):

  1. POST /v1/contribute with bad HMAC
     → 200 with {"ok": false, "error": "rejected"}
     [contract: telemetry.contribute() returns 200 on every legitimate
      outcome; client reads the reason from the body]

  2. POST /v1/forget
     → 410 with code "endpoint_retired" + message mentioning
     "nothing to forget"
     [contract: retired-forget path with v2 semantics]

  3. POST /v1/bug-reports
     → 410 with code "endpoint_retired" + message mentioning
     "/v1/contribute"
     [contract: retired-ingest path; FINDING 7 from validation memo]

  4. POST /v1/installations/report
     → 410 with code "endpoint_retired" + same v2-pointer message
     [contract: retired-ingest path]

  5. GET /v1/forget
     → 410 (NOT 405 method_not_allowed)
     [contract: FINDING 8 — retired-path check runs before method
      allowlist, so a retired endpoint is "gone" regardless of method]

  6. GET /v1/contribute
     → 405 with code "method_not_allowed"
     [contract: method allowlist for active endpoints]

  7. POST /v1/unknown-path
     → 404 with code "not_found"
     [contract: path allowlist]

  8. POST /v1/contribute with body > 100KB
     → 413 with code "payload_too_large"
     [contract: body-size cap on actual bytes (not Content-Length);
      defense-in-depth against chunked-transfer bypass]

  9. POST /v1/contribute with body {"foo": "bar"} (unexpected keys)
     → 400 with code "bad_request"
     [contract: FINDING 1 — Worker validates body shape before
      forwarding to PostgREST so PGRST schema-cache hints don't leak]

 10. POST /v1/contribute with body missing claimed_hmac_hex
     → 400 with code "bad_request"
     [contract: FINDING 1 — required-key validation]

Usage:

    python3 system-validation/telemetry-worker-probe.py
    # exit 0 if all probes pass; non-zero with summary on any failure

    python3 system-validation/telemetry-worker-probe.py --url https://...
    # override the production URL (e.g., for a staging deploy)

The probe is deliberately read-only: every contribute() POST uses a
bad HMAC, so no row is created in the rollup tables. The retired
paths and unknown paths never touch Supabase at all. Safe to run
against the production Worker on any cadence.
"""
from __future__ import annotations

import argparse
import json
import sys
from typing import Any, Callable

import requests

DEFAULT_URL = "https://smirror-telemetry.selectivemirror.workers.dev"


# A check produces a result tuple: (passed: bool, detail: str). The
# detail is shown on failure for debugging.
Check = Callable[[str], tuple[bool, str]]


def post(url: str, path: str, body: Any, *, raw_body: bytes | None = None,
         method: str = "POST") -> requests.Response:
    target = url.rstrip("/") + path
    if raw_body is not None:
        return requests.request(method, target,
                                data=raw_body,
                                headers={"Content-Type": "application/json"},
                                timeout=15)
    if body is None:
        return requests.request(method, target, timeout=15)
    return requests.request(method, target, json=body, timeout=15)


def expect_status(resp: requests.Response, want: int, label: str) -> tuple[bool, str]:
    if resp.status_code != want:
        return False, f"{label}: expected HTTP {want}, got {resp.status_code} (body: {resp.text[:200]})"
    return True, ""


def expect_json_field(resp: requests.Response, field: str, want_value: Any,
                      label: str) -> tuple[bool, str]:
    try:
        body = resp.json()
    except Exception:
        return False, f"{label}: response body is not JSON: {resp.text[:200]}"
    got = body.get(field)
    if got != want_value:
        return False, f"{label}: expected body[{field!r}] = {want_value!r}, got {got!r} (full body: {body})"
    return True, ""


def expect_substring_in_body(resp: requests.Response, want_substring: str,
                             label: str) -> tuple[bool, str]:
    text = resp.text
    if want_substring not in text:
        return False, f"{label}: expected body to contain {want_substring!r}, got: {text[:300]}"
    return True, ""


# ---------------------------------------------------------------------------
# Individual checks
# ---------------------------------------------------------------------------

def check_contribute_bad_hmac(url: str) -> tuple[bool, str]:
    body = {
        "payload": {"event_kind": "first_seen", "client_version": "0.0.0-probe",
                    "install_method": "msi", "os_family": "windows"},
        "claimed_version": "0.0.0-probe",
        "claimed_hmac_hex": "deadbeef" * 8,
    }
    resp = post(url, "/v1/contribute", body)
    ok, detail = expect_status(resp, 200, "contribute-bad-hmac")
    if not ok:
        return ok, detail
    ok, detail = expect_json_field(resp, "ok", False, "contribute-bad-hmac")
    if not ok:
        return ok, detail
    return expect_json_field(resp, "error", "rejected", "contribute-bad-hmac")


def check_response_came_from_cloudflare_sm_worker(url: str) -> tuple[bool, str]:
    """Round-3 fix (NEW-FINDING 13): assert that responses come from
    the SelectiveMirror Worker SPECIFICALLY, not just from any
    Cloudflare-fronted server. The earlier round-2 implementation only
    checked the `cf-ray` header — but every Cloudflare customer's
    domain carries that header, including parked domains, the wrong
    workers.dev account, and example.com. So the round-2 check would
    pass for any catastrophic misroute that happened to land on
    Cloudflare's edge, defeating its stated purpose.

    The fix layers two assertions:

      1. `cf-ray` header present  — confirms the response originated
         on Cloudflare's edge (not, e.g., a 4xx-emitting non-CF
         server).
      2. SelectiveMirror Worker's distinctive 400 body fingerprint
         — POSTing a body with the wrong shape ({"foo":"bar"}) hits
         the Worker's `parseContributeBody` path (renamed from
         isValidContributeBody in FINDING 15), which returns 400 with
         `{"code":"bad_request","message":"Body must be a JSON object
         with exactly the keys ..."}`. That code+message shape is
         unique to our Worker; any other Cloudflare-fronted service
         would respond differently to the same probe.

    Together these two pin both the edge AND the worker identity.
    Run first in the probe sequence so a misroute fails immediately
    and the rest of the run isn't a confusing pattern of shape-
    mismatch failures."""
    # The wrong-shape body deliberately exercises the Worker's
    # FINDING-1 body-shape validator. We pick a body that's parseable
    # JSON (so we get past the JSON parser and into the validator)
    # but doesn't have any of the three expected keys.
    resp = post(url, "/v1/contribute", {"foo": "bar"})

    if "cf-ray" not in (h.lower() for h in resp.headers):
        return False, (
            "response is missing the cf-ray header — the probe URL is "
            "not on Cloudflare's edge. Probe URL: " + url)

    if resp.status_code != 400:
        return False, (
            f"expected HTTP 400 from SM Worker on bad body shape, got "
            f"{resp.status_code}. Probe URL is on Cloudflare's edge "
            f"but it's not the SelectiveMirror Worker. Verify URL: " + url)

    try:
        body = resp.json()
    except Exception:
        return False, (
            "expected JSON body from SM Worker on bad body shape, got "
            "non-JSON. Probe URL may be a Cloudflare-fronted non-SM "
            "server. Verify URL: " + url)

    if body.get("code") != "bad_request":
        return False, (
            f"expected SM Worker's 400 fingerprint code='bad_request', "
            f"got body={body!r}. Probe URL is on Cloudflare's edge but "
            f"the response shape doesn't match the SelectiveMirror "
            f"Worker. Verify URL: " + url)

    return True, ""


def check_forget_410(url: str) -> tuple[bool, str]:
    resp = post(url, "/v1/forget", {})
    ok, detail = expect_status(resp, 410, "forget-410")
    if not ok:
        return ok, detail
    ok, detail = expect_json_field(resp, "code", "endpoint_retired", "forget-410")
    if not ok:
        return ok, detail
    return expect_substring_in_body(resp, "nothing to forget", "forget-410")


def check_bug_reports_410(url: str) -> tuple[bool, str]:
    resp = post(url, "/v1/bug-reports", {})
    ok, detail = expect_status(resp, 410, "bug-reports-410")
    if not ok:
        return ok, detail
    ok, detail = expect_json_field(resp, "code", "endpoint_retired", "bug-reports-410")
    if not ok:
        return ok, detail
    return expect_substring_in_body(resp, "/v1/contribute", "bug-reports-410")


def check_installations_report_410(url: str) -> tuple[bool, str]:
    resp = post(url, "/v1/installations/report", {})
    ok, detail = expect_status(resp, 410, "installations-report-410")
    if not ok:
        return ok, detail
    ok, detail = expect_json_field(resp, "code", "endpoint_retired", "installations-report-410")
    if not ok:
        return ok, detail
    return expect_substring_in_body(resp, "/v1/contribute", "installations-report-410")


def check_get_on_retired_path_returns_410(url: str) -> tuple[bool, str]:
    # GET /v1/forget should return 410 (the endpoint is gone), not 405
    # (the endpoint exists but only accepts POST). FINDING 8.
    resp = post(url, "/v1/forget", body=None, method="GET")
    return expect_status(resp, 410, "get-forget-returns-410")


def check_get_on_active_path_returns_405(url: str) -> tuple[bool, str]:
    resp = post(url, "/v1/contribute", body=None, method="GET")
    ok, detail = expect_status(resp, 405, "get-contribute-405")
    if not ok:
        return ok, detail
    return expect_json_field(resp, "code", "method_not_allowed", "get-contribute-405")


def check_unknown_path_returns_404(url: str) -> tuple[bool, str]:
    resp = post(url, "/v1/totally-made-up-path", {})
    ok, detail = expect_status(resp, 404, "unknown-path-404")
    if not ok:
        return ok, detail
    return expect_json_field(resp, "code", "not_found", "unknown-path-404")


def check_oversized_body_returns_413(url: str) -> tuple[bool, str]:
    # Construct a body larger than the 100KB cap. Even though it's
    # invalid JSON shape, the body cap fires before parsing.
    huge = b'{"payload":' + b'"' + (b"X" * 200_000) + b'"}'
    resp = post(url, "/v1/contribute", body=None, raw_body=huge)
    ok, detail = expect_status(resp, 413, "oversized-body-413")
    if not ok:
        return ok, detail
    return expect_json_field(resp, "code", "payload_too_large", "oversized-body-413")


def check_malformed_body_returns_400(url: str) -> tuple[bool, str]:
    # FINDING 1: a body that doesn't match the {payload,
    # claimed_version, claimed_hmac_hex} shape must get a 400 from the
    # Worker, NOT a 404 PGRST202 with schema-cache hints.
    resp = post(url, "/v1/contribute", {"foo": "bar"})
    ok, detail = expect_status(resp, 400, "malformed-body-400")
    if not ok:
        return ok, detail
    ok, detail = expect_json_field(resp, "code", "bad_request", "malformed-body-400")
    if not ok:
        return ok, detail
    # The response must NOT contain PGRST hints.
    if "PGRST" in resp.text:
        return False, ("malformed-body-400: response leaks PGRST string "
                       f"(schema-cache hint passthrough): {resp.text[:300]}")
    return True, ""


def check_missing_required_key_returns_400(url: str) -> tuple[bool, str]:
    # FINDING 1: a body that has the right shape but is missing one of
    # the three required keys must also get a 400 (not be forwarded to
    # PostgREST and rejected with PGRST202).
    resp = post(url, "/v1/contribute",
                {"payload": {}, "claimed_version": "0.0.0-probe"})
    ok, detail = expect_status(resp, 400, "missing-key-400")
    if not ok:
        return ok, detail
    return expect_json_field(resp, "code", "bad_request", "missing-key-400")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--url", default=DEFAULT_URL,
                    help="Worker base URL (default: production)")
    ap.add_argument("--quiet", action="store_true",
                    help="Suppress per-check pass lines; only print failures")
    args = ap.parse_args()

    checks: list[tuple[str, Check]] = [
        ("response_came_from_sm_worker",       check_response_came_from_cloudflare_sm_worker),
        ("contribute_bad_hmac",                check_contribute_bad_hmac),
        ("forget_returns_410_with_v2_message", check_forget_410),
        ("bug_reports_returns_410_v1_msg",     check_bug_reports_410),
        ("installations_report_returns_410",   check_installations_report_410),
        ("get_on_retired_returns_410",         check_get_on_retired_path_returns_410),
        ("get_on_active_returns_405",          check_get_on_active_path_returns_405),
        ("unknown_path_returns_404",           check_unknown_path_returns_404),
        ("oversized_body_returns_413",         check_oversized_body_returns_413),
        ("malformed_body_returns_400",         check_malformed_body_returns_400),
        ("missing_required_key_returns_400",   check_missing_required_key_returns_400),
    ]

    print(f"Probing live Worker at: {args.url}")
    print(f"Total checks: {len(checks)}")
    print()

    passes = 0
    failures: list[tuple[str, str]] = []

    for name, check in checks:
        try:
            ok, detail = check(args.url)
        except Exception as e:
            ok, detail = False, f"exception: {type(e).__name__}: {e}"
        if ok:
            passes += 1
            if not args.quiet:
                print(f"  PASS  {name}")
        else:
            failures.append((name, detail))
            print(f"  FAIL  {name}")
            print(f"        {detail}")

    print()
    print(f"=== {passes}/{len(checks)} probes passed ===")

    if failures:
        print()
        print("FAILURES:")
        for name, detail in failures:
            print(f"  {name}: {detail}")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
