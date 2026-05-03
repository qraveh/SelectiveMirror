#!/usr/bin/env python3
"""
End-to-end live verification of FINDING 16 path-(a) — first_seen +
upgrade install-event submit pipeline.

Mirrors what `internal/telemetry/install_events.go::SendInstallEventsIfDue`
does at daemon startup, but runs from this script so we can exercise
the full chain (HMAC sign → live Worker → live PostgREST → live
contribute() RPC → installation_daily_rollup UPSERT) without
building a CI-signed binary or running the daemon.

What this proves end-to-end:

  1. The per-version HMAC scheme (server's verify_versioned_hmac
     against the client's per-version derived key) round-trips for
     the install-event payload shape (not just bug_report).

  2. The live Worker accepts a well-formed first_seen payload via
     /v1/contribute and PostgREST dispatches it to _bump_install,
     which UPSERTs into installation_daily_rollup.

  3. The live Worker accepts an upgrade payload (with prior_version
     + days_since_first_seen_bucket dimensions) and the same UPSERT
     path lands the row in the same table with a different bucket
     key.

  4. Both rows are visible via DATABASE_URL queries (k-anon NOT
     applied at the table level).

  5. Cleanup: the script DELETEs both test rows after verification.

Test-marker convention: every contribution this script sends carries
a version string starting with `0.0.0-r12-path-a-` so the cleanup
DELETE is a clean grep. Validator-pass test markers are flagged by
classify_version's regex (-r\\d+[a-z]?-) so the operator-debug view
classifies them correctly.

Usage:
    set -a; source ~/.smirror-deploy.env; set +a
    python3 scripts/verify-path-a-live.py
    # exit 0 if both events landed and were cleaned up;
    # non-zero if any step fails (test rows are still cleaned up)

Required env:
    DATABASE_URL          — live Supabase Transaction pooler URL
    TELEMETRY_MASTER_KEY  — master HMAC key from Vault
"""
from __future__ import annotations

import datetime as dt
import hashlib
import hmac
import json
import os
import sys
from typing import Any

# Round-11 FINDING 38 echo: this script prints non-ASCII (the "→"
# arrow) in its closing line. On Windows the default stdout is
# cp1252; print() of "→" raises UnicodeEncodeError after main work
# is done — exit code is 0 but the traceback in the user's terminal
# is misleading. Reconfigure stdout/stderr to UTF-8 at module load.
try:
    sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    sys.stderr.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
except (AttributeError, ValueError):
    pass

import requests

try:
    import psycopg
except ImportError:
    sys.stderr.write("ERROR: psycopg required (pip install 'psycopg[binary]')\n")
    sys.exit(2)


WORKER_URL = "https://smirror-telemetry.selectivemirror.workers.dev"

# Test versions. Both contain `-r12-path-a-` so the validator-pass
# regex `-r\d+[a-z]?-` flags them as test-markers in classify_version.
PRIOR_VERSION = "0.0.0-r12-path-a-prior"
NEW_VERSION = "0.0.0-r12-path-a-current"


# ---------------------------------------------------------------------------
# Canonical JSON — length-first key ordering, no HTML escape.
# Mirrors internal/telemetry/canonical.go::CanonicalJSON exactly.
# ---------------------------------------------------------------------------

def canonical_json(value: Any) -> str:
    if isinstance(value, dict):
        items = sorted(value.items(), key=lambda kv: (len(kv[0]), kv[0]))
        return "{" + ", ".join(
            f"{json.dumps(k, ensure_ascii=False)}: {canonical_json(v)}"
            for k, v in items
        ) + "}"
    if isinstance(value, list):
        return "[" + ", ".join(canonical_json(x) for x in value) + "]"
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        return json.dumps(value)
    return json.dumps(value, ensure_ascii=False)


def derive_key(master_key: str, version: str) -> bytes:
    """HMAC-SHA256(version, master) — message=version, key=master.
    Mirrors the SQL verify_versioned_hmac and Go SignPayload chain."""
    return hmac.new(master_key.encode("utf-8"),
                    version.encode("utf-8"),
                    hashlib.sha256).digest()


def sign_payload(payload: dict, master_key: str, version: str) -> str:
    """Compute the HMAC over canonical(payload - event_kind - version_hmac)."""
    signing_payload = {k: v for k, v in payload.items()
                       if k not in ("event_kind", "version_hmac")}
    canonical = canonical_json(signing_payload).encode("utf-8")
    derived = derive_key(master_key, version)
    return hmac.new(derived, canonical, hashlib.sha256).hexdigest()


def post_contribute(payload: dict, version: str, master_key: str) -> tuple[int, dict]:
    sig = sign_payload(payload, master_key, version)
    body = {
        "payload": payload,
        "claimed_version": version,
        "claimed_hmac_hex": sig,
    }
    resp = requests.post(WORKER_URL + "/v1/contribute", json=body, timeout=15)
    try:
        return resp.status_code, resp.json()
    except Exception:
        return resp.status_code, {"raw": resp.text[:200]}


# ---------------------------------------------------------------------------
# Payloads — mirror internal/telemetry/payloads.go shape
# ---------------------------------------------------------------------------

def make_first_seen_payload(version: str) -> dict:
    """A complete first_seen payload at the v2 schema; the same shape
    BuildInstallationPayload produces."""
    return {
        "event_kind":          "first_seen",
        "schema_version":      1,
        "install_id":          "sm-r12-path-a-verify-deadbeef",
        "reported_at":         dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S+00:00"),
        "client_version":      version,
        "install_method":      "msi",
        "os_family":           "windows",
        "mirror_count_bucket": "1",
        "background_mode":     "service",
        "delete_policy":       "delete",
        "has_hooks":           False,
        "has_filters":         True,
        "has_alert_webhook":   False,
        "has_bandwidth_limit": False,
        "rclone_version":      "v1.73.5-r12",
    }


def make_upgrade_payload(new_version: str, prior_version: str) -> dict:
    """An upgrade payload — first_seen fields plus prior_version
    and days_since_first_seen_bucket."""
    base = make_first_seen_payload(new_version)
    base["event_kind"] = "upgrade"
    base["prior_version"] = prior_version
    base["days_since_first_seen_bucket"] = "1-7"
    return base


# ---------------------------------------------------------------------------
# DB verification + cleanup
# ---------------------------------------------------------------------------

def query_install_row(cur, version: str, event_name: str) -> dict | None:
    cur.execute("""
        SELECT rollup_date, event_name::text, install_method, os_family,
               client_version, mirror_count_bucket::text, prior_version,
               days_since_first_seen_bucket::text, count
          FROM telemetry.installation_daily_rollup
         WHERE client_version = %s AND event_name::text = %s
    """, (version, event_name))
    rows = cur.fetchall()
    if not rows:
        return None
    return {
        "rollup_date": rows[0][0],
        "event_name": rows[0][1],
        "install_method": rows[0][2],
        "os_family": rows[0][3],
        "client_version": rows[0][4],
        "mirror_count_bucket": rows[0][5],
        "prior_version": rows[0][6],
        "days_since_first_seen_bucket": rows[0][7],
        "count": rows[0][8],
    }


def cleanup(cur) -> int:
    """DELETE every row this script's test markers contributed."""
    cur.execute("""
        DELETE FROM telemetry.installation_daily_rollup
         WHERE client_version LIKE '0.0.0-r12-path-a-%%'
            OR prior_version LIKE '0.0.0-r12-path-a-%%'
        RETURNING client_version, event_name::text
    """)
    deleted = cur.fetchall()
    return len(deleted)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    if "DATABASE_URL" not in os.environ:
        sys.stderr.write("ERROR: DATABASE_URL env var required.\n")
        return 2
    if "TELEMETRY_MASTER_KEY" not in os.environ:
        sys.stderr.write("ERROR: TELEMETRY_MASTER_KEY env var required.\n")
        return 2

    master = os.environ["TELEMETRY_MASTER_KEY"]
    db_url = os.environ["DATABASE_URL"]

    print(f"==> Phase 1: cleanup any stale r12 rows from a prior run")
    with psycopg.connect(db_url, autocommit=True) as conn:
        with conn.cursor() as cur:
            n = cleanup(cur)
            print(f"  Stale rows removed: {n}")

    print()
    print(f"==> Phase 2: POST first_seen via live Worker (version={PRIOR_VERSION})")
    payload = make_first_seen_payload(PRIOR_VERSION)
    status, body = post_contribute(payload, PRIOR_VERSION, master)
    print(f"  HTTP {status}: {body}")
    if status != 200 or body.get("ok") is not True:
        print("  FAIL: first_seen was not accepted")
        return 1

    print()
    print(f"==> Phase 3: verify first_seen row landed in installation_daily_rollup")
    with psycopg.connect(db_url, autocommit=True) as conn:
        with conn.cursor() as cur:
            row = query_install_row(cur, PRIOR_VERSION, "first_seen")
    if row is None:
        print("  FAIL: first_seen row NOT visible in DB after server returned ok=true")
        return 1
    print(f"  Found: {row}")

    print()
    print(f"==> Phase 4: POST upgrade via live Worker")
    print(f"    (prior_version={PRIOR_VERSION}, client_version={NEW_VERSION})")
    payload = make_upgrade_payload(NEW_VERSION, PRIOR_VERSION)
    status, body = post_contribute(payload, NEW_VERSION, master)
    print(f"  HTTP {status}: {body}")
    if status != 200 or body.get("ok") is not True:
        print("  FAIL: upgrade was not accepted")
        cleanup_after_failure(db_url)
        return 1

    print()
    print(f"==> Phase 5: verify upgrade row landed in installation_daily_rollup")
    with psycopg.connect(db_url, autocommit=True) as conn:
        with conn.cursor() as cur:
            row = query_install_row(cur, NEW_VERSION, "upgrade")
    if row is None:
        print("  FAIL: upgrade row NOT visible in DB")
        cleanup_after_failure(db_url)
        return 1
    print(f"  Found: {row}")
    if row["prior_version"] != PRIOR_VERSION:
        print(f"  FAIL: upgrade row prior_version mismatch: {row['prior_version']!r} != {PRIOR_VERSION!r}")
        cleanup_after_failure(db_url)
        return 1
    if row["days_since_first_seen_bucket"] != "1-7":
        print(f"  FAIL: days_since_first_seen_bucket mismatch: {row['days_since_first_seen_bucket']!r}")
        cleanup_after_failure(db_url)
        return 1
    print(f"  OK: prior_version + days_since_first_seen_bucket are correct")

    print()
    print(f"==> Phase 6: cleanup test rows")
    with psycopg.connect(db_url, autocommit=True) as conn:
        with conn.cursor() as cur:
            n = cleanup(cur)
            print(f"  Rows DELETEd: {n}")

    print()
    print(f"==> ALL PHASES PASSED")
    print(f"    Path-(a) install-event submit pipeline verified end-to-end.")
    print(f"    first_seen + upgrade events land in installation_daily_rollup")
    print(f"    via the live Worker → PostgREST → contribute() chain.")
    return 0


def cleanup_after_failure(db_url: str) -> None:
    try:
        with psycopg.connect(db_url, autocommit=True) as conn:
            with conn.cursor() as cur:
                n = cleanup(cur)
                print(f"  (cleaned up {n} test row(s) after failure)")
    except Exception as e:
        print(f"  WARN: cleanup-after-failure failed: {e}")


if __name__ == "__main__":
    sys.exit(main())
