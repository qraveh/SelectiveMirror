-- SelectiveMirror telemetry: Row-Level Security + CHECK constraints
--
-- Purpose: Layer 1 defenses for the public ingest surface. Apply AFTER
-- loading telemetry-microserver.sql (which creates the tables).
--
-- Threat model: SM ships binaries containing the public anon API key. That
-- key is NOT secret — anyone can extract it. The real gate is Postgres
-- RLS and a server-side HMAC verification function. The HMAC key (master)
-- lives in Supabase Vault; only the SECURITY DEFINER function below has
-- access to it.
--
-- Order of application:
--   1. Load telemetry-microserver.sql first (creates tables and seeds)
--   2. In Project Settings → API → Exposed schemas, add `telemetry`
--   3. In Vault, create secret `telemetry_master_key` (hex, 64 chars) —
--      generate with `openssl rand -hex 32`. NEVER commit it. NEVER paste
--      it into chat. Store ONLY in your password manager and in Supabase
--      Vault.
--   4. Run this file
--
-- A development-mode shortcut policy is provided at the bottom (commented
-- out) for testing ingest before HMAC is set up. Remove before exposing
-- the endpoint to real clients.

-- ============================================================================
-- HMAC verification function
-- ============================================================================
--
-- Computes HMAC-SHA256(master_key, claimed_version) → derived_key, then
-- HMAC-SHA256(derived_key, payload_canonical) → expected_hmac, then compares
-- to the claimed hmac. Returns TRUE on match, FALSE otherwise.
--
-- Master key access goes through Supabase Vault. The function is
-- SECURITY DEFINER so anon-role callers (in the RLS policy) can invoke it
-- without having direct read on vault.decrypted_secrets.
--
-- IMPORTANT: requires the secret 'telemetry_master_key' to exist in
-- Supabase Vault before this function works. If the secret is missing,
-- the function raises an exception and INSERT fails closed (which is the
-- correct posture).

CREATE OR REPLACE FUNCTION telemetry.verify_versioned_hmac(
    canonical_payload   BYTEA,
    claimed_version     TEXT,
    claimed_hmac_hex    TEXT
)
RETURNS BOOLEAN
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
DECLARE
    master_key_bytes BYTEA;
    derived_key      BYTEA;
    expected_hmac    TEXT;
BEGIN
    -- Read the master key from Supabase Vault. Stored as a hex string.
    SELECT decode(decrypted_secret, 'hex') INTO master_key_bytes
    FROM vault.decrypted_secrets
    WHERE name = 'telemetry_master_key';

    IF master_key_bytes IS NULL THEN
        RAISE EXCEPTION
          'telemetry_master_key not found in Supabase Vault. Set it via the dashboard before enabling HMAC-protected ingest.';
    END IF;

    -- Derive the per-version key
    derived_key := extensions.hmac(claimed_version::bytea, master_key_bytes, 'sha256');

    -- Compute expected HMAC over the canonical payload bytes
    expected_hmac := encode(
        extensions.hmac(canonical_payload, derived_key, 'sha256'),
        'hex'
    );

    -- Constant-time-ish comparison via PostgreSQL string equality.
    -- Postgres equality on TEXT is not strictly constant-time, but for
    -- defense against opportunistic abuse this is acceptable; a true
    -- timing-attack adversary is outside our threat model.
    RETURN expected_hmac = claimed_hmac_hex;
END;
$$;

REVOKE ALL ON FUNCTION telemetry.verify_versioned_hmac(BYTEA, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION telemetry.verify_versioned_hmac(BYTEA, TEXT, TEXT) TO anon, authenticated, service_role;

-- ============================================================================
-- Privilege baseline: anon gets nothing by default
-- ============================================================================

REVOKE ALL ON ALL TABLES    IN SCHEMA telemetry FROM anon, authenticated;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA telemetry FROM anon, authenticated;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA telemetry FROM anon, authenticated;

-- The bulk REVOKE above is broad and also stripped the EXECUTE grants we
-- set earlier on telemetry.verify_versioned_hmac. Re-grant explicitly so
-- the RLS policy on ingest_envelope can call the verifier as the anon
-- role. Without this re-grant, anon clients hit "permission denied for
-- function verify_versioned_hmac" (SQLSTATE 42501) on every INSERT.
GRANT EXECUTE ON FUNCTION telemetry.verify_versioned_hmac(BYTEA, TEXT, TEXT) TO anon, authenticated;

-- Anon needs USAGE on the schema itself to reach the tables it can write to.
GRANT USAGE ON SCHEMA telemetry TO anon, authenticated, service_role;

-- service_role retains full access via RLS bypass (Supabase default).
-- We do not need explicit GRANTs for it on telemetry.* tables.

-- ============================================================================
-- ingest_envelope: anon may INSERT (only), with strict CHECK constraints
-- ============================================================================

GRANT INSERT ON telemetry.ingest_envelope TO anon;

ALTER TABLE telemetry.ingest_envelope ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.ingest_envelope FORCE ROW LEVEL SECURITY;

-- Drop any pre-existing policy with our name (idempotent re-run)
DROP POLICY IF EXISTS anon_insert_with_hmac ON telemetry.ingest_envelope;

CREATE POLICY anon_insert_with_hmac
    ON telemetry.ingest_envelope
    FOR INSERT
    TO anon
    WITH CHECK (
        -- Allowed kinds (matches the ENUM)
        ingest_kind IN ('bug_report', 'installation_event')

        -- Reasonable schema version range (clients must send a number)
        AND schema_version BETWEEN 1 AND 100

        -- Payload size cap. 100 KB ≈ 1024 typical bug reports per 1 MB of
        -- DB; with the 500 MB free-tier ceiling that gives ~5,000 reports
        -- before crowding the database.
        AND octet_length(payload::text) < 100000

        -- client_version must match a coarse semver shape. Keeps obvious
        -- bots out without being too strict.
        AND client_version IS NOT NULL
        AND client_version ~ '^[0-9]+\.[0-9]+\.[0-9]+'

        -- dedupe_key must be present and have plausible length
        AND dedupe_key IS NOT NULL
        AND length(dedupe_key) BETWEEN 16 AND 200

        -- payload_sha256 must look like a hex SHA-256
        AND payload_sha256 ~ '^[0-9a-f]{64}$'

        -- HMAC verification: payload must contain version_hmac, and the
        -- HMAC must validate against the master key + claimed version.
        AND payload ? 'version_hmac'
        AND telemetry.verify_versioned_hmac(
            -- Canonical: JSON minus the version_hmac field, serialized
            -- to text. Client must use the same canonicalization (sort
            -- keys, no whitespace) when computing HMAC.
            (payload - 'version_hmac')::text::bytea,
            client_version,
            payload->>'version_hmac'
        )
    );

-- ============================================================================
-- All other telemetry.* tables: deny anon entirely
-- ============================================================================
--
-- Enabling RLS without any policy means: nobody but service_role can
-- access. service_role bypasses RLS automatically.

ALTER TABLE telemetry.bug_report                       ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_report_signal                ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_report_taxonomy_assignment   ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation                     ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_event               ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_taxonomy_assignment ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.taxonomy_term                    ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.classification_job               ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_daily_rollup                 ENABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_daily_rollup        ENABLE ROW LEVEL SECURITY;

ALTER TABLE telemetry.bug_report                       FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_report_signal                FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_report_taxonomy_assignment   FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation                     FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_event               FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_taxonomy_assignment FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.taxonomy_term                    FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.classification_job               FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.bug_daily_rollup                 FORCE ROW LEVEL SECURITY;
ALTER TABLE telemetry.installation_daily_rollup        FORCE ROW LEVEL SECURITY;

-- ============================================================================
-- Sequences used by SERIAL/BIGSERIAL columns
-- ============================================================================
--
-- These are internal; anon must not have any sequence access. service_role
-- already has it. Explicit revoke for clarity.

REVOKE ALL ON ALL SEQUENCES IN SCHEMA telemetry FROM anon, authenticated;

-- ============================================================================
-- DEVELOPMENT-MODE SHORTCUT (commented out — uncomment ONLY for testing)
-- ============================================================================
--
-- Use this if you want to test ingest before configuring the master HMAC
-- key in Vault. It allows anon INSERT without HMAC verification but keeps
-- the size/shape checks. UNCOMMENT FOR TESTING, RE-COMMENT BEFORE
-- EXPOSING THE ENDPOINT TO REAL CLIENTS.
--
-- DROP POLICY IF EXISTS anon_insert_with_hmac ON telemetry.ingest_envelope;
-- DROP POLICY IF EXISTS anon_insert_dev_only ON telemetry.ingest_envelope;
-- CREATE POLICY anon_insert_dev_only
--     ON telemetry.ingest_envelope
--     FOR INSERT
--     TO anon
--     WITH CHECK (
--         ingest_kind IN ('bug_report', 'installation_event')
--         AND schema_version BETWEEN 1 AND 100
--         AND octet_length(payload::text) < 100000
--         AND client_version IS NOT NULL
--         AND client_version ~ '^[0-9]+\.[0-9]+\.[0-9]+'
--         AND dedupe_key IS NOT NULL
--         AND length(dedupe_key) BETWEEN 16 AND 200
--         AND payload_sha256 ~ '^[0-9a-f]{64}$'
--     );
