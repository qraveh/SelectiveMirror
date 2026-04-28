# SM-158 — `report-bug --submit` / `--one-shot` / `--browser` pipeline

**Status**: surface implemented (flags parsed, help text, None-tier
gate); submission pipeline deferred to a focused implementation
session.
**Severity**: major
**Author**: Raveh, in response to Codex Validation Report
2026-04-28.

## What landed in 0.9.18-dev

The `report-bug` command now:

- Recognizes `--submit`, `--one-shot`, and `--browser` flags
  (no more "unknown flag" failure for those names).
- Documents all four flag spellings (`--stdout`, `--browser`, `--open`,
  `--submit`, `--one-shot`) in `--help`, with `--open` marked as a
  deprecated alias for `--browser`.
- Reads the telemetry tier (state DB → registry → "none" fallback,
  fail-closed on read errors) and gates `--submit` accordingly:
  - At tier None without `--one-shot`: prints a clear error pointing
    at `--one-shot` and `smirror telemetry policy`, exits 1.
  - At tier Standard / Reliability, or with `--one-shot`: prints a
    "pipeline not yet wired" notice and continues to print the
    sanitized report so the user still gets value.

## What's deferred

The actual submission pipeline:

1. **Bundle preview + per-event approval** — show the user the
   redacted bundle, prompt y/N. Required at every tier.
2. **Enqueue to the durable disk queue** (`internal/telemetry/queue.go`)
   with `submitted_tier` set to one of `'standard'`, `'reliability'`,
   `'one_shot'`.
3. **HMAC sign** (`internal/telemetry/hmac.go::Sign`) using the build-
   time-injected `buildKey`.
4. **POST to `<endpoint>/v1/bug-reports`** through the Cloudflare
   Worker (allowlisted path).
5. **Print server reference / handle failure** with retry semantics
   compatible with the queue.
6. **Stuck-user prompt** for the None + interactive case (per
   `docs/cli-telemetry-command.md`):
   - `[1] Enable Standard tier and submit`
   - `[2] One-shot: send just this report, don't change tier`
   - `[3] Save to local file and paste into a GitHub issue yourself`
   - `[v] View the full report bundle`
   - `[c] Cancel`
7. **`--browser` orchestration**: open the prefilled GitHub issue
   page after the telemetry submission completes (or if the user
   chose `[3]` at the stuck-user prompt).

## Why deferred

The Codex round-2 validation report listed nine privacy / correctness
bugs (SM-159 already partial, SM-164, SM-165, SM-166, SM-167 already
done; SM-171, SM-172, SM-173, SM-174, SM-175, SM-176, SM-177, SM-178
this round). All were tractable as small, independent commits.
Implementing the submit pipeline is at least:

- ~150 lines in `cmd/smirror/main.go` (interactive prompt + dispatch)
- ~80 lines in `internal/telemetry/submit.go` (new file: bundle
  envelope + sign + enqueue)
- ~60 lines in `internal/telemetry/queue.go` (SendTo helpers, retry
  budget)
- New `cmd/smirror/reportbug_submit_test.go` covering the matrix
  (tier × interactive × one-shot × browser)

That's a single coherent change set, one focused review, one focused
session. Bundling it with the round-2 fixes would have stretched the
review surface unproductively.

## When this lands

After:
- SM-157 (`smirror telemetry` CLI command) — provides the tier-change
  primitive the stuck-user prompt's `[1]` path needs.
- SM-162 (HMAC envelope binding plan) — must be ratified before we
  serialize new payload shapes; otherwise the submit code locks in
  the unbinded scope.

## Test plan

Per the system-validation contract:

- `TestTelemetryReportBug_SubmitAtNoneIsConsentAware` — exit 1, hint
  about `--one-shot` (✅ passing as of 0.9.18-dev with the surface fix).
- `TestTelemetryReportBug_HelpDocumentsSubmitBrowserAndOneShot` —
  help text contract (✅ passing).
- New tests to add when the pipeline lands:
  - Standard + interactive + approve → enqueue + queue size = 1
  - Standard + interactive + deny → queue unchanged
  - None + non-interactive + `--one-shot` → enqueue with
    `submitted_tier='one_shot'`
  - Standard + `--browser` → enqueue + browser invoked
  - Stuck-user prompt at None + `[1]` → tier change to Standard +
    enqueue
  - Stuck-user prompt at None + `[3]` → save-to-file path; nothing
    enqueued
