# Morning checklist — Supabase setup + autonomous-work review

**Generated**: 2026-04-24/25 overnight
**Scope**: bring telemetry from "designed" to "live and accepting traffic" with consent-honest defaults.

---

## Summary of what was done while you slept

### Files created
- `docs/telemetry-rls.sql` — RLS + CHECK constraints + HMAC verify function. Ready to paste after main schema.
- `docs/cli-telemetry-command.md` — Design for `smirror telemetry on/off/status` (not yet implemented in Go).
- `installer/TelemetryConsent.wxi` — WiX include file: `INSTALL_TELEMETRY_OPT_IN` property + registry write component. Not yet wired into `Package.wxs` (intentional — needs your review).
- `~/.claude/projects/C--SelectiveMirror/memory/project_telemetry_consent.md` — Memory of the consent model decisions.

### Files modified
- `docs/telemetry-microserver.sql` — Re-added install-telemetry tables (scope b: first_seen + upgrade only, no heartbeat).
- `docs/telemetry-microserver-architecture.md` — Added "Installation telemetry (opt-in)" section, dual-endpoint API surface, updated topology diagram, updated implementation order.
- `~/.claude/projects/C--SelectiveMirror/memory/MEMORY.md` — Index updated with `project_telemetry_consent.md` reference.

### Files NOT modified (deliberately)
- `installer/Package.wxs` — UI integration of the consent checkbox is left for you to review. The TelemetryConsent.wxi file documents how to integrate.
- `cmd/smirror/*.go` — No Go code changes (could break tests/builds; better as a deliberate session).
- All other Go source files.
- No commits, no pushes, no version bumps.

---

## Steps for you to do, in order

### Step 1 — Load main schema into Supabase

1. Open https://supabase.com/dashboard/project/qkspigvkniiiwxggdvbr
2. Left sidebar → **SQL Editor** → **New query**
3. Open `docs/telemetry-microserver.sql` locally
4. Copy the entire file contents
5. Paste into the SQL Editor
6. Click **Run** (or Ctrl+Enter)

**Expected result**: ~10 seconds. No errors. Table Editor (left sidebar) → switch dropdown from `public` to `telemetry` → you should see 11 tables:

```
telemetry.bug_daily_rollup
telemetry.bug_report
telemetry.bug_report_signal
telemetry.bug_report_taxonomy_assignment
telemetry.classification_job
telemetry.ingest_envelope
telemetry.installation
telemetry.installation_daily_rollup
telemetry.installation_event
telemetry.installation_taxonomy_assignment
telemetry.taxonomy_term  (with 30 seeded rows: 22 bug-side + 2 install.lifecycle + 6 install.channel)
```

Plus 5 ENUM types (`ingest_kind`, `taxonomy_target`, `classification_state`, `bug_source`, `report_format`).

**If errors appear**: paste them back to me. Most likely causes are `pg_cron` extension not preinstalled on this region (rare; we'll handle if so).

### Step 2 — Expose the `telemetry` schema to the Data API

1. Project Settings → **API** (in left sidebar bottom area)
2. Find **Exposed schemas** field
3. Add `telemetry` to the list (alongside `public`)
4. Click **Save**

Without this step the auto-generated REST API cannot see your tables even though they exist.

### Step 3 — Generate and store the master HMAC key

**Do this exactly as instructed; do NOT paste the key into chat with me.**

1. In a terminal:
   ```
   openssl rand -hex 32
   ```
2. Copy the resulting 64-character hex string.
3. Save it to your password manager under a clearly named entry (e.g., `SelectiveMirror Telemetry Master HMAC Key`).
4. In Supabase dashboard → Project Settings → **Vault** → **Add new secret**:
   - Name: `telemetry_master_key`
   - Secret: paste the hex string
   - Description: `SelectiveMirror version-derived HMAC master key. Used by telemetry.verify_versioned_hmac() to validate incoming bug-report and install-event payloads. Compromise means rotating (per-version) keys; never store in repo or binary.`
5. Click **Save**.
6. Verify it's stored: `SELECT name FROM vault.secrets WHERE name = 'telemetry_master_key';` should return one row.

**Critical**: this key never goes anywhere except your password manager and Supabase Vault. Not in CI yet (CI integration is a future step when you ship the first telemetry-capable binary). Not in any binary. Not in chat with me.

### Step 4 — Apply RLS + CHECK + HMAC verify function

1. Open `docs/telemetry-rls.sql` locally
2. Copy entire contents
3. Paste into Supabase SQL Editor
4. Click **Run**

**Expected result**: no errors. Multiple `ALTER TABLE` and `CREATE POLICY` statements complete.

**Verification**:
```sql
-- Should return 1 row
SELECT proname FROM pg_proc WHERE proname = 'verify_versioned_hmac';

-- Should show 11 telemetry tables, all with rowsecurity=true
SELECT relname, relrowsecurity
FROM pg_class
WHERE relnamespace = 'telemetry'::regnamespace AND relkind = 'r'
ORDER BY relname;

-- Should show one policy on ingest_envelope
SELECT polname, polcmd
FROM pg_policy
WHERE polrelid = 'telemetry.ingest_envelope'::regclass;
```

**If you want to test ingest before HMAC-protected key is set up**: see the "DEVELOPMENT-MODE SHORTCUT" block at the bottom of `telemetry-rls.sql`. Uncomment it temporarily, but **re-comment before exposing the endpoint to real clients**.

### Step 5 — Review the MSI consent integration

`installer/TelemetryConsent.wxi` was created but not yet integrated into `Package.wxs`. To activate:

1. Review the file end to end. Confirm wording, registry path, default value (currently `0` = unchecked = opt-out).
2. In `installer/Package.wxs`, after `<?include Variables.wxi ?>`, add:
   ```xml
   <?include TelemetryConsent.wxi ?>
   ```
3. Inside the existing `<Feature Id="ProductFeature" Title="SelectiveMirror" Level="1">`, add:
   ```xml
   <ComponentGroupRef Id="TelemetryConsentComponents" />
   ```
4. Test: rebuild the MSI; install with `msiexec /i SelectiveMirror.msi INSTALL_TELEMETRY_OPT_IN=1`; verify HKLM\Software\SelectiveMirror\TelemetryOptIn = 1.
5. **NOT YET DONE**: integrating a UI checkbox into the installer dialogs. See "UI integration TODO" comment block at the bottom of `TelemetryConsent.wxi` for the approaches. This is a deliberate session of work; don't try to do it autonomously.

If you'd rather skip step 5 entirely for now, it's safe to defer — the Property mechanism is purely additive and inert until you wire it in.

---

## What I did NOT do (that you might expect)

- **No Go code changes.** `cmd/smirror/main.go`, `internal/telemetry/*.go`, etc. — untouched. The telemetry rewrite (per-event-approval bug-report flow, opt-in install-telemetry consent flow, HMAC client, durable on-disk queue, `smirror telemetry on/off/status` command) is a deliberate coding session that should happen with your oversight.
- **No commits, no pushes, no version bumps.** Per memory rule, each commit cycle bumps `-dev` patch by 1; you'd be jumping from 0.8.48-dev to 0.8.49-dev (or beyond) for these changes. Your call when to commit.
- **No HMAC key generation.** Done by you in Step 3 above.
- **No actual loading of SQL into Supabase.** Done by you in Steps 1, 2, 4.
- **No installer UI integration.** Documented in `TelemetryConsent.wxi` as TODO.

---

## Open follow-ups for future sessions

When you're ready (next session or later):

- **`internal/telemetry/` rewrite**: drop the old install-telemetry-heavy code, build a clean implementation matching the new consent model. ~300-500 lines of Go. Tests.
- **`smirror telemetry` command**: implement per `docs/cli-telemetry-command.md`. ~150 lines + tests.
- **MSI UI checkbox integration**: WiX dialog work. Review the three approaches in `TelemetryConsent.wxi`; pick one.
- **CI HMAC injection**: GitHub Actions workflow gets `SMIRROR_TELEMETRY_MASTER_KEY` secret; build step computes per-version derived key with `openssl dgst -sha256 -hmac` and passes via `-ldflags -X`. ~10 lines of YAML.
- **Cloudflare Worker**: deferred until first telemetry-capable binary ships. Worker forwards `*.workers.dev/v1/{bug-reports,installations/report}` to Supabase, with optional rate-limiting.
- **pg_cron worker jobs**: rollup refresh + retention janitor. ~80 lines of SQL.
- **Bug-report submission preview UX**: ensure user sees `install_id` / `client_version` / `anomaly_summary` before approving, with redaction options.

---

## Caveats and things to double-check

1. **Install_id source for bug reports**: I assumed install_id always exists (generated once on first state DB creation, opt-in or not). Confirm this — it should be true since it's a state-DB metadata key, independent of telemetry consent. If install_id only exists when telemetry is opted in, the bug-report flow needs a fallback.

2. **`pg_cron` extension availability**: Supabase generally enables it on free tier, but if `CREATE EXTENSION IF NOT EXISTS pg_cron;` fails when you run a worker setup later, we'll need to enable it via the Database → Extensions UI in the dashboard.

3. **Schema FK ordering in main SQL**: I rearranged the order to declare `bug_report_taxonomy_assignment` and `installation_taxonomy_assignment` *before* `taxonomy_term`, then attach FKs after via `ALTER TABLE`. This avoids a circular dependency in the file ordering. If you see "relation does not exist" errors loading the schema, tell me and I'll restructure.

4. **HMAC RLS policy fails closed**: If you load `telemetry-rls.sql` BEFORE setting the master key in Vault (Step 3), every subsequent INSERT will fail with "telemetry_master_key not found in Supabase Vault". This is the correct security posture but might surprise you if you test ingest before doing Step 3. Order: schema → expose → Vault → RLS.

5. **Architecture doc is long now (~600 lines)**. Skim it to see if anything contradicts your intent. The "Installation telemetry (opt-in)" section is the most newly-shaped piece.

6. **The WiX include file uses `Bitness="always64"`** matching existing components. Confirm this matches your release intent (you're 64-bit Windows-only currently).

---

## Total uncommitted changes after this session

```
docs/telemetry-microserver.sql                      (rewritten — re-added install-telemetry)
docs/telemetry-microserver-architecture.md          (multiple section updates)
docs/telemetry-rls.sql                              (NEW)
docs/cli-telemetry-command.md                       (NEW)
docs/MORNING_CHECKLIST.md                           (NEW — this file)
installer/TelemetryConsent.wxi                      (NEW)
installer/Variables.wxi                             (Manufacturer change, from earlier)
.github/ISSUE_TEMPLATE/bug_report.yml               (install_id field, from earlier)
~/.claude/projects/C--SelectiveMirror/memory/MEMORY.md         (index update)
~/.claude/projects/C--SelectiveMirror/memory/project_telemetry_consent.md  (NEW)
```

Plus whatever was uncommitted before this session (per prior memory: ~20 patch-version's worth of v0.8.x work).

---

## When you wake up

The minimum to get telemetry live:

1. Steps 1, 2, 3, 4 above (15-30 minutes total).

That's it. After those four steps:
- Your Supabase project is ready to receive HMAC-signed POSTs to `ingest_envelope`.
- No clients are sending yet (Go rewrite hasn't happened).
- All defenses (RLS, CHECK, HMAC, anon INSERT-only) are in place.
- You can verify end-to-end by manually crafting a test POST with a valid HMAC (or temporarily uncommenting the dev-mode policy in `telemetry-rls.sql`).

Step 5 (MSI integration) is optional now; can be done in any later session.

Sleep well.
