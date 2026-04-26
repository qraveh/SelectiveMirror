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
    INSERT INTO telemetry.bug_daily_rollup AS dst (
        rollup_date, bug_kind, bug_surface, client_version,
        reports, unique_signatures, unclassified_reports
    )
    SELECT
        target_date AS rollup_date,
        COALESCE(MAX(t_kind.slug),    'unknown') AS bug_kind,
        COALESCE(MAX(t_surface.slug), 'unknown') AS bug_surface,
        COALESCE(br.client_version,   'unknown') AS client_version,
        COUNT(DISTINCT br.id)         AS reports,
        COUNT(DISTINCT br.signature)
            FILTER (WHERE br.signature IS NOT NULL) AS unique_signatures,
        COUNT(DISTINCT br.id)
            FILTER (WHERE br.taxonomy_state != 'classified') AS unclassified_reports
    FROM telemetry.bug_report br
    LEFT JOIN telemetry.bug_report_taxonomy_assignment a_kind
        ON a_kind.bug_report_id = br.id
    LEFT JOIN telemetry.taxonomy_term t_kind
        ON t_kind.id = a_kind.term_id
       AND t_kind.namespace = 'bug.kind'
    LEFT JOIN telemetry.bug_report_taxonomy_assignment a_surface
        ON a_surface.bug_report_id = br.id
    LEFT JOIN telemetry.taxonomy_term t_surface
        ON t_surface.id = a_surface.term_id
       AND t_surface.namespace = 'bug.surface'
    WHERE br.reported_at >= target_date
      AND br.reported_at <  target_date + INTERVAL '1 day'
    GROUP BY t_kind.slug, t_surface.slug, br.client_version
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
-- Only operates on classified rows. Unclassified envelopes remain
-- intact for inspection regardless of age.

CREATE OR REPLACE FUNCTION telemetry.purge_old_envelopes(
    retention_days INTEGER DEFAULT 90
)
RETURNS TABLE(purged_count BIGINT)
LANGUAGE plpgsql
AS $$
DECLARE
    affected BIGINT;
BEGIN
    UPDATE telemetry.ingest_envelope
    SET payload = '{}'::jsonb
    WHERE received_at < (now() - (retention_days || ' days')::INTERVAL)
      AND payload != '{}'::jsonb
      AND classification_state = 'classified';

    GET DIAGNOSTICS affected = ROW_COUNT;
    RETURN QUERY SELECT affected;
END;
$$;

COMMENT ON FUNCTION telemetry.purge_old_envelopes(INTEGER) IS
'Retention janitor: nulls out raw payloads of classified ingest_envelope rows older than retention_days (default 90). Keeps the row and dedupe_key for idempotency. Safe to run repeatedly; no-op when nothing matches.';

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
