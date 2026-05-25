-- SelectiveMirror telemetry schema v2 — stream-aggregate-and-discard
--
-- Design: docs/telemetry-architecture-v2.md
-- Adopted: 2026-04-29
-- Replaces: docs/telemetry-microserver.sql (v1), docs/telemetry-rls.sql (v1)
--
-- This file defines the COMPLETE telemetry schema for v2. It contains
-- ONLY tables that hold aggregate counters; there is no table for
-- individual events, raw payloads, or per-install records.
--
-- Apply order:
--   1. v1 schema (telemetry-microserver.sql) — already deployed; remains
--      operational during the migration window.
--   2. THIS file — additive; does not modify v1 tables.
--   3. v2 RLS (inline below) — restricts new objects.
--
-- During cutover (Phase B/C in the architecture doc), the Worker dual-
-- writes to both v1 and v2 endpoints; once v2 is verified, v1 traffic
-- stops and v1 tables age out via SM-172's retention janitor before
-- being dropped (Phase D).
--
-- Re-runnable: every CREATE uses IF NOT EXISTS or OR REPLACE.

BEGIN;

-- ============================================================================
-- Schema namespace (already exists from v1; no-op if present)
-- ============================================================================
CREATE SCHEMA IF NOT EXISTS telemetry;

-- ============================================================================
-- Closed taxonomies — bucketed dimensions used by rollups
-- ============================================================================
--
-- These ENUMs lock the value space for client-supplied categorical
-- fields. Adding a value requires a release; this is intentional —
-- the bucket choices ARE the privacy posture.

DO $$ BEGIN
    CREATE TYPE telemetry.event_kind AS ENUM (
        'first_seen', 'upgrade', 'bug_report', 'reliability_snapshot'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.consent_tier_v2 AS ENUM (
        'standard', 'reliability', 'one_shot'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Severity hint for bug reports. Client-side enum; never derived from
-- prose.
DO $$ BEGIN
    CREATE TYPE telemetry.severity_hint AS ENUM (
        'info', 'warning', 'error', 'critical'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

-- Bucket types — string ENUMs to keep the rollup tuples self-describing.
-- These match the buckets defined in PRIVACY.md.

DO $$ BEGIN
    CREATE TYPE telemetry.mirror_count_bucket AS ENUM (
        '0', '1', '2-5', '6-20', '21+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.background_mode AS ENUM (
        'foreground', 'service', 'task', 'unknown'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.delete_policy AS ENUM (
        'ignore', 'delete', 'quarantine'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.days_since_first_seen_bucket AS ENUM (
        '1-7', '8-30', '31-90', '91-365', '>365'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.attempts_bucket AS ENUM (
        '<100', '100-1k', '1k-10k', '10k-100k', '100k+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.queue_depth_bucket AS ENUM (
        '<100', '100-1k', '1k-10k', '10k+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.dead_letter_bucket AS ENUM (
        '0', '1-10', '11-100', '100+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.state_db_size_bucket AS ENUM (
        '<10MB', '10-100MB', '100MB-1GB', '1GB+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.anomaly_count_bucket AS ENUM (
        '0', '1-5', '6-25', '26-100', '100+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.restart_count_bucket AS ENUM (
        '0', '1-5', '6-25', '26-100', '100+'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;

DO $$ BEGIN
    CREATE TYPE telemetry.bug_source AS ENUM (
        'report_bug', 'crash_report'
    );
EXCEPTION WHEN duplicate_object THEN NULL; END $$;


-- ============================================================================
-- Rollup table 1: installation_daily_rollup
-- ============================================================================
--
-- One row per (rollup_date, full bucket tuple). Counter sums on
-- conflict. Both first_seen and upgrade events land here, distinguished
-- by event_name. prior_version and days_since_first_seen_bucket are
-- non-NULL only for upgrade events; NULL constraints are NOT enforced
-- so the UNIQUE INDEX can include them with the standard NULLS NOT
-- DISTINCT semantics.

CREATE TABLE IF NOT EXISTS telemetry.installation_daily_rollup (
    rollup_date                    DATE NOT NULL,
    event_name                     telemetry.event_kind NOT NULL
        CHECK (event_name IN ('first_seen', 'upgrade')),
    install_method                 TEXT NOT NULL,         -- "msi" / "winget" / "zip" / ...
    os_family                      TEXT NOT NULL,         -- "windows" / "linux" / "macos"
    client_version                 TEXT NOT NULL,
    mirror_count_bucket            telemetry.mirror_count_bucket NOT NULL,
    background_mode                telemetry.background_mode NOT NULL,
    delete_policy                  telemetry.delete_policy NOT NULL,
    has_hooks                      BOOLEAN NOT NULL,
    has_filters                    BOOLEAN NOT NULL,
    has_alert_webhook              BOOLEAN NOT NULL,
    has_bandwidth_limit            BOOLEAN NOT NULL,
    rclone_version                 TEXT NOT NULL,
    -- Upgrade-only dimensions (NULL for first_seen):
    prior_version                  TEXT,
    days_since_first_seen_bucket   telemetry.days_since_first_seen_bucket,
    -- Counter:
    count                          BIGINT NOT NULL DEFAULT 0,
    -- Uniqueness, NOT a primary key, because PRIMARY KEY would force
    -- prior_version and days_since_first_seen_bucket to NOT NULL — they
    -- are legitimately NULL on first_seen events. UNIQUE NULLS NOT
    -- DISTINCT (PG 15+) gives us the same ON CONFLICT semantics
    -- (NULL = NULL is treated as a duplicate) without rejecting the row.
    CONSTRAINT installation_daily_rollup_uniq
        UNIQUE NULLS NOT DISTINCT (
            rollup_date, event_name, install_method, os_family,
            client_version, mirror_count_bucket, background_mode,
            delete_policy, has_hooks, has_filters, has_alert_webhook,
            has_bandwidth_limit, rclone_version,
            prior_version, days_since_first_seen_bucket
        )
);

COMMENT ON TABLE telemetry.installation_daily_rollup IS
'Anonymous counts of first_seen and upgrade events. No install_id, no PII. Maintained inline by telemetry.contribute(); k-anon floor of 5 enforced at publish time.';


-- ============================================================================
-- Rollup table 2: bug_daily_rollup
-- ============================================================================
--
-- Counts bug-report submissions by client-side classification only.
-- The narrative content lives in GitHub Issues (not here). Each row is
-- the count of submissions matching the bucket on that day.

CREATE TABLE IF NOT EXISTS telemetry.bug_daily_rollup (
    rollup_date        DATE NOT NULL,
    bug_kind           TEXT NOT NULL,        -- closed taxonomy (see taxonomy_term)
    bug_surface        TEXT NOT NULL,        -- closed taxonomy
    client_version     TEXT NOT NULL,
    severity_hint      telemetry.severity_hint NOT NULL,
    source             telemetry.bug_source NOT NULL,
    submitted_tier     telemetry.consent_tier_v2 NOT NULL,
    reports            BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (rollup_date, bug_kind, bug_surface, client_version,
                 severity_hint, source, submitted_tier)
);

COMMENT ON TABLE telemetry.bug_daily_rollup IS
'Counts of bug-report submissions, classified client-side at submit time. No report_text, no install_id, no GitHub-issue link. The narrative lives in GitHub Issues.';


-- ============================================================================
-- Rollup table 3: reliability_daily_rollup
-- ============================================================================
--
-- Counts of reliability snapshots (Reliability tier, attached to
-- upgrade events). Each row is the count of snapshots matching the
-- bucket tuple on that day. Per-anomaly-kind counts are collapsed to
-- a single bucket (anomaly_count_bucket) plus the leading kind.

CREATE TABLE IF NOT EXISTS telemetry.reliability_daily_rollup (
    rollup_date                 DATE NOT NULL,
    client_version              TEXT NOT NULL,
    anomaly_count_bucket        telemetry.anomaly_count_bucket NOT NULL,
    most_common_anomaly_kind    TEXT,                    -- NULL when no anomalies
    sync_attempts_bucket        telemetry.attempts_bucket NOT NULL,
    sync_failures_bucket        telemetry.attempts_bucket NOT NULL,
    restart_count_bucket        telemetry.restart_count_bucket NOT NULL,
    max_queue_depth_bucket      telemetry.queue_depth_bucket NOT NULL,
    dead_letter_count_bucket    telemetry.dead_letter_bucket NOT NULL,
    state_db_size_bucket        telemetry.state_db_size_bucket NOT NULL,
    count                       BIGINT NOT NULL DEFAULT 0,
    -- Same NULL-friendly uniqueness as installation_daily_rollup; see
    -- the comment there. most_common_anomaly_kind is NULL when zero
    -- anomalies, so PRIMARY KEY would reject the row.
    CONSTRAINT reliability_daily_rollup_uniq
        UNIQUE NULLS NOT DISTINCT (
            rollup_date, client_version, anomaly_count_bucket,
            most_common_anomaly_kind, sync_attempts_bucket,
            sync_failures_bucket, restart_count_bucket,
            max_queue_depth_bucket, dead_letter_count_bucket,
            state_db_size_bucket
        )
);

COMMENT ON TABLE telemetry.reliability_daily_rollup IS
'Counts of reliability snapshots. All numeric fields bucketed; the per-kind anomaly map is collapsed to total-bucket + leading-kind.';


-- ============================================================================
-- HMAC verification — reused from v1 (idempotent CREATE OR REPLACE)
-- ============================================================================
--
-- This function lives in v1's telemetry-rls.sql; we re-declare it here
-- so v2 can be applied standalone. Identical signature.

CREATE OR REPLACE FUNCTION telemetry.verify_versioned_hmac(
    canonical_payload BYTEA,
    claimed_version TEXT,
    claimed_hmac_hex TEXT
) RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    master_key TEXT;
    derived_key BYTEA;
    expected_hmac BYTEA;
    claimed_hmac  BYTEA;
BEGIN
    -- Master key from Vault (Supabase). The vault.decrypted_secrets
    -- view exposes the secret only to SECURITY DEFINER functions
    -- and never appears in logs.
    SELECT decrypted_secret INTO master_key
    FROM vault.decrypted_secrets
    WHERE name = 'telemetry_master_key';
    IF master_key IS NULL THEN
        RAISE EXCEPTION 'telemetry_master_key not configured in vault';
    END IF;

    -- Derive per-version key.
    --
    -- SM-219 (2026-05-21): master_key is the HEX REPRESENTATION of a
    -- 32-byte random secret. It MUST be hex-decoded before use as an
    -- HMAC key, to match the binary's derivation in
    -- .github/workflows/release.yml (`bytes.fromhex(master)`) and
    -- scripts/telemetry-v2-smoke-test.py (`bytes.fromhex(master_key)`).
    -- The pre-SM-219 form `master_key::BYTEA` cast the UTF-8 bytes of
    -- the hex string itself, which produced a different derived key
    -- than the binary computed — every CI-built smirror.exe was being
    -- silently rejected because its HMAC never matched the server's
    -- expectation. See BugTracker SM-219.
    derived_key := hmac(claimed_version::BYTEA, decode(master_key, 'hex'), 'sha256');

    -- Compute expected HMAC over the canonical payload bytes.
    expected_hmac := hmac(canonical_payload, derived_key, 'sha256');
    claimed_hmac  := decode(claimed_hmac_hex, 'hex');

    -- Constant-time compare via Postgres '=' on equal-length bytea is
    -- short-circuit; for higher assurance use a length+xor loop. For
    -- the threat model here (forge a per-version key with a
    -- single-binary leak), the timing risk is acceptable.
    RETURN expected_hmac = claimed_hmac;
END;
$$;

-- Lock down execute. Only the SECURITY DEFINER caller (which is
-- contribute() below) should invoke verify directly.
REVOKE ALL ON FUNCTION telemetry.verify_versioned_hmac(BYTEA, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION telemetry.verify_versioned_hmac(BYTEA, TEXT, TEXT) TO service_role;


-- ============================================================================
-- Internal bump helpers (private; called only from contribute())
-- ============================================================================
--
-- Each helper takes the parsed payload and UPSERTs into one rollup
-- table. They are SECURITY DEFINER so they can write to telemetry.*
-- regardless of the caller's role.

CREATE OR REPLACE FUNCTION telemetry._bump_install(
    payload JSONB,
    p_event_kind TEXT
) RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
BEGIN
    INSERT INTO telemetry.installation_daily_rollup AS dst (
        rollup_date, event_name, install_method, os_family, client_version,
        mirror_count_bucket, background_mode, delete_policy,
        has_hooks, has_filters, has_alert_webhook, has_bandwidth_limit,
        rclone_version, prior_version, days_since_first_seen_bucket,
        count
    ) VALUES (
        (payload->>'reported_at')::TIMESTAMPTZ::DATE,
        p_event_kind::telemetry.event_kind,
        payload->>'install_method',
        payload->>'os_family',
        payload->>'client_version',
        (payload->>'mirror_count_bucket')::telemetry.mirror_count_bucket,
        (payload->>'background_mode')::telemetry.background_mode,
        (payload->>'delete_policy')::telemetry.delete_policy,
        (payload->>'has_hooks')::BOOLEAN,
        (payload->>'has_filters')::BOOLEAN,
        (payload->>'has_alert_webhook')::BOOLEAN,
        (payload->>'has_bandwidth_limit')::BOOLEAN,
        payload->>'rclone_version',
        payload->>'prior_version',         -- NULL on first_seen
        CASE WHEN payload ? 'days_since_first_seen_bucket'
             THEN (payload->>'days_since_first_seen_bucket')::telemetry.days_since_first_seen_bucket
             ELSE NULL END,
        1
    )
    ON CONFLICT (rollup_date, event_name, install_method, os_family,
                 client_version, mirror_count_bucket, background_mode,
                 delete_policy, has_hooks, has_filters, has_alert_webhook,
                 has_bandwidth_limit, rclone_version,
                 prior_version, days_since_first_seen_bucket)
    DO UPDATE SET count = dst.count + 1;
END;
$$;

CREATE OR REPLACE FUNCTION telemetry._bump_bug(payload JSONB)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
BEGIN
    INSERT INTO telemetry.bug_daily_rollup AS dst (
        rollup_date, bug_kind, bug_surface, client_version,
        severity_hint, source, submitted_tier, reports
    ) VALUES (
        (payload->>'reported_at')::TIMESTAMPTZ::DATE,
        payload->>'bug_kind',
        payload->>'bug_surface',
        payload->>'client_version',
        (payload->>'severity_hint')::telemetry.severity_hint,
        (payload->>'source')::telemetry.bug_source,
        (payload->>'submitted_tier')::telemetry.consent_tier_v2,
        1
    )
    ON CONFLICT (rollup_date, bug_kind, bug_surface, client_version,
                 severity_hint, source, submitted_tier)
    DO UPDATE SET reports = dst.reports + 1;
END;
$$;

CREATE OR REPLACE FUNCTION telemetry._bump_reliability(payload JSONB)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
BEGIN
    INSERT INTO telemetry.reliability_daily_rollup AS dst (
        rollup_date, client_version, anomaly_count_bucket,
        most_common_anomaly_kind, sync_attempts_bucket,
        sync_failures_bucket, restart_count_bucket,
        max_queue_depth_bucket, dead_letter_count_bucket,
        state_db_size_bucket, count
    ) VALUES (
        (payload->>'reported_at')::TIMESTAMPTZ::DATE,
        payload->>'client_version',
        (payload->>'anomaly_count_bucket')::telemetry.anomaly_count_bucket,
        payload->>'most_common_anomaly_kind',          -- NULL when no anomalies
        (payload->>'sync_attempts_bucket')::telemetry.attempts_bucket,
        (payload->>'sync_failures_bucket')::telemetry.attempts_bucket,
        (payload->>'restart_count_bucket')::telemetry.restart_count_bucket,
        (payload->>'max_queue_depth_bucket')::telemetry.queue_depth_bucket,
        (payload->>'dead_letter_count_bucket')::telemetry.dead_letter_bucket,
        (payload->>'state_db_size_bucket')::telemetry.state_db_size_bucket,
        1
    )
    ON CONFLICT (rollup_date, client_version, anomaly_count_bucket,
                 most_common_anomaly_kind, sync_attempts_bucket,
                 sync_failures_bucket, restart_count_bucket,
                 max_queue_depth_bucket, dead_letter_count_bucket,
                 state_db_size_bucket)
    DO UPDATE SET count = dst.count + 1;
END;
$$;


-- ============================================================================
-- Public entry: telemetry.contribute()
-- ============================================================================
--
-- The single function the Worker calls. Verifies HMAC, dispatches by
-- event_kind, returns a JSONB result. Never INSERTs or UPDATEs anything
-- outside the three rollup tables.
--
-- The payload argument is a JSONB value bound by PostgREST. It is held
-- in memory for the duration of this function call; it does not appear
-- in pg_stat_statements (parameter values are normalized) and does not
-- appear in pg_log unless log_min_duration_statement is set to 0
-- (which it must not be).
--
-- Returns:
--   {"ok": true}
--   {"ok": false, "error": "rejected"}                   -- HMAC mismatch
--   {"ok": false, "error": "unknown_event"}              -- bad event_kind
--   {"ok": false, "error": "schema_violation:<detail>"}  -- malformed payload

CREATE OR REPLACE FUNCTION telemetry.contribute(
    payload JSONB,
    claimed_version TEXT,
    claimed_hmac_hex TEXT
) RETURNS JSONB
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    canonical    BYTEA;
    e_kind       TEXT;
BEGIN
    -- 0. Sanity: payload must be an object.
    IF jsonb_typeof(payload) <> 'object' THEN
        RETURN jsonb_build_object('ok', false, 'error', 'schema_violation:not_object');
    END IF;

    -- 1. Compute canonical bytes for HMAC verification. Strip
    --    `version_hmac` (the field that carries the signature itself)
    --    and `event_kind` (carried alongside but not part of signed
    --    bytes — clients sign over the dimensional payload).
    canonical := convert_to(
        ((payload - 'version_hmac') - 'event_kind')::TEXT,
        'UTF8');

    IF NOT telemetry.verify_versioned_hmac(canonical, claimed_version, claimed_hmac_hex) THEN
        RETURN jsonb_build_object('ok', false, 'error', 'rejected');
    END IF;

    -- 2. Dispatch by event kind.
    e_kind := payload->>'event_kind';
    IF e_kind IS NULL THEN
        RETURN jsonb_build_object('ok', false, 'error', 'schema_violation:missing_event_kind');
    END IF;

    BEGIN
        CASE e_kind
            WHEN 'first_seen', 'upgrade' THEN
                PERFORM telemetry._bump_install(payload, e_kind);
            WHEN 'bug_report' THEN
                PERFORM telemetry._bump_bug(payload);
            WHEN 'reliability_snapshot' THEN
                PERFORM telemetry._bump_reliability(payload);
            ELSE
                RETURN jsonb_build_object('ok', false, 'error', 'unknown_event');
        END CASE;
    EXCEPTION WHEN OTHERS THEN
        -- Schema violation (bad enum value, missing required field,
        -- bucket value not in vocabulary, etc.). Don't disclose the
        -- detail to the wire — log it locally only via RAISE NOTICE
        -- (which goes to the Supabase server log, NOT to the response).
        RAISE NOTICE 'telemetry.contribute schema violation: %', SQLERRM;
        RETURN jsonb_build_object('ok', false, 'error', 'schema_violation');
    END;

    -- 3. Done. Function returns; payload exits scope; no row was
    --    written outside the rollup tables. The transaction commits
    --    when the connection finishes.
    RETURN jsonb_build_object('ok', true);
END;
$$;

-- Privilege model. Callable by `anon` (the Worker authenticates to
-- PostgREST with SUPABASE_ANON_KEY — see worker/wrangler.toml) and by
-- `service_role` (operator + CI direct-call path). No `PUBLIC` access.
--
-- Granting EXECUTE to anon does NOT widen the attack surface: the
-- function is SECURITY DEFINER, so it runs as postgres and writes
-- through to the rollup tables regardless of anon's own table grants;
-- the only thing anon's EXECUTE buys an attacker is the ability to
-- *attempt* a contribution. Every attempted contribution is gated by
-- telemetry.verify_versioned_hmac() against a per-version key derived
-- from TELEMETRY_MASTER_KEY at build time, so a successful write
-- requires possession of a build key (i.e., a real smirror.exe
-- binary). The anon key is public by design; HMAC is the gate.
--
-- This is deliberately *more* secure than granting service_role to
-- the Worker would be: service_role bypasses RLS on every table in
-- the project, which would mean a Worker compromise = full DB read.
REVOKE ALL ON FUNCTION telemetry.contribute(JSONB, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION telemetry.contribute(JSONB, TEXT, TEXT) TO anon;
GRANT EXECUTE ON FUNCTION telemetry.contribute(JSONB, TEXT, TEXT) TO service_role;

-- Internal helpers should not be callable from outside.
REVOKE ALL ON FUNCTION telemetry._bump_install(JSONB, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION telemetry._bump_bug(JSONB) FROM PUBLIC;
REVOKE ALL ON FUNCTION telemetry._bump_reliability(JSONB) FROM PUBLIC;


-- ============================================================================
-- RLS for the rollup tables
-- ============================================================================
--
-- Aggregate counters are not personal data, but limiting writes to the
-- service_role keeps the audit clean: only contribute()'s SECURITY
-- DEFINER context can write here, and contribute()'s privilege chain
-- starts at service_role.

ALTER TABLE telemetry.installation_daily_rollup ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_daily_rollup FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_daily_rollup ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_daily_rollup FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.reliability_daily_rollup ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.reliability_daily_rollup FORCE ROW LEVEL SECURITY;

-- service_role bypasses RLS by design in Supabase; admin (postgres)
-- role bypasses it for maintenance. No anon policies — the anon role
-- has NO access to these tables and no need to.


-- ============================================================================
-- Public read view: version_dist
-- ============================================================================
--
-- Materialized aggregate: active 30-day version distribution, derived
-- from installation_daily_rollup. Replaces v1's `installation` table
-- query that joined per-install rows.

CREATE OR REPLACE VIEW telemetry.version_dist AS
WITH events_30d AS (
    SELECT client_version, SUM(count) AS events
    FROM telemetry.installation_daily_rollup
    WHERE rollup_date >= CURRENT_DATE - INTERVAL '30 days'
    GROUP BY client_version
)
SELECT
    client_version,
    events                                                           AS events_30d,
    ROUND(100.0 * events / NULLIF(SUM(events) OVER (), 0), 1)         AS pct
FROM events_30d
ORDER BY events DESC, client_version DESC;

COMMENT ON VIEW telemetry.version_dist IS
'30-day version distribution measured by event volume (first_seen + upgrade) — NOT distinct install count. Apply k-anonymity floor of 5 in the digest before publishing.';


-- ============================================================================
-- Public read view: bug_unknown_share (drift detection)
-- ============================================================================
--
-- Drift fix (multi-role review, 2026-04-30): if a particular release
-- is misclassifying bug reports — picking 'unknown' from the closed
-- taxonomy when a real category exists, or hitting genuinely novel
-- failure modes that the taxonomy doesn't cover — the unknown share
-- per client_version is the leading indicator.
--
-- Under v2 the client picks bug_kind from a fixed taxonomy at submit
-- time. Each binary release ships with the taxonomy it was built
-- against, so client_version is effectively a proxy for taxonomy
-- version. A version with high unknown share signals either:
--   1. A failure mode that doesn't fit any known kind (legitimate;
--      schedule a taxonomy update for the next release).
--   2. A buggy classifier in that binary (revoke / advise upgrade).
--
-- Maintainer review trigger: any version with unknown_pct ≥ 5% and
-- total_reports ≥ 5 (k-anon floor) is worth investigating.

CREATE OR REPLACE VIEW telemetry.bug_unknown_share AS
WITH per_version AS (
    SELECT
        client_version,
        SUM(reports) FILTER (WHERE bug_kind = 'unknown') AS unknown_reports,
        SUM(reports)                                     AS total_reports
    FROM telemetry.bug_daily_rollup
    WHERE rollup_date >= CURRENT_DATE - INTERVAL '30 days'
    GROUP BY client_version
    HAVING SUM(reports) >= 5         -- k-anon floor
)
SELECT
    client_version,
    unknown_reports,
    total_reports,
    ROUND(100.0 * unknown_reports / NULLIF(total_reports, 0), 1) AS unknown_pct
FROM per_version
WHERE unknown_reports > 0
ORDER BY unknown_pct DESC, client_version DESC;

COMMENT ON VIEW telemetry.bug_unknown_share IS
'Bug-kind drift detection (multi-role review). Per-version unknown_pct over the last 30 days, restricted to versions with ≥ 5 reports (k-anon floor). Flag any row with unknown_pct ≥ 5% for taxonomy review.';


-- ============================================================================
-- Smoke test: synthetic contribution (manual; commented out)
-- ============================================================================
--
-- After deployment, run this from a transaction you ROLLBACK to verify
-- the function is reachable with a known-bad HMAC (should return
-- {"ok":false,"error":"rejected"}). A real signed payload requires the
-- master key in vault.

-- BEGIN;
--   SELECT telemetry.contribute(
--     '{
--        "event_kind": "first_seen",
--        "schema_version": 1,
--        "install_id": "sm-test-deadbeef",
--        "client_version": "0.9.19-dev",
--        "reported_at": "2026-04-29T10:00:00+00:00",
--        "install_method": "msi",
--        "os_family": "windows",
--        "mirror_count_bucket": "1",
--        "background_mode": "service",
--        "delete_policy": "delete",
--        "has_hooks": false,
--        "has_filters": true,
--        "has_alert_webhook": false,
--        "has_bandwidth_limit": false,
--        "rclone_version": "v1.73.5"
--      }'::jsonb,
--     '0.9.19-dev',
--     'deadbeef'  -- intentionally wrong; expect ok:false
--   );
-- ROLLBACK;

COMMIT;
