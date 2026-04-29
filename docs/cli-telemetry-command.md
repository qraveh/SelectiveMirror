# `smirror telemetry` — three-tier consent management

> **v2 architecture (2026-04-29).** Telemetry is stream-aggregate-and-
> discard. There is no `forget` subcommand because there is no server-
> side record to forget — every contribution is processed in a single
> Postgres transaction and the payload is discarded with the
> connection. The user-facing contract is in
> [`PRIVACY.md`](./PRIVACY.md); the architecture is in
> [`telemetry-architecture-v2.md`](./telemetry-architecture-v2.md).
> Earlier versions of this document specified a `forget` subcommand;
> it has been removed from the design.

## Purpose

Lets the user view and change their telemetry tier at runtime, independently of the choice made during MSI installation. The tier governs what (if anything) smirror sends to the telemetry endpoint.

This command is the runtime counterpart to the MSI installer's tier selection. It is the user's primary consent surface after install.

## Design context

The user-facing contract for each tier is in `docs/PRIVACY.md`. This document specifies the CLI shape and persistence. Round-3/4 panel decisions led to the three-tier model; round-5 (BMad panel, 2026-04-29) led to the v2 architecture above.

**Default tier**: **None**. If the user never runs this command, smirror sends nothing.

## Command shape

```
smirror telemetry status               Show current tier, last sent, queued
smirror telemetry none                 Opt out completely
smirror telemetry standard             Opt in to install + bug counts
smirror telemetry reliability          Opt in to all of the above + reliability counts
smirror telemetry policy               Open docs/PRIVACY.md (or print path)
smirror telemetry --help               Show help
```

Exit codes: 0 on success, 1 on error, 2 if requested action requires admin privileges that aren't available.

> **Removed**: `smirror telemetry forget`. Under v2 there is no
> server-side record to delete; "withdraw consent" via `smirror
> telemetry none` is the complete erasure path. Bug-report narratives
> filed via `--browser` to GitHub Issues are deletable by the user via
> their GitHub account or [GitHub's privacy
> support](https://support.github.com/contact/privacy) — see
> `PRIVACY.md` "Bug reports are not telemetry."

## Storage of the tier flag

**Single source of truth at runtime: state DB metadata.** The `meta` table key `telemetry_tier`, values one of: `'none'` (default) | `'standard'` | `'reliability'`.

### First run after install

On first run, smirror checks:

1. Is `telemetry_tier` set in state DB metadata? → use that.
2. Else, is the registry value `HKLM\Software\SelectiveMirror\TelemetryTier` set? → copy into state DB (`'none' | 'standard' | 'reliability'`), then proceed.
3. Else, default to `'none'`.

After first run, only the state DB is consulted. The registry value is never updated — runtime changes affect only the state DB. This avoids requiring admin to flip tiers at runtime.

### Subsequent runs

Read from state DB only. Registry is ignored after the first-run migration.

## `smirror telemetry status` output

### At None tier (default)

```
$ smirror telemetry status
tier:           none
source:         default (no tier ever set)
bug reports:    disabled (use --stdout for local file, or --submit --one-shot)
install events: not sent
reliability:    not sent

To opt in:
  smirror telemetry standard      Bug reports + 2 install events ever
  smirror telemetry reliability   Above + bucketed reliability deltas

To send a single bug report without changing tier:
  smirror report-bug --submit --one-shot

Read what each tier collects:
  smirror telemetry policy
```

### At Standard tier

```
$ smirror telemetry status
tier:              standard
source:            user (smirror telemetry standard, 2026-04-26 13:45 UTC)
bug reports:       enabled, per-event approval
install events:    sent at first_seen and on each upgrade
reliability:       not sent
last sent:         first_seen   2026-04-26 13:50 UTC  (v0.9.15-dev, accepted)
                   upgrade      (none yet)
queued events:     0
dead-letter:       0

Change tier:
  smirror telemetry none          Stop all telemetry; purge queued events
  smirror telemetry reliability   Add bucketed reliability deltas at upgrades

Note: bug reports submitted via `report-bug --submit` are always per-event
explicit user approval, regardless of tier.
```

### At Reliability tier

```
$ smirror telemetry status
tier:              reliability
source:            user (smirror telemetry reliability, 2026-04-28 09:14 UTC)
bug reports:       enabled, per-event approval
install events:    sent at first_seen and on each upgrade
reliability:       bucketed deltas attached to upgrade events ONLY
                   (no schedule, no heartbeat — fires only on version change)
last sent:         first_seen          2026-04-28 09:15 UTC  (v0.9.15-dev, accepted)
                   upgrade + snapshot  (none yet — fires on next version change)
queued events:     0
dead-letter:       0

Reliability snapshot fields (sent only at upgrade):
  - anomaly_counts_30d (kind -> count, no payloads)
  - sync_attempts_bucket / sync_failures_bucket
  - restart_count (capped at 1000)
  - max_queue_depth_bucket / dead_letter_count_bucket
  - state_db_size_bucket

Change tier:
  smirror telemetry none      Stop all telemetry; purge queued events
  smirror telemetry standard  Drop reliability deltas; keep install census
```

## Tier transitions

### `smirror telemetry standard`

Possible from: any tier.

1. Update state DB: `meta.telemetry_tier = 'standard'`, `meta.telemetry_tier_changed_at = now()`, `meta.telemetry_tier_source = 'user'`
2. If transitioning FROM `'none'` → enqueue a `first_seen` event (one-time visibility ping)
3. Print confirmation: `Tier set to Standard. first_seen event queued for delivery.`
4. Exit 0

### `smirror telemetry reliability`

Possible from: any tier.

1. Update state DB: `meta.telemetry_tier = 'reliability'`, etc.
2. If transitioning FROM `'none'` → enqueue a `first_seen` event with reliability snapshot attached
3. If transitioning FROM `'standard'` → no immediate event; reliability deltas will attach to the next `upgrade` event
4. Print confirmation: `Tier set to Reliability. Thank you for opting in.`
5. Exit 0

### `smirror telemetry none`

Possible from: any tier.

1. Update state DB: `meta.telemetry_tier = 'none'`, etc.
2. **Drop all queued telemetry events from disk** (`~/.selectivemirror/telemetry-queue/` — both pending and dead-letter). Bug-report queue is kept ONLY if it contains items currently being processed; future bug-report attempts will be refused.
3. Print confirmation: `Tier set to None. Queued events dropped.`
4. Exit 0

> **v2 change**: the v1 design prompted "Also request deletion of past
> server data? [y/N]" and could enqueue a `forget` request. Under v2
> there is no per-install server data to delete (the schema has only
> aggregate counters), so the prompt is obsolete and removed.

## `smirror report-bug --submit` behavior at each tier

### At None tier (the "stuck-user prompt")

Running `smirror report-bug --submit` while `telemetry_tier == 'none'` triggers an interactive prompt:

```
$ smirror report-bug --submit
Generating sanitized bug report... done. (348 lines)

Telemetry is currently None — bug submission is off.

  [1] Enable Standard tier and submit (sends 2 install events ever
      + this bug report; you can preview what's collected)
  [2] One-shot: send just this report, don't change tier
  [3] Save to local file and paste into a GitHub issue yourself
  [v] View the full report bundle
  [c] Cancel

Read first?  smirror telemetry policy

Choose [1/2/3/v/c]:
```

Behavior:
- `[1]` → set `telemetry_tier = 'standard'`, then proceed with normal Standard-tier submission flow (preview → approve → enqueue)
- `[2]` → submit the report with `submitted_tier = 'one_shot'`; tier remains `'none'`; nothing else is sent now or in the future
- `[3]` → emit the bundle to a local file (same as `--stdout > file`); print the GitHub issue URL; exit
- `[v]` → print the bundle to stdout; re-prompt
- `[c]` → cancel; exit 0 with no action

For non-interactive contexts (CI, scripts), the prompt cannot be displayed. The command exits with code 1 and an error: `bug submission requires telemetry tier 'standard' or 'reliability', or pass --one-shot for per-event consent.`

### At Standard / Reliability tier

Standard flow: generate bundle → show preview → prompt y/N → enqueue → print "Submitted. Reference: <server_id>" or "Cancelled."

The `--one-shot` flag is a no-op at these tiers (would be redundant); accept it for forward-compat but don't change behavior.

### `--browser` flag interactions

`smirror report-bug --browser` (or the deprecated `--open`) opens a prefilled GitHub issue page after the telemetry submission completes. Works at all tiers including None (skips telemetry, just opens browser). With `--submit --browser` at None, the prompt offers the same [1][2][3] choices, and either submitted path also opens the browser.

## State DB metadata schema

Keys in the `meta` table:

| Key | Values | Meaning |
|-----|--------|---------|
| `telemetry_tier` | `'none'` \| `'standard'` \| `'reliability'` | Current tier |
| `telemetry_tier_changed_at` | RFC3339 timestamp | When the current tier was set |
| `telemetry_tier_source` | `'installer'` \| `'user'` \| `'default'` | Whether the current state came from MSI registry, runtime CLI, or never-set default |

Plus the existing keys:

| Key | Meaning |
|-----|---------|
| `install_id` | Anonymous random UUID, this install's ID |
| `last_first_seen_event` | RFC3339 timestamp of most recent first_seen send (NULL if never) |
| `last_upgrade_event` | RFC3339 timestamp of most recent upgrade send (NULL if never) |
| `last_recorded_version` | Last `client_version` for which the upgrade-detection logic ran |

## `smirror telemetry forget` — removed in v2

This subcommand was specified in earlier drafts and is **removed in v2**.
Rationale, per the round-5 BMad panel:

- Under v2 (stream-aggregate-and-discard), no server-side record of an
  individual install ever exists. There is nothing to delete.
- A `forget` command that printed "Forget request sent" while routing
  to a no-op handler would be a public commitment the project couldn't
  honor — exactly the failure mode the round-1 panel flagged as
  "product malpractice."
- Bug-report narratives filed via `--browser` to GitHub Issues are
  outside SelectiveMirror's controllership; deletion is handled by the
  user via their own GitHub account or via GitHub's DSAR process.

Migration notes for any existing builds that shipped the design:

- The CLI rejects `smirror telemetry forget` with a clear pointer to
  the new model: "Under SelectiveMirror v2, no per-install server data
  exists; there is nothing to forget. Run `smirror telemetry none` to
  stop contributing, and see `smirror telemetry policy` for the full
  data-handling contract."
- The Cloudflare Worker's `/v1/forget` endpoint (if it was ever wired
  during migration) returns `410 Gone` with a JSON body pointing at
  PRIVACY.md.

## Tests to add (when implementing)

- `cmd/smirror/telemetry_test.go`:
  - `status` shows correct output at each tier
  - `none/standard/reliability` correctly persist to state DB
  - First-run migration: registry → state DB on first run only
  - Transition from `'none'` to `'standard'`/`'reliability'` enqueues `first_seen`
  - Transition to `'none'` purges queued events
  - `forget` subcommand → rejected with v2 migration message; no
    network call attempted

- `cmd/smirror/reportbug_test.go`:
  - `--submit` at None tier → triggers prompt in interactive mode
  - `--submit` at None in non-interactive → exits 1 with hint
  - `--submit --one-shot` at None tier → submits with `submitted_tier='one_shot'`
  - `--submit` at Standard/Reliability → normal flow, `submitted_tier` set correctly

## Out of scope for this design

- A "remind user occasionally" prompt to reconsider their choice — explicitly NOT done; respects user's stated preference.
- Per-mirror tier granularity — telemetry is per-install, not per-mirror.
- Centralized fleet management — n/a for SM's threat model.
- Auto-upgrade-on-error from None to Standard.
- Server-side erasure (`forget`) — removed in v2; see "removed in v2"
  section above. There is no record to erase; v2's architecture is
  the erasure.
