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

This command is the runtime counterpart to the MSI installer's tier selection. It is the user's primary consent surface after install — **and the only surface that exposes all three tiers**: as of v1.0.1 the MSI consent dialog is binary (None / Standard) per [`docs/PROPOSAL-2026-05-03-msi-binary-consent.md`](./PROPOSAL-2026-05-03-msi-binary-consent.md), with Reliability reachable only via `smirror telemetry reliability` (or via silent install with `INSTALL_TELEMETRY_TIER=reliability`). Existing v1.0.0 users who chose Reliability keep that choice across the v1.0.1 upgrade — the dialog isn't re-shown.

## Design context

The user-facing contract for each tier is in `docs/PRIVACY.md`. This document specifies the CLI shape and persistence. Earlier multi-role review rounds led to the three-tier model; a subsequent review round (2026-04-29) led to the v2 architecture above.

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

Running `smirror report-bug --submit` while `telemetry_tier == 'none'` and stdin is interactive triggers a 5-option prompt (implemented in `cmd/smirror/cmd_report_bug_submit.go::stuckUserPrompt`):

```
Bug submission options (telemetry tier is None — bug reports are not
sent automatically). Choose how to proceed:

  [1] Enable Standard tier and submit
       (changes your default; future bug reports submit automatically)
  [2] One-shot: send THIS report only, don't change tier
       (anonymous count contributed; tier stays None)
  [3] Save the report locally; I'll file it manually
       (no telemetry; you paste the bundle into a GitHub issue yourself)
  [v] View the full report bundle before deciding
  [c] Cancel
Choice [1/2/3/v/c]:
```

Behavior:
- `[1]` → write `telemetry_tier=standard` to the state DB (best-effort; falls back to one-shot if the DB write fails), then submit with `submitted_tier=standard`.
- `[2]` → submit with `submitted_tier='one_shot'`; tier remains `'none'`; nothing else is sent now or in the future.
- `[3]` → skip telemetry; the existing file-write/`--stdout`/`--clipboard`/`--browser` path runs as if `--submit` were absent.
- `[v]` → print the sanitized bundle, then re-prompt.
- `[c]` → cancel: print "Cancelled." and return.

For non-interactive contexts (CI, scripts), the prompt cannot be displayed. The command exits with code 1 and the error: `bug submission requires telemetry tier 'standard' or 'reliability', or pass --one-shot for per-event consent.`

### At Standard / Reliability tier

Standard flow (as shipped in 0.9.89-dev): generate bundle → sanitize → classify (`internal/telemetry/classify.go::ClassifyBugReport`) → sign + POST to `telemetry.contribute()` via the Worker → print one-line outcome to stderr (`Submitted: bug_kind=… bug_surface=… severity=… source=report_bug submitted_tier=…`) → print the prefilled GitHub-issue URL.

There is no per-event y/N preview prompt at Standard or Reliability today: `--submit` is taken as the explicit user action. If user feedback shows confusion, a preview can be added in a follow-up.

There is no "server reference" returned by the v2 RPC — `contribute()` returns `{"ok":true}` on a successful UPSERT and nothing more. The user's record of the contribution is the local file (when `--stdout` / `--clipboard` are not set, smirror writes the bundle to `<configdir>/reports/`).

The `--one-shot` flag is a no-op at Standard/Reliability tiers (would be redundant); the implementation accepts it for forward-compat.

### `--browser` flag interactions

`smirror report-bug --browser` (or the deprecated `--open`) opens a prefilled GitHub issue page. With `--submit --browser`, the telemetry contribution is attempted first, then the browser opens to the same prefilled URL the always-print rule emits. Works at all tiers including None (with None + `--browser` alone, no telemetry is attempted; only the browser opens).

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
Rationale, per the multi-role review:

- Under v2 (stream-aggregate-and-discard), no server-side record of an
  individual install ever exists. There is nothing to delete.
- A `forget` command that printed "Forget request sent" while routing
  to a no-op handler would be a public commitment the project couldn't
  honor — exactly the failure mode an earlier review round flagged as
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

## Tests in tree

- `cmd/smirror/cmd_telemetry_test.go` — SM-157 surface tests:
  status output at each tier; persist tier on `none/standard/
  reliability`; first-run registry → state DB migration; transition
  enqueues `first_seen` from None; transition to None drops queued.
- `cmd/smirror/cmd_report_bug_submit_test.go` — SM-158 payload +
  URL helpers: `bug_report` payload shape under each tier (one_shot
  / standard / reliability); the `NoNarrativeFields` privacy
  invariant; prefilled-issue-URL shape, no-logs-section path,
  truncation-at-cap.
- `internal/telemetry/contribute_test.go` — 12 cases for the HTTP
  client: HMAC round-trip vs server-side recompute, all rejection
  reason codes, HTTP 410 / 500 / malformed, context-deadline,
  env-var endpoint override.
- `internal/telemetry/classify_test.go` — 12 cases for the
  classifier: taxonomy lock against `scripts/telemetry-report.py`,
  per-kind happy paths, severity escalation on `panic:`, the
  "rclone version" success-line is not misclassified.

End-to-end against the live Worker is documented in
`docs/SM-158-report-bug-submit-plan.md`'s "Verified live" section.

## Out of scope for this design

- A "remind user occasionally" prompt to reconsider their choice — explicitly NOT done; respects user's stated preference.
- Per-mirror tier granularity — telemetry is per-install, not per-mirror.
- Centralized fleet management — n/a for SM's threat model.
- Auto-upgrade-on-error from None to Standard.
- Server-side erasure (`forget`) — removed in v2; see "removed in v2"
  section above. There is no record to erase; v2's architecture is
  the erasure.
