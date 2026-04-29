# Deploy runbook — telemetry architecture v2

**Audience**: Raveh (the maintainer), or anyone with Supabase service-role
credentials and Cloudflare account access.
**Estimated wall-clock time**: 30 minutes for Phase A + B; one week of
soak before Phase C; 90+ days before Phase D.
**Reversibility**: Phase A and B are non-destructive (additive). Phase C
changes client-visible routing but the v1 path remains live. Phase D is
the only destructive step and only happens after the retention window.

---

## Pre-flight checklist

Before starting, confirm you have:

- [ ] Supabase project access (service-role JWT or `psql` connection
      string) for `qkspigvkniiiwxggdvbr` (or the project you're
      deploying into).
- [ ] The `telemetry_master_key` is already in Supabase Vault from v1
      deployment (it's reused; see `docs/telemetry-rls.sql`).
- [ ] Cloudflare account access via `wrangler` (run `npx wrangler whoami`
      from `worker/`).
- [ ] `python3` with `psycopg[binary]` and `requests` installed for the
      smoke-test script.
- [ ] A backup of the current v1 schema (Supabase has automatic backups;
      take a manual snapshot anyway via Studio → Database → Backups).
- [ ] You're on a clean working tree of SelectiveMirror with the v2 SQL
      and Worker code already merged.

---

## Phase A — Apply the v2 schema (additive)

**Goal**: create v2 rollup tables + `telemetry.contribute()` RPC alongside
the existing v1 schema. v1 traffic continues to land in v1 tables. v2 is
unused by clients.

**Reversibility**: full. To roll back, drop the v2 objects:
```sql
DROP FUNCTION IF EXISTS telemetry.contribute(JSONB, TEXT, TEXT) CASCADE;
DROP FUNCTION IF EXISTS telemetry._bump_install(JSONB, TEXT) CASCADE;
DROP FUNCTION IF EXISTS telemetry._bump_bug(JSONB) CASCADE;
DROP FUNCTION IF EXISTS telemetry._bump_reliability(JSONB) CASCADE;
DROP TABLE IF EXISTS telemetry.installation_daily_rollup CASCADE;
DROP TABLE IF EXISTS telemetry.bug_daily_rollup CASCADE;
DROP TABLE IF EXISTS telemetry.reliability_daily_rollup CASCADE;
-- (v1 tables and the views built on them are untouched)
```

### A.1 Apply the SQL

Either via Supabase Studio's SQL editor (one shot, big paste), or via psql:

```bash
psql "${DATABASE_URL}" < docs/telemetry-v2.sql
```

Expected output: a series of `CREATE TYPE` / `CREATE TABLE` /
`CREATE FUNCTION` notices, ending with `COMMIT`. The DO blocks for the
ENUMs print "duplicate_object — skipping" if you re-run the file —
that's idempotent-by-design and harmless.

### A.2 Verify the schema

```sql
\dt telemetry.*
-- Expect: installation_daily_rollup, bug_daily_rollup,
-- reliability_daily_rollup, plus the existing v1 tables.

\df telemetry.contribute
-- Expect: telemetry.contribute(payload jsonb, claimed_version text,
-- claimed_hmac_hex text) RETURNS jsonb

SELECT proname, prosecdef
FROM pg_proc
WHERE proname IN ('contribute', '_bump_install', '_bump_bug', '_bump_reliability')
  AND pronamespace = 'telemetry'::regnamespace;
-- Expect: all four with prosecdef = true (SECURITY DEFINER)

SELECT has_function_privilege('anon', 'telemetry.contribute(jsonb, text, text)', 'EXECUTE');
-- Expect: false

SELECT has_function_privilege('service_role', 'telemetry.contribute(jsonb, text, text)', 'EXECUTE');
-- Expect: true
```

### A.3 Smoke-test (direct to Supabase)

```bash
export SUPABASE_URL="https://qkspigvkniiiwxggdvbr.supabase.co"
export SUPABASE_SERVICE_ROLE_KEY="<service-role JWT>"
export TELEMETRY_MASTER_KEY="<from Supabase Vault>"
export DATABASE_URL="postgresql://postgres.<ref>:<pwd>@aws-0-eu-west-1.pooler.supabase.com:6543/postgres"

python3 scripts/telemetry-v2-smoke-test.py --version 0.0.0-phaseA-smoke
```

Expected: all 5 cases pass (bad-hmac rejected, good-hmac accepted,
schema-violation rejected, unknown-event rejected, rollup-delta = 1).
The retired-forget case will SKIP because we're not via the Worker.

If any case fails:
- **HMAC verify failures**: confirm `TELEMETRY_MASTER_KEY` matches the
  vault entry and the canonical-JSON code path agrees with PG JSONB.
  Reference impl: `test/telemetry-validation.py::canonical_json`.
- **Schema-violation false positive**: an ENUM value the smoke test
  used isn't in the type. Update the test or the type.
- **Permission denied**: `service_role` GRANT didn't apply. Re-run the
  GRANT block from `telemetry-v2.sql`.

### A.4 Cleanup the smoke-test rows (optional)

The smoke-test inserts a row into `installation_daily_rollup` with
`rclone_version='v1.73.5-smoke'`. To remove it:

```sql
DELETE FROM telemetry.installation_daily_rollup
WHERE rclone_version = 'v1.73.5-smoke';
```

(Leaving it is harmless — it won't make it past the digest's k-anonymity
floor.)

---

## Phase B — Roll out the updated Worker

**Goal**: deploy the new `worker/src/index.ts` that exposes
`/v1/contribute` (in addition to keeping the v1 paths alive). Includes
SM-163's salted-IP-hash rate-limit-key fix.

**Reversibility**: full. `npx wrangler rollback` reverts to the previous
deployment.

### B.1 Set the salt secret (one-time)

```bash
cd worker
npx wrangler secret put RATE_LIMIT_SALT_SECRET
# Paste any 32+ random bytes when prompted, e.g.:
# python3 -c "import secrets; print(secrets.token_hex(32))"
```

If you skip this, the Worker still rate-limits but logs a warning and
falls back to raw-IP keys (SM-163 protection disabled). The deploy will
succeed either way.

### B.2 Deploy

```bash
cd worker
npm install                  # ensure wrangler is current
npx wrangler deploy
```

Expected output: a successful deploy line ending in
`https://smirror-telemetry.selectivemirror.workers.dev`.

### B.3 Smoke-test through the Worker

```bash
export WORKER_URL="https://smirror-telemetry.selectivemirror.workers.dev"
export TELEMETRY_MASTER_KEY="<from Supabase Vault>"

python3 scripts/telemetry-v2-smoke-test.py --via-worker --skip-rollup --version 0.0.0-phaseB-smoke
```

Expected: 5 cases pass — same as Phase A, but routed through the Worker.
The retired-forget case now ACTUALLY runs and confirms the Worker
returns 410 Gone for `/v1/forget`. Use `--skip-rollup` because the
Worker doesn't pass through DB credentials needed for the rollup query;
verify rollup-delta separately in psql:

```sql
SELECT * FROM telemetry.installation_daily_rollup
WHERE rclone_version = 'v1.73.5-smoke'
  AND client_version = '0.0.0-phaseB-smoke';
-- Expect: one row with count = 1 (or higher if you re-ran).
```

### B.4 Verify v1 paths still work

```bash
# A v1 bug-report shape (will be ingested into v1 ingest_envelope)
curl -X POST "${WORKER_URL}/v1/bug-reports" \
  -H "Content-Type: application/json" \
  -d '{}'
# Expect: 4xx (RLS rejects bare empty body), but the path is reachable.
# A 200/201 means it landed; a 404 means we broke a v1 route.
```

If any v1 path returns 404, the Worker's `ALLOWED_PATHS` set is wrong —
revert and investigate.

---

## Phase C — Cut clients over to v2 (deferred)

**Goal**: update the Go client to call `/v1/contribute` instead of the
v1 paths. After this lands and ships in a release, v1 paths receive no
new traffic.

**Reversibility**: a release-version-pin reverts (clients that already
upgraded will use v2 — it's a one-way door for those installs unless
they downgrade).

This phase is NOT part of this runbook because the Go client work
(SM-157 + SM-158) is its own focused session per
`docs/SM-157-telemetry-cli-plan.md` and
`docs/SM-158-report-bug-submit-plan.md`. Sequence under v2:

1. Land the runtime CLI (`smirror telemetry [none|standard|reliability|status|policy]`).
2. Land the submit pipeline (`report-bug --submit / --one-shot / --browser`).
3. Update the contribution payload builder to include `event_kind`
   and the v2 bucket dimensions.
4. Switch the client's POST target from `/v1/bug-reports` and
   `/v1/installations/report` to `/v1/contribute`.
5. Cut a release.
6. Wait one week for telemetry to confirm v2 traffic is healthy and
   v1 traffic has trailed off.

### Pre-cutover dual-write window (optional)

If you want belt-and-suspenders verification, the Worker can be modified
during a short window to dual-write — forward to BOTH the v1 ingest path
AND `telemetry.contribute()`. Compare counts daily for a week. Note that
the v1 normalize path doesn't carry `bug_kind`/`bug_surface` (those are
asynchronous classifications), so dual-write only works cleanly for
installation events. For SM volume, skipping the dual-write window is
acceptable — the smoke tests in Phase A and B are the load-bearing
checks.

---

## Phase D — Drop v1 (deferred 90+ days)

**Goal**: drop the v1 individual-event tables once the retention janitor
has aged out the last v1 row.

**Reversibility**: NONE for the data; the schema can be re-created from
git history, but the rows are gone.

**Pre-conditions**:
- Phase C has been live for 90+ days.
- `purge_old_envelopes` has run and emptied all v1 raw payloads.
- A manual sample query confirms v1 `bug_report.report_text` is empty
  for all rows older than the retention window.

**SQL**:

```sql
BEGIN;

-- Drop v1 tables (CASCADE handles dependent views and FKs)
DROP TABLE IF EXISTS telemetry.bug_report_taxonomy_assignment CASCADE;
DROP TABLE IF EXISTS telemetry.bug_report                     CASCADE;
DROP TABLE IF EXISTS telemetry.installation_event             CASCADE;
DROP TABLE IF EXISTS telemetry.installation                   CASCADE;
DROP TABLE IF EXISTS telemetry.installation_reliability_snapshot CASCADE;
DROP TABLE IF EXISTS telemetry.ingest_envelope                CASCADE;

-- Drop v1 functions (no longer relevant)
DROP FUNCTION IF EXISTS telemetry.purge_old_envelopes(INTEGER) CASCADE;
DROP FUNCTION IF EXISTS telemetry.refresh_install_daily_rollup(DATE) CASCADE;
DROP FUNCTION IF EXISTS telemetry.refresh_bug_daily_rollup(DATE) CASCADE;

-- Drop v1 views that referenced individual-event tables
DROP VIEW IF EXISTS telemetry.bug_report_human         CASCADE;
DROP VIEW IF EXISTS telemetry.bug_report_clusters      CASCADE;
DROP VIEW IF EXISTS telemetry.install_summary          CASCADE;
DROP VIEW IF EXISTS telemetry.weekly_health            CASCADE;
DROP VIEW IF EXISTS telemetry.tier_distribution        CASCADE;
DROP VIEW IF EXISTS telemetry.reliability_snapshot_human CASCADE;
DROP VIEW IF EXISTS telemetry.install_config_distribution CASCADE;

-- Drop v1 cron jobs (the two telemetry-* schedules)
SELECT cron.unschedule('telemetry-bug-rollup-daily');
SELECT cron.unschedule('telemetry-install-rollup-daily');
SELECT cron.unschedule('telemetry-purge-old-envelopes');

COMMIT;
```

After Phase D:
- `\dt telemetry.*` returns only the three v2 rollup tables and
  `taxonomy_term`.
- The schema dump IS the privacy story.
- Update the v1 SQL files in `docs/` with a "Phase D complete; this
  file describes a dropped historical schema" header note.

---

## Operational acceptance criteria

The deploy is "done" when:

- [ ] `telemetry.contribute()` is callable from the Worker and from
      `service_role` in psql.
- [ ] All 5 smoke-test cases pass via the Worker route.
- [ ] `\df telemetry.contribute` returns the function with
      `prosecdef = true`.
- [ ] `has_function_privilege('anon', 'telemetry.contribute(...)', 'EXECUTE')`
      is `false`.
- [ ] The Worker's `/v1/forget` returns 410 Gone with
      `code=endpoint_retired`.
- [ ] v1 paths (`/v1/bug-reports`, `/v1/installations/report`) still
      reach Supabase ingest_envelope.
- [ ] A manual `SELECT count(*) FROM telemetry.installation_daily_rollup`
      shows the smoke-test contributions.
- [ ] The CHANGELOG entry for this deploy says "Telemetry v2 deployed
      (Phase A + B); client cutover deferred to a future release."

---

## Operational notes

### What if the smoke test fails on `case_good_hmac`?

This is almost always a canonical-JSON drift. PG JSONB sorts keys by
length-first, then codepoint. Python's `json.dumps(sort_keys=True)`
sorts alphabetically, which differs whenever the keys have different
lengths. The smoke test uses `canonical_json` which mirrors PG.

To debug:
1. Take the payload the smoke test built (excluding `version_hmac` and
   `event_kind`).
2. Insert it into a temp table as `JSONB`, then `SELECT payload::TEXT`.
3. Compare byte-for-byte with the smoke test's `canonical_json` output.
4. Any difference is the bug.

Reference: `~/.claude/projects/C--SelectiveMirror/memory/reference_jsonb_canonicalization.md`.

### What if the Worker can't read `RATE_LIMIT_SALT_SECRET`?

Two states:
- Secret not set → console.warn in worker logs, falls back to raw-IP
  keys (SM-163 protection disabled). Set the secret and redeploy.
- Secret set but Worker is using cached metadata → `npx wrangler deploy`
  again to refresh.

### What if Supabase `pg_stat_statements` shows the contribute call?

It should NEVER show payload literals — PostgREST RPC binds parameters,
which `pg_stat_statements` normalizes to `$1`, `$2`. If you see actual
JSONB values in the normalized statement text, something has overridden
the normalization (rare; check `pg_stat_statements.track`). Stop
serving traffic and investigate before resuming.

### Rotation of `RATE_LIMIT_SALT_SECRET`

Quarterly is fine. Rotation invalidates currently-counting rate-limit
windows (max 60 seconds of disruption — KV TTL). Procedure:

```bash
cd worker
npx wrangler secret put RATE_LIMIT_SALT_SECRET
# paste new 32-byte random
# (no redeploy needed; secrets propagate to running Worker instances
# within seconds)
```
