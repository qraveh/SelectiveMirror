-- SelectiveMirror telemetry: human-readable SQL views
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
    br.report_format
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
-- Read access for non-admin users
-- ============================================================================
--
-- These views are admin-facing only — no anon access. service_role bypasses
-- RLS and can SELECT freely. If you ever add an authenticated dashboard
-- role, grant it explicitly on the views (not the underlying tables).
--
-- (No GRANTs added here intentionally — leaves them service_role-only.)
