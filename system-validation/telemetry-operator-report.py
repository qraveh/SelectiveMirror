#!/usr/bin/env python3
"""
Operator-facing telemetry report — superset of scripts/telemetry-report.py.

**INTERNAL DEBUG TOOL — NOT FOR PUBLICATION.** This report dumps raw
rollup data WITHOUT the k-anonymity floor that the published weekly
digest applies. It is intended for the maintainer to inspect the
state of the rollup tables, not for sharing externally. Pasting this
output to Slack / GitHub Issues / a blog post would publish small-
cell counts that the privacy contract suppresses in the canonical
digest.

The script renders a banner at the top of every output reminding the
reader, and the default filename pattern (`*-INTERNAL-*.md`) carries
the same warning. Pass `--confirm-internal-only` to acknowledge.

Adds the four sections the user (validation round 7b) flagged as missing:

  1. "smirror versions" — every distinct client_version that has ever
     contributed, across all three rollup tables, sorted by recency
     of contribution.
  2. "How many distinct versions reported" — single headline number
     up top.
  3. "Release-date join" — every contributed version cross-referenced
     against the git-tag history (release-date, latest-tag flag, dev/
     released classification). Versions seen in telemetry but with no
     matching tag are surfaced (likely -dev or -r7-/-r7b- markers).
  4. "Raw data dump" — per-table top-50 rows so the operator can see
     the actual rollup-table contents (not just the k-anonymity-
     filtered summary).

Round-8 hardening (validation memo 2026-05-03):

  FINDING 26: replaced inferior `md_escape` with `md_cell_escape`
              — strips control chars, escapes <>&{}[], converts
              embedded newlines to spaces (Markdown-injection
              defense).
  FINDING 27: <>& now escaped (HTML-injection defense).
  FINDING 28: control characters (incl. RTL override U+202E) stripped.
  FINDING 29: cell content truncated at 120 chars with `…`.
  FINDING 30: NOT FOR PUBLICATION banner at the top of every report
              + opt-in `--confirm-internal-only` flag (script refuses
              to render without it). `--for-publish` is intentionally
              not provided; the canonical publish path is
              scripts/telemetry-report.py.
  FINDING 31: Supabase hostname removed from output; only project
              ID surfaces.
  FINDING 32: data-freshness section added (latest contribution
              age across all rollup tables).
  FINDING 33: each section query wrapped in try/except so a DB
              hiccup mid-report produces a "(query failed)" cell
              instead of a half-rendered Markdown file with a
              Python traceback wedged in.

Inputs:
  DATABASE_URL: live Supabase PostgreSQL connection (sourced from
                ~/.smirror-deploy.env).
  Repo root:    git tag history is read via `git tag --list 'v*'
                --format='%(refname:short)|%(creatordate:short)'`.

Output: Markdown to stdout. Pipe to a file for the operator
deliverable. Convention: filename includes `-INTERNAL-`.

Usage:
    set -a; source ~/.smirror-deploy.env; set +a
    python3 system-validation/telemetry-operator-report.py \\
        --confirm-internal-only \\
        > system-validation/telemetry-operator-report-INTERNAL-r7b.md
"""
from __future__ import annotations

import argparse
import datetime as dt
import os
import re
import subprocess
import sys
import traceback

import psycopg


# ---------------------------------------------------------------------------
# Markdown escape — PANEL-2 (BMAD review 2026-05-03).
#
# Imported from scripts/_telemetry_md.py — the single source of truth
# for the Markdown table-cell escaper. Both this script and
# scripts/telemetry-report.py (the canonical published weekly digest)
# import from there.
#
# Background: the panel found two near-identical-but-not-identical
# escape functions in the two scripts. Divergence between operator-
# debug and publish-safe sanitization is itself a privacy bug —
# whichever is patched first becomes the more-hardened one; the other
# regresses silently. PANEL-2 collapsed both into one module.
#
# DO NOT add a copy of md_cell_escape back here. If a render path
# needs different behavior, parameterize the shared helper.
# ---------------------------------------------------------------------------
_REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(_REPO_ROOT, "scripts"))
from _telemetry_md import md_cell_escape  # noqa: E402,F401


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def get_release_dates() -> dict[str, dt.date]:
    """Read git tag history. Returns {version_str_no_v_prefix: release_date}."""
    try:
        out = subprocess.check_output(
            ["git", "tag", "--list", "v*", "--format=%(refname:short)|%(creatordate:short)"],
            cwd=os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
            text=True,
        )
    except Exception as e:
        sys.stderr.write(f"WARN: git tag list failed ({e}); release-date join will be empty.\n")
        return {}
    releases = {}
    for line in out.strip().splitlines():
        parts = line.split("|")
        if len(parts) != 2:
            continue
        tag, date_str = parts
        # tag like "v0.9.26" -> "0.9.26"
        version = tag.lstrip("v")
        try:
            releases[version] = dt.date.fromisoformat(date_str)
        except ValueError:
            continue
    return releases


# Validation-pass test-marker patterns. Any client_version that ends
# with one of these — or contains the parameterized round form
# `-r\d+[a-z]?-` (e.g. `-r7-`, `-r7b-`, `-r8-`, `-r10-`) — is treated
# as a test-marker injection, not real-user data. Edit this when a new
# validation pass introduces a new marker style; keep round-number
# parameterization generic so the classifier doesn't need a code
# change every round.
_TEST_MARKER_SUFFIXES = ("-r7b", "-r7-validation", "-r7-handshake")
_TEST_MARKER_ROUND_RE = re.compile(r"-r\d+[a-z]?(-|$)")


def classify_version(v: str, releases: dict) -> tuple[str, str]:
    """Return (release_date_str, classification).
    Classifications: released | dev | test-marker | unknown"""
    base = v.split("-")[0]
    if base in releases:
        return (releases[base].isoformat(),
                "released" if "-" not in v else f"derived-from-released-{base}")
    if v.endswith(_TEST_MARKER_SUFFIXES) or _TEST_MARKER_ROUND_RE.search(v):
        return ("—", "test-marker")
    if "-dev" in v:
        return ("—", "dev (unreleased)")
    return ("—", "unknown / no matching tag")


def project_id_from_url(url: str) -> str:
    """FINDING 31: pull the Supabase project ID out of the connection
    string without surfacing the AWS region / pooler hostname.

    DATABASE_URL pattern (Supabase Transaction pooler):
      postgresql://postgres.<project_ref>:<pwd>@aws-0-eu-west-1.pooler.supabase.com:6543/postgres

    The project ref appears once in the username field. Match it
    explicitly; fall back to "unknown" if anything looks off.
    """
    m = re.search(r"postgres\.([a-z0-9]{16,32})", url or "")
    if m:
        return m.group(1)
    return "unknown"


def banner_block() -> list[str]:
    """The 'NOT FOR PUBLICATION' framing that runs at the top of the
    report (FINDING 30)."""
    return [
        "> ⚠️ **INTERNAL OPERATOR REPORT — NOT FOR PUBLICATION** ⚠️",
        ">",
        "> This report dumps raw rollup-table data WITHOUT the k-anonymity",
        "> floor that the canonical published digest applies. Cells with",
        "> 1-4 contributors appear here verbatim and would leak distinct",
        "> install fingerprints if shared externally. Suppress before",
        "> sharing — or run `scripts/telemetry-report.py` for the",
        "> publish-safe weekly digest.",
        ">",
        "> Reading this is fine; copy-pasting it to Slack / a blog post /",
        "> a public GitHub issue is not.",
        "",
    ]


def safe_section(name: str, fn):
    """FINDING 33: wrap a section's rendering in try/except so a DB
    error mid-report produces a clean error stanza instead of a
    half-rendered Markdown file with a Python traceback wedged in."""
    try:
        fn()
    except Exception as e:
        sys.stderr.write(f"ERROR in section '{name}': {type(e).__name__}: {e}\n")
        sys.stderr.write(traceback.format_exc())
        print(f"_(query failed for section '{name}': {type(e).__name__}. The rest of the report continues; check stderr for details.)_")
        print()


# ---------------------------------------------------------------------------
# Section renderers
# ---------------------------------------------------------------------------

def _classification_bucket(v: str, releases: dict) -> str:
    """Bucket a client_version into 'released' / 'dev' / 'test-marker'
    / 'unknown' for the Section-0 breakdown. Lighter-weight wrapper
    over `classify_version` that drops the date and normalizes the
    'derived-from-released-…' refinement back to 'released'."""
    if not v:
        return "unknown"
    _date, cls = classify_version(v, releases)
    if cls.startswith("released") or cls.startswith("derived-from-released"):
        return "released"
    if cls.startswith("dev"):
        return "dev"
    if cls == "test-marker":
        return "test-marker"
    return "unknown"


def render_real_data_baseline(cur, releases) -> None:
    """PANEL-3 (BMAD review 2026-05-03): Mary's "Section 0 — Real-data
    baseline."

    The user's framing — *"we do not store raw data, so the report
    should be perfect from the first run"* — combined with FINDING 16
    (real-user rollups are empty in production) means the operator
    cannot trust ANY downstream number until they know what fraction
    is real users vs. test-marker injection.

    This section answers, BEFORE anything else: of the contributions
    in each rollup table, how many came from production binaries
    downloaded by humans (`released`), from local -dev builds (`dev`),
    from validation-pass test markers (`test-marker`), or from
    versions with no matching git tag (`unknown`). The breakdown is
    derived client-side from the existing `classify_version()` rather
    than a server-side WHERE filter, so the SQL stays readable and
    the classification rules are visible in one place.

    Why first: under FINDING 16 reality, the answer is almost always
    'all rows are test-marker; 0 real users.' If that's the answer,
    the operator can stop reading immediately — every other section
    is synthetic noise."""
    print("## 0 — Real-data baseline (read this BEFORE anything below)")
    print()
    print("Of all contributions currently in the rollup tables, this")
    print("breakdown shows how many came from each version classification.")
    print("`test-marker` rows are validation-pass injections (`-r7`, `-r7b`,")
    print("`-r8-`) that will be DELETEd. `released` rows are from real users")
    print("running a CI-built binary that matches a `v*` git tag.")
    print()
    print("| Rollup table | released | dev | test-marker | unknown | TOTAL |")
    print("| :--- | ---: | ---: | ---: | ---: | ---: |")

    # Aggregate per (table, classification). One row per (table, version)
    # → bucket in Python → sum.
    table_totals = {}
    for tbl, count_col in [("installation_daily_rollup", "count"),
                            ("bug_daily_rollup", "reports"),
                            ("reliability_daily_rollup", "count")]:
        cur.execute(
            f"SELECT client_version, COALESCE(SUM({count_col}), 0)::bigint "
            f"FROM telemetry.{tbl} GROUP BY client_version")
        bucket_sums = {"released": 0, "dev": 0, "test-marker": 0, "unknown": 0}
        for v, n in cur.fetchall():
            bucket = _classification_bucket(v, releases)
            bucket_sums[bucket] += int(n)
        total = sum(bucket_sums.values())
        table_totals[tbl] = (bucket_sums, total)
        print(f"| `{tbl}` | {bucket_sums['released']} | "
              f"{bucket_sums['dev']} | {bucket_sums['test-marker']} | "
              f"{bucket_sums['unknown']} | {total} |")
    print()

    # Verdict line: if ALL three tables have 0 released contributions,
    # tell the operator explicitly that everything below is synthetic.
    all_real = sum(t[0]["released"] for t in table_totals.values())
    all_total = sum(t[1] for t in table_totals.values())
    if all_total == 0:
        print("> 🟢 **Production is empty.** No data in any rollup table.")
        print("> This is the FINDING-16 reality if the install-event submit")
        print("> pipeline (0.9.102-dev) hasn't yet been exercised by real")
        print("> users.")
    elif all_real == 0:
        print("> ⚠️ **Zero real-user contributions.** Every row below is")
        print("> synthetic (test-marker / dev / unknown). Treat aggregate")
        print("> numbers as fixture data, not signal. Real users will appear")
        print("> here as `released` once they install a v1.0 (or later)")
        print("> CI-built binary AND opt into a non-None tier.")
    else:
        real_pct = round(100.0 * all_real / all_total, 1)
        synthetic = all_total - all_real
        print(f"> **{all_real} real-user contributions** ({real_pct}% of "
              f"{all_total}); {synthetic} are synthetic / test-marker.")
        if real_pct < 50:
            print(">")
            print("> Synthetic data exceeds real-user data. Sections below")
            print("> mix both — interpret with caution.")
    print()


def render_freshness(cur) -> None:
    """FINDING 32: data-freshness indicator. Latest contribution date
    across all three rollup tables, plus wall-clock age."""
    print("## Data freshness")
    print()
    cur.execute("""
        SELECT MAX(d) AS last_rollup_date
        FROM (
            SELECT MAX(rollup_date) AS d FROM telemetry.installation_daily_rollup
            UNION ALL
            SELECT MAX(rollup_date)      FROM telemetry.bug_daily_rollup
            UNION ALL
            SELECT MAX(rollup_date)      FROM telemetry.reliability_daily_rollup
        ) m
    """)
    row = cur.fetchone()
    last = row[0] if row else None
    if last is None:
        print("_(all rollup tables empty — no data freshness signal yet)_")
    else:
        today = dt.date.today()
        delta = (today - last).days
        if delta == 0:
            age = "today"
        elif delta == 1:
            age = "1 day ago"
        elif delta < 7:
            age = f"{delta} days ago"
        elif delta < 30:
            weeks = delta // 7
            age = f"{weeks} week{'s' if weeks > 1 else ''} ago"
        else:
            months = delta // 30
            age = f"~{months} month{'s' if months > 1 else ''} ago"
        emoji = "🟢" if delta < 7 else "🟡" if delta < 30 else "🔴"
        print(f"{emoji} **Last rollup_date**: `{last.isoformat()}` ({age})")
        if delta >= 30:
            print()
            print("_The pipeline may be stalled. Check Worker probe + Supabase status._")
    print()


def render_headline_counts(cur) -> None:
    print("## 1 — Headline counts (no k-anonymity floor; raw)")
    print()
    print("| Table | Buckets | Total contributions | Distinct client_versions | Distinct rclone_versions |")
    print("| :--- | ---: | ---: | ---: | ---: |")
    for tbl, c in [("installation_daily_rollup", "count"),
                    ("bug_daily_rollup", "reports"),
                    ("reliability_daily_rollup", "count")]:
        rcl_col = ", COUNT(DISTINCT rclone_version)" if tbl == "installation_daily_rollup" else ", '—'"
        cur.execute(f"SELECT COUNT(*), COALESCE(SUM({c}), 0), COUNT(DISTINCT client_version){rcl_col} FROM telemetry.{tbl}")
        n, total, n_v, n_rcl = cur.fetchone()
        print(f"| {tbl} | {n} | {total} | {n_v} | {n_rcl} |")
    print()


def render_versions_seen(cur, releases) -> None:
    print("## 2 — Every smirror version seen in telemetry")
    print()
    # PANEL-1 (BMAD review 2026-05-03): the prior implementation used
    # nested FULL OUTER JOINs with `ON COALESCE(av.client_version,
    # bv.client_version) = rv.client_version`, which silently dropped
    # versions that only ever fired reliability snapshots — when both
    # av and bv sides are NULL the COALESCE is NULL, the join condition
    # `NULL = rv.client_version` is false, and the rel-only row is lost.
    # Headline "Distinct versions seen: N" silently undercount.
    #
    # Fix: build the union of client_versions across all three rollup
    # tables FIRST, then LEFT JOIN each per-table aggregate onto it.
    # The driver row set is now correctly the UNION; nothing falls off.
    cur.execute("""
        WITH all_versions AS (
            SELECT DISTINCT client_version FROM telemetry.installation_daily_rollup
            UNION
            SELECT DISTINCT client_version FROM telemetry.bug_daily_rollup
            UNION
            SELECT DISTINCT client_version FROM telemetry.reliability_daily_rollup
        ),
        install_agg AS (
            SELECT client_version,
                   MAX(rollup_date) AS last_seen,
                   MIN(rollup_date) AS first_seen,
                   SUM(count)       AS install_events
            FROM telemetry.installation_daily_rollup
            GROUP BY client_version
        ),
        bug_agg AS (
            SELECT client_version,
                   MAX(rollup_date) AS bug_last,
                   MIN(rollup_date) AS bug_first,
                   SUM(reports)     AS bug_events
            FROM telemetry.bug_daily_rollup
            GROUP BY client_version
        ),
        rel_agg AS (
            SELECT client_version,
                   MAX(rollup_date) AS rel_last,
                   MIN(rollup_date) AS rel_first,
                   SUM(count)       AS rel_events
            FROM telemetry.reliability_daily_rollup
            GROUP BY client_version
        )
        SELECT
            v.client_version,
            -- last_seen / first_seen across ALL tables (PANEL-1 bug
            -- before: used only install_agg.last_seen / first_seen,
            -- so versions with no install events showed `—` for
            -- both even when bug or reliability data existed).
            GREATEST(ia.last_seen,  ba.bug_last,  ra.rel_last)  AS last_seen,
            LEAST(   ia.first_seen, ba.bug_first, ra.rel_first) AS first_seen,
            COALESCE(ia.install_events, 0) AS install_events,
            COALESCE(ba.bug_events,     0) AS bug_events,
            COALESCE(ra.rel_events,     0) AS rel_events
        FROM all_versions v
        LEFT JOIN install_agg ia ON v.client_version = ia.client_version
        LEFT JOIN bug_agg     ba ON v.client_version = ba.client_version
        LEFT JOIN rel_agg     ra ON v.client_version = ra.client_version
        ORDER BY (COALESCE(ia.install_events, 0)
                   + COALESCE(ba.bug_events, 0)
                   + COALESCE(ra.rel_events, 0)) DESC,
                 v.client_version DESC
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(none — all rollup tables empty)_")
    else:
        print(f"**Distinct versions seen**: **{len(rows)}**")
        print()
        print("| client_version | release_date | classification | first_seen (telemetry) | last_seen | install events | bug events | reliability events | TOTAL |")
        print("| :--- | :--- | :--- | :--- | :--- | ---: | ---: | ---: | ---: |")
        for r in rows:
            v, last_seen, first_seen, ins, bug, rel = r
            rdate, cls = classify_version(v or "", releases)
            total = ins + bug + rel
            print(f"| `{md_cell_escape(v)}` | {md_cell_escape(rdate)} | {md_cell_escape(cls)} | {md_cell_escape(first_seen)} | {md_cell_escape(last_seen)} | {ins} | {bug} | {rel} | {total} |")
    print()


def render_release_timeline(cur, releases) -> None:
    print("## 3 — git-tag release timeline (cross-reference)")
    print()
    if not releases:
        print("_(no git tags matching `v*` found)_")
        print()
        return
    print("| Tag | Release date | Currently in telemetry? |")
    print("| :--- | :--- | :--- |")
    cur.execute("""
        SELECT DISTINCT split_part(client_version, '-', 1)
        FROM (
            SELECT client_version FROM telemetry.installation_daily_rollup
            UNION SELECT client_version FROM telemetry.bug_daily_rollup
            UNION SELECT client_version FROM telemetry.reliability_daily_rollup
        ) v
    """)
    base_versions_in_telemetry = {r[0] for r in cur.fetchall() if r[0]}
    for v, d in sorted(releases.items(), key=lambda kv: kv[1], reverse=True):
        in_tel = "yes" if v in base_versions_in_telemetry else "no"
        print(f"| v{md_cell_escape(v)} | {d.isoformat()} | {in_tel} |")
    print()


def render_raw_dumps(cur) -> None:
    print("## 4 — Raw rollup-table dumps (top 30 rows per table by count)")
    print()
    print("These are the actual rows the digest script aggregates from. Useful for ops debugging when the digest looks wrong.")
    print()

    print("### 4a — installation_daily_rollup (top 30 by count)")
    print()
    cur.execute("""
        SELECT rollup_date, event_name::text, install_method, os_family,
               client_version, mirror_count_bucket::text, background_mode::text,
               delete_policy::text, rclone_version, prior_version,
               days_since_first_seen_bucket::text, count
        FROM telemetry.installation_daily_rollup
        ORDER BY count DESC, rollup_date DESC
        LIMIT 30
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(no rows)_")
    else:
        print("| date | event | method | os | client_version | mirrors | bg | delete | rclone | prior | days_since | count |")
        print("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | ---: |")
        for r in rows:
            print("| " + " | ".join(md_cell_escape(c) for c in r) + " |")
    print()

    print("### 4b — bug_daily_rollup (top 30 by reports)")
    print()
    cur.execute("""
        SELECT rollup_date, bug_kind, bug_surface, client_version,
               severity_hint::text, source::text, submitted_tier::text, reports
        FROM telemetry.bug_daily_rollup
        ORDER BY reports DESC, rollup_date DESC
        LIMIT 30
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(no rows)_")
    else:
        print("| date | kind | surface | client_version | severity | source | tier | reports |")
        print("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | ---: |")
        for r in rows:
            print("| " + " | ".join(md_cell_escape(c) for c in r) + " |")
    print()

    print("### 4c — reliability_daily_rollup (top 30 by count)")
    print()
    cur.execute("""
        SELECT rollup_date, client_version, anomaly_count_bucket::text,
               most_common_anomaly_kind, sync_attempts_bucket::text,
               sync_failures_bucket::text, restart_count_bucket::text,
               max_queue_depth_bucket::text, dead_letter_count_bucket::text,
               state_db_size_bucket::text, count
        FROM telemetry.reliability_daily_rollup
        ORDER BY count DESC, rollup_date DESC
        LIMIT 30
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(no rows)_")
    else:
        print("| date | client_version | anomalies | leading anomaly | sync_att | sync_fail | restart | queue | dead | db_size | count |")
        print("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | ---: |")
        for r in rows:
            print("| " + " | ".join(md_cell_escape(c) for c in r) + " |")
    print()


def render_rclone_distribution(cur) -> None:
    print("## 5 — rclone version distribution (across install events)")
    print()
    cur.execute("""
        SELECT rclone_version, SUM(count) AS installs
        FROM telemetry.installation_daily_rollup
        GROUP BY rclone_version
        ORDER BY installs DESC
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(no rows)_")
    else:
        print("| rclone_version | install events | % of base |")
        print("| :--- | ---: | ---: |")
        total = sum(int(r[1]) for r in rows)
        for v, n in rows:
            n = int(n)
            pct = round(100.0 * n / total, 1) if total else 0
            print(f"| {md_cell_escape(v)} | {n} | {pct}% |")
    print()


def render_bug_taxonomy(cur) -> None:
    print("## 6 — bug-kind taxonomy coverage (which categories have data)")
    print()
    cur.execute("""
        SELECT bug_kind, COUNT(DISTINCT client_version) AS versions_affected,
               SUM(reports) AS total_reports
        FROM telemetry.bug_daily_rollup
        GROUP BY bug_kind
        ORDER BY total_reports DESC
    """)
    rows = cur.fetchall()
    if not rows:
        print("_(no rows)_")
    else:
        print("| bug_kind | distinct versions affected | total reports |")
        print("| :--- | ---: | ---: |")
        for k, nv, tr in rows:
            print(f"| `{md_cell_escape(k)}` | {nv} | {tr} |")
    print()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    # PANEL-2 follow-up: the banner_block() output contains a ⚠️ emoji,
    # the verdict line contains 🟢, and the data-freshness section
    # contains 🟢/🟡/🔴. On Windows the default stdout encoding is
    # cp1252 (which cannot encode emoji), so writing the report to a
    # pipe or file via plain `print()` raises UnicodeEncodeError and
    # produces a 0-byte output file. Force UTF-8 on stdout/stderr at
    # entry — the project is Windows-first; make the script Just Work
    # for the maintainer it's built for.
    try:
        sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
        sys.stderr.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    except (AttributeError, ValueError):
        # Older Python (<3.7) or already-replaced stream — best effort.
        pass

    ap = argparse.ArgumentParser(
        description="Internal operator-facing telemetry report (NOT FOR PUBLICATION).",
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--confirm-internal-only",
                    action="store_true",
                    help="Acknowledge this is an internal report and won't be published. Required.")
    args = ap.parse_args()

    if not args.confirm_internal_only:
        sys.stderr.write(
            "REFUSING to render: this report dumps raw rollup data without the\n"
            "k-anonymity floor. Pass --confirm-internal-only to acknowledge that\n"
            "the output is for the maintainer's eyes only and not for sharing.\n"
            "For the publish-safe digest, run scripts/telemetry-report.py.\n")
        return 2

    if "DATABASE_URL" not in os.environ:
        sys.stderr.write("ERROR: DATABASE_URL env var required.\n")
        return 2

    releases = get_release_dates()
    sys.stderr.write(f"Loaded {len(releases)} releases from git tag history.\n")

    url = os.environ["DATABASE_URL"]
    project_id = project_id_from_url(url)
    try:
        conn = psycopg.connect(url)
        cur = conn.cursor()
    except Exception as e:
        sys.stderr.write(f"ERROR: could not connect to database: {type(e).__name__}: {e}\n")
        return 3

    # Banner + header
    for line in banner_block():
        print(line)
    print("# SelectiveMirror Telemetry — Operator Report")
    print()
    print(f"**Generated**: {dt.datetime.now(dt.timezone.utc).isoformat()} UTC")
    # FINDING 31: project ID only; no hostname / region.
    print(f"**Source**: live Supabase (project ID `{project_id}`)")
    print(f"**Schema**: docs/telemetry-v2.sql (stream-aggregate-and-discard)")
    print()
    print("---")
    print()

    # PANEL-3 (BMAD review 2026-05-03): Section 0 must come BEFORE
    # everything else — under FINDING 16 reality the operator's first
    # question is "is this real-user data or test-marker injection?"
    # If the answer is "0 real users," the operator can stop reading.
    safe_section("real-data baseline", lambda: render_real_data_baseline(cur, releases))

    # FINDING 32: data freshness next so the operator sees pipeline
    # liveness right after the real-vs-synthetic verdict.
    safe_section("freshness", lambda: render_freshness(cur))

    # All other sections wrapped per FINDING 33.
    safe_section("headline counts",       lambda: render_headline_counts(cur))
    safe_section("versions seen",         lambda: render_versions_seen(cur, releases))
    safe_section("release timeline",      lambda: render_release_timeline(cur, releases))
    safe_section("raw rollup dumps",      lambda: render_raw_dumps(cur))
    safe_section("rclone distribution",   lambda: render_rclone_distribution(cur))
    safe_section("bug taxonomy coverage", lambda: render_bug_taxonomy(cur))

    print("---")
    print()
    print("## Notes")
    print()
    print("- **Versions ending in `-r7b` / `-r8-...`** are validation-pass test")
    print("  contributions. They will be DELETEd after operator review.")
    print("- **k-anonymity floor (5) is NOT applied** in this operator report.")
    print("  The published weekly digest (`scripts/telemetry-report.py` via")
    print("  `telemetry-digest.yml`) DOES apply it.")
    print("- **No personal data is in any of these tables by construction** —")
    print("  see `docs/telemetry-architecture-v2.md` and `docs/telemetry-v2.sql`.")
    print("  Every column is either a closed-vocabulary enum or a bucketed range.")
    print()
    # Closing banner so a reader scrolled to the end is reminded.
    for line in banner_block():
        print(line)

    cur.close()
    conn.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
