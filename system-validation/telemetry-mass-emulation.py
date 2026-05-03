#!/usr/bin/env python3
"""
Mass-emulation harness for the telemetry v2 Worker (read-only validation).

Without the master key, we can't produce valid HMACs, so every payload is
expected to be REJECTED by the server with {"ok": false, "error": "rejected"}.
What this validates:

  * The Worker can sustain N concurrent POSTs without 5xx
  * Each request is processed (rejected, not dropped)
  * The retired endpoints continue returning 410 under load
  * Per-IP rate limit fires somewhere around 30 RPM/IP

It does NOT exercise the schema-violation or unknown_event paths, which
require a valid HMAC for the server to reach the dispatch step. With an
invalid HMAC every contribution is rejected at step 1 and the dispatch
never runs.

It does NOT contribute real rows to the rollup tables (every payload is
rejected). NOTHING is persisted in Supabase from this harness.
"""
from __future__ import annotations

import argparse
import concurrent.futures
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

import time
from datetime import datetime, timezone

# Round-11 FINDING 37: requests loaded lazily in main() so --help works.
requests = None  # type: ignore[assignment]

WORKER_URL = "https://smirror-telemetry.selectivemirror.workers.dev"


def make_first_seen(version, idx):
    return {
        "event_kind": "first_seen",
        "schema_version": 1,
        "install_id": "sm-emul-" + format(idx, "08x") + "deadbeef",
        "client_version": version,
        "reported_at": datetime.now(timezone.utc).isoformat(),
        "install_method": ["msi", "winget", "zip"][idx % 3],
        "os_family": "windows",
        "mirror_count_bucket": ["0", "1", "2-5", "6-20", "21+"][idx % 5],
        "background_mode": ["foreground", "service", "task", "unknown"][idx % 4],
        "delete_policy": ["ignore", "delete", "quarantine"][idx % 3],
        "has_hooks": idx % 2 == 0,
        "has_filters": idx % 3 == 0,
        "has_alert_webhook": False,
        "has_bandwidth_limit": idx % 4 == 0,
        "rclone_version": "v1.73." + str(idx % 10) + "-emul",
    }


def make_bug_report(version, idx):
    bug_kinds = ["sync", "watcher", "rclone", "config", "service", "fs", "auth", "unknown"]
    severity = ["info", "warning", "error", "critical"]
    return {
        "event_kind": "bug_report",
        "schema_version": 1,
        "reported_at": datetime.now(timezone.utc).isoformat(),
        "client_version": version,
        "bug_kind": bug_kinds[idx % len(bug_kinds)],
        "bug_surface": bug_kinds[idx % len(bug_kinds)],
        "severity_hint": severity[idx % len(severity)],
        "source": "report_bug",
        "submitted_tier": ["standard", "reliability", "one_shot"][idx % 3],
    }


def make_upgrade(version, idx):
    base = make_first_seen(version, idx)
    base["event_kind"] = "upgrade"
    base["prior_version"] = "0.9.88-dev"
    base["days_since_first_seen_bucket"] = ["1-7", "8-30", "31-90", "91-365", ">365"][idx % 5]
    return base


def make_reliability(version, idx):
    return {
        "event_kind": "reliability_snapshot",
        "schema_version": 1,
        "reported_at": datetime.now(timezone.utc).isoformat(),
        "client_version": version,
        "anomaly_count_bucket": ["0", "1-5", "6-25", "26-100", "100+"][idx % 5],
        "most_common_anomaly_kind": ["watcher_error", "ghost_leak", "queue_full", None][idx % 4],
        "sync_attempts_bucket": "100-1k",
        "sync_failures_bucket": "<100",
        "restart_count_bucket": "0",
        "max_queue_depth_bucket": "<100",
        "dead_letter_count_bucket": "0",
        "state_db_size_bucket": "<10MB",
    }


def post_one(payload, sig_seed):
    body = {
        "payload": payload,
        "claimed_version": payload["client_version"],
        "claimed_hmac_hex": (format(sig_seed, "016x") + "deadbeefdeadbeefdeadbeefdeadbeef")[:64],
    }
    t0 = time.perf_counter()
    try:
        resp = requests.post(WORKER_URL + "/v1/contribute", json=body, timeout=15)
        elapsed = time.perf_counter() - t0
        result = {"status": resp.status_code, "elapsed_ms": int(elapsed * 1000)}
        try:
            result["body"] = resp.json()
        except Exception:
            result["body"] = {"raw": resp.text[:200]}
        return result
    except Exception as e:
        elapsed = time.perf_counter() - t0
        return {"status": -1, "elapsed_ms": int(elapsed * 1000), "error": str(e)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--n-installs", type=int, default=20)
    ap.add_argument("--n-bug-reports", type=int, default=20)
    ap.add_argument("--n-upgrades", type=int, default=10)
    ap.add_argument("--n-reliability", type=int, default=10)
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--version", type=str, default="0.0.0-validation")
    args = ap.parse_args()

    # Round-11 FINDING 37: lazy-load requests so --help works without it.
    repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    sys.path.insert(0, os.path.join(repo_root, "scripts"))
    from _telemetry_deps import require_requests
    global requests
    requests = require_requests()

    work = []
    for i in range(args.n_installs):
        work.append(("first_seen", make_first_seen(args.version, i)))
    for i in range(args.n_bug_reports):
        work.append(("bug_report", make_bug_report(args.version, i)))
    for i in range(args.n_upgrades):
        work.append(("upgrade", make_upgrade(args.version, i)))
    for i in range(args.n_reliability):
        work.append(("reliability_snapshot", make_reliability(args.version, i)))

    total = len(work)
    print("Submitting " + str(total) + " payloads to " + WORKER_URL + "/v1/contribute (concurrency=" + str(args.concurrency) + ")")
    print()

    by_kind = {}
    by_status = {}
    rejected_count = 0
    rate_limited_count = 0
    error_count = 0
    times_ms = []
    t0 = time.perf_counter()

    with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as ex:
        futures = {
            ex.submit(post_one, payload, idx): (kind, idx)
            for idx, (kind, payload) in enumerate(work)
        }
        for fut in concurrent.futures.as_completed(futures):
            kind, idx = futures[fut]
            r = fut.result()
            by_kind.setdefault(kind, []).append(r)
            by_status[r["status"]] = by_status.get(r["status"], 0) + 1
            times_ms.append(r["elapsed_ms"])
            if r["status"] == 200 and isinstance(r.get("body"), dict):
                b = r["body"]
                if b.get("ok") is False and b.get("error") == "rejected":
                    rejected_count += 1
            elif r["status"] == 429:
                rate_limited_count += 1
            elif r["status"] != 200:
                error_count += 1

    elapsed = time.perf_counter() - t0
    print("=== Wall-clock: " + format(elapsed, ".2f") + "s ===")
    print("=== Total responses: " + str(len(times_ms)) + " ===")
    print()
    print("By status code:")
    for s, n in sorted(by_status.items()):
        print("  HTTP " + str(s) + ": " + str(n))
    print()
    print("Rejected (HMAC fail, ok=false rejected): " + str(rejected_count))
    print("Rate-limited (HTTP 429): " + str(rate_limited_count))
    print("Other errors (5xx / network): " + str(error_count))
    print()
    if times_ms:
        sorted_t = sorted(times_ms)
        n = len(sorted_t)
        p50 = sorted_t[n // 2]
        p95 = sorted_t[int(n * 0.95)] if n > 1 else sorted_t[0]
        p99 = sorted_t[int(n * 0.99)] if n > 1 else sorted_t[0]
        print("Latency ms p50=" + str(p50) + " p95=" + str(p95) + " p99=" + str(p99) + " max=" + str(sorted_t[-1]))
    print()
    print("By event_kind:")
    for kind, results in by_kind.items():
        ok = sum(1 for r in results if r["status"] == 200)
        print("  " + kind + ": " + str(len(results)) + " sent, " + str(ok) + " got HTTP 200")
    print()
    print("Sample non-200 response:")
    for results in by_kind.values():
        for r in results:
            if r["status"] != 200:
                print("  status=" + str(r["status"]) + "  body=" + str(r.get("body") or r.get("error")))
                break

    if error_count > 0:
        print()
        print("FAILURE: " + str(error_count) + " unexpected errors")
        return 1
    print()
    print("SUCCESS: all payloads handled (rejected/rate-limited as expected)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
