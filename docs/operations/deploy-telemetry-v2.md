# Deploy runbook — telemetry architecture v2

**Audience**: Raveh (the maintainer), or anyone with Supabase service-role
credentials and Cloudflare account access.
**Estimated wall-clock time**: 30 minutes.
**Reversibility**: drop-v1-leftover.sql is destructive (it removes the
v1 schema rows that may still exist on Supabase). Take a manual snapshot
first via Supabase Studio → Database → Backups. v2 schema is additive
and reversible.

---

## What this deploys

After this runbook executes, the Supabase project hosts:

- Three v2 rollup tables: `installation_daily_rollup`, `bug_daily_rollup`,
  `reliability_daily_rollup`.
- The `telemetry.contribute()` RPC + three internal `_bump` helpers.
- The `verify_versioned_hmac` shared HMAC verifier.
- The `version_dist` view.

The v1 schema (raw `ingest_envelope`, normalized `bug_report` /
`installation_event`, nightly rollup functions, retention janitor,
pg_cron jobs) is **dropped**. v1 was a leftover — never wired to a
live client after SM-160 (0.9.18-dev) deleted the client-side
`SendReport`. There is no migration window, no soak, no cutover.

The Cloudflare Worker exposes only `/v1/contribute`. Legacy paths
(`/v1/bug-reports`, `/v1/installations/report`, `/v1/forget`) return
410 Gone.

---

## Pre-flight checklist

Before starting, confirm you have:

- [ ] Supabase project access (service-role JWT or `psql` connection
      string) for the project you're deploying into. The production
      project ref is recorded in `worker/wrangler.toml` under
      `SUPABASE_PROJECT_REF`; substitute your own when forking.
- [ ] The `telemetry_master_key` is in Supabase Vault. Verify with:
      `SELECT name FROM vault.decrypted_secrets WHERE name = 'telemetry_master_key';`
- [ ] Cloudflare account access via `wrangler` (run `npx wrangler whoami`
      from `worker/`).
- [ ] `python3` with `psycopg[binary]` and `requests` installed for the
      smoke-test script.
- [ ] **A manual Supabase snapshot taken** (Studio → Database → Backups
      → Take backup). The drop is destructive against any v1 row that
      still exists; the snapshot is your rollback path.
- [ ] You're on a clean working tree of SelectiveMirror with the v2
      code merged.

---

## Step 1 — Drop the v1 leftover

```bash
psql "$DATABASE_URL" -f docs/operations/sql/drop-v1-leftover.sql
```

Idempotent. Safe to re-run. Drops:

- pg_cron jobs (`telemetry-bug-rollup-daily`,
  `telemetry-install-rollup-daily`, `telemetry-purge-old-envelopes`)
- v1 functions (`refresh_bug_daily_rollup`, `refresh_install_daily_rollup`,
  `purge_old_envelopes`) — `verify_versioned_hmac` is INTENTIONALLY KEPT
- v1 views (`bug_report_human`, `bug_report_clusters`, `install_summary`,
  `weekly_health`, `tier_distribution`, `reliability_snapshot_human`,
  `install_config_distribution`, v1's `version_dist`)
- v1 tables (`ingest_envelope`, `bug_report`, `bug_report_signal`,
  `bug_report_taxonomy_assignment`, `installation`,
  `installation_event`, `installation_taxonomy_assignment`,
  `installation_reliability_snapshot`, `classification_job`,
  `taxonomy_term`, v1's `bug_daily_rollup`, v1's
  `installation_daily_rollup`)
- v1 ENUMs that conflict with v2 (`bug_source`) or are unused
  (`consent_tier`, `report_format`, `classification_state`,
  `taxonomy_target`, `ingest_kind`)

Expected output: a stream of `DROP …` notices ending in `COMMIT`.
Re-running on a fresh project (no v1 ever deployed) is a no-op —
every statement is `IF EXISTS`.

---

## Step 2 — Apply the v2 schema

```bash
psql "$DATABASE_URL" -f docs/telemetry-v2.sql
```

Creates the v2 ENUMs, the three rollup tables, `telemetry.contribute()`,
the three internal `_bump` helpers, the `version_dist` view, and the
RLS posture (FORCE ROW LEVEL SECURITY on all rollup tables; revoke
PUBLIC EXECUTE; grant only `service_role`).

Verify:

```sql
\dt telemetry.*
-- Expect exactly: installation_daily_rollup, bug_daily_rollup, reliability_daily_rollup

\df telemetry.contribute
-- Expect: telemetry.contribute(payload jsonb, claimed_version text,
--          claimed_hmac_hex text) RETURNS jsonb

SELECT proname, prosecdef
FROM pg_proc
WHERE proname IN ('contribute', '_bump_install', '_bump_bug', '_bump_reliability', 'verify_versioned_hmac')
  AND pronamespace = 'telemetry'::regnamespace;
-- Expect: all five with prosecdef = true (SECURITY DEFINER)

SELECT has_function_privilege('anon', 'telemetry.contribute(jsonb, text, text)', 'EXECUTE');
-- Expect: TRUE
-- (The Cloudflare Worker authenticates to PostgREST as the anon role, not
--  service_role. SECURITY DEFINER + the contribute() function's HMAC check
--  mean granting anon EXECUTE does not widen the attack surface — every
--  contribution still has to carry a valid per-version HMAC. See
--  telemetry-v2.sql line ~485 for the rationale comment.)

SELECT has_function_privilege('service_role', 'telemetry.contribute(jsonb, text, text)', 'EXECUTE');
-- Expect: TRUE (operator + CI direct-call path)

SELECT has_function_privilege('authenticated', 'telemetry.contribute(jsonb, text, text)', 'EXECUTE');
-- Expect: FALSE (we don't use the authenticated role at all)
```

### About the rollup-table constraints

The two rollup tables that include nullable bucket columns
(`installation_daily_rollup` for `prior_version` +
`days_since_first_seen_bucket`, `reliability_daily_rollup` for
`most_common_anomaly_kind`) use `UNIQUE NULLS NOT DISTINCT` rather
than `PRIMARY KEY`. Reason: PostgreSQL implicitly enforces NOT NULL
on every PK column, so a `first_seen` event (which legitimately has
NULL `prior_version`) would have failed at INSERT time. The
`NULLS NOT DISTINCT` semantics (PG 15+) treat NULL = NULL as a
duplicate for `ON CONFLICT` purposes, which is what `_bump_install`
needs. After applying `telemetry-v2.sql` you can verify:

```sql
SELECT conname, indnullsnotdistinct
  FROM pg_constraint c JOIN pg_index i ON i.indexrelid = c.conindid
 WHERE conname IN ('installation_daily_rollup_uniq',
                   'reliability_daily_rollup_uniq');
-- Expect both with indnullsnotdistinct = true
```

---

## Step 3 — Deploy the Worker

```bash
cd worker

# One-time setup (skip if already done):
npx wrangler secret put SUPABASE_ANON_KEY              # paste anon JWT
npx wrangler secret put RATE_LIMIT_SALT_SECRET         # paste 32+ random bytes
# python3 -c "import secrets; print(secrets.token_hex(32))"

# Deploy
npm install
npx wrangler deploy
```

Expected output: `https://smirror-telemetry.selectivemirror.workers.dev`.

Verify the retired paths return 410:

```bash
curl -X POST "https://smirror-telemetry.selectivemirror.workers.dev/v1/forget" -d '{}'
# Expect: 410 Gone with body {"code":"endpoint_retired",...}

curl -X POST "https://smirror-telemetry.selectivemirror.workers.dev/v1/bug-reports" -d '{}'
# Expect: 410 Gone (legacy v1 path)
```

---

## Step 4 — Refresh PostgREST schema cache

After Step 1 + 2's DDL, PostgREST may still be holding the old
schema in its in-memory cache. Force a reload:

```sql
NOTIFY pgrst, 'reload schema';
```

Allow ~2 seconds for propagation, then proceed.

## Step 5 — Smoke test end-to-end

```bash
export TELEMETRY_MASTER_KEY="<from Supabase Vault>"
export WORKER_URL="https://smirror-telemetry.selectivemirror.workers.dev"
export DATABASE_URL="postgresql://postgres.<ref>:<pwd>@aws-0-eu-west-1.pooler.supabase.com:6543/postgres"

python3 scripts/telemetry-v2-smoke-test.py --via-worker --version 0.0.0-deploy-smoke
```

Expected: 6 cases pass —
- bad HMAC → rejected
- good HMAC → accepted (rollup row +1)
- schema violation (bad enum value) → rejected
- unknown event_kind → rejected
- retired `/v1/forget` → 410 Gone
- rollup delta — 1 row in `installation_daily_rollup` matching the smoke
  test's `client_version` and `rclone_version='v1.73.5-smoke'`

---

## Step 6 — Cleanup the smoke-test row (optional)

```sql
DELETE FROM telemetry.installation_daily_rollup
WHERE rclone_version = 'v1.73.5-smoke';
```

(Leaving it is harmless — it won't make it past the digest's k-anonymity
floor of 5 unless someone re-runs the smoke test 5+ times.)

---

## Validators without master-key access

The full smoke test (`scripts/telemetry-v2-smoke-test.py
--via-worker`) exercises both rejection paths AND the
acceptance-with-good-HMAC path. The good-HMAC path requires the
master key — that's the point: the master key IS the gate.

A validator (external auditor, release-validator rotation, periodic
CI gate) without master-key access can still verify substantial
ground truth via two complementary harnesses, neither of which
needs the master:

- **`system-validation/telemetry-worker-probe.py`** — single-pass
  structural checks against the live Worker. Verifies retired
  paths return 410 with the right messages, body cap holds at
  100 KB, malformed bodies get 400 (FINDING 1: no PGRST passthrough),
  GET on retired returns 410 (FINDING 8), unknown paths 404, and
  bad-HMAC contribute attempts get 200 + `{ok:false,error:rejected}`.
  Run by the
  `.github/workflows/telemetry-worker-probe.yml` CI gate daily +
  on every PR that touches `worker/**`. Read-only (every
  contribute() POST is bad-HMAC, so no row is created).

- **`system-validation/telemetry-mass-emulation.py`** — burst test
  with N concurrent bad-HMAC POSTs across all four event_kinds.
  Verifies the Worker sustains load without 5xx, rejects every
  payload at HMAC step 1, retired endpoints continue 410 under
  load, and per-IP rate limit fires somewhere around 30 RPM.

What the master-key-less validator CAN'T test: the schema_violation
and unknown_event paths (those run only after HMAC verification)
and the rollup-delta side of the contract (good-HMAC contributes a
row). The smoke test catches both. Operators with the master key
should still run the full smoke test on every deploy.

---

## Acceptance criteria

The deploy is "done" when:

- [ ] `\dt telemetry.*` lists exactly the three v2 rollup tables.
- [ ] `\df telemetry.contribute` returns the function with
      `prosecdef = true`.
- [ ] `has_function_privilege('anon', 'telemetry.contribute(...)', 'EXECUTE')`
      is `false`; `service_role` is `true`.
- [ ] All 5 smoke-test cases pass via the Worker route.
- [ ] `/v1/forget`, `/v1/bug-reports`, `/v1/installations/report`
      return 410 Gone.
- [ ] CHANGELOG entry: "Telemetry v2 deployed; v1 leftover removed.
      Stream-aggregate-and-discard architecture is live."

---

## Operational notes

### What if the smoke test fails on `case_good_hmac`?

This is almost always canonical-JSON drift. PG JSONB sorts keys
length-first then codepoint; Python's `json.dumps(sort_keys=True)`
sorts alphabetically (different whenever keys have different lengths).
The smoke test uses its own `canonical_json` that mirrors PG.

To debug:
1. Take the payload the smoke test built (excluding `version_hmac`
   and `event_kind`).
2. `SELECT '<payload>'::JSONB::TEXT` in psql.
3. Compare byte-for-byte with the smoke test's `canonical_json`
   output.
4. Any difference is the bug.

Reference: `~/.claude/projects/C--SelectiveMirror/memory/reference_jsonb_canonicalization.md`.

### Rotation of `RATE_LIMIT_SALT_SECRET`

Quarterly is fine. Rotation invalidates currently-counting rate-limit
windows (max 60 seconds of disruption — KV TTL).

```bash
cd worker
npx wrangler secret put RATE_LIMIT_SALT_SECRET
# (no redeploy needed; secrets propagate to running Workers within seconds)
```

### Recovery from a partial deploy

If Step 1 succeeds but Step 2 fails (network blip, syntax error,
etc.), the database is in a clean state with no v1 schema and no
v2 schema. Re-running Step 2 directly is safe.

If Step 2 fails partway through (some objects created, some not),
the file is wrapped in `BEGIN/COMMIT` and rolls back as a unit —
re-run Step 2 directly. No manual cleanup needed.

If Step 3 fails (Worker deploy error), the Supabase side is healthy
and idle. Fix the Worker config and re-deploy.

### "I want to confirm the v1 schema is really gone before applying v2"

Between Step 1 and Step 2:

```sql
SELECT relname FROM pg_class
JOIN pg_namespace ON relnamespace = pg_namespace.oid
WHERE nspname = 'telemetry' AND relkind IN ('r','v','m')
ORDER BY relname;
-- Expect: zero rows.
```
