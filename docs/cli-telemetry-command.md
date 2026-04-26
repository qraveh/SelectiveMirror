# `smirror telemetry` — runtime consent management

## Purpose

Lets the user view and change install-telemetry consent at runtime, independently of the choice made during MSI installation. Not yet implemented; this document is the design.

This command does **not** affect bug-report submission (which is always per-event explicit approval, regardless of any global setting).

## Command shape

```
smirror telemetry status         Show current install-telemetry consent state and origin
smirror telemetry on             Enable install-telemetry (first_seen + upgrade events)
smirror telemetry off            Disable install-telemetry
smirror telemetry --help         Show help
```

Exit codes: 0 on success, 1 on error.

## Storage of the consent flag

**Single source of truth at runtime: state DB metadata.** Specifically the `meta` table key `install_telemetry_opt_in`, values `'on'` or `'off'`.

### First run after install

On first run, smirror checks:

1. Is `install_telemetry_opt_in` set in state DB metadata? → use that.
2. Else, is the registry value `HKLM\Software\SelectiveMirror\TelemetryOptIn` set? → copy into state DB (`'on'` if value=1, `'off'` if value=0), then proceed.
3. Else, default to `'off'`.

After first run, only the state DB is consulted. The registry value is never updated — runtime changes affect only the state DB. This avoids requiring admin to flip consent at runtime.

### Subsequent runs

Read from state DB only. Registry is ignored after the first-run migration.

### `smirror clean`

The clean command (per-user `--self` and global `--all`) wipes user data including the state DB. After clean + reinstall, the registry-default flow runs again on first start.

## `smirror telemetry status` output

```
$ smirror telemetry status
install-telemetry: off
source:            installer (HKLM\Software\SelectiveMirror\TelemetryOptIn = 0)
last changed:      2026-04-25 14:32:11 +03:00 (set during install)
events that would be sent if 'on':
  - first_seen (one-time, on first run)
  - upgrade (each time client_version changes)

bug reports submitted via 'smirror report-bug --submit' are always
per-event explicit approval; this setting does not affect them.
```

When consent is on:

```
$ smirror telemetry status
install-telemetry: on
source:            user (smirror telemetry on)
last changed:      2026-04-26 09:14:55 +03:00
events that would be sent:
  - first_seen (already sent at first run on 2026-04-25)
  - upgrade (will fire when client_version changes from 0.8.5 to a higher version)
queued and pending events:
  - none
```

## `smirror telemetry on`

1. Update state DB: `meta.install_telemetry_opt_in = 'on'`, set `meta.install_telemetry_changed_at = now()`, `meta.install_telemetry_source = 'user'`
2. If `first_seen` has not yet been recorded for this install → enqueue a `first_seen` event right now (will drain on next telemetry tick or restart)
3. Print confirmation: `install-telemetry enabled. first_seen event queued for delivery.`
4. Exit 0

## `smirror telemetry off`

1. Update state DB: `meta.install_telemetry_opt_in = 'off'`, set `meta.install_telemetry_changed_at = now()`, `meta.install_telemetry_source = 'user'`
2. **Drop any queued install-telemetry events from disk** (in `~/.selectivemirror/telemetry-queue/`). Bug-report queue is untouched.
3. Print confirmation: `install-telemetry disabled. Future install/upgrade events will not be sent. Bug reports submitted via 'report-bug' are unaffected.`
4. Exit 0

## State DB metadata schema

Three keys in the `meta` table:

| Key | Values | Meaning |
|-----|--------|---------|
| `install_telemetry_opt_in` | `'on'` \| `'off'` | Current consent state |
| `install_telemetry_changed_at` | RFC3339 timestamp | When the current state was set |
| `install_telemetry_source` | `'installer'` \| `'user'` | Whether the current state came from the MSI registry value or a runtime command |

Plus the existing keys (already in use):

| Key | Meaning |
|-----|---------|
| `install_id` | Anonymous random UUID, this install's ID |
| `last_first_seen_event` | RFC3339 timestamp of the most recent first_seen send (NULL if never) |
| `last_upgrade_event` | RFC3339 timestamp of the most recent upgrade send (NULL if never) |
| `last_recorded_version` | Last `client_version` for which the upgrade-detection logic ran |

## Interaction with bug-report submission

**Independent.** `smirror telemetry off` does not block `smirror report-bug --submit`. Bug-report submission has its own per-event approval flow at the time of report.

When the user runs `report-bug --submit`:
1. Generate bundle
2. Show preview to user (including the `install_id`)
3. Prompt for explicit y/N approval
4. On approval, enqueue and submit

This per-event approval happens regardless of the install-telemetry setting.

## Privacy guarantees printed in `--help` and `status`

The user should be able to verify, without reading code, what each setting controls. The status output (above) explicitly distinguishes install-telemetry from bug reports. The `--help` output should make the same distinction:

```
$ smirror telemetry --help
Usage: smirror telemetry <action>

Actions:
  status    Show current install-telemetry state
  on        Enable install-telemetry (first_seen + upgrade events only)
  off       Disable install-telemetry

Notes:
  This setting affects ONLY install-telemetry: at most two events ever
  ('first_seen' once per install, 'upgrade' on each version change).
  No heartbeats are ever sent.

  Bug reports submitted via 'smirror report-bug --submit' are always
  per-event explicit user approval and are NOT affected by this setting.

  Crashes and anomalies are ALWAYS recorded only to local disk and are
  NOT auto-submitted. They may be embedded in a user-approved bug report
  via 'report-bug --include-crashes' / '--include-anomalies'.
```

## Tests to add (when implementing)

- `cmd/smirror/telemetry_test.go`:
  - `status` shows correct output when consent is unset / on / off
  - `on` updates state DB and enqueues first_seen on first call
  - `on` does not duplicate first_seen if already recorded
  - `off` updates state DB and drops queued install-telemetry events but keeps bug-report queue
  - First-run migration: registry HKLM=1 → state DB 'on'; HKLM=0 → 'off'; HKLM missing → 'off'
  - State DB takes precedence over registry on subsequent runs

## Out of scope for this design

- A "remind user occasionally" prompt to reconsider their choice — explicitly NOT done; respects user's stated preference.
- Per-mirror consent granularity — install-telemetry is per-install, not per-mirror.
- Centralized fleet management — n/a for SM's threat model.
- Any opt-out for bug reports — bug reports are always per-event opt-in; no global toggle exists.
