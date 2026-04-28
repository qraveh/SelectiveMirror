# SelectiveMirror Privacy Policy

**Audience**: end users of SelectiveMirror.
**Plain-language version of**: `docs/telemetry-microserver-architecture.md` (the technical spec).
**Last updated**: 2026-04-28.

---

SelectiveMirror is a free open-source tool. It synchronizes files between your machine and cloud backends. We try hard to be respectful of what we touch and what we send anywhere.

This document tells you in plain English: what we collect, what we don't, when, and how to opt in or out.

---

## Default: None

**If you do nothing, nothing leaves your machine.** This is the default and it does not change without you actively choosing to share.

---

## Three tiers

You pick one of three tiers. Two are silent in steady state; only one shares data while you work, and even that one is bucketed and infrequent.

| Tier | Bug-report submission | Install events | Reliability deltas | Default? |
|------|----------------------|----------------|---------------------|----------|
| **None** | ❌ disabled (use `--stdout` for local file) | ❌ | ❌ | **✅ default** |
| **Standard** | ✅ per-event approval | ✅ first_seen + upgrade with structural fields | ❌ | |
| **Reliability** | ✅ per-event approval | ✅ same as Standard | ✅ bucketed deltas at upgrade time | |

You can change tiers at any time:

```
smirror telemetry none          # opt out completely
smirror telemetry standard      # opt in to bug reports + install census
smirror telemetry reliability   # opt in to all of the above + reliability deltas
smirror telemetry status        # show current tier and what's been sent
smirror telemetry policy        # opens this file
```

There is also a **one-shot escape**: if you're at None and want to send a single bug report without changing tier, run `smirror report-bug --submit --one-shot`. The report is sent with explicit per-event consent; your tier remains None; nothing else is sent now or later.

---

## The three promises

### Tier 1 — None

> **Run SelectiveMirror in private. Nothing leaves your machine — not a heartbeat, not a version check, not a bug report.**
>
> When you pick None, smirror runs entirely offline as far as we're concerned. The `report-bug --submit` command is disabled (you can still generate reports locally with `--stdout` and paste them into a GitHub issue manually). No install events. No update pings. Crashes stay in `~/.selectivemirror/crashes/` where they always do.
>
> *This is a complete product. We built it this way on purpose.*

### Tier 2 — Standard

> **Two anonymous events over the lifetime of this install, plus bug reports you write yourself.**
>
> One event when smirror first runs on this machine, one when it upgrades. Each carries: a random install UUID, smirror version, OS family + version, CPU arch, install method, and the *types* of backends you configured. Plus structural facts: how many mirrors (bucketed), what background mode, which features you've enabled (booleans only — never values).
>
> No daily check-ins. No usage counters. No telemetry while you work. Bug reports remain per-event opt-in: you see what's about to be sent, you approve, you submit.
>
> *This tells the maintainer which versions are alive in the wild. Nothing more.*

### Tier 3 — Reliability

> **Be a co-developer. Help see what we can't see from one machine.**
>
> SelectiveMirror is maintained by one person on a single Windows laptop. Reliability adds aggregated, anonymized signals to the install events you already share at Standard: anomaly counts grouped by type, sync success/failure ratios in broad buckets, restart counts (capped), state-DB size in size ranges. **Sent only when your version changes** — never on a schedule, never while you work.
>
> No filenames, no paths, no contents, no identity. Bug reports still require your per-event approval; this just adds the boring statistics that turn "works on my machine" into "works on yours, too."
>
> *Twenty minutes of weekly sync data does more for the next release than a dozen forum posts. If you're rooting for this project, this is how rich data helps the maintainer ship faster.*

---

## What we collect, by tier

### Tier 1 — None
**Nothing.** No events, no IDs, no version checks, no error reports.

### Tier 2 — Standard

#### `first_seen` event (once per install)
- `install_id` (random UUID, generated locally; not derived from identity)
- `client_version` (smirror version)
- `os_family` (`windows` / `linux` / `macos`)
- `os_detail` (e.g. "Windows 11 Pro 24H2")
- `arch` (`amd64` / `arm64`)
- `install_method` (`msi` / `winget` / `zip` / `selfupdate` / `manual` / `unknown`)
- `mirror_count_bucket` (`0` / `1` / `2-5` / `6-20` / `21+`)
- `background_mode` (`foreground` / `service` / `task` / `unknown`)
- `delete_policy` (`ignore` / `delete` / `quarantine`)
- `has_hooks` (boolean)
- `has_filters` (boolean)
- `has_alert_webhook` (boolean — never the URL)
- `has_bandwidth_limit` (boolean — never the value)
- `rclone_version` (e.g. "v1.73.5")
- `reported_at` (UTC timestamp)

#### `upgrade` event (each time smirror's version changes)
Same fields as `first_seen`, plus:
- `prior_version` (the version smirror was running before)
- `days_since_first_seen_bucket` (`1-7` / `8-30` / `31-90` / `91-365` / `>365`)

#### `bug_report` (when YOU run `smirror report-bug --submit`)
The full sanitized report you reviewed and approved before sending.

### Tier 3 — Reliability

Everything from Standard, plus a `reliability_snapshot` attached to each `upgrade` event:

- `anomaly_counts_30d` — JSON object: anomaly kind → count (e.g. `{"watcher_error": 3, "ghost_leak": 0}`). Counts only, no payloads, no timestamps.
- `sync_attempts_bucket` (`<100` / `100-1k` / `1k-10k` / `10k-100k` / `100k+`)
- `sync_failures_bucket` (same buckets)
- `restart_count_since_last_upgrade` (integer, **capped at 1000**)
- `max_queue_depth_bucket` (`<100` / `100-1k` / `1k-10k` / `10k+`)
- `dead_letter_count_bucket` (`0` / `1-10` / `11-100` / `100+`)
- `state_db_size_bucket` (`<10MB` / `10-100MB` / `100MB-1GB` / `1GB+`)

These are bucketed, not raw, to prevent fingerprinting via extreme values.

---

## What we never collect — at any tier

Under no circumstances does SelectiveMirror collect:

- **Your name, email, or any identity.** Not from your OS user account, not from your config.
- **File paths.** Source paths in your config are not sent; remote paths are fully redacted.
- **File contents.** Ever.
- **Filenames.** Ever.
- **URLs of your remotes.** Only the backend type ("gdrive" vs "s3").
- **Credentials.** rclone tokens, API keys, passwords — none of these reach the wire.
- **Hostnames** of your machine.
- **MAC addresses, serial numbers, or hardware fingerprints.**
- **Timezone, locale, language tag, or geographic data.** All timestamps are UTC.
- **Your IP address.** The Cloudflare edge proxy may briefly log IPs for rate-limiting (60-second window), but they're never written to the database.
- **Filter pattern strings or hashes of them** — workload structure leakage.
- **Per-mirror identifying labels** (project names, etc.).
- **Bytes mirrored, files synced, uptime, error counts** as continuous metrics. These are explicitly off the table at every tier.

---

## Forward commitment

SelectiveMirror's telemetry scope will not expand silently. The following constraints bind future versions:

- **No heartbeats, ever.** The only events that will ever be sent on the install-telemetry channel are structural lifecycle events (`first_seen`, `upgrade`). No periodic phone-home, no usage pings, no "active install" beacons.
- **No accumulated counts.** Bytes mirrored, files synced, uptime, error counts, and any other accumulating metric are out of scope. They will not be added.
- **No geography.** Timezone, locale, language tag, and IP-derived data are out of scope.
- **No hardware fingerprint.** CPU, memory, disk class are out of scope.
- **No workload structure.** Filter patterns, mirror path names, configuration complexity beyond coarse boolean has-feature flags are out of scope.
- **Bucketization is mandatory** for any numeric field. Raw counts that could uniquely identify a heavy user will not appear in the schema.
- **Per-tier upper bounds.** Each tier carries an explicit field list (above) and a commitment that fields will only be removed or made coarser, never added or made finer, without re-consent.
- **No silent tier migration.** Existing opt-ins do not roll forward into a broader scope. If the scope of any tier changes, every opted-in install is asked again, and "decline" is the safe default.
- **The k-anonymity floor for any published aggregate is 5.** Cells with fewer contributors are suppressed in the digest, not estimated.
- **Reliability cadence lock.** Tier 3 reliability deltas fire ONLY on the `upgrade` event. There is no heartbeat, no daily check-in, no scheduled stats push. Changing this cadence requires re-prompting every consenting user.
- **Bundled-consent prohibition.** Bug-report submission and install-event collection are *separable purposes*. The Standard and Reliability tiers offer them as a package because users requested simplicity, but a `--one-shot` per-event submission path is always available so users at None can send single reports without committing to ongoing collection.

If a future maintainer wishes to relax any of these, the change must land in this document in the same commit as the schema change, with a clear changelog entry, and a one-time re-consent flow for existing users.

---

## How to opt in / opt out

```
smirror telemetry none          # opt out completely
smirror telemetry standard      # opt in to bug reports + install census
smirror telemetry reliability   # opt in to all of the above + reliability deltas
smirror telemetry status        # show current tier and what's been sent
smirror telemetry policy        # open this file
```

**If you change from a higher tier to None**:
- Any queued telemetry events on disk are deleted immediately (not sent).
- Server-side data is **not** auto-deleted. To request server-side deletion, run `smirror telemetry forget` — this sends a single signed deletion request keyed on your install_id; the maintainer honors it within 30 days.

**If you change from None to a higher tier**:
- No backfill. Past data isn't reconstructed; the next event flows under the new tier.
- For a transition from None, the next `first_seen` event is emitted to make your install visible at the new tier (one-time, on tier change).

**One-shot bug submission** (for users staying at None):
```
smirror report-bug --submit --one-shot
```
This sends one bug report with explicit per-event consent. Your tier remains None; nothing else is sent now or in the future.

---

## Where the data lives

- **Backend**: Supabase PostgreSQL, hosted in West EU (Ireland). Account owned by Raveh Neeman personally.
- **Edge proxy**: Cloudflare Worker at `smirror-telemetry.selectivemirror.workers.dev`, free tier.
- **Retention**: raw payloads are stripped from the database after 90 days; aggregate counts retained indefinitely.

The choice of EU region means data is subject to GDPR by default.

---

## Your rights

- **Withdraw consent at any time**: `smirror telemetry none` (immediate effect; queued events are deleted from disk).
- **Reset your install ID**: delete `~/.selectivemirror/state.db` and restart smirror. A fresh anonymous UUID is generated on next run.
- **Request deletion of past data**: run `smirror telemetry forget` — sends a signed deletion request keyed on your install_id, processed within 30 days. Or open a GitHub issue at https://github.com/qraveh/SelectiveMirror/issues with the first 8 chars of your install_id.

---

## How telemetry data is used

The maintainer (Raveh) reviews aggregated telemetry weekly via an automated digest committed to the repo at `docs/telemetry/weekly-*.md`. Key questions answered:

- Which versions are active in the wild? (helps prioritize backports, deprecations)
- Which bug signatures recur across multiple installs? (helps prioritize fixes)
- What install channels are used? (helps decide where to invest distribution effort)
- (At Reliability tier) Which anomaly kinds are spiking on the latest release? (helps catch regressions fast)

The data is NOT used to:
- Track individual users
- Sell to third parties
- Send marketing
- Generate ML/AI training datasets
- Build behavioral profiles

---

## How to verify

- **Source code**: Open. https://github.com/qraveh/SelectiveMirror
- **Telemetry client code**: `internal/telemetry/` — read it.
- **Server schema**: `docs/telemetry-microserver.sql` — read it.
- **Validation script**: `test/telemetry-validation.py` — proves the security model.
- **Weekly digests**: `docs/telemetry/weekly-*.md` — see exactly what aggregates the maintainer reviews.

If you find any discrepancy between this policy and the actual behavior, it's a bug. Please file it.

---

## Contact

For privacy questions or data deletion requests:

- GitHub issue: https://github.com/qraveh/SelectiveMirror/issues
- Email: `smirror@qodeh.com`
- Security-sensitive issues: see `SECURITY.md`
