# SelectiveMirror Privacy Policy

**Audience**: end users of SelectiveMirror.
**Plain-language version of**: `docs/telemetry-architecture-v2.md` (the technical spec).
**Last updated**: 2026-04-29.

---

SelectiveMirror is a free open-source tool. It synchronizes files between your machine and cloud backends. We try hard to be respectful of what we touch and what we send anywhere.

This document tells you in plain English: what we collect, what we don't, when, and how to opt in or out.

---

## The shape of the promise

**SelectiveMirror's telemetry never stores personal data — by construction.**

When you opt in (Standard or Reliability), every contribution your client makes is processed inside a single database transaction and then discarded. The categorical fields are extracted, the matching anonymous counter is incremented, and the original payload exits the database with the connection. There is no table for raw events, no row per install, no archive of bug-report text.

What this means in practice:

- There is **nothing to delete**, because nothing is stored. There is no "right to be forgotten" command — there is no record to forget.
- A regulator's audit answers itself: the schema is the privacy story. The tables that exist are aggregate counters; the tables that *don't* exist are the ones that would worry anyone.
- If the maintainer abandons the project, **no personal data is left behind**. The only residue is anonymous counts.

This is the strongest privacy posture an open-source tool can offer. It's also the simplest. The cost (real, accepted by design) is that some things move out of the telemetry path entirely — see "What is no longer telemetry" below.

---

## Default: None

**If you do nothing, nothing leaves your machine.** This is the default and it does not change without you actively choosing to share.

---

## Three tiers

| Tier | What you contribute | Default? |
|------|---------------------|----------|
| **None** | Nothing. No events, no version checks, no pings. | **✅ default** |
| **Standard** | Anonymous categorical counts: install / upgrade / bug-report bucket increments. | |
| **Reliability** | Standard plus operational-health bucket increments at upgrade events. | |

You can change tiers at any time:

```
smirror telemetry none          # opt out completely
smirror telemetry standard      # opt in to install + bug counts
smirror telemetry reliability   # opt in to all of the above + reliability counts
smirror telemetry status        # show current tier
smirror telemetry policy        # open this file
```

There is also a **one-shot escape**: if you're at None and want to contribute a single bug-report count without changing tier, run `smirror report-bug --submit --one-shot`. The contribution is sent with explicit per-event consent; your tier remains None; nothing else is sent now or later.

**Note.** Under v2, the privacy difference between Standard and Reliability is the *number of dimensions* you contribute to, not the *retention* of what you contribute. Both are anonymous-by-construction. The tier UX preserves user choice over how much help you'd like to be.

---

## How a contribution works (the technical promise)

When the client sends an event:

1. **Client builds a payload** of categorical fields only — bucket choices, version strings, structural booleans. No paths, no IDs of yours, no narratives.
2. **Client signs** with a per-version HMAC key embedded in the binary, then POSTs to the Cloudflare edge.
3. **Cloudflare Worker** validates the envelope shape, rate-limits using a daily-salted hash of the IP (the salt rotates and is not stored), and forwards to Supabase.
4. **Supabase function** verifies the HMAC, dispatches to the matching anonymous counter, increments by 1, returns. The payload is held in memory for the duration of one function call (microseconds), then discarded.
5. **What's on disk afterwards**: the counter is one larger. Nothing else.

The full technical detail is in `docs/telemetry-architecture-v2.md`. The function body is in `docs/telemetry-v2.sql`.

---

## What we collect, by tier

All "data" below means: a counter incremented by 1 in a row keyed on the listed dimensions. None of these fields are stored as individual records — only as bucket keys aggregated across all opted-in users.

### Tier 1 — None

**Nothing.** No events, no version checks, no error reports.

### Tier 2 — Standard

#### `first_seen` event (fires once on a new install)
Bucket dimensions:
- `install_method` (`msi` / `winget` / `zip` / `selfupdate` / `manual` / `unknown`)
- `os_family` (`windows` / `linux` / `macos`)
- `client_version` (smirror version)
- `mirror_count_bucket` (`0` / `1` / `2-5` / `6-20` / `21+`)
- `background_mode` (`foreground` / `service` / `task` / `unknown`)
- `delete_policy` (`ignore` / `delete` / `quarantine`)
- `has_hooks` / `has_filters` / `has_alert_webhook` / `has_bandwidth_limit` (booleans only)
- `rclone_version` (e.g. "v1.73.5")

#### `upgrade` event (fires when smirror's version changes)
Same dimensions as `first_seen`, plus:
- `prior_version` (the version smirror was running before)
- `days_since_first_seen_bucket` (`1-7` / `8-30` / `31-90` / `91-365` / `>365`)

#### `bug_report` event (when YOU run `smirror report-bug --submit`)
Bucket dimensions:
- `bug_kind` (closed taxonomy you pick from at submit time — e.g. `sync` / `rclone` / `watcher` / `config` / `service`)
- `bug_surface` (closed taxonomy — e.g. `windows-fs` / `gdrive` / `s3` / `sftp` / `local`)
- `client_version`
- `severity_hint` (`info` / `warning` / `error` / `critical`)
- `source` (`report_bug` or `crash_report`)
- `submitted_tier` (`standard` / `reliability` / `one_shot`)

**The bug-report narrative is not in this list.** See "What is no longer telemetry" below.

### Tier 3 — Reliability

Everything from Standard, plus a `reliability_snapshot` increment fired on each `upgrade` event:

- `client_version`
- `anomaly_count_bucket` (`0` / `1-5` / `6-25` / `26-100` / `100+`) — total across all kinds
- `most_common_anomaly_kind` (string from the closed anomaly taxonomy, or NULL when there are no anomalies)
- `sync_attempts_bucket` (`<100` / `100-1k` / `1k-10k` / `10k-100k` / `100k+`)
- `sync_failures_bucket` (same buckets)
- `restart_count_bucket` (`0` / `1-5` / `6-25` / `26-100` / `100+`)
- `max_queue_depth_bucket` (`<100` / `100-1k` / `1k-10k` / `10k+`)
- `dead_letter_count_bucket` (`0` / `1-10` / `11-100` / `100+`)
- `state_db_size_bucket` (`<10MB` / `10-100MB` / `100MB-1GB` / `1GB+`)

These are bucketed, not raw, to prevent fingerprinting via extreme values. The per-anomaly-kind map (`{"watcher_error":3,"ghost_leak":0}`) is collapsed at the client to two scalars (the bucketed total, and the leading kind) before contribution.

---

## What is no longer telemetry

Two things were in v1 telemetry and have been **moved out** in v2:

### 1. Bug-report narratives → GitHub Issues

When you use `smirror report-bug --browser`, your written narrative is filed as a GitHub Issue on `github.com/qraveh/SelectiveMirror`. That content is hosted by GitHub Inc. and governed by [GitHub's Privacy Statement](https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement), not by this document.

**You control your GitHub Issue content.** You can edit, delete, or request platform-level erasure through your GitHub account or via [GitHub's privacy support](https://support.github.com/contact/privacy).

**SelectiveMirror does not store copies of your GitHub Issues.** Issue text is never quoted in changelogs, weekly digests, or other artifacts the project publishes. References to issues, when they appear, are by URL or number only.

The only thing SelectiveMirror's telemetry knows about your bug report (when you also opt to submit a count via `--submit`) is the categorical bucket above — `(bug_kind, bug_surface, client_version, severity_hint, source, submitted_tier)`. No narrative, no GitHub link, no install_id.

The smirror client sanitizes the report bundle (paths, mirror names, credentials, remote URIs, log lines) **before** prefilling the GitHub Issue draft, so even the data you submit voluntarily to GitHub has been redacted by the client first.

### 2. Per-install history / "active install" precise count

v1 maintained a row per `install_id` so the maintainer could measure "active in the last 30 days" as a distinct count. v2 has no per-install row and no `install_id` retention. The replacement metric is **30-day event volume** — the total of first_seen + upgrade + bug-report + reliability events across the last 30 days. This isn't the same number, but it answers the same question ("is the project alive, growing, slowing").

If a stronger cardinality measure is ever needed, the architectural upgrade path is HyperLogLog cardinality sketches (Postgres `hll` extension). HLL sketches are not personal data — they're cardinality estimators — so the architecture stays clean. The current design ships without HLL.

---

## What we never collect — at any tier

Under no circumstances does SelectiveMirror collect:

- **Your name, email, or any identity.** Not from your OS user account, not from your config.
- **File paths.** Source paths in your config are not sent; remote paths are fully redacted in any artifact.
- **File contents.** Ever.
- **Filenames.** Ever.
- **URLs of your remotes.** Only the backend type ("gdrive" vs "s3") appears in any artifact.
- **Credentials.** rclone tokens, API keys, passwords — never reach the wire.
- **Hostnames** of your machine.
- **MAC addresses, serial numbers, or hardware fingerprints.**
- **Timezone, locale, language tag, or geographic data.** All timestamps are UTC.
- **Your IP address.** The Cloudflare edge proxy hashes IPs with a daily-rotating salt for rate-limiting; the salt is not retained, so the hashes are unlinkable across days. No raw IP ever sits in storage.
- **`install_id`.** It is verified for HMAC purposes during the function call and discarded the same millisecond. It is not stored, not indexed, not joined.
- **Filter pattern strings or hashes of them** — workload structure leakage.
- **Per-mirror identifying labels** (project names, paths, etc.).
- **Bytes mirrored, files synced, uptime, error counts** as continuous metrics. These are explicitly off the table at every tier.

---

## Forward commitment

SelectiveMirror's telemetry scope will not expand silently. The following constraints bind future versions:

- **No raw events stored, ever.** Adding a "let's just keep raw events for a few days" table requires re-consent. Storage of personal data is the property the architecture exists to prevent; any change to it is a change to the contract.
- **No heartbeats, ever.** The only events that will ever be sent on the install-telemetry channel are structural lifecycle events (`first_seen`, `upgrade`). No periodic phone-home, no usage pings, no "active install" beacons.
- **No accumulated counts.** Bytes mirrored, files synced, uptime, error counts, and any other accumulating metric are out of scope. They will not be added.
- **No geography.** Timezone, locale, language tag, and IP-derived data are out of scope. The Worker's salted-IP-hash for rate-limiting does not count — it's not retained and not joinable.
- **No hardware fingerprint.** CPU, memory, disk class are out of scope.
- **No workload structure.** Filter patterns, mirror path names, configuration complexity beyond coarse boolean has-feature flags are out of scope.
- **Bucketization is mandatory** for any numeric field. Raw counts that could uniquely identify a heavy user will not appear in the schema.
- **Per-tier upper bounds.** Each tier carries an explicit bucket-dimension list (above) and a commitment that fields will only be removed or made coarser, never added or made finer, without re-consent.
- **No silent tier migration.** Existing opt-ins do not roll forward into a broader scope. If the bucket-dimension set of any tier changes, every opted-in install is asked again, and "decline" is the safe default.
- **The k-anonymity floor for any published aggregate is 5.** Cells with fewer contributors are suppressed in the digest, not estimated.
- **Reliability cadence lock.** Tier 3 reliability deltas fire ONLY on the `upgrade` event. There is no heartbeat, no daily check-in, no scheduled stats push. Changing this cadence requires re-prompting every consenting user.
- **Bundled-consent prohibition.** Bug-report submission and install-event collection are *separable purposes*. The Standard and Reliability tiers offer them as a package because users requested simplicity, but a `--one-shot` per-event submission path is always available so users at None can contribute a single count without committing to ongoing collection.
- **Bug-report narratives stay on GitHub.** SelectiveMirror's published artifacts (changelogs, digests, READMEs, posts) reference issues by URL or number only — never by quotation. This rule preserves the property that the maintainer is not a data controller for narrative content.

If a future maintainer wishes to relax any of these, the change must land in this document in the same commit as the schema change, with a clear changelog entry, and a one-time re-consent flow for existing users.

---

## How to opt in / opt out

```
smirror telemetry none          # opt out completely
smirror telemetry standard      # opt in to install + bug counts
smirror telemetry reliability   # opt in to all of the above + reliability counts
smirror telemetry status        # show current tier
smirror telemetry policy        # open this file
```

**If you change from a higher tier to None**:
- Any queued contribution events on disk are deleted immediately (not sent).
- There is no server-side data to delete on your behalf, because no record of you was ever stored.

**If you change from None to a higher tier**:
- No backfill. Past data isn't reconstructed; the next event flows under the new tier.
- For a transition from None, the next `first_seen` event is contributed to mark your install visible at the new tier (one-time, on tier change).

**One-shot bug submission** (for users staying at None):
```
smirror report-bug --submit --one-shot
```
This contributes one bug-report count with explicit per-event consent. Your tier remains None; nothing else is contributed now or in the future.

---

## Where the data lives (and doesn't)

- **Backend**: Supabase PostgreSQL, hosted in West EU (Ireland). Account owned by Raveh Neeman personally.
- **Edge proxy**: Cloudflare Worker at `smirror-telemetry.selectivemirror.workers.dev`, free tier.
- **What's on the database disk**: only the rollup tables (`installation_daily_rollup`, `bug_daily_rollup`, `reliability_daily_rollup`). Schema dump in `docs/telemetry-v2.sql`.
- **What is NOT on disk**: raw payloads (no table for them), `install_id` (verified and discarded), IP addresses (hashed and unrelated to storage), bug-report narratives (GitHub Issues only).
- **Retention**: aggregate counts are retained indefinitely. There are no raw payloads to retain or strip.

The choice of EU region means data is subject to GDPR by default. The schema satisfies GDPR by storing no personal data.

---

## Your rights

- **Withdraw consent at any time**: `smirror telemetry none` (immediate effect; queued events are deleted from disk).
- **Reset your install ID**: delete `~/.selectivemirror/state.db` and restart smirror. A fresh anonymous UUID is generated on next run. (Note: since `install_id` is never stored on the server, the reset is purely client-local.)
- **Erasure of "your" telemetry data**: there is no such request to make under v2 because there is no record of you to erase. Any aggregate counter your contributions touched cannot be unwound — the counter has no memory of which contributions came from where.
- **Erasure of bug-report narratives** filed via `--browser` to GitHub Issues: handled by GitHub. Edit or delete the issue from your GitHub account, or contact [GitHub's privacy support](https://support.github.com/contact/privacy).
- **Concerns about the architecture itself**: open a GitHub issue or email `smirror@qodeh.com`. The contract is the architecture; if behavior diverges from the contract, that's a bug.

---

## How telemetry data is used

The maintainer (Raveh) reviews aggregated telemetry weekly via an automated digest committed to the repo at `docs/telemetry/weekly-*.md`. Key questions answered:

- Which versions are active in the wild? (helps prioritize backports, deprecations)
- Which (kind, surface) bug buckets recur or grow? (helps prioritize fixes)
- What install channels are used? (helps decide where to invest distribution effort)
- (At Reliability tier) Which anomaly kinds dominate on the latest release? (helps catch regressions fast)

The data is NOT used to:
- Track individual users (it's not stored that way)
- Sell to third parties
- Send marketing
- Generate ML/AI training datasets
- Build behavioral profiles

---

## How to verify

- **Source code**: Open. https://github.com/qraveh/SelectiveMirror
- **Telemetry client code**: `internal/telemetry/` — read it.
- **Server schema (v2)**: `docs/telemetry-v2.sql` — read it. The `\dt telemetry.*` IS the privacy story.
- **Architecture**: `docs/telemetry-architecture-v2.md` — full detail.
- **Validation script**: `test/telemetry-validation.py` — proves the security model.
- **Weekly digests**: `docs/telemetry/weekly-*.md` — see exactly what aggregates the maintainer reviews.

If you find any discrepancy between this policy and the actual behavior, it's a bug. Please file it.

---

## Contact

For privacy questions or data-handling concerns:

- GitHub issue: https://github.com/qraveh/SelectiveMirror/issues
- Email: `smirror@qodeh.com`
- Security-sensitive issues: see `SECURITY.md`
