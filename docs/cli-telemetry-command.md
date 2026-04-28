# `smirror telemetry` — three-tier consent management

## Purpose

Lets the user view and change their telemetry tier at runtime, independently of the choice made during MSI installation. The tier governs what (if anything) smirror sends to the telemetry endpoint.

This command is the runtime counterpart to the MSI installer's tier selection. It is the user's primary consent surface after install.

## Design context

The user-facing contract for each tier is in `docs/PRIVACY.md`. This document specifies the CLI shape and persistence. Round-3 + round-4 panel decisions are captured in memory at `~/.claude/.../memory/reference_telemetry_tier_model.md`.

**Default tier**: **None**. If the user never runs this command, smirror sends nothing.

## Command shape

```
smirror telemetry status               Show current tier, last sent, queued
smirror telemetry none                 Opt out completely
smirror telemetry standard             Opt in to bug reports + install census
smirror telemetry reliability          Opt in to all of the above + reliability deltas
smirror telemetry policy               Open docs/PRIVACY.md (or print path)
smirror telemetry forget               Send signed deletion request for past server data
smirror telemetry --help               Show help
```

Exit codes: 0 on success, 1 on error, 2 if requested action requires admin privileges that aren't available.

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
3. Prompt the user (interactive only): `Also request deletion of past server data? [y/N]`
   - On y: enqueue a single signed `forget` request with the install_id; submit immediately
   - On n: leave server data as-is
4. Print confirmation: `Tier set to None. Queued events dropped. {Server-deletion request sent | Server data unchanged.}`
5. Exit 0

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

## `smirror telemetry forget`

Sends a signed deletion request for past server-side data keyed on the install_id. Behavior:

1. Construct request body: `{"install_id": <uuid>, "requested_at": <utc>, "reason": "user_forget"}`
2. Sign with the same HMAC scheme as other telemetry submissions
3. POST to `<endpoint>/v1/forget`
4. Print confirmation: `Forget request sent. Server-side deletion within 30 days.`
5. Exit 0

This works at any tier (a user at None may still have legacy data on the server from before they switched).

## Tests to add (when implementing)

- `cmd/smirror/telemetry_test.go`:
  - `status` shows correct output at each tier
  - `none/standard/reliability` correctly persist to state DB
  - First-run migration: registry → state DB on first run only
  - Transition from `'none'` to `'standard'`/`'reliability'` enqueues `first_seen`
  - Transition to `'none'` purges queued events
  - `forget` produces a properly-signed request

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
