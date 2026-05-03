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

Relationship to scripts/telemetry-report.py (the canonical
published weekly digest):

  This script is a SIBLING of `telemetry-report.py`, intentionally
  named to reflect subordination — `telemetry-debug` is the
  operator's "give me the un-floored view RIGHT NOW" tool, while
  `telemetry-report` is the contract-bound publish-safe path the
  CI cron uses. They share `_telemetry_md.py` (escape primitive)
  and the same SQL query patterns. The split is explicitly NOT
  flag-toggled (`--debug`) on the canonical script because that
  flag would proliferate concerns across one entry point; the
  sibling-script structure keeps each entry's CLI surface clean.

  Doc-graph: both scripts are listed in
  `docs/operations/telemetry-ops.md`'s glossary table, side-by-side.

Usage:
    set -a; source ~/.smirror-deploy.env; set +a
    python3 scripts/telemetry-debug.py \\
        --confirm-internal-only \\
        > docs/telemetry/telemetry-debug-INTERNAL-$(date +%Y-%m-%d).md
"""
from __future__ import annotations

import argparse
import datetime as dt
import os
import re
import subprocess
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

import traceback

# psycopg is loaded LAZILY in main() rather than at module-load time.
# Round-11 FINDING 37: eager import here breaks --help for any operator
# without psycopg installed. `psycopg` is bound by main() via
# _telemetry_deps.require_psycopg().
psycopg = None  # type: ignore[assignment]


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
# 0.9.10x-dev: operator-debug script moved from system-validation/ to
# scripts/ alongside the canonical published digest (panel scope
# decision, Option C). _telemetry_md is now a sibling — same dir,
# direct import.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
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
    """The 'INTERNAL — DO NOT PUBLISH' framing that runs at the top
    AND bottom of the report.

    Panel item 5 (P2, BMAD round 9): the prior version called itself
    an "Operator Report" — a name that sounds publishable. The
    canonical artifact is the published weekly digest
    (`scripts/telemetry-report.py`); this one is the un-floored
    sibling, intentionally named for subordination. Title in the
    body uses "INTERNAL — Operator View" so a reader who scrolls
    past the banner still lands on the same framing.
    """
    return [
        "> ⚠️ **INTERNAL — DO NOT PUBLISH** ⚠️",
        ">",
        "> This is `scripts/telemetry-debug.py` output — the operator's",
        "> un-floored sibling of `scripts/telemetry-report.py` (the",
        "> canonical published weekly digest). It dumps raw rollup-table",
        "> rows WITHOUT the k-anonymity floor of 5 that the published",
        "> digest applies. Cells with 1-4 contributors appear here",
        "> verbatim and would leak distinct install fingerprints if",
        "> shared externally.",
        ">",
        "> **Reading this is fine; copy-pasting it to Slack / a blog post /",
        "> a public GitHub issue is not.** For a publish-safe view, run:",
        ">",
        "> ```",
        "> python3 scripts/telemetry-report.py",
        "> ```",
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
        print("_(all rollup tables empty — no data freshness signal yet. ")
        print("This is the **FINDING 16 / install-event pipeline** state if no real users have ")
        print("yet contributed: server-side schema is in place, client-side first_seen + upgrade ")
        print("submit pipeline shipped in 0.9.102-dev (commit 11285cb), but rollups stay empty ")
        print("until a CI-signed binary at Standard or Reliability tier runs `smirror start`.)_")
    else:
        # Panel item 6 (P2, BMAD round 9): use UTC date for the
        # freshness arithmetic, not local. The rollup_date column is
        # stored as a UTC date by the server's _bump_* functions
        # (`(reported_at)::TIMESTAMPTZ::DATE`), so comparing against
        # local `today()` produces an off-by-one for operators in
        # any timezone west of UTC.
        today = dt.datetime.now(dt.timezone.utc).date()
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
    # Panel item 11 (P3, BMAD round 9): "raw" → "un-floored." The
    # word "raw" is overloaded in the privacy contract (raw payloads
    # are the v1 antipattern that v2 architecturally eliminated).
    # The accurate description is that this section bypasses the
    # k-anonymity FLOOR — the data is still aggregated; just not
    # suppressed at the small-cell threshold.
    print("## 1 — Headline counts (un-floored; no k-anonymity suppression)")
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


def _empty_state_msg(table: str) -> str:
    """Panel item 7 (P2, BMAD round 9): empty-state message links to
    the FINDING-16 deferral context so the operator immediately
    knows whether "no rows" means "broken pipeline" or "we
    haven't shipped that pipeline yet."

    installation_daily_rollup empty + path-(a) shipped means no
    real-user contributions YET (server expects them; no users have
    started a CI-signed daemon).

    bug_daily_rollup empty means no `report-bug --submit` calls yet.

    reliability_daily_rollup empty is FINDING-16 PERSISTENT — the
    reliability_snapshot writer is deferred to v1.0.x; this rollup
    will stay empty until the sync-engine + watcher counters land.
    """
    if table == "installation_daily_rollup":
        return ("_(no rows. Pipeline shipped 0.9.102-dev / commit 11285cb; "
                "stays empty until a CI-signed binary at non-None tier runs `smirror start`.)_")
    if table == "bug_daily_rollup":
        return ("_(no rows. `report-bug --submit` (SM-158, 0.9.89-dev) has the "
                "wire path; rollup populates per submission.)_")
    if table == "reliability_daily_rollup":
        return ("_(no rows. **FINDING 16 deferred-pipeline:** the "
                "`reliability_snapshot` writer's bucket-dimension counters "
                "are pending in `internal/sync` + `internal/watcher` — see "
                "`docs/PRIVACY.md` 'Currently shipped vs. deferred.' "
                "Reliability tier is functionally identical to Standard "
                "tier today.)_")
    return "_(no rows)_"


def render_raw_dumps(cur, full_dump: bool = False) -> None:
    """Panel item 8 (P2, BMAD round 9): raw dumps now opt-in. Default
    truncates each table to top-10 rows with a "Showing X of N"
    note; pass `--full-dump` for the prior 30-row behavior. Most
    operator visits are first-pass triage — if they need deeper
    inspection they can re-run with the flag.
    """
    cap = 30 if full_dump else 10
    title_n = "top 30" if full_dump else "top 10 (pass `--full-dump` for top 30)"
    print(f"## 4 — Un-floored rollup-table dumps ({title_n} rows per table by count)")
    print()
    print("Actual rows the digest script aggregates from. Useful for ops debugging when the digest looks wrong.")
    print()

    print(f"### 4a — installation_daily_rollup ({title_n} by count)")
    print()
    cur.execute(f"""
        SELECT rollup_date, event_name::text, install_method, os_family,
               client_version, mirror_count_bucket::text, background_mode::text,
               delete_policy::text, rclone_version, prior_version,
               days_since_first_seen_bucket::text, count
        FROM telemetry.installation_daily_rollup
        ORDER BY count DESC, rollup_date DESC
        LIMIT {cap}
    """)
    rows = cur.fetchall()
    if not rows:
        print(_empty_state_msg("installation_daily_rollup"))
    else:
        cur.execute("SELECT COUNT(*) FROM telemetry.installation_daily_rollup")
        total = cur.fetchone()[0]
        print(f"_Showing {len(rows)} of {total} bucket row(s)._")
        print()
        print("| date | event | method | os | client_version | mirrors | bg | delete | rclone | prior | days_since | count |")
        print("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | :--- | ---: |")
        for r in rows:
            print("| " + " | ".join(md_cell_escape(c) for c in r) + " |")
    print()

    print(f"### 4b — bug_daily_rollup ({title_n} by reports)")
    print()
    cur.execute(f"""
        SELECT rollup_date, bug_kind, bug_surface, client_version,
               severity_hint::text, source::text, submitted_tier::text, reports
        FROM telemetry.bug_daily_rollup
        ORDER BY reports DESC, rollup_date DESC
        LIMIT {cap}
    """)
    rows = cur.fetchall()
    if not rows:
        print(_empty_state_msg("bug_daily_rollup"))
    else:
        cur.execute("SELECT COUNT(*) FROM telemetry.bug_daily_rollup")
        total = cur.fetchone()[0]
        print(f"_Showing {len(rows)} of {total} bucket row(s)._")
        print()
        print("| date | kind | surface | client_version | severity | source | tier | reports |")
        print("| :--- | :--- | :--- | :--- | :--- | :--- | :--- | ---: |")
        for r in rows:
            print("| " + " | ".join(md_cell_escape(c) for c in r) + " |")
    print()

    print(f"### 4c — reliability_daily_rollup ({title_n} by count)")
    print()
    cur.execute(f"""
        SELECT rollup_date, client_version, anomaly_count_bucket::text,
               most_common_anomaly_kind, sync_attempts_bucket::text,
               sync_failures_bucket::text, restart_count_bucket::text,
               max_queue_depth_bucket::text, dead_letter_count_bucket::text,
               state_db_size_bucket::text, count
        FROM telemetry.reliability_daily_rollup
        ORDER BY count DESC, rollup_date DESC
        LIMIT {cap}
    """)
    rows = cur.fetchall()
    if not rows:
        print(_empty_state_msg("reliability_daily_rollup"))
    else:
        cur.execute("SELECT COUNT(*) FROM telemetry.reliability_daily_rollup")
        total = cur.fetchone()[0]
        print(f"_Showing {len(rows)} of {total} bucket row(s)._")
        print()
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
    # Panel item 8 (P2, BMAD round 9): raw dumps are opt-in.
    ap.add_argument("--full-dump",
                    action="store_true",
                    help="Show top-30 rows per rollup table in Section 4 (default top-10).")
    ap.add_argument("--verbose-env", action="store_true",
                    help="Print env-file discovery diagnostics to stderr.")
    args = ap.parse_args()

    if not args.confirm_internal_only:
        sys.stderr.write(
            "REFUSING to render: this report dumps raw rollup data without the\n"
            "k-anonymity floor. Pass --confirm-internal-only to acknowledge that\n"
            "the output is for the maintainer's eyes only and not for sharing.\n"
            "For the publish-safe digest, run scripts/telemetry-report.py.\n")
        return 2

    # Round-10 FINDING 36b: PowerShell has no `source` builtin and WSL2's
    # `~` maps to Linux home (not Windows home where ~/.smirror-deploy.env
    # actually lives). Auto-discover the env file across both shells so
    # `python3 scripts/telemetry-debug.py --confirm-internal-only` Just
    # Works without prior `source ~/.smirror-deploy.env`. Operator's
    # manually-set env wins over the file (variables already in
    # os.environ are not overwritten).
    sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
    from _telemetry_env import ensure_database_url
    db_url = ensure_database_url(verbose=args.verbose_env)
    if not db_url:
        sys.stderr.write(
            "ERROR: DATABASE_URL environment variable not set, and no\n"
            ".smirror-deploy.env file found in any of the standard\n"
            "locations (~ / $USERPROFILE / /mnt/c/Users/<you> / repo root).\n"
            "\n"
            "Either:\n"
            "  - Set DATABASE_URL=postgresql://... in your shell, or\n"
            "  - Place .smirror-deploy.env at one of the locations above, or\n"
            "  - Set $SMIRROR_DEPLOY_ENV to its full path.\n"
            "\n"
            "Run with --verbose-env to see which paths were probed.\n")
        return 2

    # Round-11 FINDING 37: lazy-import psycopg so --help works even when
    # the dep isn't installed.
    from _telemetry_deps import require_psycopg
    global psycopg
    psycopg = require_psycopg()

    url = os.environ["DATABASE_URL"]
    project_id = project_id_from_url(url)

    # Emit the banner + header FIRST, before the DB connect attempt.
    # This way an operator who runs the script with a bad
    # DATABASE_URL still sees the "INTERNAL — DO NOT PUBLISH"
    # framing in the output, plus a structured error stanza in
    # place of the data sections. Pre-fix, a connection failure
    # produced an empty stdout with the error only on stderr —
    # operators piping to a file would see an empty file. (Also
    # the path the round-9 Bonus-1 cp1252 regression took before
    # the utf-8 reconfigure landed.)
    for line in banner_block():
        print(line)
    # Panel item 5 (P2, BMAD round 9): title flipped from "Operator
    # Report" (sounds publishable) to "INTERNAL — Operator View"
    # (the framing the banner above just primed). The canonical
    # publishable artifact is the WEEKLY DIGEST; this is its
    # un-floored sibling.
    print("# SelectiveMirror Telemetry — INTERNAL Operator View")
    print()
    print(f"**Generated**: {dt.datetime.now(dt.timezone.utc).isoformat()} UTC")
    # FINDING 31: project ID only; no hostname / region.
    print(f"**Source**: live Supabase (project ID `{project_id}`)")
    print(f"**Schema**: docs/telemetry-v2.sql (stream-aggregate-and-discard)")
    print(f"**Sibling of**: `scripts/telemetry-report.py` (the canonical published digest)")
    print()

    releases = get_release_dates()
    sys.stderr.write(f"Loaded {len(releases)} releases from git tag history.\n")

    try:
        conn = psycopg.connect(url)
        cur = conn.cursor()
    except Exception as e:
        sys.stderr.write(f"ERROR: could not connect to database: {type(e).__name__}: {e}\n")
        # Emit a clean error stanza in the body so the Markdown file
        # is still valid and the operator can see what went wrong.
        print("---")
        print()
        print("## ❌ Database connection failed")
        print()
        print(f"_Could not reach the live Supabase project `{project_id}`._")
        print()
        print("Possible causes:")
        print()
        print("- `DATABASE_URL` env var is malformed or stale")
        print("- Network blocked from this host to the Supabase pooler")
        print("- Supabase project is paused (free tier inactivity)")
        print()
        print(f"Underlying error: `{type(e).__name__}: {e}`")
        print()
        for line in banner_block():
            print(line)
        return 3
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
    safe_section("un-floored rollup dumps", lambda: render_raw_dumps(cur, full_dump=args.full_dump))
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
