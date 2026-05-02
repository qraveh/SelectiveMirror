# Telemetry Architecture v2 — stream-aggregate-and-discard

**Status**: target architecture for SelectiveMirror v1.0. Replaces the
v1 architecture in `telemetry-microserver-architecture.md`.
**Adopted**: 2026-04-29, after the round-2 BMad panel review of
`smirror telemetry forget`.
**Implementation status**: schema in `telemetry-v2.sql`. The earlier
v1 schema is a server-side leftover (never wired to a live client
post-SM-160) and is dropped by `docs/operations/sql/drop-v1-leftover.sql`
as part of the standard deploy. See `Deployment` below.

---

## One-paragraph contract

SelectiveMirror's telemetry never persists personal data. Every
contribution that crosses the network boundary is processed inside a
single Postgres transaction — its categorical fields are extracted,
the matching anonymous counter is incremented, and the payload exits
the transaction with the connection. Nothing identifiable is ever
written to disk. The audit story is a `\dt` of the telemetry schema:
the tables that exist are aggregate counters; the tables that *don't*
exist are the ones a regulator would worry about.

---

## Why v2

The v1 architecture (raw `ingest_envelope` → normalized `bug_report` /
`installation_event` → `bug_daily_rollup`) created a 90-day retention
window during which personal data lived on the server. Inside that
window: GDPR Art 17 erasure obligations, CCPA delete requests, the
Cloudflare/Supabase outage surface that made `smirror telemetry forget`
silently fail-prone, and the maintainer-abandonment scenario where data
lives on after the project doesn't.

The round-2 BMad panel converged on a deeper reframe:

- **Mary**: scope-narrowing (excluding EU users) doesn't clear CCPA /
  UK-GDPR / LGPD / PIPEDA / the cascade. The cleanest legal posture is
  *anonymity-by-construction*, where the data the regulator cares
  about doesn't exist.
- **Quinn**: the system contradiction is "we want the user to have
  proof of deletion, but the proof can only exist after their session
  ends." The TRIZ resolution is to *eliminate the request-response*
  primitive entirely — if there's nothing to delete, there's nothing
  to prove.
- **Victor**: in OSS, the strongest market signal a single-maintainer
  project has against Microsoft / Google / Resilio is "we don't watch
  you, by construction." Every architecture that *could* watch you
  pays that signal away.

v2 is the architecture that makes the privacy promise *structural*
rather than *procedural*. There is no `forget` because there is no
record to forget.

---

## The processing scheme

### Entry point: `telemetry.contribute()`

A single Postgres function consumes every contribution. The Cloudflare
Worker's role becomes: rate-limit, validate envelope shape, call the
function, return its result. No intermediate storage anywhere in the
pipeline.

```
┌───────────────────────────────────────────────────────────────┐
│ Client (smirror.exe)                                          │
│   build canonical payload                                     │
│   sign with version-derived HMAC                              │
│   POST to Worker                                              │
└────────────────────────────┬──────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────┐
│ Cloudflare Worker                                             │
│   rate-limit by salted-IP-hash (rotating daily salt)          │
│   verify envelope shape + size cap (10KB)                     │
│   POST to Supabase RPC: telemetry.contribute(payload, ...)    │
│   relay status code; do not log payload                       │
└────────────────────────────┬──────────────────────────────────┘
                             │
                             ▼
┌───────────────────────────────────────────────────────────────┐
│ Supabase / Postgres                                           │
│   BEGIN                                                       │
│     verify_versioned_hmac(canonical, version, hmac)           │
│     dispatch on event_kind                                    │
│     UPSERT into matching rollup table                         │
│   COMMIT                                                      │
│   payload exits scope; never INSERTed                         │
└───────────────────────────────────────────────────────────────┘
```

### Event kinds and their bucket dimensions

The set of event kinds is closed and small. Adding a new one requires
a schema migration AND a re-consent flow per `PRIVACY.md`'s forward
commitment.

| Event kind | Tier | Purpose | Rollup table |
|---|---|---|---|
| `first_seen` | Standard, Reliability | Anonymous install census, structural fields | `installation_daily_rollup` |
| `upgrade` | Standard, Reliability | Version-transition signal, structural fields | `installation_daily_rollup` |
| `bug_report` | Standard, Reliability | Categorical bug count by `(kind, surface, version)` | `bug_daily_rollup` |
| `reliability_snapshot` | Reliability only | Bucketed operational-health deltas at upgrade time | `reliability_daily_rollup` |

Note: bug-report **narratives** are NOT a telemetry event in v2. They
go to GitHub Issues via `smirror report-bug --browser`. Telemetry
counts only the categorical fact of a submission. See "What we
moved off the telemetry path" below.

#### `installation_daily_rollup` bucket key

```
(rollup_date, event_name, install_method, os_family, client_version,
 mirror_count_bucket, background_mode, delete_policy,
 has_hooks, has_filters, has_alert_webhook, has_bandwidth_limit,
 rclone_version, prior_version /* nullable */,
 days_since_first_seen_bucket /* nullable */)
```

Counter: `count`.

`prior_version` and `days_since_first_seen_bucket` are non-NULL only
for `event_name = 'upgrade'`. UPSERT key is the full tuple; counter
sums on conflict.

#### `bug_daily_rollup` bucket key

```
(rollup_date, bug_kind, bug_surface, client_version,
 severity_hint, source, submitted_tier)
```

Counter: `reports`.

`bug_kind` and `bug_surface` are derived **client-side at submission
time** from a fixed taxonomy of seven values plus `unknown`:
`sync` / `watcher` / `rclone` / `config` / `service` / `fs` / `auth`.
The classifier (`internal/telemetry/classify.go::ClassifyBugReport`)
runs over the already-sanitized bundle; rules are simple keyword
matches in a defined precedence order (most-specific first). The user
does not pick the bucket — the keyword match does, deterministically,
locally, before the contribution is signed and sent. Bundles that
match no rule classify as `unknown`. The `bug_unknown_share` view
surfaces the per-version unknown share over 30 days so the maintainer
can evolve the taxonomy when it drifts (Mary's drift detection,
A-10).

`severity_hint` is a small ENUM (`info` / `warning` / `error` /
`critical`); user-initiated `--submit` defaults to `error`. `source`
is `report_bug` (current SM-158 path) or `crash_report` (deferred).
`submitted_tier` records whether the contributor was at `standard`,
`reliability`, or `one_shot`.

The `bug_surface` column is intentionally separate from `bug_kind`
even though today the classifier sets them equal, so a future split
("kind=auth, surface=installer-msi") doesn't require a schema
migration.

#### `reliability_daily_rollup` bucket key

```
(rollup_date, client_version,
 anomaly_count_bucket /* total across all kinds */,
 most_common_anomaly_kind /* may be NULL */,
 sync_attempts_bucket, sync_failures_bucket,
 restart_count_bucket, max_queue_depth_bucket,
 dead_letter_count_bucket, state_db_size_bucket)
```

Counter: `count`.

A reliability snapshot's per-kind anomaly map (`{"watcher_error":3,
"ghost_leak":0,...}`) is collapsed to two scalars at submission time:
the bucketed total, and the kind with the highest count (or NULL if
none). This loses the distribution but keeps the leading-anomaly
signal, which is what's actionable.

### What never touches disk

- Raw payload bytes.
- `install_id` — verified for HMAC, used for nothing else, exits
  scope with the function call. Never indexed, never joined,
  never logged.
- IP addresses — the Worker rate-limits using a daily-salted hash of
  the IP; the salt rotates and is not retained, so the hash is
  non-reversible across days.
- Any free-text field (bug report `report_text`, `signature`,
  `title`, `severity_hint`, `component_hint`, `reproduction_hint`).
- HMAC version-key — embedded in the binary, used to verify, never
  written to a Postgres column.
- The connection-state's `pg_stat_statements` entries are normalized
  (parameter values are stripped to `$1`); no payload literal can
  appear in `pg_stat_statements`.

### What does touch disk

- The four rollup tables (`installation_daily_rollup`,
  `bug_daily_rollup`, `reliability_daily_rollup`, plus a public
  `version_dist` materialized view).
- Aggregate counters incrementing in those tables.
- The `taxonomy_term` lookup table (closed vocabulary; not user data).
- Schema metadata (Postgres catalog, RLS policies, function bodies).

That's it.

---

## What we moved off the telemetry path

Three things that lived in v1 telemetry are deliberately moved out of
v2:

### 1. Bug-report narratives → GitHub Issues

`smirror report-bug --browser` opens a prefilled GitHub Issue with the
sanitized environment + log lines. The user reviews, edits, submits
on GitHub. The narrative content is hosted by GitHub Inc. under their
Privacy Statement; the user retains edit/delete rights via their
GitHub account.

**Telemetry's role for bug reports** in v2: at most a single
categorical contribution to `bug_daily_rollup` — `(kind, surface,
version, severity_hint, "report_bug", tier)`. No narrative, no
install_id link, no GitHub-issue cross-reference.

The maintainer's discipline: never copy user-submitted GitHub issue
text into the project's own published artifacts (changelogs, weekly
digests, READMEs). Reference issues by URL or number only. See
`PRIVACY.md` "Bug reports are not telemetry."

### 2. Install-tracking history → not measured

v1 maintained an `installation` row per `install_id` with
`first_seen_at` / `last_seen_at` / aggregated history. v2 has no
per-install row. The cost: **"active in last 30 days"** as a precise
distinct-install count is no longer measurable.

The replacement metric: **30-day event volume** = total events
(first_seen + upgrade + bug_report + reliability_snapshot) recorded in
the rollup tables for the last 30 days. This isn't the same number,
but it answers the same maintainer question ("is the project alive,
growing, slowing"). For a single-maintainer OSS project, the loss is
acceptable.

If a stronger cardinality measure becomes necessary later, the
upgrade path is HyperLogLog sketches (Postgres `hll` extension).
Sketches are not personal data — they're cardinality estimators —
so the architecture stays clean. Ship without HLL; add only if
needed.

### 3. Per-event audit trail → not kept

v1's `bug_report_taxonomy_assignment`, `bug_report.taxonomy_state =
'pending' → 'classified'`, and the manual-review workflow assumed
the maintainer would re-classify reports after submission. v2 forces
classification at submission time (client picks `kind` and `surface`
from the fixed taxonomy). The "let me look at last week's reports
and re-classify" workflow is replaced by "let me look at last week's
ROLLUP and notice that bucket X grew." Different workflow, smaller
data.

---

## Implementation

### Worker → Supabase RPC call shape

```
POST https://<project>.supabase.co/rest/v1/rpc/contribute
  apikey: <SUPABASE_ANON_KEY>
  Authorization: Bearer <SUPABASE_ANON_KEY>
  Content-Type: application/json

  {
    "payload": { ...full canonical payload object... },
    "claimed_version": "0.9.18-dev",
    "claimed_hmac_hex": "abc123..."
  }
```

PostgREST binds these as named parameters; the payload goes through
the wire as a JSONB value, not as a SQL literal. `pg_stat_statements`
sees `$1`, `$2`, `$3` — values are stripped.

### `telemetry.contribute()` pseudocode

Full SQL is in `telemetry-v2.sql`. Sketch:

```sql
CREATE FUNCTION telemetry.contribute(
    payload JSONB,
    claimed_version TEXT,
    claimed_hmac_hex TEXT
) RETURNS JSONB
LANGUAGE plpgsql
SECURITY DEFINER
AS $$
DECLARE
    canonical BYTEA;
    event_kind TEXT;
BEGIN
    -- 1. HMAC verify (raises on mismatch); does not look at install_id.
    canonical := convert_to(
        (payload - 'version_hmac' - 'event_kind')::TEXT, 'UTF8');
    IF NOT telemetry.verify_versioned_hmac(
            canonical, claimed_version, claimed_hmac_hex) THEN
        RETURN jsonb_build_object('ok', false, 'error', 'rejected');
    END IF;

    -- 2. Dispatch. Each branch UPSERTs into one rollup table only.
    event_kind := payload->>'event_kind';
    CASE event_kind
        WHEN 'first_seen', 'upgrade' THEN
            PERFORM telemetry._bump_install(payload, event_kind);
        WHEN 'bug_report' THEN
            PERFORM telemetry._bump_bug(payload);
        WHEN 'reliability_snapshot' THEN
            PERFORM telemetry._bump_reliability(payload);
        ELSE
            RETURN jsonb_build_object('ok', false, 'error', 'unknown_event');
    END CASE;

    -- 3. Done. payload exits scope on RETURN; no INSERT, no UPDATE
    --    against any non-rollup table.
    RETURN jsonb_build_object('ok', true);
END;
$$;
```

### k-anonymity floor + publishing

The rollup tables CAN contain rows with `count < 5`. Publishing those
to the weekly digest CAN'T. The k-anon enforcement lives in
`scripts/telemetry-report.py` (already shipped: SM-166 round-2 fix
extended k-anon to per-report bug rows and recurrence rows).

For v2, the digest reads the rollup tables only — no per-event tables
exist anyway. The k-anon filter is unchanged.

### Logging guards

The function MUST NOT emit the payload to any log. Three guards:

1. **Postgres `log_min_duration_statement`** — Supabase default is
   high (e.g. 5s); the function returns in microseconds. No log
   entry.
2. **`pg_stat_statements`** — normalizes parameters; payload literal
   never appears.
3. **`SECURITY DEFINER` + revoked PUBLIC EXECUTE** — only the
   service_role and explicitly granted callers can invoke. The
   anon role uses PostgREST's RPC path with controlled binding.

If any of those guards drift (e.g. someone enables verbose
statement logging during debugging), it must be reverted before
`contribute()` runs again. Add an alert or runbook note.

### Rate limiting (Cloudflare Worker)

Unchanged from v1's design intent, with one strengthening: the rate-
limit key in KV is `rl:HMAC-SHA256(salt_today, ip)`, with a daily-
rotating salt that's NOT persisted. The IP is hashed inside the
Worker; KV stores only the hash. After the rotation, yesterday's
hashes are unlinkable to today's. No raw IP ever sits in KV.

This addresses SM-163's "raw IP in KV key" concern.

---

## Deployment

v2 is the current and only telemetry architecture. The earlier v1
schema (raw `ingest_envelope` → normalized `bug_report` /
`installation_event` → nightly rollup) was a leftover: SM-160
(0.9.18-dev) deleted the client-side `SendReport`, so no released
binary has posted to v1 endpoints since then. The Cloudflare Worker
no longer exposes v1 paths (0.9.7x-dev).

Deployment is a single operator session:

1. Apply `docs/operations/sql/drop-v1-leftover.sql` to drop the v1
   tables / functions / views / cron jobs that exist on Supabase
   from earlier deploys. Idempotent (`DROP IF EXISTS` everywhere);
   skips silently if the project is fresh.
2. Apply `docs/telemetry-v2.sql` to create the v2 rollup tables,
   ENUMs, `telemetry.contribute()` RPC, and the
   `verify_versioned_hmac` shared function.
3. Set `RATE_LIMIT_SALT_SECRET` on the Worker (`npx wrangler secret
   put RATE_LIMIT_SALT_SECRET`) and `npx wrangler deploy` from
   `worker/`.
4. Run `python3 scripts/telemetry-v2-smoke-test.py --via-worker` to
   verify the four standard cases (bad HMAC / good HMAC / schema
   violation / unknown event / retired forget).

The full step-by-step is in
[`docs/operations/deploy-telemetry-v2.md`](./operations/deploy-telemetry-v2.md).

There is no soak window, no cutover, no scheduled drop. v1 was
never wired to a live client; treating its server-side residue as
"leftover" rather than "running" collapses what was a four-phase
runbook into one operator session.

---

## What this retires from the deferral list

| Deferred bug | v1 status | v2 status |
|---|---|---|
| **SM-157** (`smirror telemetry` runtime CLI) | Open | **Shipped 0.9.83-dev.** Five subcommands: `none` / `standard` / `reliability` / `status` / `policy`. No `forget`. |
| **SM-158** (`report-bug --submit` pipeline) | Open | **Shipped 0.9.89-dev.** End-to-end verified live against the production Worker. See `docs/SM-158-report-bug-submit-plan.md`. |
| **SM-161** (Worker → ingest/normalization) | Open | **Retired.** No normalization; Worker calls one RPC. |
| **SM-162** (HMAC envelope binding) | Critical, deferred | **Downgraded to minor.** Replay can only over-count an aggregate; counters are monotonic and rate-limited. No exfiltration vector. |
| **SM-163** (Worker rate-limit raw-IP) | Open | **Closed 0.9.84-dev.** Daily-salted IP hash with `RATE_LIMIT_SALT_SECRET`. The Worker no longer falls back to raw-IP keys when the salt is missing — rate-limiting is skipped entirely (with a log warning) so the privacy invariant always holds. |
| **SM-168** (MSI build embeds telemetry key) | Open | **Shipped before this round.** `internal/telemetry/hmac.go` reads `buildKey` from a build-time `-ldflags -X` injection; CI computes the per-version derived key from `SMIRROR_TELEMETRY_MASTER_KEY`. `smirror version` shows an 8-char fingerprint or `none`. |
| **SM-172** (retention janitor purges normalized text) | Shipped 0.9.18-dev | **Obsolete.** v1 tables are dropped by `drop-v1-leftover.sql` as part of the standard deploy; v2 has nothing to purge (no raw stored). |
| `smirror telemetry forget` (SM-157 sub-design) | Designed | **Deleted from design.** No record to forget. |

**As of 2026-05-02 the v2 server is live** at
`https://smirror-telemetry.selectivemirror.workers.dev`. Phase A
(schema apply) and Phase B (Worker deploy) of the runbook were
executed; smoke tests + an SM-158 end-to-end through real
`smirror.exe` both passed and their rows were cleaned up. Two
deploy-day fixes were rolled into the source: `GRANT EXECUTE ON
telemetry.contribute() TO anon` (the Worker authenticates as the
anon role, not service_role), and replacing `PRIMARY KEY` with
`UNIQUE NULLS NOT DISTINCT` on `installation_daily_rollup` and
`reliability_daily_rollup` (so first_seen events with NULL
prior_version, and reliability snapshots with NULL
most_common_anomaly_kind, can land cleanly).

---

## Trade-offs explicitly accepted

In adopting v2, the project commits to:

1. **Bug-report narratives live on GitHub, not on telemetry servers.**
   The maintainer's debugging workflow uses GitHub's search, not a
   private grep over a local archive.
2. **No re-aggregation.** Buckets chosen at submission time are the
   only buckets the rollup will ever know about. Adding a new
   dimension requires the next release; historical data does not
   gain the new dimension retroactively.
3. **No "active install" precise count.** Replaced by 30-day event
   volume. HLL is the upgrade path if needed.
4. **Per-event audit / re-classification is gone.** Client-side
   taxonomy at submission time is the classification.
5. **Some maintainer affordances disappear.** "Let me read what
   users actually wrote in last week's bug reports" → goes to
   GitHub Issues UI. "Let me re-aggregate by `os_version`" →
   not possible without a release that adds the bucket.

In return:

1. **Zero personal data on disk.**
2. **No GDPR Art 15/17/16/20 obligations** against the telemetry
   stack.
3. **Audit story is a schema dump.**
4. **No retention janitor needed.**
5. **No `forget` command needed.**
6. **Maintainer-abandonment is harmless** — there's nothing left
   behind.
7. **PRIVACY.md gets shorter and stronger.**

---

## Threat model

### Replay attack on a captured signed payload

An attacker captures a valid HMAC-signed contribution and replays
it. Result: the matching aggregate counter increments by 1 (or by
N for N replays). The counter is monotonic — it can only grow.
There is no victim whose data is exfiltrated; there is no row
created. The maximum harm is a slightly inflated count.

The Worker's rate-limit caps replay velocity. The HMAC version-
derived key means a given binary's signatures are only valid for
that version; once the version is revoked, all replayed signatures
become invalid.

**Severity**: low. SM-162 (HMAC envelope binding) was a critical
concern in v1 because envelope tampering could associate a
signature with different ingest metadata in stored rows; in v2
there are no stored rows, so the worst case is over-counting.

### Malformed payload

The function rejects with `{ok: false, error: ...}` and does not
increment any counter. No state change.

**Ordering note**: the `not_object` type guard fires *before* the
HMAC verification — a payload that isn't even a JSON object is
rejected at the type gate before any crypto runs. The "every
contribution is gated by HMAC" framing is therefore precise to: every
contribution **with a parseable, JSON-object payload shape** is gated
by HMAC. The pre-HMAC guard cannot leak personal data (the response
just says "not an object") and cannot create a row (no INSERT path
runs); it just fails fast on garbage so the function doesn't waste
crypto cycles on it. The defensive ordering is the right one for a
high-volume edge function. See `docs/telemetry-v2.sql` for the
literal sequence.

### HMAC key compromise (single-binary)

Same as v1: only that version's contributions are forgeable. The
Vault-stored master key derives per-version keys; revoking a
version invalidates only its key. Other versions remain valid.

### Postgres compromise (catastrophic)

If the Postgres instance is compromised, the attacker sees the
schema and the rollup contents. The schema reveals what kinds
of events SelectiveMirror counts; the rollups reveal aggregate
counts. **No personal data is recoverable** because none was
stored.

This is the architectural property the entire design exists to
enforce.

### Maintainer compromise

If the maintainer's account is compromised, the attacker can
modify the schema, drop tables, or insert fake data. They CANNOT
recover personal data that was never stored.

### Worker compromise

A compromised Worker could drop contributions or forward fake
ones. Forged contributions still need a valid HMAC, which is
keyed per-version and embedded in signed binaries; an attacker
without the key can only DoS (drop) or rate-limit-abuse, not
exfiltrate.

### Side-channel: pg_stat_statements

Already covered in "Logging guards." Parameter binding via
PostgREST RPC is normalized; payload values are not captured.

---

## Files of record

- **This file**: `docs/telemetry-architecture-v2.md` — design.
- **Schema**: `docs/telemetry-v2.sql` — the v2 rollup tables, ENUMs,
  `telemetry.contribute()` RPC, and `verify_versioned_hmac`.
- **v1 cleanup**: `docs/operations/sql/drop-v1-leftover.sql` — drops
  the v1 server-side residue. Run before `telemetry-v2.sql` on any
  Supabase project that pre-dates v2.
- **Deploy runbook**: `docs/operations/deploy-telemetry-v2.md` —
  step-by-step operator session (drop v1, apply v2, deploy Worker,
  smoke test).
- **User-facing contract**: `docs/PRIVACY.md` — plain-language version
  of this design.
- **CLI design**: `docs/cli-telemetry-command.md` — `forget`
  subcommand is not in the surface (no record to forget).
- **Deferral plans**: `docs/SM-157-telemetry-cli-plan.md`,
  `docs/SM-158-report-bug-submit-plan.md`,
  `docs/SM-162-hmac-envelope-binding-plan.md`,
  `docs/SM-server-side-deferred.md`.

---

## Future maintainers

If you're reading this in 2028 and considering "let's add a small
table for X just temporarily" — the answer is no. Every table that
exists is one a regulator can ask about. The architecture's value is
exactly that there is no answer to give beyond `\dt`.

If you genuinely need to persist personal data, that's a different
project. Fork it, build it cleanly under a different name, with a
proper DPIA and consent flow. Don't sneak it into SelectiveMirror's
schema; the privacy promise here is structural, and structural
promises are the only kind users can verify.
