-- SelectiveMirror telemetry: pg_cron background workers
--
-- Defines two recurring jobs that run inside the Postgres database
-- (no external service needed):
--   1. Daily rollup refresh — aggregates yesterday's events into
--      bug_daily_rollup and installation_daily_rollup
--   2. Retention janitor — strips raw payloads from old ingest_envelope
--      rows to keep DB size manageable on the free tier
--
-- Apply AFTER loading telemetry-microserver.sql (creates the tables).
-- Apply ONCE; pg_cron tracks scheduled jobs and re-running this would
-- create duplicate schedules. To remove, see "Unschedule" section at bottom.
--
-- Verify after loading:
--   SELECT * FROM cron.job;            -- two rows expected
--   SELECT * FROM cron.job_run_details ORDER BY end_time DESC LIMIT 10;
--   SELECT telemetry.refresh_bug_daily_rollup(); -- run manually once

-- ============================================================================
-- pg_cron extension
-- ============================================================================
--
-- Supabase enables pg_cron on all tiers, but the extension may need to be
-- explicitly created. Safe to run repeatedly.

CREATE EXTENSION IF NOT EXISTS pg_cron;

-- ============================================================================
-- Function 1: refresh_bug_daily_rollup
-- ============================================================================
--
-- Aggregates bug_report rows into bug_daily_rollup. Default: yesterday's
-- data (pass an explicit date for backfill / re-run).
--
-- Bug.kind and bug.surface taxonomy terms are picked up via the join to
-- bug_report_taxonomy_assignment + taxonomy_term. Reports without a
-- bug.kind assignment (taxonomy not yet run) get bucketed as 'unknown'.

CREATE OR REPLACE FUNCTION telemetry.refresh_bug_daily_rollup(
    target_date DATE DEFAULT (CURRENT_DATE - 1)
)
RETURNS TABLE(rollup_date DATE, segments BIGINT)
LANGUAGE plpgsql
AS $$
DECLARE
    affected BIGINT;
BEGIN
    -- SM-178: each taxonomy dimension is collapsed to one row per
    -- bug_report BEFORE we join into the parent — so a report tagged
    -- with both a 'bug.kind' and a 'bug.surface' produces one
    -- (kind, surface) tuple, not the cross-product
    -- {(kind,surface), (kind,unknown), (unknown,surface),
    -- (unknown,unknown)} that the previous join order produced.
    -- The collapsing aggregator picks an arbitrary slug when a single
    -- report has multiple terms in one namespace; that is acceptable
    -- because the schema does not document multi-tag-per-namespace as
    -- a valid state. (If multi-tag becomes a feature, change MIN()
    -- to a deterministic tiebreaker.)
    INSERT INTO telemetry.bug_daily_rollup AS dst (
        rollup_date, bug_kind, bug_surface, client_version,
        reports, unique_signatures, unclassified_reports
    )
    SELECT
        target_date AS rollup_date,
        COALESCE(kind_per_report.slug,    'unknown') AS bug_kind,
        COALESCE(surface_per_report.slug, 'unknown') AS bug_surface,
        COALESCE(br.client_version,       'unknown') AS client_version,
        COUNT(DISTINCT br.id)         AS reports,
        COUNT(DISTINCT br.signature)
            FILTER (WHERE br.signature IS NOT NULL) AS unique_signatures,
        COUNT(DISTINCT br.id)
            FILTER (WHERE br.taxonomy_state != 'classified') AS unclassified_reports
    FROM telemetry.bug_report br
    LEFT JOIN (
        SELECT a.bug_report_id, MIN(t.slug) AS slug
        FROM telemetry.bug_report_taxonomy_assignment a
        JOIN telemetry.taxonomy_term t ON t.id = a.term_id
        WHERE t.namespace = 'bug.kind'
        GROUP BY a.bug_report_id
    ) kind_per_report ON kind_per_report.bug_report_id = br.id
    LEFT JOIN (
        SELECT a.bug_report_id, MIN(t.slug) AS slug
        FROM telemetry.bug_report_taxonomy_assignment a
        JOIN telemetry.taxonomy_term t ON t.id = a.term_id
        WHERE t.namespace = 'bug.surface'
        GROUP BY a.bug_report_id
    ) surface_per_report ON surface_per_report.bug_report_id = br.id
    WHERE br.reported_at >= target_date
      AND br.reported_at <  target_date + INTERVAL '1 day'
    GROUP BY kind_per_report.slug, surface_per_report.slug, br.client_version
    ON CONFLICT (rollup_date, bug_kind, bug_surface, client_version)
    DO UPDATE SET
        reports              = EXCLUDED.reports,
        unique_signatures    = EXCLUDED.unique_signatures,
        unclassified_reports = EXCLUDED.unclassified_reports;

    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN QUERY SELECT target_date, affected;
END;
$$;

COMMENT ON FUNCTION telemetry.refresh_bug_daily_rollup(DATE) IS
'Recomputes bug_daily_rollup for the given date (default: yesterday). Idempotent — uses ON CONFLICT to update existing rows. Safe to run manually for backfill.';

-- ============================================================================
-- Function 2: refresh_install_daily_rollup
-- ============================================================================
--
-- Aggregates installation_event rows into installation_daily_rollup.
-- 'first_seen_count' and 'upgrade_count' are derived from event_name.
-- 'active_installs_30d' counts distinct install_ids that emitted any
-- event in the 30 days ending on rollup_date — a proxy for "active" given
-- we don't collect heartbeats.

CREATE OR REPLACE FUNCTION telemetry.refresh_install_daily_rollup(
    target_date DATE DEFAULT (CURRENT_DATE - 1)
)
RETURNS TABLE(rollup_date DATE, segments BIGINT)
LANGUAGE plpgsql
AS $$
DECLARE
    affected BIGINT;
BEGIN
    INSERT INTO telemetry.installation_daily_rollup AS dst (
        rollup_date, install_method, os_family, client_version,
        first_seen_count, upgrade_count, active_installs_30d
    )
    SELECT
        target_date AS rollup_date,
        COALESCE(ie.install_method, 'unknown') AS install_method,
        COALESCE(ie.os_family,      'unknown') AS os_family,
        COALESCE(ie.client_version, 'unknown') AS client_version,
        COUNT(*) FILTER (WHERE ie.event_name = 'first_seen') AS first_seen_count,
        COUNT(*) FILTER (WHERE ie.event_name = 'upgrade')    AS upgrade_count,
        (
            SELECT COUNT(DISTINCT ie2.install_id)
            FROM telemetry.installation_event ie2
            WHERE ie2.reported_at >= target_date - INTERVAL '30 days'
              AND ie2.reported_at <  target_date + INTERVAL '1 day'
              AND ie2.install_method IS NOT DISTINCT FROM ie.install_method
              AND ie2.os_family      IS NOT DISTINCT FROM ie.os_family
              AND ie2.client_version IS NOT DISTINCT FROM ie.client_version
        ) AS active_installs_30d
    FROM telemetry.installation_event ie
    WHERE ie.reported_at >= target_date
      AND ie.reported_at <  target_date + INTERVAL '1 day'
    GROUP BY ie.install_method, ie.os_family, ie.client_version
    ON CONFLICT (rollup_date, install_method, os_family, client_version)
    DO UPDATE SET
        first_seen_count    = EXCLUDED.first_seen_count,
        upgrade_count       = EXCLUDED.upgrade_count,
        active_installs_30d = EXCLUDED.active_installs_30d;

    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN QUERY SELECT target_date, affected;
END;
$$;

COMMENT ON FUNCTION telemetry.refresh_install_daily_rollup(DATE) IS
'Recomputes installation_daily_rollup for the given date (default: yesterday). Idempotent.';

-- ============================================================================
-- Function 3: purge_old_envelopes (retention janitor)
-- ============================================================================
--
-- After the retention window, strips the raw JSONB payload from
-- ingest_envelope rows whose downstream normalized record has been
-- classified. Keeps the row, dedupe_key, and metadata (so retries are
-- still idempotent and analytics still see the row), but frees the bulk
-- storage of the original payload.
--
-- SM-172: also strips the normalized raw text fields on
-- telemetry.bug_report (`report_text`, `anomaly_summary`,
-- `status_snapshot`). The PRIVACY.md commitment is "raw payloads are
-- stripped after 90 days"; the previous implementation only emptied
-- ingest_envelope.payload, leaving the same content alive in
-- bug_report's normalized columns. Now both layers are purged in a
-- single transaction, keyed on the same retention boundary.
--
-- Only operates on classified rows. Unclassified envelopes / unfiled
-- bug reports remain intact for inspection regardless of age — the
-- maintainer needs them to do the classification.

CREATE OR REPLACE FUNCTION telemetry.purge_old_envelopes(
    retention_days INTEGER DEFAULT 90
)
RETURNS TABLE(purged_envelopes BIGINT, purged_bug_report_text BIGINT)
LANGUAGE plpgsql
AS $$
DECLARE
    affected_envelopes      BIGINT;
    affected_bug_report     BIGINT;
    cutoff                  TIMESTAMPTZ := now() - (retention_days || ' days')::INTERVAL;
BEGIN
    -- Layer 1: raw envelope payload.
    UPDATE telemetry.ingest_envelope
    SET payload = '{}'::jsonb
    WHERE received_at < cutoff
      AND payload != '{}'::jsonb
      AND classification_state = 'classified';
    GET DIAGNOSTICS affected_envelopes = ROW_COUNT;

    -- Layer 2: SM-172 — normalized raw text on bug_report.
    -- report_text is NOT NULL TEXT, so we replace with the empty
    -- string. anomaly_summary and status_snapshot are NOT NULL JSONB
    -- with default '{}'::jsonb, matching the empty-payload sentinel
    -- we use for envelopes. Only purge classified rows so the
    -- maintainer can still look at unfiled reports.
    UPDATE telemetry.bug_report
    SET report_text     = '',
        anomaly_summary = '{}'::jsonb,
        status_snapshot = '{}'::jsonb
    WHERE reported_at < cutoff
      AND taxonomy_state = 'classified'
      AND (report_text != '' OR anomaly_summary != '{}'::jsonb OR status_snapshot != '{}'::jsonb);
    GET DIAGNOSTICS affected_bug_report = ROW_COUNT;

    RETURN QUERY SELECT affected_envelopes, affected_bug_report;
END;
$$;

COMMENT ON FUNCTION telemetry.purge_old_envelopes(INTEGER) IS
'Retention janitor: empties raw payloads on classified ingest_envelope rows AND raw text on classified bug_report rows older than retention_days (default 90). Keeps row identity / dedupe_key / aggregate-safe metadata. Safe to run repeatedly; no-op when nothing matches. Returns (envelope_rows_emptied, bug_report_rows_emptied).';

-- ============================================================================
-- Schedule the jobs
-- ============================================================================
--
-- All times are UTC. Order matters: rollup before purge so the rollup
-- can still see classified rows that the purge will then strip.
--
-- Use cron.schedule(name, schedule, command). The 'name' allows
-- subsequent unschedule by name without knowing the job ID.

-- Bug rollup at 02:00 UTC daily
SELECT cron.schedule(
    'telemetry-bug-rollup-daily',
    '0 2 * * *',
    $$ SELECT telemetry.refresh_bug_daily_rollup(); $$
);

-- Install rollup at 02:15 UTC daily (after bug rollup)
SELECT cron.schedule(
    'telemetry-install-rollup-daily',
    '15 2 * * *',
    $$ SELECT telemetry.refresh_install_daily_rollup(); $$
);

-- Retention janitor at 03:00 UTC daily (after rollups)
SELECT cron.schedule(
    'telemetry-purge-old-envelopes',
    '0 3 * * *',
    $$ SELECT telemetry.purge_old_envelopes(); $$
);

-- ============================================================================
-- Verification queries (run manually to inspect)
-- ============================================================================
--
-- All scheduled jobs:
--   SELECT jobid, jobname, schedule, command, active
--   FROM cron.job
--   WHERE jobname LIKE 'telemetry-%';
--
-- Recent execution history:
--   SELECT jobid, status, start_time, end_time, return_message
--   FROM cron.job_run_details
--   WHERE jobid IN (SELECT jobid FROM cron.job WHERE jobname LIKE 'telemetry-%')
--   ORDER BY end_time DESC LIMIT 10;
--
-- Manual run (good for testing):
--   SELECT * FROM telemetry.refresh_bug_daily_rollup();
--   SELECT * FROM telemetry.refresh_install_daily_rollup();
--   SELECT * FROM telemetry.purge_old_envelopes();
--
-- View current rollup data:
--   SELECT * FROM telemetry.bug_daily_rollup
--   ORDER BY rollup_date DESC, reports DESC LIMIT 20;
--   SELECT * FROM telemetry.installation_daily_rollup
--   ORDER BY rollup_date DESC, first_seen_count DESC LIMIT 20;

-- ============================================================================
-- Unschedule (only run if you want to remove these jobs)
-- ============================================================================
--
-- SELECT cron.unschedule('telemetry-bug-rollup-daily');
-- SELECT cron.unschedule('telemetry-install-rollup-daily');
-- SELECT cron.unschedule('telemetry-purge-old-envelopes');
