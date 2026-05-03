#!/usr/bin/env python3
"""
SelectiveMirror telemetry weekly digest generator (v2).

Produces a single Markdown file summarizing the week's telemetry
data: install events, bug-categorical counts, version distribution,
reliability snapshots, action items.

Targets the v2 schema (`docs/telemetry-v2.sql`):
  - installation_daily_rollup
  - bug_daily_rollup
  - reliability_daily_rollup
  - telemetry.version_dist (view)

Under v2 (stream-aggregate-and-discard), the schema holds aggregate
counters only — there are no per-event rows. The digest queries the
rollup tables directly. Several v1-era sections are intentionally
absent under v2:

  - Recurring bug signatures: there are no signatures stored (no
    per-event rows). The kind × surface × version count rollup
    replaces it.
  - Backend mix: backend_types isn't a v2 dimension.
  - Per-report install_id prefix: never published (always was
    forbidden; v2 makes it structurally impossible).

A new section appears under v2: bucketed reliability snapshot
patterns (anomaly_count_bucket × leading anomaly kind × version),
when Reliability-tier contributions are present.

Designed for a single-maintainer project at low volume — the report
gracefully degrades to "n is too small for analysis; pipeline is
alive" when there's not enough data to draw conclusions.

Form factor and design: see panel review (Mary the Analyst,
2026-04-28; rounds 3-5) and docs/telemetry-architecture-v2.md.

Usage:
    # Set DATABASE_URL to your Supabase connection string:
    #   PowerShell:
    #     $env:DATABASE_URL = "postgresql://postgres.<ref>:<pwd>@aws-0-eu-west-1.pooler.supabase.com:6543/postgres"
    #   Bash:
    #     export DATABASE_URL="postgresql://..."
    #
    # Then:
    python3 scripts/telemetry-report.py > docs/telemetry/weekly-2026-W17.md

    # For Sunday-night automation, see .github/workflows/telemetry-digest.yml.

Dependencies: psycopg2-binary OR psycopg (v3). The script tries v3 first.

Output: Markdown to stdout. Exit code 0 on success, 1 on database error,
2 on missing DATABASE_URL.

Privacy note: the script connects with the service_role key (or the
postgres user) so it sees all rollup rows. By construction (v2
architecture) there are no raw payloads on disk, no install_ids, no
narratives — only aggregate counts. The script applies an additional
k-anonymity floor of 5 before publishing.
"""

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

from datetime import date, datetime, timedelta, timezone

# psycopg (v3 preferred, v2 fallback) is needed but is loaded LAZILY
# inside main() rather than at module-load time. Round-11 FINDING 37:
# eager import here breaks `--help` for any operator who hasn't yet
# `pip install`ed psycopg, since argparse never gets a chance to run.
# `pg` and `PSYCOPG_VERSION` are populated by main() via _telemetry_deps.
pg = None             # type: ignore[assignment]
PSYCOPG_VERSION = None  # type: ignore[assignment]


# ---------------------------------------------------------------------------
# Privacy guards
# ---------------------------------------------------------------------------

# K-anonymity floor: cells with fewer contributors than this are suppressed
# (rendered as "<5" or omitted from grouped tables) rather than published.
# This is a binding commitment in PRIVACY.md. Do not lower without
# re-consenting all opted-in users.
K_ANONYMITY_FLOOR = 5


def k_anon_guard(count: int) -> str:
    """Render a count, suppressing values below the k-anonymity floor."""
    if count is None:
        return "—"
    if count < K_ANONYMITY_FLOOR:
        return f"<{K_ANONYMITY_FLOOR}"
    return str(count)


def k_anon_filter(rows, count_field: str) -> list:
    """Filter rows where the given count field is below the k-anonymity floor.

    Used for grouped tables (e.g. version distribution) where small cells
    must not appear at all — leaving the row visible with "<5" would still
    leak that the version exists. Filter is the right move for those cases.

    FINDING 20 (round-5 validation memo, 2026-05-03): defensive coding
    for schema drift. The current rollup tables use BIGINT count
    columns and psycopg returns Python int, so the inline ``or 0``
    suffices today. But a future schema change to TEXT, a downstream
    view that emits a string via ``to_char``, or a custom user query
    threaded through this filter would crash with TypeError on the
    ``>=`` comparison. ``safe_int`` wraps the value in a try/except so
    schema-drift falls open to "treat as 0 → suppress" rather than
    crashing the digest run. Defense-only; no behavior change today.
    """
    def safe_int(v) -> int:
        if v is None:
            return 0
        try:
            return int(v)
        except (TypeError, ValueError):
            return 0
    return [r for r in rows if safe_int(r.get(count_field)) >= K_ANONYMITY_FLOOR]


# ---------------------------------------------------------------------------
# SQL queries — v2 rollup-only.
# ---------------------------------------------------------------------------
#
# All queries hit the three rollup tables:
#   - installation_daily_rollup (first_seen + upgrade events)
#   - bug_daily_rollup (categorical bug-report counts)
#   - reliability_daily_rollup (Reliability-tier snapshots)
# Plus the v2 view:
#   - telemetry.version_dist (30-day version distribution)
#
# Under v2 there is no install_id retention, so "active in last 30
# days as a distinct install count" doesn't exist. The replacement is
# event_volume_30d — the total number of installation events over the
# window. Different number; same maintainer-question.

Q_HEADLINE = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT
  COALESCE((SELECT SUM(reports)::bigint FROM telemetry.bug_daily_rollup, wk
            WHERE rollup_date >= wk.this_wk
              AND rollup_date <  wk.this_wk + INTERVAL '7 days'), 0) AS bugs_this_wk,
  COALESCE((SELECT SUM(reports)::bigint FROM telemetry.bug_daily_rollup, wk
            WHERE rollup_date >= wk.this_wk - INTERVAL '7 days'
              AND rollup_date <  wk.this_wk), 0) AS bugs_prev_wk,
  COALESCE((SELECT SUM(reports)::bigint FROM telemetry.bug_daily_rollup, wk
            WHERE rollup_date >= wk.this_wk - INTERVAL '28 days'
              AND rollup_date <  wk.this_wk), 0) AS bugs_4wk,
  COALESCE((SELECT SUM(count)::bigint FROM telemetry.installation_daily_rollup, wk
            WHERE event_name = 'first_seen'
              AND rollup_date >= wk.this_wk
              AND rollup_date <  wk.this_wk + INTERVAL '7 days'), 0) AS new_installs_this_wk,
  COALESCE((SELECT SUM(count)::bigint FROM telemetry.installation_daily_rollup, wk
            WHERE event_name = 'upgrade'
              AND rollup_date >= wk.this_wk
              AND rollup_date <  wk.this_wk + INTERVAL '7 days'), 0) AS upgrades_this_wk,
  -- v2 has no install_id retention; this is total event volume,
  -- a proxy for "is the project alive."
  COALESCE((SELECT SUM(count)::bigint FROM telemetry.installation_daily_rollup
            WHERE rollup_date >= now() - INTERVAL '30 days'), 0) AS event_volume_30d,
  wk.this_wk AS week_start
FROM wk;
"""

# Bug-this-week breakdown by categorical bucket. Direct read of
# bug_daily_rollup; no joins needed — the rollup is already
# pre-aggregated by (bug_kind, bug_surface, client_version, ...).
Q_BUGS_THIS_WEEK = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT
  bug_kind,
  bug_surface,
  client_version,
  SUM(reports)::bigint AS reports
FROM telemetry.bug_daily_rollup, wk
WHERE rollup_date >= wk.this_wk
  AND rollup_date <  wk.this_wk + INTERVAL '7 days'
GROUP BY bug_kind, bug_surface, client_version
ORDER BY reports DESC, bug_kind, bug_surface, client_version;
"""

# 12-week sparkline for bug-report volume.
Q_BUG_SPARKLINE = """
SELECT
  date_trunc('week', rollup_date)::date AS wk,
  SUM(reports)::bigint AS bugs
FROM telemetry.bug_daily_rollup
WHERE rollup_date >= now() - INTERVAL '12 weeks'
GROUP BY 1 ORDER BY 1;
"""

# Version distribution from the materialized v2 view. Renames
# events_30d → installs for backwards compat with the digest's
# render_versions() function.
Q_VERSION_DIST = """
SELECT
  client_version,
  events_30d AS installs,
  pct
FROM telemetry.version_dist;
"""

# Install-channel mix over the last 30 days.
Q_INSTALL_CHANNEL = """
SELECT
  COALESCE(install_method, 'unknown') AS channel,
  SUM(count)::bigint AS installs
FROM telemetry.installation_daily_rollup
WHERE rollup_date >= now() - INTERVAL '30 days'
GROUP BY install_method
ORDER BY installs DESC;
"""

# Reliability-tier snapshot patterns — NEW under v2. Reports the
# combination of (version, leading anomaly kind, anomaly count
# bucket) that has been most-contributed-to over the last 30 days.
# Empty result means no Reliability-tier contributions yet.
Q_RELIABILITY_PATTERNS = """
SELECT
  client_version,
  COALESCE(most_common_anomaly_kind::text, 'none') AS leading_anomaly,
  anomaly_count_bucket::text AS anomaly_bucket,
  SUM(count)::bigint AS snapshots
FROM telemetry.reliability_daily_rollup
WHERE rollup_date >= now() - INTERVAL '30 days'
GROUP BY client_version, most_common_anomaly_kind, anomaly_count_bucket
ORDER BY snapshots DESC, client_version DESC
LIMIT 20;
"""

# Round-11 user request (2026-05-03): "We also need cumulative
# installation events by version. We also need cumulative bug reports
# by version." The existing Q_VERSION_DIST is a 30-day window only.
# These two queries are ALL-TIME — they survive when there's no recent
# activity (the FINDING-16 reality where the "low-volume" digest
# branch shows zeros for every weekly metric). The cumulative view
# preserves the longer-arc signal: which versions have EVER landed,
# which versions have EVER reported bugs, what the long-tail
# distribution looks like.
Q_CUMULATIVE_INSTALL_EVENTS_BY_VERSION = """
SELECT
  client_version,
  SUM(count)::bigint AS install_events,
  MIN(rollup_date)   AS first_seen_in_telemetry,
  MAX(rollup_date)   AS last_seen_in_telemetry
FROM telemetry.installation_daily_rollup
GROUP BY client_version
ORDER BY install_events DESC, client_version DESC;
"""

Q_CUMULATIVE_BUG_REPORTS_BY_VERSION = """
SELECT
  client_version,
  SUM(reports)::bigint AS bug_reports,
  COUNT(DISTINCT bug_kind) AS distinct_bug_kinds,
  MIN(rollup_date)   AS first_report,
  MAX(rollup_date)   AS last_report
FROM telemetry.bug_daily_rollup
GROUP BY client_version
ORDER BY bug_reports DESC, client_version DESC;
"""

# Hygiene: free-tier ceiling + last ingest activity.
Q_HYGIENE = """
SELECT
  pg_size_pretty(pg_database_size(current_database())) AS db_size,
  GREATEST(
    (SELECT MAX(rollup_date) FROM telemetry.bug_daily_rollup),
    (SELECT MAX(rollup_date) FROM telemetry.installation_daily_rollup),
    (SELECT MAX(rollup_date) FROM telemetry.reliability_daily_rollup)
  ) AS last_rollup_date;
"""

# "What nobody hit this week" — bug_kind values from the documented
# closed taxonomy that DON'T appear in this week's rollup. Under v2
# the taxonomy is a fixed Python list (matched to PRIVACY.md); v1's
# taxonomy_term table is gone.
Q_QUIET_KINDS = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT DISTINCT bug_kind
FROM telemetry.bug_daily_rollup, wk
WHERE rollup_date >= wk.this_wk
  AND rollup_date <  wk.this_wk + INTERVAL '7 days';
"""

# Closed taxonomy — must match docs/telemetry-architecture-v2.md
# "bug_kind" enumeration and PRIVACY.md "Tier 2 — Standard / bug_report
# event". Update both in lockstep with any change here.
KNOWN_BUG_KINDS = ["sync", "rclone", "watcher", "config", "service", "fs", "auth"]

# ---------------------------------------------------------------------------
# Render helpers
# ---------------------------------------------------------------------------

SPARK_BARS = " ▁▂▃▄▅▆▇█"


def sparkline(values: list) -> str:
    """Return a unicode sparkline. Empty list returns empty string."""
    if not values:
        return ""
    mx = max(values) or 1
    return "".join(SPARK_BARS[min(int(v / mx * 8), 8)] for v in values)


# SM-166 + PANEL-2 (BMAD review 2026-05-03): the Markdown cell
# escaper used to live here as a copy. PANEL-2 found that the
# operator-report had its own near-identical-but-not-identical copy,
# and divergence between the two is itself a privacy bug (whichever
# is patched first becomes the more-hardened one; the other regresses
# silently). Both consumers now import from `_telemetry_md`.
#
# Why imported via sys.path manipulation rather than as a regular
# package: scripts/ is not a package (no __init__.py) and the script
# filenames have hyphens that block normal `import`. The shared
# helper sits as a sibling .py with a leading underscore to mark it
# as internal; importing it requires nothing more than the standard
# script-self-locating dance.
import os as _os, sys as _sys
_sys.path.insert(0, _os.path.dirname(_os.path.abspath(__file__)))
from _telemetry_md import md_cell_escape, _MD_ESCAPE_PAIRS  # noqa: E402,F401


def md_table(rows, headers, aligns=None):
    """Render a list of dict rows as a Markdown table.

    SM-166: every cell value is run through md_cell_escape so that
    user-supplied fields cannot break or inject formatting into the
    published digest. Pre-escaped values (which our renderers do not
    produce) would suffer one extra layer of backslash escaping —
    visible but harmless. Numeric values are unaffected by the escape
    (they contain only safe characters).
    """
    if not rows:
        return "_(no rows)_"
    aligns = aligns or ["left"] * len(headers)
    # Headers go in unescaped: they're hard-coded in this script.
    lines = ["| " + " | ".join(headers) + " |"]
    sep = []
    for a in aligns:
        if a == "right":
            sep.append("---:")
        elif a == "center":
            sep.append(":---:")
        else:
            sep.append(":---")
    lines.append("| " + " | ".join(sep) + " |")
    for r in rows:
        if isinstance(r, dict):
            cells = [md_cell_escape(r.get(h, "")) for h in headers]
        else:
            cells = [md_cell_escape(c) for c in r]
        lines.append("| " + " | ".join(cells) + " |")
    return "\n".join(lines)


def fetch_all(conn, query):
    """Run a query, return list of dict rows."""
    cur = conn.cursor()
    try:
        cur.execute(query)
        cols = [d[0] if isinstance(d, tuple) else d.name for d in cur.description]
        return [dict(zip(cols, row)) for row in cur.fetchall()]
    finally:
        cur.close()


# ---------------------------------------------------------------------------
# Report sections
# ---------------------------------------------------------------------------

def render_honesty_banner():
    """SM-165: doc honesty banner.

    Every published digest committed to docs/telemetry/weekly-*.md gets
    this prefix so a reader landing on the file directly (e.g. via
    GitHub search) immediately sees:

    1. What the data is (aggregated, opt-in)
    2. What it is NOT (raw payloads, install IDs, paths)
    3. What protects it (k-anonymity floor)
    4. Where the contract lives (PRIVACY.md)

    Wording is locked to PRIVACY.md's tier matrix. Changes here that
    soften the promise must be cross-checked against PRIVACY.md and
    the re-consent triggers documented in
    reference_telemetry_tier_model.md.
    """
    return (
        "> 📊 **About this report.** Aggregated counts derived from "
        "opt-in SelectiveMirror users at Standard or Reliability tier. "
        f"Cells with fewer than {K_ANONYMITY_FLOOR} contributors are "
        "suppressed (k-anonymity floor). No raw bug-report payloads, "
        "file paths, install IDs, IP addresses, or geography appear in "
        "this file.\n>\n"
        "> See [`docs/PRIVACY.md`](../PRIVACY.md) for the full data "
        "contract, the tier matrix, and the forward-commitment list of "
        "things SelectiveMirror has bound itself never to collect.\n\n"
    )


def render_header(headline, week_start):
    end = week_start + timedelta(days=6)
    iso_year, iso_wk, _ = week_start.isocalendar()
    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    # v2: there is no "total opted-in installs ever" number — install_id
    # isn't retained. event_volume_30d is the closest proxy.
    event_volume_30d = headline["event_volume_30d"]
    return (
        render_honesty_banner()
        + f"# SelectiveMirror Telemetry Digest — Week {iso_year}-W{iso_wk:02d}\n\n"
        + f"**Window**: {week_start} to {end} UTC. Generated {now}.\n\n"
        + f"> Installation events in last 30 days: **{event_volume_30d}**. "
        + f"Trends are anecdote until weekly volume > 20.\n"
    )


def render_pipeline_alive_only(headline, hygiene, sparkline_values):
    """Degenerate (low-n) week — output is intentionally short."""
    event_volume_30d = headline["event_volume_30d"]
    bugs_ever = sum(sparkline_values) if sparkline_values else 0
    last_rollup = hygiene[0]["last_rollup_date"] or "(never)"
    db_size = hygiene[0]["db_size"]
    return (
        "## State of telemetry\n\n"
        f"- Installation events in last 30 days: **{event_volume_30d}**\n"
        f"- Bug reports this week: **{headline['bugs_this_wk']}**\n"
        f"- Bug reports last 12 weeks: **{bugs_ever}**\n\n"
        "> Sample size is too small for analysis. This file confirms the "
        "pipeline is running and the database is alive. Come back when "
        "weekly volume > 10.\n\n"
        "## Hygiene\n\n"
        f"- Free-tier DB usage: **{db_size}** (cap: 500 MB)\n"
        f"- Last rollup activity: **{last_rollup}**\n"
    )


def render_headline(headline, sparkline_values):
    """v2 headline numbers.

    The "Installs emitting any event/30d" row from v1 was a
    distinct-install count (impossible under v2). It's replaced by
    "Installation events / 30d" — total event volume, which answers
    the same maintainer question ("is the project alive") without
    needing install_id retention.
    """
    rows = [
        {
            "Metric": "Bug reports submitted",
            "This week": headline["bugs_this_wk"],
            "Prev week": headline["bugs_prev_wk"],
            "4-wk avg": round(headline["bugs_4wk"] / 4.0, 1) if headline["bugs_4wk"] else 0,
        },
        {
            "Metric": "New installs (first_seen)",
            "This week": headline["new_installs_this_wk"],
            "Prev week": "—",
            "4-wk avg": "—",
        },
        {
            "Metric": "Upgrades",
            "This week": headline["upgrades_this_wk"],
            "Prev week": "—",
            "4-wk avg": "—",
        },
        {
            "Metric": "Installation events / 30d",
            "This week": headline["event_volume_30d"],
            "Prev week": "—",
            "4-wk avg": "—",
        },
    ]
    spark = sparkline(sparkline_values) or "_(no data)_"
    return (
        "## Headline numbers\n\n"
        + md_table(
            rows,
            ["Metric", "This week", "Prev week", "4-wk avg"],
            aligns=["left", "right", "right", "right"],
        )
        + f"\n\nSparkline of bug-reports/week (12 wk): `{spark}`\n"
    )


def render_bugs_section(bugs_this_week, quiet_kinds_seen):
    """v2 bug section.

    Rows are aggregated by (kind, surface, client_version) at the SQL
    layer with a count. The k-anonymity floor is applied here: cells
    with fewer than K_ANONYMITY_FLOOR reports are suppressed entirely
    (a "<5"-shaped placeholder would still leak the existence of a
    unique combination).

    The v1 "Recurring signatures" section is REMOVED under v2 — there
    are no signatures stored, since per-event rows don't exist.
    Recurrence is approximated by the kind × surface × version rollup
    aggregated over a longer window, but that's a different shape;
    keeping just the weekly rollup until we have signal.

    quiet_kinds_seen is a list of bug_kind values that DID appear this
    week; we compute the complement against KNOWN_BUG_KINDS to get
    "what nobody hit."
    """
    out = ["## Bug reports — this week\n"]
    if not bugs_this_week:
        out.append("_No bug reports this week._\n")
    else:
        visible = k_anon_filter(bugs_this_week, "reports")
        suppressed = len(bugs_this_week) - len(visible)
        if not visible:
            out.append(
                f"_All bug-categorization cells fall below the "
                f"k-anonymity floor ({K_ANONYMITY_FLOOR}). "
                f"Suppressed: {suppressed} category combination(s)._\n"
            )
        else:
            out.append(
                md_table(
                    [
                        {
                            "Kind": b["bug_kind"],
                            "Surface": b["bug_surface"],
                            "Version": b["client_version"],
                            "Reports": b["reports"],
                        }
                        for b in visible
                    ],
                    ["Kind", "Surface", "Version", "Reports"],
                    aligns=["left", "left", "left", "right"],
                )
            )
            if suppressed:
                out.append(
                    f"\n_({suppressed} additional category combination(s) "
                    f"with reports below k={K_ANONYMITY_FLOOR} suppressed.)_"
                )
        out.append("")

    out.append("\n### What nobody hit this week\n")
    seen_kinds = {q["bug_kind"] for q in quiet_kinds_seen}
    quiet = [k for k in KNOWN_BUG_KINDS if k not in seen_kinds]
    if not quiet:
        out.append("_All known bug.kind categories saw at least one report._\n")
    else:
        out.append(f"No reports for: {', '.join(quiet)}\n")
    return "\n".join(out)


def render_versions(version_dist, install_channels):
    out = ["## Version distribution (active installs / 30d)\n"]
    # K-anonymity: suppress versions with fewer than K installs entirely
    # (showing "<5" would still leak the version's existence).
    visible_versions = k_anon_filter(version_dist, "installs")
    suppressed_versions = len(version_dist) - len(visible_versions)
    if not version_dist:
        out.append("_No active installs._\n")
    elif not visible_versions:
        out.append(f"_All version cells below k-anonymity floor "
                   f"({K_ANONYMITY_FLOOR}). Suppressed: {suppressed_versions} "
                   f"version(s)._\n")
    else:
        out.append(
            md_table(
                [
                    {
                        "Version": v["client_version"],
                        "Installs": v["installs"],
                        "% of base": f"{v['pct']}%" if v["pct"] is not None else "—",
                    }
                    for v in visible_versions
                ],
                ["Version", "Installs", "% of base"],
                aligns=["left", "right", "right"],
            )
        )
        if suppressed_versions:
            out.append(f"\n_({suppressed_versions} additional version(s) "
                       f"with installs below k={K_ANONYMITY_FLOOR} suppressed.)_")

    out.append("\n## Install channel mix (cumulative)\n")
    visible_channels = k_anon_filter(install_channels, "installs")
    suppressed_channels = len(install_channels) - len(visible_channels)
    if not install_channels:
        out.append("_No install-method data._\n")
    elif not visible_channels:
        out.append(f"_All channel cells below k-anonymity floor "
                   f"({K_ANONYMITY_FLOOR}). Suppressed: {suppressed_channels}._\n")
    else:
        out.append(
            md_table(
                [{"Channel": c["channel"], "Installs": c["installs"]}
                 for c in visible_channels],
                ["Channel", "Installs"],
                aligns=["left", "right"],
            )
        )
        if suppressed_channels:
            out.append(f"\n_({suppressed_channels} channel(s) suppressed below k={K_ANONYMITY_FLOOR}.)_")

    # Backend mix is removed under v2 — `backend_types` is not a v2
    # bucket dimension. Re-add only after a re-consent cycle adds the
    # field to PRIVACY.md and the rollup schema.
    return "\n".join(out)


def render_reliability(reliability_patterns):
    """v2-only: bucketed reliability snapshot patterns."""
    out = ["## Reliability snapshot patterns (last 30 days)\n"]
    if not reliability_patterns:
        out.append("_No Reliability-tier contributions in the last 30 days._\n")
        return "\n".join(out)
    visible = k_anon_filter(reliability_patterns, "snapshots")
    suppressed = len(reliability_patterns) - len(visible)
    if not visible:
        out.append(
            f"_All reliability cells below k-anonymity floor "
            f"({K_ANONYMITY_FLOOR}). Suppressed: {suppressed} pattern(s)._\n"
        )
        return "\n".join(out)
    out.append(
        md_table(
            [
                {
                    "Version": r["client_version"],
                    "Leading anomaly": r["leading_anomaly"],
                    "Anomaly bucket": r["anomaly_bucket"],
                    "Snapshots": r["snapshots"],
                }
                for r in visible
            ],
            ["Version", "Leading anomaly", "Anomaly bucket", "Snapshots"],
            aligns=["left", "left", "left", "right"],
        )
    )
    if suppressed:
        out.append(
            f"\n_({suppressed} additional pattern(s) below k={K_ANONYMITY_FLOOR} suppressed.)_"
        )
    return "\n".join(out)


def render_hygiene(hygiene):
    h = hygiene[0]
    last = h["last_rollup_date"] or "(never)"
    return (
        "## Hygiene\n\n"
        f"- Free-tier DB usage: **{h['db_size']}** (cap: 500 MB)\n"
        f"- Last rollup activity: **{last}**\n"
    )


def render_cumulative_install_events_by_version(rows):
    """Round-11 user request: cumulative install events per version,
    all-time. Survives the FINDING-16 reality where the 30-day window
    is empty — even when nothing landed this week, the all-time
    arc is preserved."""
    out = ["## Cumulative installation events by version (all-time)\n"]
    visible = k_anon_filter(rows, "install_events")
    suppressed = len(rows) - len(visible)
    if not rows:
        out.append("_No install events ever recorded._\n")
    elif not visible:
        out.append(
            f"_All version cells below k-anonymity floor ({K_ANONYMITY_FLOOR}). "
            f"Suppressed: {suppressed} version(s)._\n")
    else:
        out.append(
            md_table(
                [
                    {
                        "Version": r["client_version"],
                        "Install events": r["install_events"],
                        "First seen": r["first_seen_in_telemetry"],
                        "Last seen": r["last_seen_in_telemetry"],
                    }
                    for r in visible
                ],
                ["Version", "Install events", "First seen", "Last seen"],
                aligns=["left", "right", "left", "left"],
            )
        )
        if suppressed:
            out.append(
                f"\n_({suppressed} additional version(s) with install_events "
                f"below k={K_ANONYMITY_FLOOR} suppressed.)_")
    return "\n".join(out)


def render_cumulative_bug_reports_by_version(rows):
    """Round-11 user request: cumulative bug reports per version,
    all-time. Same survival property as the install-events sibling."""
    out = ["## Cumulative bug reports by version (all-time)\n"]
    visible = k_anon_filter(rows, "bug_reports")
    suppressed = len(rows) - len(visible)
    if not rows:
        out.append("_No bug reports ever recorded._\n")
    elif not visible:
        out.append(
            f"_All version cells below k-anonymity floor ({K_ANONYMITY_FLOOR}). "
            f"Suppressed: {suppressed} version(s)._\n")
    else:
        out.append(
            md_table(
                [
                    {
                        "Version": r["client_version"],
                        "Bug reports": r["bug_reports"],
                        "Distinct kinds": r["distinct_bug_kinds"],
                        "First report": r["first_report"],
                        "Last report": r["last_report"],
                    }
                    for r in visible
                ],
                ["Version", "Bug reports", "Distinct kinds",
                 "First report", "Last report"],
                aligns=["left", "right", "right", "left", "left"],
            )
        )
        if suppressed:
            out.append(
                f"\n_({suppressed} additional version(s) with bug_reports "
                f"below k={K_ANONYMITY_FLOOR} suppressed.)_")
    return "\n".join(out)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    # Round-10 FINDING 35: render_honesty_banner() emits a 📊 emoji in
    # the published digest. On Windows the default stdout encoding is
    # cp1252 which can't encode emoji, so a maintainer running this
    # script locally to spot-check the Sunday-cron output gets a 0-byte
    # file + UnicodeEncodeError. CI (Ubuntu) is utf-8 by default so
    # this never fires in the digest workflow itself. Force utf-8 on
    # entry — the project is Windows-first; the script should Just
    # Work for the Windows operator. Mirrors the same fix in
    # scripts/telemetry-debug.py (round-9 bonus).
    try:
        sys.stdout.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
        sys.stderr.reconfigure(encoding="utf-8")  # type: ignore[attr-defined]
    except (AttributeError, ValueError):
        pass  # Older Python or already-replaced stream — best effort.

    # Round-10 FINDING 36: `--help` must always work and never crash
    # on missing env. Pre-fix the script had no argument parsing at all
    # — `--help` flowed straight to the `DATABASE_URL` env-var check
    # and exited with the database error, leaving the maintainer
    # unable to discover what the script does. argparse below makes
    # `--help` a top-level concern so it always answers cleanly.
    import argparse
    ap = argparse.ArgumentParser(
        prog="telemetry-report.py",
        description=(
            "Generate the SelectiveMirror weekly telemetry digest "
            "(k-anonymity floor of 5 applied; safe to publish). "
            "Reads from live Supabase via $DATABASE_URL; writes "
            "Markdown to stdout. The published Sunday-cron variant "
            "of this script is wired in `.github/workflows/"
            "telemetry-digest.yml`. For the un-floored operator-debug "
            "view, see `scripts/telemetry-debug.py --confirm-internal-only`."),
        epilog=(
            "ENV: DATABASE_URL (required) — postgresql://... pointing at "
            "the Supabase project that hosts the v2 telemetry schema. "
            "Source ~/.smirror-deploy.env if you have it."),
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--verbose-env", action="store_true",
                    help="Print env-file discovery diagnostics to stderr.")
    args = ap.parse_args()

    # Round-10 FINDING 36b: PowerShell has no `source` builtin and WSL2's
    # `~` maps to Linux home (not Windows home where ~/.smirror-deploy.env
    # actually lives). Auto-discover the env file across both shells so
    # `python3 scripts/telemetry-report.py` Just Works without prior
    # `source ~/.smirror-deploy.env`. Operator's manually-set env wins
    # over the file (variables already in os.environ are not overwritten).
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
            "Run with --verbose-env to see which paths were probed.\n"
            "Run --help for the full usage.\n"
        )
        sys.exit(2)

    # Round-11 FINDING 37: lazy-import psycopg so --help works even when
    # the dep isn't installed. require_psycopg() prefers v3, falls back
    # to v2; on failure prints actionable error with interpreter path
    # and exits 2.
    from _telemetry_deps import require_psycopg
    global pg, PSYCOPG_VERSION
    pg = require_psycopg()
    PSYCOPG_VERSION = 3 if pg.__name__ == "psycopg" else 2

    try:
        conn = pg.connect(db_url) if PSYCOPG_VERSION == 3 else pg.connect(db_url)
    except Exception as e:
        sys.stderr.write(f"ERROR: cannot connect to database: {e}\n")
        sys.exit(1)

    try:
        headline = fetch_all(conn, Q_HEADLINE)[0]
        bugs = fetch_all(conn, Q_BUGS_THIS_WEEK)
        sparkline_rows = fetch_all(conn, Q_BUG_SPARKLINE)
        version_dist = fetch_all(conn, Q_VERSION_DIST)
        install_channels = fetch_all(conn, Q_INSTALL_CHANNEL)
        reliability_patterns = fetch_all(conn, Q_RELIABILITY_PATTERNS)
        hygiene = fetch_all(conn, Q_HYGIENE)
        quiet_kinds = fetch_all(conn, Q_QUIET_KINDS)
        # Round-11 user request: cumulative all-time per-version views.
        # Survive the FINDING-16 reality where the 30-day window is empty.
        cum_install_by_ver = fetch_all(conn, Q_CUMULATIVE_INSTALL_EVENTS_BY_VERSION)
        cum_bugs_by_ver = fetch_all(conn, Q_CUMULATIVE_BUG_REPORTS_BY_VERSION)
    except Exception as e:
        sys.stderr.write(f"ERROR: query failed: {e}\n")
        sys.exit(1)
    finally:
        conn.close()

    sparkline_values = [int(r["bugs"]) for r in sparkline_rows]
    week_start = headline["week_start"]

    # Degenerate-week heuristic under v2: low 30-day event volume +
    # zero bugs this week + nearly-zero 12-week bug volume → render
    # the short pipeline-alive form.
    event_volume_30d = headline["event_volume_30d"] or 0
    bugs_ever = sum(sparkline_values)
    if event_volume_30d < 5 and headline["bugs_this_wk"] == 0 and bugs_ever < 3:
        print(render_header(headline, week_start))
        print(render_pipeline_alive_only(headline, hygiene, sparkline_values))
        # Round-11 user request: even in the low-volume "pipeline alive"
        # branch, surface the cumulative-by-version sections. They're the
        # ONE thing that survives a quiet week — they show whether ANY
        # version has ever contributed, even when the 7d/30d windows
        # are zeros across the board.
        print()
        print(render_cumulative_install_events_by_version(cum_install_by_ver))
        print()
        print(render_cumulative_bug_reports_by_version(cum_bugs_by_ver))
        return

    # Full report
    print(render_header(headline, week_start))
    print(render_headline(headline, sparkline_values))
    print()
    print(render_bugs_section(bugs, quiet_kinds))
    print()
    print(render_versions(version_dist, install_channels))
    print()
    # Round-11 user request: cumulative-by-version sections. Placed
    # between the 30d-window version distribution and the reliability
    # patterns so the operator's eye flows from "this week's churn" →
    # "all-time per-version arc" → "operational health."
    print(render_cumulative_install_events_by_version(cum_install_by_ver))
    print()
    print(render_cumulative_bug_reports_by_version(cum_bugs_by_ver))
    print()
    print(render_reliability(reliability_patterns))
    print()
    print(render_hygiene(hygiene))


if __name__ == "__main__":
    main()
