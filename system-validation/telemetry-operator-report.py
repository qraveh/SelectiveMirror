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
# Markdown escape — round-8 FINDING 26-29 hardening.
#
# Mirrors the canonical implementation in
# scripts/telemetry-report.py::md_cell_escape. Kept as a duplicate
# rather than imported because the script lives in a different
# directory (system-validation/ vs scripts/) and adding sys.path
# manipulation for one function isn't worth the indirection.
# ---------------------------------------------------------------------------

# Markdown special characters to escape with a backslash. Order matters:
# escape `\` first so we don't double-escape.
_MD_ESCAPE_PAIRS = [
    ("\\", "\\\\"),
    ("|", "\\|"),
    ("`", "\\`"),
    ("*", "\\*"),
    ("_", "\\_"),
    ("[", "\\["),
    ("]", "\\]"),
    ("{", "\\{"),
    ("}", "\\}"),
    ("<", "\\<"),
    (">", "\\>"),
    # Note: `&` is intentionally NOT escaped here. Markdown renderers
    # treat `&` as literal unless it's part of an HTML entity, and
    # escaping it as `\&` produces uglier output for legitimate uses
    # ("Acme & Co" in a project name). HTML-injection via & alone is
    # not a known vector — the dangerous attacks need `<` first, which
    # IS escaped above.
]


def md_cell_escape(s, max_len: int = 120) -> str:
    """Sanitize a string for inclusion in a Markdown table cell.

    1. None → "—"
    2. Strip control characters (< 0x20 plus DEL=0x7F), but convert
       \\r / \\n / \\t to a single space first so they don't introduce
       row-splitting line breaks (FINDING 26).
    3. Strip RTL override / bidi-formatting characters that visually
       reorder text (FINDING 28).
    4. Escape Markdown specials (FINDINGs 26, 27).
    5. Collapse runs of whitespace.
    6. Truncate at max_len chars with `…` (FINDING 29).
    """
    if s is None:
        return "—"
    s = str(s)

    # 2 + 3: clean control + bidi-format chars.
    cleaned = []
    for ch in s:
        cp = ord(ch)
        if ch in ("\r", "\n", "\t"):
            cleaned.append(" ")
        elif cp < 0x20 or cp == 0x7F:
            continue
        elif 0x202A <= cp <= 0x202E or 0x2066 <= cp <= 0x2069:
            # Unicode bidi controls (LRE/RLE/PDF/LRO/RLO + LRI/RLI/FSI/PDI).
            # Strip; their visual effect on table cells is misleading.
            continue
        else:
            cleaned.append(ch)
    s = "".join(cleaned)

    # 4: escape Markdown specials.
    for ch, esc in _MD_ESCAPE_PAIRS:
        s = s.replace(ch, esc)

    # 5: collapse whitespace.
    s = " ".join(s.split())

    # 6: truncate.
    if len(s) > max_len:
        s = s[: max_len - 1] + "…"
    return s


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


def classify_version(v: str, releases: dict) -> tuple[str, str]:
    """Return (release_date_str, classification).
    Classifications: released | dev | test-marker | unknown"""
    base = v.split("-")[0]
    if base in releases:
        return (releases[base].isoformat(),
                "released" if "-" not in v else f"derived-from-released-{base}")
    if v.endswith("-r7b") or v.endswith("-r7-validation") or v.endswith("-r7-handshake") \
            or "-r8-" in v:
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
    cur.execute("""
        WITH all_versions AS (
            SELECT client_version,
                   MAX(rollup_date)               AS last_seen,
                   MIN(rollup_date)               AS first_seen,
                   SUM(count)                     AS install_events
            FROM telemetry.installation_daily_rollup
            GROUP BY client_version
        ),
        bug_versions AS (
            SELECT client_version, SUM(reports) AS bug_events
            FROM telemetry.bug_daily_rollup
            GROUP BY client_version
        ),
        rel_versions AS (
            SELECT client_version, SUM(count) AS rel_events
            FROM telemetry.reliability_daily_rollup
            GROUP BY client_version
        )
        SELECT
            COALESCE(av.client_version, bv.client_version, rv.client_version) AS client_version,
            av.last_seen,
            av.first_seen,
            COALESCE(av.install_events, 0) AS install_events,
            COALESCE(bv.bug_events, 0)     AS bug_events,
            COALESCE(rv.rel_events, 0)     AS rel_events
        FROM all_versions av
        FULL OUTER JOIN bug_versions bv ON av.client_version = bv.client_version
        FULL OUTER JOIN rel_versions rv ON COALESCE(av.client_version, bv.client_version) = rv.client_version
        ORDER BY (COALESCE(av.install_events, 0) + COALESCE(bv.bug_events, 0) + COALESCE(rv.rel_events, 0)) DESC,
                 client_version DESC
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

    # FINDING 32: data freshness up top so it's the first thing
    # the operator sees.
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
