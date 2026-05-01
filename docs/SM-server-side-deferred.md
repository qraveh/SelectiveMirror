# Deferred server-side / release-engineering work

**Status**: tracked but not landed in 0.9.18-dev. Each item below
needs a focused change set across multiple components and is
intentionally NOT batched with the privacy-bug rounds.

> **Updated 2026-04-29 for v2 architecture.** The adoption of
> stream-aggregate-and-discard
> ([`telemetry-architecture-v2.md`](./telemetry-architecture-v2.md))
> retires or downgrades several items below. Status changes:
>
> - **SM-161 retired.** Under v2 there is no normalization step: the
>   Worker calls `telemetry.contribute()`, which UPSERTs a counter
>   in a single transaction. No queue, no `process_ingest()`, no
>   trigger.
> - **SM-162 downgraded** from medium-severity blocker to minor
>   architectural debt. Replay can only over-count a monotonic
>   counter; no exfiltration vector exists. See
>   [`SM-162-hmac-envelope-binding-plan.md`](./SM-162-hmac-envelope-binding-plan.md).
> - **SM-163 partially retired.** Daily-salted IP hash replaces raw
>   IP in rate-limit KV; the atomic-counter / non-atomic-counter
>   trade-off remains an open Worker design choice.
> - **SM-168 SHIPPED** (verified 0.9.82-dev tip). `release.yml`
>   computes `HMAC-SHA256(master_key, version)` from the
>   `SMIRROR_TELEMETRY_MASTER_KEY` secret, exports it to GoReleaser
>   as `SMIRROR_TELEMETRY_DERIVED_KEY`. `.goreleaser.yaml` ldflag
>   `-X .../internal/telemetry.buildKey={{.Env.SMIRROR_TELEMETRY_DERIVED_KEY}}`
>   bakes it into the binary. `internal/telemetry/hmac.go` exposes
>   `BuildKeyFingerprint()` (returns `"none"` / `"invalid"` /
>   8-hex-char fingerprint). `cmd/smirror/main.go` prints
>   `telemetry build-key:` line in `smirror version`. release.yml
>   has a verification gate that fails the release if the binary
>   reports `"none"` (with `RELEASE_ALLOW_NO_TELEMETRY_KEY=1` repo-
>   variable escape valve for intentional no-telemetry builds).
>   Marek's panel concern (round-3, 2026-04-30) was based on
>   outdated context; SM-168 was wired in an earlier commit batch
>   not yet visible to the panel summary.

## SM-161 — Worker proxy → ingest/normalize flow

**Status**: ~~major~~ **RETIRED in v2** (2026-04-29).
**Component**: Cloudflare Worker + Supabase.

> Retired by the v2 architecture. The Worker now calls one RPC
> (`telemetry.contribute()`); there is no normalization step, no
> ingest_envelope, no async classification. The "missing" flow this
> bug described doesn't need to exist. Detail preserved below for
> historical context.

The Worker currently forwards a raw envelope into
`telemetry.ingest_envelope` and stops. The architecture document
(`docs/telemetry-microserver-architecture.md`, section "Ingest flow")
describes a normalized path where a database trigger or RPC function
reads the envelope, validates the HMAC against the version-derived
key, and inserts a row into `bug_report` / `installation_event` /
`installation_reliability_snapshot` while marking the envelope
classified.

What's missing:

- A `telemetry.process_ingest()` function (or trigger on
  `ingest_envelope INSERT`) that consumes the raw payload.
- A queue/cursor that lets retries be idempotent against
  `dedupe_key`.
- Failure semantics for HMAC mismatch (reject), schema violation
  (record but unclassified), and downstream RLS rejection (record but
  flag).

This is gated by SM-162 (the HMAC scope decision) — without that the
ingest function would lock in a scope choice prematurely.

## SM-162 — HMAC envelope binding

Already documented in
[`SM-162-hmac-envelope-binding-plan.md`](./SM-162-hmac-envelope-binding-plan.md).
Not duplicated here; cross-reference only.

## SM-163 — Worker rate-limiter security

**Status**: 2/3 sub-items shipped in 0.9.71-dev. **Severity**: minor
remainder. **Component**: Cloudflare Worker.

Three flaws documented in the validation report; current status of each:

1. **Raw client IP stored in KV key** — ✅ **FIXED** in 0.9.71-dev.
   `rl:${ip}` is replaced with `rl:HMAC-SHA256(salt_secret, ip + ":" + utc_date)[:16]`,
   so KV at rest is non-reversible to IPs without the deploy-time
   secret. Same IP within a UTC day → same key (counter accumulates);
   across days → different keys (linkability broken at 24h boundary).
2. **Body-size cap trusts `Content-Length`** — ✅ **FIXED** in 0.9.71-dev.
   The Worker now reads `await request.arrayBuffer()` and checks
   `byteLength`, so chunked-transfer can't bypass the 100 KB cap.
3. **Non-atomic `kv.get()` then `kv.put()`** — open. Parallel same-IP
   requests can each read the pre-increment counter and write back
   N+1 instead of N+2. KV doesn't support atomic increment; the
   choice is accept-the-slack (counter is best-effort; ±10% over
   the rate limit is fine) or move to Durable Objects. Default
   posture: accept-the-slack until measured abuse appears.

## SM-168 — MSI build pipeline + telemetry signing key

**Status**: ✅ **SHIPPED** (verified at 0.9.82-dev tip; see status
banner above for full details). **Severity**: was major. **Component**:
installer + release.yml. The plan documented below is preserved for
historical context; the implementation is in place.

The release workflow (`.github/workflows/release.yml`) calls
`installer/build-msi.ps1` to produce the per-platform MSI. The build
script does NOT pass a `-X github.com/qraveh/SelectiveMirror/internal/telemetry.buildKey=...`
ldflag, so the MSI ships a binary with an empty `buildKey`. Every
HMAC submission from that binary then fails server-side verification
silently — the full submit path (SM-158) cannot work in
production until the build script embeds a per-version derived key.

Required changes:

1. CI job derives the version-specific key from the master key in
   `Secrets`:
   ```
   $derivedKey = HMAC-SHA256(SUPABASE_TELEMETRY_MASTER_KEY, $version)
   ```
2. `installer/build-msi.ps1` is invoked with the derived key as a
   parameter and forwards it to `go build` via `-ldflags
   "-X .../internal/telemetry.buildKey=$derivedKey"`.
3. A `smirror version` enhancement prints `telemetry build-key:
   <fingerprint>` so the user can see whether their binary has a key.
   (`internal/telemetry.BuildKeyFingerprint()` already exists; it
   needs to be threaded through the version-print path.)

Validation tests `TestTelemetryReleaseBuild_EmbedsBuildKeyForMSI` and
`TestTelemetryVersionReportsBuildKeyFingerprint` lock the contract.

## Why these are deferred together

They all need:
- A single coherent design pass (envelope binding → ingest semantics
  → rate-limit posture → release-time key embedding form a tight
  sequence).
- Changes spanning the Go client, the Worker, the Supabase schema,
  and the GitHub Actions release pipeline.
- Real-world testing against the live Cloudflare/Supabase
  infrastructure.

A privacy-bug-batch session is the wrong vehicle. They are tracked,
the validation harness fails-loudly when checked, and a follow-up
session can take them all.
