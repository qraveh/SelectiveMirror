-- SelectiveMirror telemetry microserver schema
-- Purpose: bug-report ingest (always per-event approval) and minimal opt-in
-- installation telemetry (first_seen + upgrade only), with asynchronous
-- taxonomy assignment, on PostgreSQL.
--
-- Scope and consent model:
--   * Bug reports: user runs `smirror report-bug` and explicitly approves
--     each submission. There is no global "send bug reports automatically"
--     setting. Per-event approval is mandatory.
--   * Installation telemetry: opt-in via the MSI checkbox at install time
--     (default unchecked) or via the runtime command `smirror telemetry on`.
--     If enabled, smirror sends a `first_seen` event on first run and an
--     `upgrade` event on each version change. NO heartbeat. NO continuous
--     phoning home. The user can revoke at any time via `smirror telemetry
--     off` and clear local consent state.
--   * Crashes: recorded locally only. May be embedded in a user-approved
--     bug report via `report-bug --include-crashes`.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE SCHEMA IF NOT EXISTS telemetry;

-- ENUMs.
--
-- ingest_kind: what kind of payload is in the envelope. Two valid values
-- because two distinct ingest flows exist:
--   - bug_report: from `smirror report-bug --submit` (always per-event consent)
--   - installation_event: from opted-in clients on first_seen / upgrade only
CREATE TYPE telemetry.ingest_kind AS ENUM (
    'bug_report',
    'installation_event'
);

CREATE TYPE telemetry.taxonomy_target AS ENUM (
    'bug_report',
    'installation_event'
);

CREATE TYPE telemetry.classification_state AS ENUM (
    'pending',
    'classified',
    'needs_review',
    'failed'
);

-- 'report_bug' is the normal source: user ran `smirror report-bug` and
-- approved the submission. 'manual' covers reports created or edited by an
-- admin during triage. Crashes arrive embedded inside a 'report_bug'
-- submission (in anomaly_summary), not as a separate source.
CREATE TYPE telemetry.bug_source AS ENUM (
    'report_bug',
    'manual'
);

CREATE TYPE telemetry.report_format AS ENUM (
    'text_bundle',
    'json_bundle'
);

-- Immutable accepted payloads for both ingest kinds. Request handlers
-- must write here before any asynchronous classification or rollup work.
CREATE TABLE IF NOT EXISTS telemetry.ingest_envelope (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ingest_kind         telemetry.ingest_kind NOT NULL,
    schema_version      INTEGER NOT NULL,
    install_id          TEXT,
    client_version      TEXT,
    received_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    dedupe_key          TEXT NOT NULL,
    payload_sha256      TEXT NOT NULL,
    payload             JSONB NOT NULL,
    classification_state telemetry.classification_state NOT NULL DEFAULT 'pending',
    classify_after      TIMESTAMPTZ NOT NULL DEFAULT now(),
    classified_at       TIMESTAMPTZ,
    classification_error TEXT,
    CONSTRAINT ingest_envelope_dedupe_unique UNIQUE (ingest_kind, dedupe_key)
);

CREATE INDEX IF NOT EXISTS ingest_envelope_received_at_idx
    ON telemetry.ingest_envelope (received_at DESC);

CREATE INDEX IF NOT EXISTS ingest_envelope_install_id_idx
    ON telemetry.ingest_envelope (install_id);

CREATE INDEX IF NOT EXISTS ingest_envelope_classification_idx
    ON telemetry.ingest_envelope (classification_state, classify_after);

-- ============================================================================
-- Bug-report side (always per-event user approval)
-- ============================================================================

-- Normalized bug reports.
--
-- install_id is a client-side anonymous identifier (random UUID stored in
-- client state, not tied to user identity, resets on uninstall). It is NOT
-- a foreign key to telemetry.installation because bug reports may arrive
-- from clients that NEVER opted in to install-telemetry (and thus have no
-- installation row). install_id is purely a correlation hint.
CREATE TABLE IF NOT EXISTS telemetry.bug_report (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    envelope_id             UUID NOT NULL UNIQUE REFERENCES telemetry.ingest_envelope(id) ON DELETE CASCADE,
    install_id              TEXT,
    source                  telemetry.bug_source NOT NULL,
    report_format           telemetry.report_format NOT NULL DEFAULT 'text_bundle',
    reported_at             TIMESTAMPTZ NOT NULL,
    client_version          TEXT,
    title                   TEXT,
    report_text             TEXT NOT NULL,
    signature               TEXT,
    signature_version       INTEGER NOT NULL DEFAULT 1,
    component_hint          TEXT,
    severity_hint           TEXT,
    reproduction_hint       TEXT,
    -- Set by the client when `smirror report-bug --browser` is used: client
    -- submitted to telemetry AND additionally launched a browser to file a
    -- prefilled GitHub issue. NULL means telemetry-only submission. The
    -- value is the client-side time of browser launch; a corresponding
    -- GitHub issue *may* exist (we cannot observe whether the user
    -- actually clicked Submit on the GitHub form), correlatable via the
    -- install_id we prefill into the issue body.
    browser_escalated_at    TIMESTAMPTZ,
    taxonomy_state          telemetry.classification_state NOT NULL DEFAULT 'pending',
    classified_at           TIMESTAMPTZ,
    classification_error    TEXT,
    duplicate_of            UUID REFERENCES telemetry.bug_report(id),
    anomaly_summary         JSONB NOT NULL DEFAULT '{}'::jsonb,
    status_snapshot         JSONB NOT NULL DEFAULT '{}'::jsonb,
    parsed_fields           JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS bug_report_reported_at_idx
    ON telemetry.bug_report (reported_at DESC);

CREATE INDEX IF NOT EXISTS bug_report_signature_idx
    ON telemetry.bug_report (signature);

CREATE INDEX IF NOT EXISTS bug_report_taxonomy_idx
    ON telemetry.bug_report (taxonomy_state, reported_at DESC);

CREATE TABLE IF NOT EXISTS telemetry.bug_report_signal (
    bug_report_id         UUID NOT NULL REFERENCES telemetry.bug_report(id) ON DELETE CASCADE,
    signal_kind           TEXT NOT NULL,
    signal_value          TEXT NOT NULL,
    signal_count          INTEGER NOT NULL DEFAULT 1,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (bug_report_id, signal_kind, signal_value)
);

CREATE TABLE IF NOT EXISTS telemetry.bug_report_taxonomy_assignment (
    bug_report_id         UUID NOT NULL REFERENCES telemetry.bug_report(id) ON DELETE CASCADE,
    term_id               BIGINT NOT NULL,  -- FK declared after taxonomy_term is created
    assigned_by           TEXT NOT NULL,
    rule_name             TEXT,
    confidence            NUMERIC(4,3) NOT NULL DEFAULT 1.000,
    assigned_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (bug_report_id, term_id)
);

-- ============================================================================
-- Installation-telemetry side (opt-in: MSI checkbox or `smirror telemetry on`)
-- ============================================================================

-- Per-install state, derived from the events table. One row per distinct
-- install_id that has ever sent telemetry. Only populated for installs
-- that opted in to install-telemetry; opted-out installs never create a
-- row here.
--
-- "Heartbeat" fields are intentionally absent: scope (b) collects only
-- first_seen and upgrade events. last_seen_at therefore tracks the most
-- recent of those, not a periodic heartbeat.
CREATE TABLE IF NOT EXISTS telemetry.installation (
    install_id              TEXT PRIMARY KEY,
    first_seen_at           TIMESTAMPTZ NOT NULL,
    last_seen_at            TIMESTAMPTZ NOT NULL,
    first_version           TEXT,
    current_version         TEXT,
    current_install_method  TEXT,
    current_os_family       TEXT,
    current_os_detail       TEXT,
    current_arch            TEXT,
    first_event_id          UUID REFERENCES telemetry.ingest_envelope(id),
    last_event_id           UUID REFERENCES telemetry.ingest_envelope(id)
);

-- Individual telemetry events from opted-in installs.
-- event_name is constrained to lifecycle terms by classifier rules, but
-- left as TEXT for forward compatibility (e.g., later adding 'reactivate'
-- or other terms without a schema migration).
--
-- No heartbeat-specific fields (no sync_workers, files_synced, sync_errors,
-- bytes_uploaded, uptime_seconds): scope (b) is structural events only.
CREATE TABLE IF NOT EXISTS telemetry.installation_event (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    envelope_id             UUID NOT NULL UNIQUE REFERENCES telemetry.ingest_envelope(id) ON DELETE CASCADE,
    install_id              TEXT NOT NULL REFERENCES telemetry.installation(install_id) ON DELETE CASCADE,
    event_name              TEXT NOT NULL,
    reported_at             TIMESTAMPTZ NOT NULL,
    client_version          TEXT,
    os_family               TEXT,
    os_detail               TEXT,
    arch                    TEXT,
    install_method          TEXT,
    backend_types           TEXT[] NOT NULL DEFAULT '{}',
    taxonomy_state          telemetry.classification_state NOT NULL DEFAULT 'pending',
    classified_at           TIMESTAMPTZ,
    classification_error    TEXT
);

CREATE INDEX IF NOT EXISTS installation_event_install_id_idx
    ON telemetry.installation_event (install_id, reported_at DESC);

CREATE INDEX IF NOT EXISTS installation_event_taxonomy_idx
    ON telemetry.installation_event (taxonomy_state, reported_at DESC);

CREATE TABLE IF NOT EXISTS telemetry.installation_taxonomy_assignment (
    installation_event_id UUID NOT NULL REFERENCES telemetry.installation_event(id) ON DELETE CASCADE,
    term_id               BIGINT NOT NULL,  -- FK declared after taxonomy_term
    assigned_by           TEXT NOT NULL,
    rule_name             TEXT,
    confidence            NUMERIC(4,3) NOT NULL DEFAULT 1.000,
    assigned_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (installation_event_id, term_id)
);

-- Daily rollup for install events. No `heartbeats` column (heartbeats
-- aren't collected). `active_installs` counts distinct install_ids that
-- emitted any event in the last 30 days — a weaker proxy for "active"
-- than heartbeat-based counting, but consistent with scope (b).
CREATE TABLE IF NOT EXISTS telemetry.installation_daily_rollup (
    rollup_date           DATE NOT NULL,
    install_method        TEXT NOT NULL,
    os_family             TEXT NOT NULL,
    client_version        TEXT NOT NULL,
    first_seen_count      BIGINT NOT NULL DEFAULT 0,
    upgrade_count         BIGINT NOT NULL DEFAULT 0,
    active_installs_30d   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (rollup_date, install_method, os_family, client_version)
);

-- ============================================================================
-- Shared taxonomy and workflow
-- ============================================================================

CREATE TABLE IF NOT EXISTS telemetry.taxonomy_term (
    id                    BIGSERIAL PRIMARY KEY,
    target                telemetry.taxonomy_target NOT NULL,
    namespace             TEXT NOT NULL,
    slug                  TEXT NOT NULL,
    display_name          TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    parent_term_id        BIGINT REFERENCES telemetry.taxonomy_term(id),
    active                BOOLEAN NOT NULL DEFAULT true,
    ordinal               INTEGER NOT NULL DEFAULT 0,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT taxonomy_term_unique UNIQUE (target, namespace, slug)
);

CREATE INDEX IF NOT EXISTS taxonomy_term_lookup_idx
    ON telemetry.taxonomy_term (target, namespace, active, ordinal, slug);

-- Now that taxonomy_term exists, attach the deferred foreign keys from
-- the assignment tables.
ALTER TABLE telemetry.bug_report_taxonomy_assignment
    ADD CONSTRAINT bug_report_taxonomy_assignment_term_fk
    FOREIGN KEY (term_id) REFERENCES telemetry.taxonomy_term(id);

ALTER TABLE telemetry.installation_taxonomy_assignment
    ADD CONSTRAINT installation_taxonomy_assignment_term_fk
    FOREIGN KEY (term_id) REFERENCES telemetry.taxonomy_term(id);

CREATE TABLE IF NOT EXISTS telemetry.classification_job (
    id                    BIGSERIAL PRIMARY KEY,
    target                telemetry.taxonomy_target NOT NULL,
    target_id             UUID NOT NULL,
    state                 telemetry.classification_state NOT NULL DEFAULT 'pending',
    attempts              INTEGER NOT NULL DEFAULT 0,
    available_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at             TIMESTAMPTZ,
    locked_by             TEXT,
    last_error            TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT classification_job_unique UNIQUE (target, target_id)
);

CREATE INDEX IF NOT EXISTS classification_job_poll_idx
    ON telemetry.classification_job (state, available_at, id);

CREATE TABLE IF NOT EXISTS telemetry.bug_daily_rollup (
    rollup_date           DATE NOT NULL,
    bug_kind              TEXT NOT NULL,
    bug_surface           TEXT NOT NULL,
    client_version        TEXT NOT NULL,
    reports               BIGINT NOT NULL DEFAULT 0,
    unique_signatures     BIGINT NOT NULL DEFAULT 0,
    unclassified_reports  BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (rollup_date, bug_kind, bug_surface, client_version)
);

-- ============================================================================
-- Seeded taxonomy terms
-- ============================================================================

INSERT INTO telemetry.taxonomy_term (target, namespace, slug, display_name, description, ordinal)
VALUES
    -- Bug-report taxonomy: kind
    ('bug_report', 'bug.kind', 'panic', 'Panic', 'Unhandled panic or crash.', 10),
    ('bug_report', 'bug.kind', 'sync_failure', 'Sync Failure', 'Sync operation failed.', 20),
    ('bug_report', 'bug.kind', 'sync_timeout', 'Sync Timeout', 'Sync exceeded timeout.', 30),
    ('bug_report', 'bug.kind', 'watcher', 'Watcher', 'Filesystem watcher issue.', 40),
    ('bug_report', 'bug.kind', 'reconciliation', 'Reconciliation', 'Reconciliation issue.', 50),
    ('bug_report', 'bug.kind', 'ghost', 'Ghost', 'Ghost classification or cleanup issue.', 60),
    ('bug_report', 'bug.kind', 'config', 'Config', 'Configuration or compatibility issue.', 70),
    ('bug_report', 'bug.kind', 'service', 'Service', 'Windows service issue.', 80),
    ('bug_report', 'bug.kind', 'selfupdate', 'Selfupdate', 'Updater issue.', 90),
    ('bug_report', 'bug.kind', 'security', 'Security', 'Security or sanitization issue.', 100),
    ('bug_report', 'bug.kind', 'unknown', 'Unknown', 'Could not classify the report.', 110),

    -- Bug-report taxonomy: anomaly_kind (mirrors local anomaly classification)
    ('bug_report', 'bug.anomaly_kind', 'panic', 'Panic', 'Derived from anomaly kind Panic.', 10),
    ('bug_report', 'bug.anomaly_kind', 'circuit_breaker', 'Circuit Breaker', 'Derived from anomaly kind CircuitBreaker.', 20),
    ('bug_report', 'bug.anomaly_kind', 'watcher_error', 'Watcher Error', 'Derived from anomaly kind Watcher:Error.', 30),
    ('bug_report', 'bug.anomaly_kind', 'queue_depth_warning', 'Queue Depth Warning', 'Derived from anomaly kind Queue:DepthWarning.', 40),
    ('bug_report', 'bug.anomaly_kind', 'ghost_leak', 'Ghost Leak', 'Derived from anomaly kind Ghost:Leak.', 50),
    ('bug_report', 'bug.anomaly_kind', 'ghost_orphan', 'Ghost Orphan', 'Derived from anomaly kind Ghost:Orphan.', 60),
    ('bug_report', 'bug.anomaly_kind', 'ghost_stale', 'Ghost Stale', 'Derived from anomaly kind Ghost:Stale.', 70),
    ('bug_report', 'bug.anomaly_kind', 'reconcile_stale', 'Reconcile Stale', 'Derived from anomaly kind Reconciliation:Stale.', 80),
    ('bug_report', 'bug.anomaly_kind', 'path_gone', 'Path Gone', 'Derived from anomaly kind Path:Gone.', 90),
    ('bug_report', 'bug.anomaly_kind', 'sync_timeout', 'Sync Timeout', 'Derived from anomaly kind Sync:Timeout.', 100),
    ('bug_report', 'bug.anomaly_kind', 'sync_failure', 'Sync Failure', 'Derived from anomaly kind Sync:Failure.', 110),

    -- Installation taxonomy: lifecycle (scope b — first_seen + upgrade only)
    ('installation_event', 'install.lifecycle', 'first_seen', 'First Seen', 'First accepted event for an install_id.', 10),
    ('installation_event', 'install.lifecycle', 'upgrade', 'Upgrade', 'Version change observed for an install.', 20),

    -- Installation taxonomy: install channel
    ('installation_event', 'install.channel', 'msi', 'MSI', 'Installed from MSI.', 10),
    ('installation_event', 'install.channel', 'winget', 'WinGet', 'Installed through WinGet.', 20),
    ('installation_event', 'install.channel', 'zip', 'ZIP', 'Portable ZIP install.', 30),
    ('installation_event', 'install.channel', 'manual', 'Manual', 'Built or placed manually.', 40),
    ('installation_event', 'install.channel', 'selfupdate', 'Self Update', 'Installed by in-app selfupdate flow.', 50),
    ('installation_event', 'install.channel', 'unknown', 'Unknown', 'Install channel could not be derived.', 60)
ON CONFLICT (target, namespace, slug) DO NOTHING;

-- ============================================================================
-- Table comments
-- ============================================================================

COMMENT ON TABLE telemetry.ingest_envelope IS
'Immutable accepted payloads (bug reports and opt-in installation events). Request handlers must write here before any asynchronous classification or rollup work.';

COMMENT ON TABLE telemetry.bug_report IS
'Normalized bug reports composed by `smirror report-bug` with explicit per-event user approval. Embedded crash and anomaly data may be present in anomaly_summary.';

COMMENT ON TABLE telemetry.installation IS
'Per-install state for clients that opted in to installation telemetry. install_id is anonymous (random UUID, no machine identity). Opted-out clients never appear here.';

COMMENT ON TABLE telemetry.installation_event IS
'Lifecycle events from opted-in installs: first_seen (one per install) and upgrade (on each version change). No heartbeat events are collected.';

COMMENT ON TABLE telemetry.classification_job IS
'Background work queue. Taxonomy assignment must happen here, not on the request path.';
