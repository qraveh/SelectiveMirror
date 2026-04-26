#!/usr/bin/env python3
"""
End-to-end validation of SelectiveMirror telemetry HMAC + RLS via Supabase REST API.

Tests are organized into groups, each probing a distinct security or
correctness property. After running, see "Cleanup" instructions at the
bottom of the printed output.

Usage (PowerShell):
  $env:SUPABASE_ANON_KEY = "<anon public key from Project Settings -> API>"
  $env:SMIRROR_TELEMETRY_MASTER_KEY = "<hex from Bitwarden>"
  python3 telemetry-validation.py

Usage (Bash/Git Bash):
  export SUPABASE_ANON_KEY="<anon public key>"
  export SMIRROR_TELEMETRY_MASTER_KEY="<hex from Bitwarden>"
  python3 telemetry-validation.py
"""

import hashlib
import hmac
import json
import os
import sys
import urllib.error
import urllib.request
from datetime import datetime, timezone

SUPABASE_URL = "https://qkspigvkniiiwxggdvbr.supabase.co"
INGEST_PATH = "/rest/v1/ingest_envelope"
TEST_VERSION = "0.8.5"


# ============================================================================
# Crypto + HTTP helpers
# ============================================================================

def derive_key(master_hex: str, version: str) -> bytes:
    return hmac.new(
        bytes.fromhex(master_hex),
        version.encode("utf-8"),
        hashlib.sha256,
    ).digest()


def canonical_json(obj) -> str:
    """Match PostgreSQL JSONB::text format.

    PostgreSQL JSONB stores object keys sorted by LENGTH FIRST, then by
    Unicode codepoint. Python's json.dumps(sort_keys=True) sorts by
    codepoint only. They diverge whenever keys in the same object have
    different lengths.

    Example divergence:
      Input: {"hello": ..., "test": ..., "reported_at": ...}
      Python alphabetical: hello, reported_at, test
      Postgres length-first: test (4), hello (5), reported_at (11)

    HMAC over the wrong serialization fails verification.
    """
    if isinstance(obj, dict):
        items = sorted(obj.items(), key=lambda kv: (len(kv[0]), kv[0]))
        return "{" + ", ".join(
            f"{json.dumps(k)}: {canonical_json(v)}" for k, v in items
        ) + "}"
    if isinstance(obj, list):
        return "[" + ", ".join(canonical_json(x) for x in obj) + "]"
    return json.dumps(obj)


def sign_payload(derived_key: bytes, canonical_payload_text: str) -> str:
    return hmac.new(
        derived_key,
        canonical_payload_text.encode("utf-8"),
        hashlib.sha256,
    ).hexdigest()


def signed_payload(master_key: str, version: str, inner_payload: dict) -> dict:
    """Build a payload dict with valid version_hmac field for the given version."""
    derived = derive_key(master_key, version)
    canonical = canonical_json(inner_payload)
    h = sign_payload(derived, canonical)
    return {**inner_payload, "version_hmac": h}


def http_request(method: str, anon_key: str, path: str, row=None) -> tuple:
    """Send a request to PostgREST. Returns (status, body_text)."""
    url = f"{SUPABASE_URL}{path}"
    body = json.dumps(row).encode("utf-8") if row is not None else None
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "apikey": anon_key,
        "Authorization": f"Bearer {anon_key}",
        "Content-Profile": "telemetry",
        "Accept-Profile": "telemetry",
        "Prefer": "return=minimal",
    }
    req = urllib.request.Request(url, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return resp.status, resp.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode("utf-8", errors="replace")
    except Exception as e:
        return 0, f"network error: {e}"


def post_envelope(anon_key: str, row: dict) -> tuple:
    return http_request("POST", anon_key, INGEST_PATH, row)


def build_row(install_id: str, dedupe_key: str, payload: dict, **overrides) -> dict:
    """Build a complete ingest_envelope row. Overrides let tests violate fields."""
    payload_text = canonical_json(payload)
    payload_sha = hashlib.sha256(payload_text.encode("utf-8")).hexdigest()
    row = {
        "ingest_kind":    "bug_report",
        "schema_version": 1,
        "install_id":     install_id,
        "client_version": TEST_VERSION,
        "dedupe_key":     dedupe_key,
        "payload_sha256": payload_sha,
        "payload":        payload,
    }
    row.update(overrides)
    return row


# ============================================================================
# Result reporting
# ============================================================================

class Reporter:
    def __init__(self):
        self.results = []
        self.current_group = None

    def group(self, name: str):
        self.current_group = name
        print(f"\n=== {name} ===")

    def record(self, name: str, passed: bool, detail: str = ""):
        self.results.append((self.current_group, name, passed, detail))
        status = "PASS" if passed else "FAIL"
        print(f"  {status}: {name}")
        if detail and not passed:
            print(f"        {detail}")
        elif detail:
            print(f"        {detail}")

    def summary(self) -> bool:
        total = len(self.results)
        passed = sum(1 for _, _, p, _ in self.results if p)
        print()
        print("=" * 60)
        print(f"Result: {passed}/{total} passed")
        if passed == total:
            print("All tests passed -- defenses verified end-to-end")
            return True
        else:
            print("Some tests failed:")
            for group, name, p, _ in self.results:
                if not p:
                    print(f"  - [{group}] {name}")
            return False


def expect_4xx(status: int, body: str = "") -> tuple:
    """Helper: caller wanted rejection. Pass iff status is 4xx."""
    return (400 <= status < 500), f"status={status}" + (f"  body={body[:120]}" if not (400 <= status < 500) else "")


def expect_2xx(status: int, body: str = "") -> tuple:
    """Helper: caller wanted success. Pass iff status is 2xx."""
    return (200 <= status < 300), f"status={status}" + (f"  body={body[:200]}" if not (200 <= status < 300) else "")


# ============================================================================
# GROUP A: Basic HMAC + RLS gate
# ============================================================================

def group_a_basic_hmac(r: Reporter, anon_key: str, master_key: str) -> str:
    """Returns the dedupe_key of the inserted valid row, for cleanup."""
    r.group("A. Basic HMAC + RLS gate")

    # A1: no HMAC -> reject
    payload = {"hello": "world", "test": "no-hmac"}
    row = build_row("test-A1", "test-dedupe-A1-no-hmac-aaaaaaaaaaa", payload)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("A1: missing version_hmac field -> 4xx", p, d)

    # A2: forged HMAC -> reject
    payload = {"hello": "world", "test": "bad-hmac", "version_hmac": "0" * 64}
    row = build_row("test-A2", "test-dedupe-A2-bad-hmac-bbbbbbbbbb", payload)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("A2: forged version_hmac -> 4xx", p, d)

    # A3: valid HMAC -> accept
    inner = {
        "hello": "world",
        "test": "valid",
        "reported_at": datetime.now(timezone.utc).isoformat(),
    }
    full = signed_payload(master_key, TEST_VERSION, inner)
    dedupe = "test-dedupe-A3-valid-cccccccccccccccccc"
    row = build_row("test-A3", dedupe, full)
    status, body = post_envelope(anon_key, row)
    p, d = expect_2xx(status, body)
    r.record("A3: valid version_hmac -> 2xx (insertion)", p, d)

    # A4: replay same dedupe_key -> reject
    inner2 = {"hello": "replay", "test": "replay"}
    full2 = signed_payload(master_key, TEST_VERSION, inner2)
    row = build_row("test-A4", dedupe, full2)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("A4: replay same dedupe_key -> 4xx", p, d)

    return dedupe


# ============================================================================
# GROUP B: RLS field validation (each rule fires independently)
# ============================================================================

def group_b_field_validation(r: Reporter, anon_key: str, master_key: str):
    r.group("B. RLS field validation")

    # Build a baseline valid HMAC payload to slot bad fields into
    inner = {"hello": "world", "test": "field-validation"}
    full = signed_payload(master_key, TEST_VERSION, inner)

    # B1: invalid ingest_kind -> reject (only 'bug_report' or 'installation_event' allowed)
    row = build_row("test-B1", "test-dedupe-B1-invalid-kind-aaaaa", full)
    # 'crash_report' was removed from the enum; this should fail at the type level
    row["ingest_kind"] = "crash_report"
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B1: invalid ingest_kind ('crash_report') -> 4xx", p, d)

    # B2: schema_version above 100 -> reject
    row = build_row("test-B2", "test-dedupe-B2-schemaver-bbbbbbb", full,
                    schema_version=101)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B2: schema_version=101 (over 100) -> 4xx", p, d)

    # B3: schema_version below 1 -> reject
    row = build_row("test-B3", "test-dedupe-B3-schemaver0-cccccc", full,
                    schema_version=0)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B3: schema_version=0 (below 1) -> 4xx", p, d)

    # B4: client_version not semver -> reject (won't match ^[0-9]+\.[0-9]+\.[0-9]+)
    row = build_row("test-B4", "test-dedupe-B4-badversion-ddddd", full,
                    client_version="not-a-version")
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B4: client_version='not-a-version' -> 4xx", p, d)

    # B5: client_version with two segments (1.2) -- does not match three-segment regex
    row = build_row("test-B5", "test-dedupe-B5-twoseg-eeeeeeeee", full,
                    client_version="1.2")
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B5: client_version='1.2' (two segments) -> 4xx", p, d)

    # B6: dedupe_key too short (<16 chars)
    row = build_row("test-B6", "shortkey", full)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B6: dedupe_key length 8 (below 16) -> 4xx", p, d)

    # B7: dedupe_key too long (>200 chars)
    row = build_row("test-B7", "x" * 250, full)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B7: dedupe_key length 250 (above 200) -> 4xx", p, d)

    # B8: payload_sha256 not hex / wrong length
    row = build_row("test-B8", "test-dedupe-B8-badsha-fffffffff", full,
                    payload_sha256="not-a-real-sha-256-hash-just-text")
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("B8: payload_sha256 invalid format -> 4xx", p, d)


# ============================================================================
# GROUP C: HMAC tampering tests
# ============================================================================

def group_c_hmac_tampering(r: Reporter, anon_key: str, master_key: str):
    r.group("C. HMAC tampering")

    # C1: HMAC computed for 0.8.5, but client_version says 0.9.0 -> derived key differs -> fails
    inner = {"hello": "world", "test": "wrong-version"}
    full_signed_for_085 = signed_payload(master_key, "0.8.5", inner)
    row = build_row("test-C1", "test-dedupe-C1-wrongver-ggggggg", full_signed_for_085,
                    client_version="0.9.0")
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("C1: HMAC signed for 0.8.5, client_version=0.9.0 -> 4xx", p, d)

    # C2: Tamper payload after signing -- change a field, keep old HMAC
    inner = {"hello": "world", "test": "original"}
    full = signed_payload(master_key, TEST_VERSION, inner)
    # Now mutate: change "test" to "tampered"
    full["test"] = "tampered-after-sign"
    row = build_row("test-C2", "test-dedupe-C2-tampered-hhhhh", full)
    status, body = post_envelope(anon_key, row)
    p, d = expect_4xx(status, body)
    r.record("C2: payload mutated after signing -> 4xx", p, d)


# ============================================================================
# GROUP D: anon read denial (no SELECT privilege)
# ============================================================================

def group_d_read_denial(r: Reporter, anon_key: str):
    r.group("D. anon read denial (SELECT)")

    # D1: SELECT ingest_envelope as anon
    status, body = http_request("GET", anon_key, "/rest/v1/ingest_envelope?limit=1")
    # PostgREST may return 401 (insufficient_privilege) or empty array depending
    # on whether RLS or grants block first. Either is acceptable as long as no rows leak.
    leaked = (status == 200 and body.strip() not in ("[]", ""))
    p = not leaked
    d = f"status={status}  body_preview={body[:80]}"
    r.record("D1: GET ingest_envelope as anon -> no rows leaked", p, d)

    # D2: SELECT bug_report as anon
    status, body = http_request("GET", anon_key, "/rest/v1/bug_report?limit=1")
    leaked = (status == 200 and body.strip() not in ("[]", ""))
    p = not leaked
    d = f"status={status}  body_preview={body[:80]}"
    r.record("D2: GET bug_report as anon -> no rows leaked", p, d)

    # D3: SELECT installation as anon
    status, body = http_request("GET", anon_key, "/rest/v1/installation?limit=1")
    leaked = (status == 200 and body.strip() not in ("[]", ""))
    p = not leaked
    d = f"status={status}  body_preview={body[:80]}"
    r.record("D3: GET installation as anon -> no rows leaked", p, d)

    # D4: SELECT taxonomy_term as anon (admin-only)
    status, body = http_request("GET", anon_key, "/rest/v1/taxonomy_term?limit=1")
    leaked = (status == 200 and body.strip() not in ("[]", ""))
    p = not leaked
    d = f"status={status}  body_preview={body[:80]}"
    r.record("D4: GET taxonomy_term as anon -> no rows leaked", p, d)


# ============================================================================
# GROUP E: anon write denial on non-ingest tables
# ============================================================================

def group_e_cross_table_writes(r: Reporter, anon_key: str):
    r.group("E. anon write denial on non-ingest tables")

    # E1: INSERT bug_report directly (only the server-side classifier should populate this)
    fake_row = {
        "envelope_id": "00000000-0000-0000-0000-000000000000",
        "source": "report_bug",
        "reported_at": datetime.now(timezone.utc).isoformat(),
        "report_text": "fake bug report direct write",
    }
    status, body = http_request("POST", anon_key, "/rest/v1/bug_report", fake_row)
    p, d = expect_4xx(status, body)
    r.record("E1: POST bug_report as anon -> 4xx", p, d)

    # E2: INSERT installation directly
    fake_row = {
        "install_id": "test-direct-install",
        "first_seen_at": datetime.now(timezone.utc).isoformat(),
        "last_seen_at": datetime.now(timezone.utc).isoformat(),
    }
    status, body = http_request("POST", anon_key, "/rest/v1/installation", fake_row)
    p, d = expect_4xx(status, body)
    r.record("E2: POST installation as anon -> 4xx", p, d)

    # E3: INSERT installation_event directly
    fake_row = {
        "envelope_id": "00000000-0000-0000-0000-000000000000",
        "install_id": "test-direct-event",
        "event_name": "first_seen",
        "reported_at": datetime.now(timezone.utc).isoformat(),
    }
    status, body = http_request("POST", anon_key, "/rest/v1/installation_event", fake_row)
    p, d = expect_4xx(status, body)
    r.record("E3: POST installation_event as anon -> 4xx", p, d)

    # E4: INSERT taxonomy_term directly (admin-only seed table)
    fake_row = {
        "target": "bug_report",
        "namespace": "test.fake",
        "slug": "injected",
        "display_name": "Injected",
    }
    status, body = http_request("POST", anon_key, "/rest/v1/taxonomy_term", fake_row)
    p, d = expect_4xx(status, body)
    r.record("E4: POST taxonomy_term as anon -> 4xx", p, d)


# ============================================================================
# Main
# ============================================================================

def main():
    anon_key = os.environ.get("SUPABASE_ANON_KEY")
    master_key = os.environ.get("SMIRROR_TELEMETRY_MASTER_KEY")

    if not anon_key:
        print("ERROR: SUPABASE_ANON_KEY environment variable not set"); sys.exit(1)
    if not master_key:
        print("ERROR: SMIRROR_TELEMETRY_MASTER_KEY environment variable not set"); sys.exit(1)
    if len(master_key) != 64 or not all(c in "0123456789abcdefABCDEF" for c in master_key):
        print(f"ERROR: SMIRROR_TELEMETRY_MASTER_KEY must be 64 hex chars (got {len(master_key)})"); sys.exit(1)
    if len(anon_key) < 100:
        print(f"ERROR: SUPABASE_ANON_KEY looks too short ({len(anon_key)} chars). Real anon JWTs are ~200 chars."); sys.exit(1)

    print(f"Endpoint: {SUPABASE_URL}{INGEST_PATH}")
    print(f"Test version: {TEST_VERSION}")

    r = Reporter()
    inserted_dedupe = group_a_basic_hmac(r, anon_key, master_key)
    group_b_field_validation(r, anon_key, master_key)
    group_c_hmac_tampering(r, anon_key, master_key)
    group_d_read_denial(r, anon_key)
    group_e_cross_table_writes(r, anon_key)

    all_passed = r.summary()

    print()
    print("=" * 60)
    print("Cleanup -- paste this in Supabase SQL Editor afterwards:")
    print("  DELETE FROM telemetry.ingest_envelope WHERE install_id LIKE 'test-%';")
    print("  -- (only one row should actually be present, from test A3)")

    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
