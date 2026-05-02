# SM-157 — `smirror telemetry` runtime CLI

**Status**: **shipped 0.9.83-dev**. Five subcommands wired in
`cmd/smirror/cmd_telemetry.go`: `none`, `standard`, `reliability`,
`status`, `policy`. The `forget` subcommand was deleted from the
design under v2 (no server-side record to forget). Behavior matches
the surface described in
[`docs/cli-telemetry-command.md`](./cli-telemetry-command.md).
**Severity**: major (closed)

> **Updated 2026-04-29 for v2 architecture.** The `forget` subcommand
> is **removed** from the design — under stream-aggregate-and-discard
> there is no server-side record to delete. The v1 design contained
> six subcommands (none / standard / reliability / status / policy /
> forget); v2 has five (no `forget`). The CLI must reject `forget`
> with a clear pointer to v2's reasoning. See
> [`telemetry-architecture-v2.md`](./telemetry-architecture-v2.md).
> All other plan items below remain valid.
**Author**: Raveh, in response to Codex Validation Report
2026-04-28.

## The gap

The three-tier consent model (None / Standard / Reliability) lives
today only in:

- The MSI installer property `INSTALL_TELEMETRY_TIER` and registry
  value `HKLM\Software\SelectiveMirror\TelemetryTier`.
- The `internal/telemetry.ReadTier` helper that gates the startup
  update check (SM-159) and the report-bug `--submit` flag (SM-158).
- The `docs/PRIVACY.md` user-facing contract.

What's missing is the runtime entry point: `smirror telemetry [none |
standard | reliability | status | policy | forget]`. Until that ships,
a user who installed via MSI with `INSTALL_TELEMETRY_TIER=none` (the
default) cannot opt up to Standard / Reliability without re-running
the installer — a UX failure that contradicts the "user can change
tier at any time post-install" promise in
`docs/cli-telemetry-command.md`.

## What the design says

The full surface is specified in
`docs/cli-telemetry-command.md`:

- `smirror telemetry status` — current tier, source, last sent,
  queued events.
- `smirror telemetry none` — opt out; purge queued events; offer
  server-side deletion.
- `smirror telemetry standard` — opt in to bug-report submission +
  install census.
- `smirror telemetry reliability` — Standard plus bucketed
  reliability deltas.
- `smirror telemetry policy` — open `docs/PRIVACY.md` (or print path
  if no browser).
- ~~`smirror telemetry forget` — send the signed deletion request.~~
  **REMOVED in v2.** The CLI rejects this subcommand with a v2
  migration message. There is no server-side per-install record to
  delete; "withdraw consent" via `smirror telemetry none` is the
  complete erasure path.

Persistence: state DB `meta.telemetry_tier`, `telemetry_tier_changed_at`,
`telemetry_tier_source`. Ordered fallback: state DB → registry →
default "none". Already implemented in `internal/telemetry.ReadTier`.

## Why deferred

This is a self-contained CLI command. It depends on:

- A `WriteTier(meta, tier, source)` companion to ReadTier (a few
  lines).
- The submit pipeline (SM-158) for the "first_seen event queued for
  delivery" message and for the `forget` request.
- A clean exit-code convention shared with the rest of the suite
  (already 0/1/2 in main.go, no surprises here).

Each subcommand is small. The whole thing is roughly 150 lines plus
tests. The reason it's not in this round-2 batch is purely
sequencing: it should land alongside or just after SM-158
(`report-bug --submit`) because the two share the same enqueue path
and the same "please review the contract" tone.

## When this lands

Best ordering under v2:

1. v2 schema deployed to Supabase (Phase A in
   `telemetry-architecture-v2.md`) — provides the
   `telemetry.contribute()` RPC the client targets.
2. SM-158 (submit pipeline) — provides the queue / sign / send
   helpers, now targeting `contribute()` instead of v1 normalize-and-
   store endpoints.
3. SM-157 (`smirror telemetry`) — uses the helpers from (2). No
   `forget` subcommand to wire up.

SM-162 (HMAC envelope binding) is downgraded under v2 — replay can
only over-count an aggregate, not exfiltrate. No longer a blocker.

## Test plan

Per the system-validation contract:

- `TestTelemetryCLI_DefaultNoneStatus` — `smirror telemetry status`
  exits 0 and shows "tier: none" + "bug reports:" + "install events:".
- `TestTelemetryCLI_TierTransitionPersists` — `smirror telemetry
  standard` succeeds and a follow-up `status` call confirms the new
  tier; `smirror telemetry none` reverts.
- New tests to add when implemented:
  - Status output at each tier matches the templates in
    `docs/cli-telemetry-command.md`.
  - `forget` produces a signed request with the install_id.
  - Tier change from any tier purges queued events when transitioning
    to `none`.
  - Non-interactive `none` skips the "request server deletion?"
    prompt without erroring.
