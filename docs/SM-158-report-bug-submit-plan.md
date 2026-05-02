# SM-158 — `report-bug --submit` / `--one-shot` / `--browser` pipeline

**Status**: **shipped 2026-05-02 (0.9.89-dev)**. `--submit` posts a
`bug_report` event to `telemetry.contribute()` via the live Cloudflare
Worker; classifier produces `(bug_kind, bug_surface, severity_hint)`
from the sanitized bundle; stuck-user prompt added for None-tier
interactive callers; always-print-URL rule honored on every
`--submit` outcome (success or failure). End-to-end verified against
the live `smirror-telemetry.selectivemirror.workers.dev` deploy:
`smirror report-bug --submit --one-shot --stdout` produced the
expected `('config','config','0.9.89-dev','error','report_bug','one_shot',1)`
row in `bug_daily_rollup`. Implementation in
`internal/telemetry/contribute.go`, `internal/telemetry/classify.go`,
`cmd/smirror/issueurl.go`, `cmd/smirror/cmd_report_bug_submit.go`,
plus the wire-through changes in `cmd/smirror/main.go::cmdReportBug`.

> **Updated 2026-04-29 for v2 architecture.** Under v2 (stream-
> aggregate-and-discard), the submit pipeline targets the
> `telemetry.contribute()` RPC and contributes a single
> `bug_report` bucket increment — `(bug_kind, bug_surface,
> client_version, severity_hint, source, submitted_tier)`. **The
> bug-report narrative does NOT travel through telemetry**; it is
> filed via `--browser` to GitHub Issues, where the user retains
> control. The two paths are independent: `--submit` contributes a
> count; `--browser` files the narrative; combining them on the
> same invocation does both.
>
> See [`telemetry-architecture-v2.md`](./telemetry-architecture-v2.md)
> for the rationale and
> [`PRIVACY.md`](./PRIVACY.md) "Bug reports are not telemetry."

> **Updated 2026-05-02 (Raveh).** Whenever `--submit` completes —
> with or without `--browser` — the CLI MUST print the GitHub-issue
> URL the user can file the narrative at. The categorical count
> contributed via telemetry is statistical; the narrative is the
> user's, owned and edited via their GitHub account. SelectiveMirror
> "does not accept ownership of the data of the bug reports."
>
> Concretely:
>   - `--submit --browser`: file count + open browser to prefilled
>     GitHub Issue page (current plan).
>   - `--submit` (no `--browser`): file count + print
>     "If you'd like the bug actually fixed, file the narrative
>     at: <URL>" so the user knows the count alone won't trigger a
>     fix.
>   - `--submit --one-shot` (no `--browser`): same — print the URL.
>     The user contributed an anonymous count without committing to
>     ongoing telemetry; they still need to file the narrative
>     themselves to get a fix.
>
> This rule preserves the architectural property: telemetry =
> anonymous statistics, narrative = GitHub Issues. The CLI always
> reminds the user where the narrative lives.
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
