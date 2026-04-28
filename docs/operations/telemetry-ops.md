# Telemetry operations runbook

**Audience**: maintainer (Raveh, anyone with admin access to the Supabase backend).
**Companion to**: `docs/telemetry-microserver-architecture.md` (the spec) and `docs/PRIVACY.md` (user-facing).

This runbook tells you how to read the data, what to check on what cadence, and what to do when something goes wrong.

---

## Daily — nothing

By design. There is no daily check; the system is too low-volume for that to be useful and you will burn out staring at zeros.

---

## Weekly — read the auto-generated digest

A GitHub Action runs every Sunday at 03:00 UTC and opens a PR with a Markdown digest at `docs/telemetry/weekly-YYYY-WWNN.md`. Read it on your phone in the GitHub mobile app, or in your morning email.

What to look for in priority order:

1. **Action prompt sections** — the digest calls out anything worth acting on (recurring signatures, unclassified backlog age, version-distribution shifts).
2. **"What nobody hit"** — stability streaks. If a category that's been quiet for weeks suddenly fires, that's news.
3. **Hygiene line** — confirms `pg_cron` jobs ran, DB usage is below the free-tier ceiling, last ingest is recent.

If a week looks like "n is too small for analysis," it is. Move on.

---

## Monthly — sanity checks

Run these in Supabase SQL Editor (the views are in `docs/telemetry-views.sql`):

```sql
-- 1. DB free-tier headroom
SELECT pg_size_pretty(pg_database_size(current_database())) AS db_size;
-- If approaching 400 MB (80% of 500 MB free-tier ceiling), tighten retention.

-- 2. pg_cron job health
SELECT jobid, jobname, schedule, active
FROM cron.job
WHERE jobname LIKE 'telemetry-%';

-- 3. Recent cron run history
SELECT jobid, status, start_time, end_time, return_message
FROM cron.job_run_details
WHERE jobid IN (SELECT jobid FROM cron.job WHERE jobname LIKE 'telemetry-%')
ORDER BY end_time DESC LIMIT 10;
-- All recent runs should be 'succeeded'.

-- 4. Dead-letter (server side: should always be 0; we don't queue server-side)
-- Client side: users with stuck telemetry would have files in
-- ~/.selectivemirror/telemetry-queue/dead-letter/. Not visible to us.
```

---

## Quarterly — taxonomy + key rotation

### Taxonomy curation

Open `telemetry.taxonomy_term` in Supabase Studio. Look at the recently-classified bug reports:

```sql
SELECT bug_kind, COUNT(*)
FROM telemetry.bug_daily_rollup
WHERE rollup_date >= now() - INTERVAL '90 days'
GROUP BY bug_kind ORDER BY COUNT(*) DESC;
```

If `unknown` is more than ~20% of classifications, the taxonomy is missing terms. Add them:

```sql
INSERT INTO telemetry.taxonomy_term (target, namespace, slug, display_name, description, ordinal)
VALUES ('bug_report', 'bug.kind', 'new_kind', 'New Kind', 'Description.', 200);
```

(Don't change ordinals of existing terms; just append.)

### HMAC key rotation (optional)

The master HMAC key in Supabase Vault doesn't expire. Rotate only if:
- A binary's derived key has been observably abused (you see traffic from a version_hmac that shouldn't exist)
- A team member leaves and had access to the master key
- General hygiene (annual is fine)

To rotate:
1. Generate new key: `openssl rand -hex 32` (locally; never paste anywhere)
2. Update in Bitwarden + Supabase Vault (`telemetry_master_key`)
3. Update GitHub Actions repo secret `SMIRROR_TELEMETRY_MASTER_KEY`
4. Cut a new release; old binaries will continue to verify (their derived keys are based on the old master) — wait, no, that's wrong. **Rotating the master invalidates ALL existing derived keys.** Old shipped binaries will silently fail to submit telemetry until users upgrade. Plan accordingly.

---

## Incident: a bad version reported in the wild

When you see a recurring signature on a specific `client_version` and need to stop more reports of the same thing:

```sql
-- Option A: deny all telemetry from that version (drastic; clients see 401s)
ALTER POLICY anon_insert_with_hmac ON telemetry.ingest_envelope
WITH CHECK (
    -- ... existing checks ...
    AND client_version != '0.9.5'  -- <-- block this version
    AND telemetry.verify_versioned_hmac(...)
);

-- Option B: just patch the bug, ship a release, let upgrade events resolve it.
-- Usually the right answer.
```

Recovery: revert the policy change after the bad version is no longer in the wild (you can tell from `version_dist` view).

---

## Going dark: how to disable ingest if compromised

If the master HMAC key leaks or someone's flooding the endpoint:

```sql
-- Disable the policy; no more anon inserts will succeed.
ALTER TABLE telemetry.ingest_envelope DISABLE ROW LEVEL SECURITY;
ALTER TABLE telemetry.ingest_envelope ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS anon_insert_with_hmac ON telemetry.ingest_envelope;
-- Now anon has no policy → all anon inserts fail.
```

To restore: re-run `docs/telemetry-rls.sql`.

For an immediate edge-layer block (faster than re-loading SQL):
- Update Cloudflare Worker to return 503 unconditionally
- `wrangler deploy` — propagates in <30 seconds

---

## Storage growing fast

If `pg_database_size(...)` is climbing toward 400 MB, the retention janitor isn't keeping up. Options:

1. Tighten retention: edit `docs/telemetry-worker.sql`, change `retention_days INTEGER DEFAULT 90` to `30`, re-load.
2. Truncate old envelopes manually:
   ```sql
   UPDATE telemetry.ingest_envelope
   SET payload = '{}'::jsonb
   WHERE received_at < now() - INTERVAL '30 days'
     AND classification_state = 'classified';
   ```
3. Strip large columns on `bug_report` after classification (currently NOT in the retention janitor — see panel review):
   ```sql
   UPDATE telemetry.bug_report
   SET report_text = '<purged>',
       anomaly_summary = '{}'::jsonb,
       status_snapshot = '{}'::jsonb
   WHERE classified_at < now() - INTERVAL '30 days';
   ```

---

## What to use when

| Situation | Tool |
|-----------|------|
| Weekly review | The auto-generated digest at `docs/telemetry/weekly-*.md` |
| Ad-hoc query | Supabase SQL Editor + the views in `docs/telemetry-views.sql` |
| Investigating a specific bug report | `SELECT * FROM telemetry.bug_report_human WHERE id = '...'` |
| Triaging recurring signatures | `SELECT * FROM telemetry.bug_report_clusters` |
| Checking install base | `SELECT * FROM telemetry.install_summary` |
| Single-row health snapshot | `SELECT * FROM telemetry.weekly_health` |
| Monthly hygiene | the queries above |
| Post-incident report | inline in CHANGELOG (see `docs/PRIVACY.md` and the panel review on transparency norms) |
