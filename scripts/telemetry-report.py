#!/usr/bin/env python3
"""
SelectiveMirror telemetry weekly digest generator.

Produces a single Markdown file summarizing the week's telemetry data:
install events, bug reports, version distribution, action items.

Designed for a single-maintainer project at low volume — the report
gracefully degrades to "n is too small for analysis; pipeline is alive"
when there's not enough data to draw conclusions.

Form factor and design: see panel review (Mary the Analyst, 2026-04-28)
and docs/telemetry-microserver-architecture.md.

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
postgres user) so it sees all rows. It writes only aggregated counts and
sanitized signatures to the report — never raw report_text or install_id.
"""

import os
import sys
from datetime import date, datetime, timedelta, timezone

try:
    import psycopg as pg
    from psycopg import sql as pg_sql  # noqa: F401
    PSYCOPG_VERSION = 3
except ImportError:
    try:
        import psycopg2 as pg
        PSYCOPG_VERSION = 2
    except ImportError:
        sys.stderr.write(
            "ERROR: psycopg (v3) or psycopg2-binary required.\n"
            "Install with: pip install psycopg2-binary\n"
        )
        sys.exit(2)


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
    """
    return [r for r in rows if (r.get(count_field) or 0) >= K_ANONYMITY_FLOOR]


# ---------------------------------------------------------------------------
# SQL queries — keep in lockstep with docs/telemetry-views.sql when possible
# ---------------------------------------------------------------------------

Q_HEADLINE = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT
  (SELECT COUNT(*) FROM telemetry.bug_report
   WHERE reported_at >= wk.this_wk
     AND reported_at <  wk.this_wk + INTERVAL '7 days') AS bugs_this_wk,
  (SELECT COUNT(*) FROM telemetry.bug_report
   WHERE reported_at >= wk.this_wk - INTERVAL '7 days'
     AND reported_at <  wk.this_wk) AS bugs_prev_wk,
  (SELECT COUNT(*) FROM telemetry.bug_report
   WHERE reported_at >= wk.this_wk - INTERVAL '28 days'
     AND reported_at <  wk.this_wk) AS bugs_4wk,
  (SELECT COUNT(*) FROM telemetry.installation_event
   WHERE event_name = 'first_seen'
     AND reported_at >= wk.this_wk
     AND reported_at <  wk.this_wk + INTERVAL '7 days') AS new_installs_this_wk,
  (SELECT COUNT(*) FROM telemetry.installation_event
   WHERE event_name = 'upgrade'
     AND reported_at >= wk.this_wk
     AND reported_at <  wk.this_wk + INTERVAL '7 days') AS upgrades_this_wk,
  (SELECT COUNT(DISTINCT install_id) FROM telemetry.installation_event
   WHERE reported_at >= now() - INTERVAL '30 days') AS active_30d,
  (SELECT COUNT(DISTINCT install_id) FROM telemetry.installation) AS installs_total,
  wk.this_wk AS week_start
FROM wk;
"""

# SM-166 + SM-178: bug-this-week breakdown.
# - install_id (and any prefix of it) is NEVER published. The previous
#   query exposed a 4-char prefix; even at sub-1% collision risk, it
#   directly contradicts PRIVACY.md's "no install_id" promise for the
#   public digest.
# - The taxonomy joins use the same per-dimension pre-aggregation as
#   refresh_bug_daily_rollup so a single multi-tag report doesn't
#   appear as multiple rows.
Q_BUGS_THIS_WEEK = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
),
kind_per_report AS (
  SELECT a.bug_report_id, MIN(t.slug) AS slug
  FROM telemetry.bug_report_taxonomy_assignment a
  JOIN telemetry.taxonomy_term t ON t.id = a.term_id
  WHERE t.namespace = 'bug.kind'
  GROUP BY a.bug_report_id
),
surface_per_report AS (
  SELECT a.bug_report_id, MIN(t.slug) AS slug
  FROM telemetry.bug_report_taxonomy_assignment a
  JOIN telemetry.taxonomy_term t ON t.id = a.term_id
  WHERE t.namespace = 'bug.surface'
  GROUP BY a.bug_report_id
)
SELECT
  COALESCE(kind_per_report.slug,    'unknown') AS bug_kind,
  COALESCE(surface_per_report.slug, 'unknown') AS bug_surface,
  COALESCE(br.client_version,       'unknown') AS client_version,
  COUNT(*)                                     AS reports
FROM telemetry.bug_report br, wk
LEFT JOIN kind_per_report    ON kind_per_report.bug_report_id    = br.id
LEFT JOIN surface_per_report ON surface_per_report.bug_report_id = br.id
WHERE br.reported_at >= wk.this_wk
  AND br.reported_at <  wk.this_wk + INTERVAL '7 days'
GROUP BY kind_per_report.slug, surface_per_report.slug, br.client_version
ORDER BY reports DESC, bug_kind, bug_surface, client_version;
"""

Q_SIGNATURE_RECURRENCE = """
SELECT
  signature,
  COUNT(DISTINCT install_id) AS distinct_installs,
  COUNT(*)                   AS total_reports,
  MIN(reported_at)::date     AS first_seen,
  MAX(reported_at)::date     AS last_seen,
  ARRAY_AGG(DISTINCT client_version ORDER BY client_version) AS versions
FROM telemetry.bug_report
WHERE signature IS NOT NULL
  AND reported_at >= now() - INTERVAL '90 days'
GROUP BY signature
HAVING COUNT(*) >= 2
ORDER BY distinct_installs DESC, total_reports DESC
LIMIT 10;
"""

Q_BUG_SPARKLINE = """
SELECT
  date_trunc('week', reported_at)::date AS wk,
  COUNT(*)                              AS bugs
FROM telemetry.bug_report
WHERE reported_at >= now() - INTERVAL '12 weeks'
GROUP BY 1 ORDER BY 1;
"""

Q_VERSION_DIST = """
WITH active AS (
  SELECT DISTINCT ON (install_id) install_id, client_version
  FROM telemetry.installation_event
  WHERE reported_at >= now() - INTERVAL '30 days'
  ORDER BY install_id, reported_at DESC
)
SELECT
  client_version,
  COUNT(*)                                                     AS installs,
  ROUND(100.0 * COUNT(*) / NULLIF(SUM(COUNT(*)) OVER (), 0), 1) AS pct
FROM active
GROUP BY client_version
ORDER BY installs DESC, client_version DESC;
"""

Q_INSTALL_CHANNEL = """
SELECT
  COALESCE(current_install_method, 'unknown') AS channel,
  COUNT(*)                                    AS installs
FROM telemetry.installation
GROUP BY current_install_method
ORDER BY installs DESC;
"""

Q_BACKEND_MIX = """
SELECT
  bt              AS backend,
  COUNT(DISTINCT i.install_id) AS installs
FROM telemetry.installation i,
     UNNEST(i.current_backend_types) AS bt
WHERE i.current_backend_types IS NOT NULL
  AND array_length(i.current_backend_types, 1) > 0
GROUP BY bt
ORDER BY installs DESC;
"""

Q_HYGIENE = """
SELECT
  (SELECT COUNT(*) FROM telemetry.bug_report
   WHERE taxonomy_state IN ('pending','needs_review')) AS unclassified_pending,
  (SELECT MAX(EXTRACT(epoch FROM (now() - br.reported_at))/3600)::int
   FROM telemetry.bug_report br
   WHERE taxonomy_state IN ('pending','needs_review')) AS oldest_pending_hours,
  pg_size_pretty(pg_database_size(current_database())) AS db_size,
  (SELECT MAX(received_at) FROM telemetry.ingest_envelope) AS last_ingest;
"""

Q_QUIET_KINDS = """
WITH wk AS (
  SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT t.display_name AS bug_kind
FROM telemetry.taxonomy_term t, wk
WHERE t.target = 'bug_report'
  AND t.namespace = 'bug.kind'
  AND t.active
  AND NOT EXISTS (
    SELECT 1
    FROM telemetry.bug_report_taxonomy_assignment a
    JOIN telemetry.bug_report br ON br.id = a.bug_report_id
    WHERE a.term_id = t.id
      AND br.reported_at >= wk.this_wk
      AND br.reported_at <  wk.this_wk + INTERVAL '7 days'
  )
ORDER BY t.ordinal;
"""

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


# SM-166: Server-side fields inserted into the published Markdown
# digest (signature, client_version, bug_kind, bug_surface) originate
# from opt-in submissions. Despite schema validation at ingest, an
# unforeseen string containing pipes, backticks, link syntax, or line
# breaks could corrupt the rendered table or inject formatting /
# clickable content into the public docs. md_cell_escape neutralizes
# Markdown structural characters and collapses whitespace so a single
# cell stays a single cell. Truncation keeps wide tables readable.
#
# Note: install_id (or any prefix of it) is intentionally NEVER part
# of the digest — see Q_BUGS_THIS_WEEK above for the SM-166 design
# rationale.
_MD_ESCAPE_PAIRS = (
    ("\\", "\\\\"),
    ("|", "\\|"),
    ("`", "\\`"),
    ("*", "\\*"),
    ("_", "\\_"),
    ("[", "\\["),
    ("]", "\\]"),
    ("<", "\\<"),
    (">", "\\>"),
    ("{", "\\{"),
    ("}", "\\}"),
)


def md_cell_escape(s, max_len: int = 120) -> str:
    """Sanitize a value for safe inclusion in a Markdown table cell.

    - Replaces None/missing with the em-dash placeholder we use elsewhere.
    - Strips ASCII control characters and collapses CR/LF/TAB to spaces
      (a Markdown table cell must be one line).
    - Escapes the table separator (|), backticks, asterisks, brackets,
      angle brackets, and braces — anything that could either break
      rendering or be interpreted as Markdown link / formatting.
    - Truncates to max_len with an ellipsis. 120 chars is long enough
      to keep error signatures useful while preventing pathological
      blow-up of the digest file.

    Numerics (int/float) are coerced to their str() form first, which
    contains only safe characters — passing them through is harmless.
    """
    if s is None:
        return "—"
    s = str(s)
    # Drop ASCII controls (0x00–0x1F except already-handled below) and
    # convert any remaining whitespace control chars to plain space.
    cleaned = []
    for ch in s:
        cp = ord(ch)
        if ch in ("\r", "\n", "\t"):
            cleaned.append(" ")
        elif cp < 0x20 or cp == 0x7F:
            # Other ASCII controls: drop entirely.
            continue
        else:
            cleaned.append(ch)
    s = "".join(cleaned)
    # Markdown structural escapes. Order matters: backslash first, so
    # the escapes added below aren't double-escaped.
    for ch, esc in _MD_ESCAPE_PAIRS:
        s = s.replace(ch, esc)
    # Collapse runs of whitespace.
    s = " ".join(s.split())
    if len(s) > max_len:
        s = s[: max_len - 1] + "…"
    return s


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
    n_total = headline["installs_total"]
    return (
        render_honesty_banner()
        + f"# SelectiveMirror Telemetry Digest — Week {iso_year}-W{iso_wk:02d}\n\n"
        + f"**Window**: {week_start} to {end} UTC. Generated {now}.\n\n"
        + f"> Total opted-in installs ever: **{n_total}**. "
        + f"Trends are anecdote until n>20.\n"
    )


def render_pipeline_alive_only(headline, hygiene, sparkline_values):
    """Degenerate (low-n) week — output is intentionally short."""
    n_total = headline["installs_total"]
    bugs_ever = sum(sparkline_values) if sparkline_values else 0
    last_ingest = hygiene[0]["last_ingest"] or "(never)"
    db_size = hygiene[0]["db_size"]
    return (
        "## State of telemetry\n\n"
        f"- Total opted-in installs ever: **{n_total}**\n"
        f"- Active in last 30 days: **{headline['active_30d']}**\n"
        f"- Bug reports this week: **{headline['bugs_this_wk']}**\n"
        f"- Bug reports last 12 weeks: **{bugs_ever}**\n\n"
        "> Sample size is too small for analysis. This file confirms the "
        "pipeline is running and the database is alive. Come back when n>10.\n\n"
        "## Hygiene\n\n"
        f"- Free-tier DB usage: **{db_size}** (cap: 500 MB)\n"
        f"- Last successful ingest: **{last_ingest}**\n"
        f"- Unclassified backlog: {hygiene[0]['unclassified_pending']}\n"
    )


def render_headline(headline, sparkline_values):
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
            "Metric": "Installs emitting any event/30d",
            "This week": headline["active_30d"],
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


def render_bugs_section(bugs_this_week, recurrence, quiet_kinds):
    """SM-166: per-report rows with low-n privacy.

    Rows are now aggregated by (kind, surface, client_version) at the
    SQL layer with a row count. We then apply the k-anonymity floor:
    cells with fewer than K_ANONYMITY_FLOOR reports are suppressed
    entirely (a "<5"-shaped placeholder would still leak the existence
    of a unique combination).

    Recurring signatures get their own k-anon-bounded section below
    (it requires distinct_installs >= K).
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

    # SM-166: apply k-anonymity to recurrence rows. A signature seen by
    # only 1-4 distinct installs identifies those installs (they're
    # the only ones in that bucket); only show signatures that 5+
    # different users have hit.
    visible_recurrence = [
        r for r in recurrence
        if (r.get("distinct_installs") or 0) >= K_ANONYMITY_FLOOR
    ]
    suppressed_recurrence = len(recurrence) - len(visible_recurrence)
    out.append(
        f"\n### Recurring signatures "
        f"(last 90 days, distinct installs ≥ {K_ANONYMITY_FLOOR})\n"
    )
    if not visible_recurrence:
        msg = "_No signature has been hit by enough distinct installs to publish."
        if suppressed_recurrence:
            msg += (
                f" Suppressed {suppressed_recurrence} signature(s) "
                f"below k={K_ANONYMITY_FLOOR}."
            )
        msg += "_\n"
        out.append(msg)
    else:
        out.append(
            md_table(
                [
                    {
                        "Signature": r["signature"][:48],
                        "Distinct installs": r["distinct_installs"],
                        "Total reports": r["total_reports"],
                        "First seen": r["first_seen"],
                        "Last seen": r["last_seen"],
                        "Versions": ", ".join(r["versions"][:4]),
                    }
                    for r in visible_recurrence
                ],
                ["Signature", "Distinct installs", "Total reports",
                 "First seen", "Last seen", "Versions"],
                aligns=["left", "right", "right", "left", "left", "left"],
            )
        )
        if suppressed_recurrence:
            out.append(
                f"\n_({suppressed_recurrence} additional signature(s) "
                f"below k={K_ANONYMITY_FLOOR} suppressed.)_"
            )

    out.append("\n### What nobody hit this week\n")
    if not quiet_kinds:
        out.append("_All bug.kind categories saw at least one report._\n")
    else:
        names = ", ".join(q["bug_kind"] for q in quiet_kinds)
        out.append(f"No reports for: {names}\n")
    return "\n".join(out)


def render_versions(version_dist, install_channels, backend_mix):
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

    out.append("\n## Backend mix (current installation snapshot)\n")
    visible_backends = k_anon_filter(backend_mix, "installs")
    suppressed_backends = len(backend_mix) - len(visible_backends)
    if not backend_mix:
        out.append("_No backends recorded yet. (Note: `backend_types` is "
                   "captured on `upgrade` events; `first_seen` typically has "
                   "an empty array.)_\n")
    elif not visible_backends:
        out.append(f"_All backend cells below k-anonymity floor "
                   f"({K_ANONYMITY_FLOOR}). Suppressed: {suppressed_backends}._\n")
    else:
        out.append(
            md_table(
                [{"Backend": b["backend"], "Installs reporting": b["installs"]}
                 for b in visible_backends],
                ["Backend", "Installs reporting"],
                aligns=["left", "right"],
            )
        )
        if suppressed_backends:
            out.append(f"\n_({suppressed_backends} backend(s) suppressed below k={K_ANONYMITY_FLOOR}.)_")
    return "\n".join(out)


def render_hygiene(hygiene):
    h = hygiene[0]
    last = h["last_ingest"] or "(never)"
    pending = h["unclassified_pending"]
    oldest_h = h["oldest_pending_hours"]
    if pending and oldest_h:
        pending_str = f"{pending} (oldest: {oldest_h}h)"
    else:
        pending_str = str(pending or 0)
    return (
        "## Hygiene\n\n"
        f"- Free-tier DB usage: **{h['db_size']}** (cap: 500 MB)\n"
        f"- Unclassified backlog: **{pending_str}**\n"
        f"- Last successful ingest: **{last}**\n"
    )


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    db_url = os.environ.get("DATABASE_URL", "")
    if not db_url:
        sys.stderr.write(
            "ERROR: DATABASE_URL environment variable not set.\n"
            "Set it to your Supabase connection string and re-run.\n"
        )
        sys.exit(2)

    try:
        conn = pg.connect(db_url) if PSYCOPG_VERSION == 3 else pg.connect(db_url)
    except Exception as e:
        sys.stderr.write(f"ERROR: cannot connect to database: {e}\n")
        sys.exit(1)

    try:
        headline = fetch_all(conn, Q_HEADLINE)[0]
        bugs = fetch_all(conn, Q_BUGS_THIS_WEEK)
        recurrence = fetch_all(conn, Q_SIGNATURE_RECURRENCE)
        sparkline_rows = fetch_all(conn, Q_BUG_SPARKLINE)
        version_dist = fetch_all(conn, Q_VERSION_DIST)
        install_channels = fetch_all(conn, Q_INSTALL_CHANNEL)
        backend_mix = fetch_all(conn, Q_BACKEND_MIX)
        hygiene = fetch_all(conn, Q_HYGIENE)
        quiet_kinds = fetch_all(conn, Q_QUIET_KINDS)
    except Exception as e:
        sys.stderr.write(f"ERROR: query failed: {e}\n")
        sys.exit(1)
    finally:
        conn.close()

    sparkline_values = [int(r["bugs"]) for r in sparkline_rows]
    week_start = headline["week_start"]

    # Degenerate-week heuristic: if all of (a) total installs, (b) bugs this
    # week, (c) bugs in last 12 weeks are tiny, render the short version.
    n_total = headline["installs_total"] or 0
    bugs_ever = sum(sparkline_values)
    if n_total < 5 and headline["bugs_this_wk"] == 0 and bugs_ever < 3:
        print(render_header(headline, week_start))
        print(render_pipeline_alive_only(headline, hygiene, sparkline_values))
        return

    # Full report
    print(render_header(headline, week_start))
    print(render_headline(headline, sparkline_values))
    print()
    print(render_bugs_section(bugs, recurrence, quiet_kinds))
    print()
    print(render_versions(version_dist, install_channels, backend_mix))
    print()
    print(render_hygiene(hygiene))


if __name__ == "__main__":
    main()
