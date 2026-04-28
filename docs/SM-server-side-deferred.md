# Deferred server-side / release-engineering work

**Status**: tracked but not landed in 0.9.18-dev. Each item below
needs a focused change set across multiple components and is
intentionally NOT batched with the privacy-bug rounds.

## SM-161 — Worker proxy → ingest/normalize flow

**Severity**: major. **Component**: Cloudflare Worker + Supabase.

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

**Severity**: major. **Component**: Cloudflare Worker.

Three flaws documented in the validation report:

1. **Raw client IP stored in KV key**: `rl:${ip}` lets the KV value
   double as a per-IP record. Privacy posture should be that IPs
   never persist anywhere; replace with a salted HMAC of
   `(ip, tenant)` so the same client coalesces to the same key but
   the key is not reversible.
2. **Body-size cap trusts `Content-Length`**: a chunked-transfer
   request without a Content-Length header bypasses the 100 KB cap.
   The fix is `request.clone().arrayBuffer()` (or similar) to
   enforce the cap on the actual body bytes.
3. **Non-atomic `kv.get()` then `kv.put()`**: parallel same-IP
   requests both read the pre-increment counter and both write
   back N+1 instead of N+2. KV doesn't support atomic increment, so
   we either accept the slack (probably fine — counter is best-
   effort) or move to Durable Objects. Documented choice goes here
   when made.

Each fix is a small change to `worker/src/index.ts`, but the trio
needs a coordinated review pass (the rate-limit semantics shift
visible to clients) and is therefore deferred to its own session.

## SM-168 — MSI build pipeline + telemetry signing key

**Severity**: major. **Component**: installer + release.yml.

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
