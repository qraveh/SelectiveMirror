-- ============================================================================
-- SUPERSEDED IN PART BY telemetry-v2.sql (2026-04-29).
-- ============================================================================
--
-- These views read from the v1 individual-event tables. Under v2 those
-- tables go away after Phase D (see docs/telemetry-architecture-v2.md);
-- views that depend on bug_report / installation / installation_event will
-- be dropped in the same migration. The k-anonymity-friendly views
-- (version_dist, install_config_distribution, tier_distribution) gain v2
-- equivalents in telemetry-v2.sql and the digest script.
--
-- New analytics queries should target the v2 rollup tables. Do NOT add new
-- views here that depend on v1 individual-event tables.
-- ============================================================================

-- SelectiveMirror telemetry: human-readable SQL views (v1 — historical)
--
-- These views are denormalized for a single-maintainer workflow: the data
-- you'd actually scan in the Supabase Studio query editor on a Monday
-- morning, not the rollup tables that power dashboards.
--
-- Apply AFTER loading telemetry-microserver.sql.
-- Re-runnable (uses CREATE OR REPLACE).

-- ============================================================================
-- View 1: bug_report_human
-- ============================================================================
--
-- One row per bug report with the most useful columns flattened: kind,
-- surface, version, signature, and a 500-char preview of report_text.
-- Sorted newest-first. Click around in Supabase Studio.

CREATE OR REPLACE VIEW telemetry.bug_report_human AS
SELECT
    br.id,
    br.reported_at,
    br.client_version,
    br.source,
    br.submitted_tier,                            -- 'standard' / 'reliability' / 'one_shot'
    br.title,
    COALESCE(t_kind.display_name,    'unclassified') AS kind,
    COALESCE(t_surface.display_name, 'unknown')      AS surface,
    br.signature,
    br.taxonomy_state,
    br.severity_hint,
    LEFT(br.install_id, 8) AS install_id_8,    -- enough to spot collisions
    LEFT(br.report_text, 500) AS preview,
    br.duplicate_of,
    br.classified_at,
    br.report_format,
    br.browser_escalated_at
FROM telemetry.bug_report br
LEFT JOIN telemetry.bug_report_taxonomy_assignment a_kind
       ON a_kind.bug_report_id = br.id
LEFT JOIN telemetry.taxonomy_term t_kind
       ON t_kind.id = a_kind.term_id
      AND t_kind.namespace = 'bug.kind'
LEFT JOIN telemetry.bug_report_taxonomy_assignment a_surf
       ON a_surf.bug_report_id = br.id
LEFT JOIN telemetry.taxonomy_term t_surface
       ON t_surface.id = a_surf.term_id
      AND t_surface.namespace = 'bug.surface'
ORDER BY br.reported_at DESC;

COMMENT ON VIEW telemetry.bug_report_human IS
'Denormalized view of bug_report joined to taxonomy_term for kind+surface. One row per report, newest first, with a 500-char preview of report_text. The view a single maintainer opens to scan recent bug reports.';


-- ============================================================================
-- View 2: bug_report_clusters
-- ============================================================================
--
-- Recurring signatures: the single most valuable query for triage. Tells
-- you which bug to fix next based on how many distinct installs hit it.

CREATE OR REPLACE VIEW telemetry.bug_report_clusters AS
SELECT
    signature,
    COUNT(DISTINCT install_id)                                   AS distinct_installs,
    COUNT(*)                                                     AS total_reports,
    MIN(reported_at)::date                                       AS first_seen,
    MAX(reported_at)::date                                       AS last_seen,
    ARRAY_AGG(DISTINCT client_version ORDER BY client_version)   AS versions,
    ARRAY_AGG(DISTINCT LEFT(install_id, 8) ORDER BY LEFT(install_id, 8)) AS install_id_prefixes,
    ARRAY_AGG(id ORDER BY reported_at DESC)                      AS report_ids
FROM telemetry.bug_report
WHERE signature IS NOT NULL
  AND reported_at >= now() - INTERVAL '180 days'
GROUP BY signature
HAVING COUNT(*) >= 1
ORDER BY distinct_installs DESC, total_reports DESC, last_seen DESC;

COMMENT ON VIEW telemetry.bug_report_clusters IS
'Bug-report clustering by signature, last 180 days. Recurring signatures (distinct_installs > 1) are the highest-leverage targets for the maintainer to fix.';


-- ============================================================================
-- View 3: install_summary
-- ============================================================================
--
-- One row per opted-in install with the latest snapshot of its known
-- attributes. For the maintainer asking "what's my install base?"

CREATE OR REPLACE VIEW telemetry.install_summary AS
SELECT
    i.install_id,
    i.first_seen_at,
    i.last_seen_at,
    i.first_version,
    i.current_version,
    i.current_install_method,
    i.current_os_family,
    i.current_os_detail,
    i.current_arch,
    i.current_backend_types,
    -- Count of events from this install (sanity check: should be 1+ for first_seen)
    (SELECT COUNT(*) FROM telemetry.installation_event ie
     WHERE ie.install_id = i.install_id) AS event_count,
    -- Days since last event — proxy for "how long since this install was active"
    EXTRACT(day FROM (now() - i.last_seen_at))::int AS days_since_last_event,
    -- Has this install ever filed a bug report?
    EXISTS (SELECT 1 FROM telemetry.bug_report br
            WHERE br.install_id = i.install_id) AS has_filed_bug_report
FROM telemetry.installation i
ORDER BY i.last_seen_at DESC;

COMMENT ON VIEW telemetry.install_summary IS
'One row per opted-in install. Includes days_since_last_event and has_filed_bug_report for quick triage. Note: silent installs (no upgrade for months) will appear inactive even if smirror is running fine — heartbeats are intentionally not collected.';


-- ============================================================================
-- View 4: weekly_health
-- ============================================================================
--
-- Single-row view summarizing the past week. Good for a CLI status command
-- or a quick "what changed?" glance.

CREATE OR REPLACE VIEW telemetry.weekly_health AS
WITH wk AS (
    SELECT date_trunc('week', now() AT TIME ZONE 'UTC')::date AS this_wk
)
SELECT
    wk.this_wk AS week_start,
    (SELECT COUNT(*) FROM telemetry.bug_report
     WHERE reported_at >= wk.this_wk
       AND reported_at <  wk.this_wk + INTERVAL '7 days') AS bugs_this_wk,
    (SELECT COUNT(*) FROM telemetry.bug_report
     WHERE reported_at >= wk.this_wk - INTERVAL '7 days'
       AND reported_at <  wk.this_wk) AS bugs_prev_wk,
    (SELECT COUNT(*) FROM telemetry.installation_event
     WHERE event_name='first_seen'
       AND reported_at >= wk.this_wk
       AND reported_at <  wk.this_wk + INTERVAL '7 days') AS new_installs_this_wk,
    (SELECT COUNT(*) FROM telemetry.installation_event
     WHERE event_name='upgrade'
       AND reported_at >= wk.this_wk
       AND reported_at <  wk.this_wk + INTERVAL '7 days') AS upgrades_this_wk,
    (SELECT COUNT(DISTINCT install_id) FROM telemetry.installation_event
     WHERE reported_at >= now() - INTERVAL '30 days') AS active_30d,
    (SELECT COUNT(*) FROM telemetry.installation) AS installs_total,
    (SELECT COUNT(*) FROM telemetry.bug_report
     WHERE taxonomy_state IN ('pending','needs_review')) AS unclassified_pending,
    pg_size_pretty(pg_database_size(current_database())) AS db_size,
    (SELECT MAX(received_at) FROM telemetry.ingest_envelope) AS last_ingest_at
FROM wk;

COMMENT ON VIEW telemetry.weekly_health IS
'Single-row dashboard for the current week. Use as the first query when checking project health.';

-- ============================================================================
-- View 5: tier_distribution
-- ============================================================================
--
-- How many opted-in installs are at each tier? Drives the digest's
-- "how many users have opted into Reliability vs Standard?" line.

CREATE OR REPLACE VIEW telemetry.tier_distribution AS
SELECT
    COALESCE(current_tier::text, 'unknown') AS tier,
    COUNT(*)                                AS installs
FROM telemetry.installation
GROUP BY current_tier
ORDER BY installs DESC;

COMMENT ON VIEW telemetry.tier_distribution IS
'How many opted-in installs at each tier. Note: None-tier users do not appear here at all (they create no installation rows).';


-- ============================================================================
-- View 6: reliability_snapshot_human
-- ============================================================================
--
-- Tier-3 reliability deltas joined to their parent upgrade event for
-- maintainer scanning. One row per upgrade-with-snapshot.

CREATE OR REPLACE VIEW telemetry.reliability_snapshot_human AS
SELECT
    ie.reported_at,
    ie.client_version,
    ie.prior_version,
    LEFT(ie.install_id, 8)                 AS install_id_8,
    rs.anomaly_counts_30d,
    rs.sync_attempts_bucket,
    rs.sync_failures_bucket,
    rs.restart_count,
    rs.max_queue_depth_bucket,
    rs.dead_letter_count_bucket,
    rs.state_db_size_bucket
FROM telemetry.installation_reliability_snapshot rs
JOIN telemetry.installation_event ie
  ON ie.id = rs.installation_event_id
ORDER BY ie.reported_at DESC;

COMMENT ON VIEW telemetry.reliability_snapshot_human IS
'Tier-3 (Reliability) reliability deltas joined to their upgrade event. One row per opted-in upgrade. The view a maintainer scans to spot regression patterns across releases.';


-- ============================================================================
-- View 7: install_config_distribution
-- ============================================================================
--
-- The 9 round-3 structural fields aggregated across opted-in installs.
-- Drives the "what does the typical install look like?" question.
-- k-anonymity must be enforced in the digest layer (not here) — these
-- views are admin-only via service_role.

CREATE OR REPLACE VIEW telemetry.install_config_distribution AS
SELECT
    'mirror_count_bucket' AS field, current_mirror_count_bucket AS value, COUNT(*) AS installs
FROM telemetry.installation
WHERE current_mirror_count_bucket IS NOT NULL
GROUP BY current_mirror_count_bucket

UNION ALL

SELECT 'background_mode', current_background_mode, COUNT(*)
FROM telemetry.installation WHERE current_background_mode IS NOT NULL
GROUP BY current_background_mode

UNION ALL

SELECT 'delete_policy', current_delete_policy, COUNT(*)
FROM telemetry.installation WHERE current_delete_policy IS NOT NULL
GROUP BY current_delete_policy

UNION ALL

SELECT 'has_hooks', current_has_hooks::text, COUNT(*)
FROM telemetry.installation WHERE current_has_hooks IS NOT NULL
GROUP BY current_has_hooks

UNION ALL

SELECT 'has_filters', current_has_filters::text, COUNT(*)
FROM telemetry.installation WHERE current_has_filters IS NOT NULL
GROUP BY current_has_filters

UNION ALL

SELECT 'has_alert_webhook', current_has_alert_webhook::text, COUNT(*)
FROM telemetry.installation WHERE current_has_alert_webhook IS NOT NULL
GROUP BY current_has_alert_webhook

UNION ALL

SELECT 'has_bandwidth_limit', current_has_bandwidth_limit::text, COUNT(*)
FROM telemetry.installation WHERE current_has_bandwidth_limit IS NOT NULL
GROUP BY current_has_bandwidth_limit

UNION ALL

SELECT 'rclone_version', current_rclone_version, COUNT(*)
FROM telemetry.installation WHERE current_rclone_version IS NOT NULL
GROUP BY current_rclone_version;

COMMENT ON VIEW telemetry.install_config_distribution IS
'Distribution of structural config fields across opted-in installs. Apply k-anonymity floor of 5 in the digest layer before publishing aggregates.';


-- ============================================================================
-- View 8: version_dist
-- ============================================================================
--
-- SM-175: the operations runbook (`docs/operations/telemetry-ops.md`)
-- references `telemetry.version_dist` from the bad-version-recovery
-- procedure. Defines it here as the canonical "active install version
-- distribution over the last 30 days," matching the digest's
-- Q_VERSION_DIST query so the maintainer sees the same number on the
-- dashboard and in the published digest.
--
-- "Active" = emitted any installation_event (first_seen / upgrade)
-- within the last 30 days. We then take each install's most recent
-- client_version and group.

CREATE OR REPLACE VIEW telemetry.version_dist AS
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

COMMENT ON VIEW telemetry.version_dist IS
'Active install version distribution over the last 30 days. Apply k-anonymity floor of 5 in the digest layer before publishing aggregates.';


-- ============================================================================
-- Read access for non-admin users
-- ============================================================================
--
-- These views are admin-facing only — no anon access. service_role bypasses
-- RLS and can SELECT freely. If you ever add an authenticated dashboard
-- role, grant it explicitly on the views (not the underlying tables).
--
-- (No GRANTs added here intentionally — leaves them service_role-only.)
