-- SelectiveMirror telemetry — drop v1 leftover (one-shot operator script)
--
-- Purpose: remove the v1 schema from a Supabase project that pre-dates
--          telemetry architecture v2. Run this BEFORE applying
--          docs/telemetry-v2.sql so the new v2 objects don't collide
--          with the v1 ones (some names overlap: installation_daily_rollup,
--          bug_daily_rollup, version_dist, the bug_source ENUM).
--
-- Status: v1 was deployed live since v0.9.4-dev. As of 0.9.18-dev (SM-160)
--         no released smirror.exe binary posts to v1 endpoints. The v1
--         tables receive zero new traffic from any active client. Existing
--         rows are aggregate-only (raw payloads were emptied by
--         purge_old_envelopes after 90 days) or recent rows that were
--         never normalized (since the v1 normalization pipeline was
--         deferred and never wired). In both cases, dropping is safe.
--
-- Reversibility: NONE for the data; the schema can be re-created from
--                git history (see commit 0.9.4-dev for the original v1
--                telemetry-microserver.sql), but the rows are gone.
--
-- Before running, take a manual snapshot of the project (Supabase Studio
-- → Database → Backups). Optional but cheap.
--
-- Apply via:
--   psql "$DATABASE_URL" -f docs/operations/sql/drop-v1-leftover.sql
--   psql "$DATABASE_URL" -f docs/telemetry-v2.sql

BEGIN;

-- ============================================================================
-- 1. Unschedule v1 pg_cron jobs
-- ============================================================================
-- These jobs run rollup refresh and retention purge on v1 tables. After
-- the tables are gone, the job functions would fail; unschedule first.
-- Use a DO block so missing jobs don't error the transaction.

DO $$ BEGIN
    PERFORM cron.unschedule('telemetry-bug-rollup-daily');
EXCEPTION WHEN OTHERS THEN NULL; END $$;

DO $$ BEGIN
    PERFORM cron.unschedule('telemetry-install-rollup-daily');
EXCEPTION WHEN OTHERS THEN NULL; END $$;

DO $$ BEGIN
    PERFORM cron.unschedule('telemetry-purge-old-envelopes');
EXCEPTION WHEN OTHERS THEN NULL; END $$;

-- ============================================================================
-- 2. Drop v1-only functions
-- ============================================================================
-- telemetry.verify_versioned_hmac is INTENTIONALLY KEPT — v2 uses it.
-- It is also redeclared in telemetry-v2.sql so v2 stays self-sufficient.

DROP FUNCTION IF EXISTS telemetry.refresh_bug_daily_rollup(DATE) CASCADE;
DROP FUNCTION IF EXISTS telemetry.refresh_install_daily_rollup(DATE) CASCADE;
DROP FUNCTION IF EXISTS telemetry.purge_old_envelopes(INTEGER) CASCADE;

-- ============================================================================
-- 3. Drop v1 views
-- ============================================================================
-- These read from v1 tables; without the tables they're useless. The
-- table-drop CASCADE below would handle most of these, but enumerate
-- them explicitly so a re-run on a partial state is idempotent.

DROP VIEW IF EXISTS telemetry.bug_report_human         CASCADE;
DROP VIEW IF EXISTS telemetry.bug_report_clusters      CASCADE;
DROP VIEW IF EXISTS telemetry.install_summary          CASCADE;
DROP VIEW IF EXISTS telemetry.weekly_health            CASCADE;
DROP VIEW IF EXISTS telemetry.tier_distribution        CASCADE;
DROP VIEW IF EXISTS telemetry.reliability_snapshot_human CASCADE;
DROP VIEW IF EXISTS telemetry.install_config_distribution CASCADE;
-- version_dist exists in BOTH v1 and v2 with different bodies. Drop the
-- v1 form here; v2 will create its own when telemetry-v2.sql runs next.
DROP VIEW IF EXISTS telemetry.version_dist             CASCADE;

-- ============================================================================
-- 4. Drop v1 tables
-- ============================================================================
-- CASCADE handles indexes, FKs, and any view that depends on these
-- tables. RLS policies attached to a table are dropped with the table.

DROP TABLE IF EXISTS telemetry.classification_job              CASCADE;
DROP TABLE IF EXISTS telemetry.bug_report_signal               CASCADE;
DROP TABLE IF EXISTS telemetry.bug_report_taxonomy_assignment  CASCADE;
DROP TABLE IF EXISTS telemetry.installation_taxonomy_assignment CASCADE;
DROP TABLE IF EXISTS telemetry.installation_reliability_snapshot CASCADE;
DROP TABLE IF EXISTS telemetry.installation_event              CASCADE;
DROP TABLE IF EXISTS telemetry.installation                    CASCADE;
DROP TABLE IF EXISTS telemetry.bug_report                      CASCADE;
DROP TABLE IF EXISTS telemetry.ingest_envelope                 CASCADE;
DROP TABLE IF EXISTS telemetry.taxonomy_term                   CASCADE;
-- Name-conflict tables — v1 and v2 use the same names with different
-- shapes. Drop the v1 form so v2's CREATE TABLE IF NOT EXISTS in
-- telemetry-v2.sql actually creates the v2 schema, not silently keep
-- the v1 columns.
DROP TABLE IF EXISTS telemetry.bug_daily_rollup                CASCADE;
DROP TABLE IF EXISTS telemetry.installation_daily_rollup       CASCADE;

-- ============================================================================
-- 5. Drop v1 ENUMs that v2 doesn't reuse OR that conflict with v2
-- ============================================================================
-- telemetry.bug_source NAME CONFLICTS — v1 has values ('report_bug', 'manual'),
-- v2 has ('report_bug', 'crash_report'). Different sets. The v2 SQL wraps
-- its CREATE TYPE in EXCEPTION WHEN duplicate_object THEN NULL, which would
-- silently keep the v1 type and break v2's INSERT later. Drop v1's first.

DROP TYPE IF EXISTS telemetry.bug_source           CASCADE;
DROP TYPE IF EXISTS telemetry.consent_tier         CASCADE;  -- v2 uses consent_tier_v2
DROP TYPE IF EXISTS telemetry.report_format        CASCADE;
DROP TYPE IF EXISTS telemetry.classification_state CASCADE;
DROP TYPE IF EXISTS telemetry.taxonomy_target      CASCADE;
DROP TYPE IF EXISTS telemetry.ingest_kind          CASCADE;

-- ============================================================================
-- 6. Verify
-- ============================================================================
-- A successful run leaves only:
--   - the telemetry schema itself (empty)
--   - telemetry.verify_versioned_hmac (kept — v2 uses it)
-- After this script, the next step is `psql -f docs/telemetry-v2.sql`,
-- which (re)creates the v2 schema cleanly.

-- Sanity check (commented; run manually if you want to inspect):
-- SELECT relname FROM pg_class JOIN pg_namespace ON relnamespace = pg_namespace.oid
--  WHERE nspname = 'telemetry' AND relkind IN ('r','v','m')
--  ORDER BY relname;
-- Expected: zero rows after this script (apart from any v2 objects already
-- present — typically none on a fresh project).

COMMIT;

-- ============================================================================
-- Optional but recommended: VACUUM
-- ============================================================================
-- Run separately (cannot be inside a transaction):
--   VACUUM FULL telemetry.*;     -- reclaim disk
-- For Supabase free-tier projects this matters because old v1 ingest_envelope
-- could have accumulated significant disk before purge_old_envelopes ran.
