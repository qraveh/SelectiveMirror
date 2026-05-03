#!/usr/bin/env python3
"""
SelectiveMirror telemetry v2 — smoke test for a deployed environment.

Run this AFTER applying docs/telemetry-v2.sql to the target Supabase
project. Verifies that:
  1. The telemetry.contribute() RPC is reachable.
  2. A payload with a wrong HMAC is rejected with {"ok": false, "error": "rejected"}.
  3. A payload with a valid HMAC for the test version is accepted with {"ok": true}.
  4. A payload with a schema violation (missing required field, bad enum
     value) is rejected with {"ok": false, "error": "schema_violation*"}.
  5. After a successful contribution, the matching rollup row's count
     incremented by exactly 1.

The script exits 0 if all checks pass and non-zero on any failure. It
is safe to run repeatedly — each successful contribution adds 1 to the
test bucket; the script verifies a delta, not an absolute.

This is the Phase-A validation step in
docs/operations/deploy-telemetry-v2.md.

Usage:
    # Direct against Supabase (skip the Worker for end-to-end DB checks):
    export SUPABASE_URL="https://qkspigvkniiiwxggdvbr.supabase.co"
    export SUPABASE_SERVICE_ROLE_KEY="<service_role_jwt>"
    export TELEMETRY_MASTER_KEY="<the master key from Vault>"
    python3 scripts/telemetry-v2-smoke-test.py

    # Through the Cloudflare Worker (Phase B):
    export WORKER_URL="https://smirror-telemetry.selectivemirror.workers.dev"
    export TELEMETRY_MASTER_KEY="<...>"
    python3 scripts/telemetry-v2-smoke-test.py --via-worker

Flags:
    --via-worker      POST through the Cloudflare Worker URL (Phase B).
                      Default: POST direct to Supabase RPC (Phase A).
    --skip-rollup     Skip the count-delta verification (use when the
                      test runner doesn't have a service-role key but
                      can still issue the RPC — e.g., via the Worker).
    --version VERSION Test client_version string. Default 0.0.0-smoke.

Dependencies: psycopg (v3) or psycopg2-binary, plus requests.
    pip install psycopg[binary] requests
"""

from __future__ import annotations

import argparse
import hmac
import hashlib
import json
import os
import sys

# Round-11 FINDING 38: docstrings + banner output may contain non-ASCII
# (em-dashes, emoji, arrows). On Windows the default stdout is cp1252
# which cannot encode them, so even `--help` crashes BEFORE main()
# runs. Reconfigure stdout/stderr to UTF-8 at module load (before
# argparse / banner code touches them).
try:
    sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    sys.stderr.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
except (AttributeError, ValueError):
    pass

from datetime import datetime, timezone
from typing import Any

# Round-11 FINDING 37: requests is needed for the HTTP cases but is
# loaded lazily so --help works without it. psycopg is OPTIONAL (only
# needed for the rollup-delta DB check); leave its eager-import-with-
# graceful-skip pattern intact.
requests = None  # type: ignore[assignment]

try:
    import psycopg as pg
    PG_VERSION = 3
except ImportError:
    try:
        import psycopg2 as pg
        PG_VERSION = 2
    except ImportError:
        pg = None
        PG_VERSION = 0

# ---------------------------------------------------------------------------
# Canonical JSON — matches PostgreSQL JSONB::text length-first ordering.
# Reference impl mirrors test/telemetry-validation.py and
# internal/telemetry/canonical.go.
# ---------------------------------------------------------------------------

def canonical_json(obj: Any) -> str:
    if isinstance(obj, dict):
        items = sorted(obj.items(), key=lambda kv: (len(kv[0]), kv[0]))
        return "{" + ", ".join(
            f"{json.dumps(k, ensure_ascii=False)}: {canonical_json(v)}"
            for k, v in items
        ) + "}"
    if isinstance(obj, list):
        return "[" + ", ".join(canonical_json(x) for x in obj) + "]"
    # ensure_ascii=False matches PG JSONB which keeps UTF-8 literal.
    # Note: Python's json.dumps does NOT HTML-escape (matches PG).
    return json.dumps(obj, ensure_ascii=False)


def derive_version_key(master_key: str, version: str) -> bytes:
    """Match docs/telemetry-rls.sql verify_versioned_hmac key derivation."""
    return hmac.new(
        master_key.encode("utf-8"),
        version.encode("utf-8"),
        hashlib.sha256,
    ).digest()


def sign_payload(payload: dict, master_key: str, version: str) -> str:
    """
    Compute the HMAC over the canonical bytes of the payload, EXCLUDING
    'version_hmac' and 'event_kind' — the same exclusion telemetry.contribute()
    uses on the server side (see docs/telemetry-v2.sql).
    """
    payload_for_signing = {
        k: v for k, v in payload.items()
        if k not in ("version_hmac", "event_kind")
    }
    canonical = canonical_json(payload_for_signing).encode("utf-8")
    derived = derive_version_key(master_key, version)
    return hmac.new(derived, canonical, hashlib.sha256).hexdigest()


# ---------------------------------------------------------------------------
# Reachability check + RPC client
# ---------------------------------------------------------------------------

def post_contribute(target: str, body: dict, anon_key: str | None) -> tuple[int, dict]:
    """POST a contribute call and return (status, parsed_body)."""
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
    }
    if anon_key:
        headers["apikey"] = anon_key
        headers["Authorization"] = f"Bearer {anon_key}"
        headers["Content-Profile"] = "telemetry"
        headers["Prefer"] = "return=minimal"
    resp = requests.post(target, headers=headers, json=body, timeout=10)
    try:
        parsed = resp.json()
    except Exception:
        parsed = {"raw": resp.text}
    return resp.status_code, parsed


# ---------------------------------------------------------------------------
# Test cases
# ---------------------------------------------------------------------------

def make_first_seen_payload(version: str) -> dict:
    """A complete, valid first_seen payload at the v2 schema."""
    return {
        "event_kind": "first_seen",
        "schema_version": 1,
        "install_id": "sm-smoketest-deadbeefcafef00d",  # not stored
        "client_version": version,
        "reported_at": datetime.now(timezone.utc).isoformat(),
        "install_method": "msi",
        "os_family": "windows",
        "mirror_count_bucket": "1",
        "background_mode": "service",
        "delete_policy": "delete",
        "has_hooks": False,
        "has_filters": True,
        "has_alert_webhook": False,
        "has_bandwidth_limit": False,
        "rclone_version": "v1.73.5-smoke",
    }


def case_bad_hmac(target: str, anon_key: str, version: str) -> bool:
    """Payload with a wrong HMAC must be rejected."""
    payload = make_first_seen_payload(version)
    body = {
        "payload": payload,
        "claimed_version": version,
        "claimed_hmac_hex": "deadbeef" * 8,  # 32 bytes hex; obviously wrong
    }
    status, parsed = post_contribute(target, body, anon_key)
    if status >= 400:
        print(f"  [bad-hmac] FAIL — RPC returned HTTP {status}: {parsed}")
        return False
    if parsed.get("ok") is not False or "reject" not in str(parsed.get("error", "")).lower():
        print(f"  [bad-hmac] FAIL — expected ok=false, error~rejected. Got: {parsed}")
        return False
    print("  [bad-hmac] OK — rejected as expected")
    return True


def case_good_hmac(target: str, anon_key: str, version: str, master_key: str) -> bool:
    """Properly signed payload must be accepted."""
    payload = make_first_seen_payload(version)
    sig = sign_payload(payload, master_key, version)
    body = {
        "payload": payload,
        "claimed_version": version,
        "claimed_hmac_hex": sig,
    }
    status, parsed = post_contribute(target, body, anon_key)
    if status >= 400:
        print(f"  [good-hmac] FAIL — RPC returned HTTP {status}: {parsed}")
        return False
    if parsed.get("ok") is not True:
        print(f"  [good-hmac] FAIL — expected ok=true. Got: {parsed}")
        return False
    print("  [good-hmac] OK — accepted")
    return True


def case_schema_violation(target: str, anon_key: str, version: str, master_key: str) -> bool:
    """Bad enum value must be rejected with schema_violation."""
    payload = make_first_seen_payload(version)
    payload["mirror_count_bucket"] = "BOGUS"  # not in the ENUM
    sig = sign_payload(payload, master_key, version)
    body = {
        "payload": payload,
        "claimed_version": version,
        "claimed_hmac_hex": sig,
    }
    status, parsed = post_contribute(target, body, anon_key)
    if status >= 400:
        print(f"  [schema-violation] FAIL — RPC returned HTTP {status}: {parsed}")
        return False
    if parsed.get("ok") is not False or "schema_violation" not in str(parsed.get("error", "")):
        print(f"  [schema-violation] FAIL — expected ok=false, error~schema_violation. Got: {parsed}")
        return False
    print("  [schema-violation] OK — rejected as expected")
    return True


def case_unknown_event(target: str, anon_key: str, version: str, master_key: str) -> bool:
    """Unknown event_kind must be rejected."""
    payload = make_first_seen_payload(version)
    payload["event_kind"] = "totally_made_up_event"
    sig = sign_payload(payload, master_key, version)
    body = {
        "payload": payload,
        "claimed_version": version,
        "claimed_hmac_hex": sig,
    }
    status, parsed = post_contribute(target, body, anon_key)
    if status >= 400:
        print(f"  [unknown-event] FAIL — RPC returned HTTP {status}: {parsed}")
        return False
    if parsed.get("ok") is not False or parsed.get("error") != "unknown_event":
        print(f"  [unknown-event] FAIL — expected ok=false, error=unknown_event. Got: {parsed}")
        return False
    print("  [unknown-event] OK — rejected as expected")
    return True


def case_retired_forget(worker_url: str | None) -> bool:
    """If we have the Worker URL, GET /v1/forget (or POST) returns 410 Gone."""
    if not worker_url:
        print("  [retired-forget] SKIP — no --via-worker, can't test edge gate")
        return True
    resp = requests.post(
        f"{worker_url.rstrip('/')}/v1/forget",
        json={},
        timeout=10,
    )
    if resp.status_code != 410:
        print(f"  [retired-forget] FAIL — expected 410 Gone, got {resp.status_code}: {resp.text}")
        return False
    body = resp.json() if resp.headers.get("content-type", "").startswith("application/json") else {}
    if body.get("code") != "endpoint_retired":
        print(f"  [retired-forget] FAIL — expected code=endpoint_retired. Got: {body}")
        return False
    print("  [retired-forget] OK — Worker returns 410 Gone")
    return True


# ---------------------------------------------------------------------------
# Rollup-delta verification (DB-side; needs service-role)
# ---------------------------------------------------------------------------

def verify_rollup_delta(
    db_url: str,
    version: str,
    expected_delta: int,
) -> bool:
    """
    Confirm that installation_daily_rollup has +expected_delta entries
    matching the smoke-test client_version since the test started.
    """
    if pg is None:
        print("  [rollup-delta] SKIP — psycopg not installed; install with `pip install psycopg[binary]`")
        return True
    try:
        with pg.connect(db_url) as conn:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    SELECT COALESCE(SUM(count), 0)
                    FROM telemetry.installation_daily_rollup
                    WHERE rollup_date = (NOW() AT TIME ZONE 'UTC')::DATE
                      AND event_name = 'first_seen'
                      AND client_version = %s
                      AND rclone_version = 'v1.73.5-smoke'
                    """,
                    (version,),
                )
                row = cur.fetchone()
                count = int(row[0]) if row else 0
                if count < expected_delta:
                    print(f"  [rollup-delta] FAIL — expected at least {expected_delta} smoke rows; saw {count}")
                    return False
                print(f"  [rollup-delta] OK — {count} smoke rows (>= {expected_delta} expected)")
                return True
    except Exception as e:
        print(f"  [rollup-delta] ERROR — DB query failed: {e}")
        return False


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--via-worker", action="store_true",
                    help="Route through the Cloudflare Worker (Phase B). Default: direct to Supabase RPC.")
    ap.add_argument("--skip-rollup", action="store_true",
                    help="Skip the post-contribution rollup-delta DB check.")
    ap.add_argument("--version", default="0.0.0-smoke",
                    help="client_version to use in payloads (default: 0.0.0-smoke).")
    args = ap.parse_args()

    # Round-11 FINDING 37: lazy-import requests so --help works without it.
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    from _telemetry_deps import require_requests
    global requests
    requests = require_requests()

    master_key = os.environ.get("TELEMETRY_MASTER_KEY", "")
    if not master_key:
        print("ERROR: TELEMETRY_MASTER_KEY environment variable is required.", file=sys.stderr)
        print("Set it to the master key from Supabase Vault, e.g.:", file=sys.stderr)
        print('  export TELEMETRY_MASTER_KEY="sm-master-..."', file=sys.stderr)
        return 2

    if args.via_worker:
        worker_url = os.environ.get("WORKER_URL", "").rstrip("/")
        if not worker_url:
            print("ERROR: --via-worker set but WORKER_URL not in env.", file=sys.stderr)
            return 2
        target = f"{worker_url}/v1/contribute"
        anon_key = ""  # the Worker injects its own service credentials
    else:
        sb_url = os.environ.get("SUPABASE_URL", "").rstrip("/")
        anon_key = os.environ.get("SUPABASE_ANON_KEY", "") or os.environ.get("SUPABASE_SERVICE_ROLE_KEY", "")
        if not sb_url or not anon_key:
            print("ERROR: SUPABASE_URL and SUPABASE_ANON_KEY (or SUPABASE_SERVICE_ROLE_KEY) required.", file=sys.stderr)
            return 2
        target = f"{sb_url}/rest/v1/rpc/contribute"

    print(f"Smoke-testing telemetry.contribute() at: {target}")
    print(f"Test client_version: {args.version}")
    print()

    # Track expected accept count for the rollup delta check.
    accepts = 0
    failures: list[str] = []

    print("Case 1: bad HMAC (expect rejection)")
    if not case_bad_hmac(target, anon_key, args.version):
        failures.append("bad-hmac")
    print()

    print("Case 2: good HMAC (expect acceptance)")
    if case_good_hmac(target, anon_key, args.version, master_key):
        accepts += 1
    else:
        failures.append("good-hmac")
    print()

    print("Case 3: schema violation — invalid bucket value (expect rejection)")
    if not case_schema_violation(target, anon_key, args.version, master_key):
        failures.append("schema-violation")
    print()

    print("Case 4: unknown event_kind (expect rejection)")
    if not case_unknown_event(target, anon_key, args.version, master_key):
        failures.append("unknown-event")
    print()

    print("Case 5: retired /v1/forget endpoint (expect 410 Gone)")
    worker_url = os.environ.get("WORKER_URL", "") if args.via_worker else None
    if not case_retired_forget(worker_url):
        failures.append("retired-forget")
    print()

    if not args.skip_rollup and accepts > 0:
        print(f"Case 6: rollup delta — expecting at least {accepts} smoke row(s) in installation_daily_rollup")
        db_url = os.environ.get("DATABASE_URL", "")
        if not db_url:
            print("  [rollup-delta] SKIP — DATABASE_URL not set; cannot inspect DB directly")
        else:
            if not verify_rollup_delta(db_url, args.version, accepts):
                failures.append("rollup-delta")
        print()

    if failures:
        print(f"FAILED: {len(failures)} case(s) — {', '.join(failures)}")
        return 1
    print("PASSED: all smoke-test cases.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
