# Telemetry operations runbook

**Audience**: maintainer (Raveh, anyone with admin access to the
Supabase backend).
**Companion to**: [`telemetry-architecture-v2.md`](../telemetry-architecture-v2.md)
(the spec) and [`PRIVACY.md`](../PRIVACY.md) (user-facing).
**Initial deploy**: see [`deploy-telemetry-v2.md`](./deploy-telemetry-v2.md).

This runbook tells you how to read v2 telemetry data, what to check on
what cadence, and what to do when something goes wrong.

---

## Daily — nothing

By design. There is no daily check; the system is too low-volume for
that to be useful and you will burn out staring at zeros.

---

## Weekly — read the auto-generated digest

A GitHub Action runs every Sunday at 03:00 UTC and opens a PR with a
Markdown digest at `docs/telemetry/weekly-YYYY-WWNN.md`. Read it on
your phone in the GitHub mobile app, or in your morning email.

What to look for in priority order:

1. **Action prompt sections** — the digest calls out anything worth
   acting on (recurring kind/surface buckets, version-distribution
   shifts, anomaly-kind spikes on the latest release).
2. **"What nobody hit"** — stability streaks. If a bucket that's been
   quiet for weeks suddenly fires, that's news.
3. **Hygiene line** — confirms recent ingest activity and DB usage is
   below the free-tier ceiling.

If a week looks like "n is too small for analysis," it is. Move on.

> **Note**: as of 0.9.7x-dev, `scripts/telemetry-report.py` still
> queries the v1 row-per-event tables. Under v2 those tables don't
> exist; the rollup tables are denormalized counters. The digest
> script is being rewritten for v2 — tracked as a follow-up. Until
> then, the weekly PR may produce stale or empty output. Fall back
> to the manual SQL queries below.

---

## Monthly — sanity checks

Run these in Supabase SQL Editor against the v2 rollup tables.

```sql
-- 30-day version distribution (what's running in the wild)
SELECT * FROM telemetry.version_dist;
-- Aggregate counts only; the view applies the events-30d window.
-- The digest filter additionally suppresses cells with < 5 contributors.

-- Bug-rollup activity over the last 30 days
SELECT bug_kind, bug_surface, client_version,
       SUM(reports) AS total_reports
FROM telemetry.bug_daily_rollup
WHERE rollup_date >= CURRENT_DATE - INTERVAL '30 days'
GROUP BY bug_kind, bug_surface, client_version
ORDER BY total_reports DESC, bug_kind, bug_surface;

-- Reliability snapshot patterns (Reliability tier only)
SELECT client_version, anomaly_count_bucket, most_common_anomaly_kind,
       SUM(count) AS snapshots
FROM telemetry.reliability_daily_rollup
WHERE rollup_date >= CURRENT_DATE - INTERVAL '30 days'
GROUP BY client_version, anomaly_count_bucket, most_common_anomaly_kind
ORDER BY snapshots DESC;

-- Free-tier database usage
SELECT pg_size_pretty(pg_database_size(current_database())) AS db_size;
-- Threshold: 500 MB (Supabase free tier). v2 schema is small —
-- only counters, no raw payloads. Should stay well under 100 MB
-- indefinitely.
```

---

## Bad-version recovery

A bug ships in version X. Telemetry shows X has growing share. Steps:

1. **Confirm the spike**: query `telemetry.version_dist` and the
   bug_daily_rollup for the 24h since X went out.
2. **Pull** the release: `gh release delete vX --yes` (or mark as
   pre-release in Studio).
3. **Restore** the prior version as `latest` so `selfupdate --check`
   pulls users back: edit the release that should be latest, click
   "Set as latest release."
4. **Watch the rollup decline** over the next 7 days as users run
   selfupdate.
5. **Document** in `CHANGELOG.md` why X was withdrawn.

---

## Health checks (when telemetry seems wrong)

| Symptom | Probable cause | Quick check |
|---|---|---|
| All recent contributions rejected | HMAC master key missing or rotated | `SELECT name FROM vault.decrypted_secrets WHERE name = 'telemetry_master_key';` |
| `contribute()` returns 4xx | RPC permissions broken | `SELECT has_function_privilege('service_role', 'telemetry.contribute(jsonb,text,text)', 'EXECUTE');` |
| Worker returns 502 | Supabase project paused/down | `curl -I https://<project>.supabase.co` |
| Aggregate counts stalled | Worker not deployed or wrong project | `npx wrangler tail` to watch live requests |
| Smoke test fails on canonical-JSON parity | client/server canonicalizer drift | Compare output of `internal/telemetry/canonical.go` to PG `JSONB::TEXT` |

The smoke-test script (`scripts/telemetry-v2-smoke-test.py
--via-worker`) exercises the four standard rejection paths and one
acceptance path. Re-run it any time Supabase or the Worker has been
touched.

---

## What changed from v1

Under v1 (now retired) operations included:
- A nightly `pg_cron` rollup-refresh job — gone (counters are inline)
- A 90-day retention janitor — gone (no raw stored)
- Multiple denormalized human-friendly views — gone (rollups ARE the
  data)
- `bug_report_human` / `bug_report_clusters` for re-classification —
  gone (taxonomy is client-side at submit time)

Anything you remembered from the v1 ops runbook that touched
`bug_report`, `installation_event`, `ingest_envelope`, `taxonomy_term`,
or `purge_old_envelopes` is no longer applicable. The v1 schema was a
leftover; it was dropped via `docs/operations/sql/drop-v1-leftover.sql`
during the v2 deploy.

---

## Glossary

| Term | Where it lives |
|---|---|
| `installation_daily_rollup` | v2 rollup table for first_seen + upgrade events |
| `bug_daily_rollup` | v2 rollup table for bug_report contributions |
| `reliability_daily_rollup` | v2 rollup table for Reliability-tier snapshots |
| `telemetry.contribute()` | the only RPC client-callers ever invoke |
| `telemetry.verify_versioned_hmac()` | shared HMAC verifier (called by contribute) |
| `version_dist` | view: 30-day version distribution from installation_daily_rollup |
| `RATE_LIMIT_SALT_SECRET` | Worker secret; salts the IP→KV-key HMAC |

| Tool | Where it lives |
|---|---|
| Live data | Supabase Studio → Database → Tables → telemetry schema |
| Schema source | [`docs/telemetry-v2.sql`](../telemetry-v2.sql) |
| Worker source | [`worker/src/index.ts`](../../worker/src/index.ts) |
| Smoke test | [`scripts/telemetry-v2-smoke-test.py`](../../scripts/telemetry-v2-smoke-test.py) |
| Digest script | [`scripts/telemetry-report.py`](../../scripts/telemetry-report.py) — pending v2 rewrite |
| Deploy runbook | [`deploy-telemetry-v2.md`](./deploy-telemetry-v2.md) |
