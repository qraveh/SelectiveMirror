# SM-162 — HMAC envelope binding

**Status**: deferred to a focused implementation session.
**Severity**: medium (architectural; not a current data-leak, but
weakens the integrity guarantee of submitted telemetry).

> **Downgraded 2026-04-29 for v2 architecture.** Under v2 (stream-
> aggregate-and-discard), no envelope is stored. A captured signed
> payload, replayed through a different proxy or with forged metadata,
> can only **over-count an aggregate counter** — it cannot create a
> spurious row, exfiltrate data, or impersonate a victim. Aggregate
> counters are monotonic and rate-limited (Cloudflare Worker side).
> The original threat model that motivated SM-162 (envelope-tampering
> on stored ingest_envelope rows) does not apply to v2.
>
> SM-162 is therefore **downgraded from "must fix before v1.0" to
> "minor architectural debt."** Either of the original two design
> options (extra verify-fn parameter, or fold envelope into payload)
> can land later as a defense-in-depth improvement. It is no longer
> a blocker for the submit pipeline (SM-158) or the runtime CLI
> (SM-157).
>
> The plan below is preserved for the future revisit.
**Author**: Raveh, with input from Codex Validation Report 2026-04-27.

## The gap

Today, the HMAC-SHA256 signature attached to telemetry submissions is
computed over the **payload JSON only**, excluding the `version_hmac`
field itself. The Worker proxy and Supabase ingest path then attach
**envelope columns** (e.g. `ingest_kind`, `received_at`, the source
IP that the Worker observes, the User-Agent, etc.) and store the
envelope as a separate row in `telemetry.ingest_envelope`.

Because the envelope is filled in by the proxy AFTER signature
verification, and because the HMAC scope does not include any of those
columns, an adversary who captures a single valid signed payload can
replay it through a different proxy / different IP / with a forged
`User-Agent` and the server cannot tell the replay from the original.
The downstream `bug_report` / `installation_event` rows would then
appear to come from a legitimate signed submission, with whatever
provenance the attacker chose for the envelope columns.

## Why it isn't fixed in this session

A correct fix touches four places in lockstep:

1. **Schema** (`docs/telemetry-microserver.sql`,
   `docs/telemetry-rls.sql`): the verify function
   `telemetry.verify_versioned_hmac` currently takes
   `(canonical_payload, claimed_version, claimed_hmac_hex)`. To bind
   the envelope, the function must take additional parameters for any
   envelope-derived column it should authenticate, OR the envelope
   claim must move into the canonical payload (e.g., a new
   `submission_meta` object inside the body).

2. **Go client** (`internal/telemetry/canonical.go`,
   `internal/telemetry/hmac.go`): whatever scope decision is made for
   (1), the client signer must include the matching fields in its
   canonical-bytes computation. Test vectors must be regenerated.

3. **Python validator** (`test/telemetry-validation.py`): the 22-test
   end-to-end harness pre-computes HMACs using a Python implementation
   of the same canonicalizer; that file's `canonical_json` must mirror
   the Go change exactly.

4. **Cloudflare Worker** (`worker/src/index.ts`): the Worker is
   currently a near-pass-through. It would need to either propagate
   the client-supplied envelope fields verbatim into the Supabase POST
   body (and not augment them), OR the protocol must be redefined so
   that authenticated envelope fields live inside the payload and the
   ingest_envelope row is derived from them rather than from the
   transport.

A change set spanning all four requires a single coherent design pass
plus full regeneration of the Python ↔ Go ↔ PG test vectors, and is
out of scope for the privacy-bug-batch session that landed
SM-159 / 160 / 164 / 165 / 166 / 167. Doing it here would either bundle
unrelated fixes (slowing review) or land a half-fix in one place that
silently breaks signature parity in the others.

## What the fix probably looks like

Two viable shapes:

### Option A — augment the verify function

```sql
CREATE OR REPLACE FUNCTION telemetry.verify_versioned_hmac(
  canonical_payload BYTEA,
  envelope_canonical BYTEA,    -- NEW: signed envelope bytes
  claimed_version TEXT,
  claimed_hmac_hex TEXT
) RETURNS BOOLEAN ...
```

Client computes:

```
canonical = canonical_json(payload_minus_hmac)
envelope_canonical = canonical_json(envelope_subset)
hmac = HMAC-SHA256(derived_key, canonical || 0x00 || envelope_canonical)
```

The `0x00` separator is a common HMAC-pitfall guard (so concatenation
ambiguity can't enable canonicalization attacks).

**Pros**: existing payload schema unchanged; envelope stays in the
envelope table; verify function continues to live in Postgres.
**Cons**: more parameters at every call site; adds a second canonical
pass on the client.

### Option B — fold envelope claims into payload

Add a `meta` object inside every signed payload:

```json
{
  "schema_version": 1,
  "install_id": "sm-...",
  "client_version": "0.9.16",
  "meta": {
    "submitted_at": "2026-04-27T10:00:00Z",
    "ingest_kind": "bug_report",
    "client_user_agent": "smirror/0.9.16-dev windows/amd64"
  },
  ...
  "version_hmac": "abc..."
}
```

The Worker then validates that the envelope columns it inserts match
the `meta` block (e.g., `ingest_kind == row.ingest_kind`). Mismatches
are rejected at the Worker layer before they ever reach Supabase.

**Pros**: only the canonical_payload computation changes; verify
function unchanged; one canonical pass.
**Cons**: payload schema grows; clients must compute meta deterministically;
the receive_at timestamp can't be authenticated this way (clients lie),
so it stays out of HMAC scope.

The current preference is **Option B** for surface-area minimization,
but the choice should be ratified in the focused session.

## Test plan for the focused session

Before merging the fix:

1. Update `test/telemetry-validation.py` — regenerate fixtures, ensure
   round-trip parity.
2. Add a Go test that signs a payload, mutates an envelope claim, and
   confirms verification fails.
3. Add an integration test that round-trips through the Worker and
   confirms a tampered envelope is rejected.
4. Run the full 22-test validation harness end-to-end against
   Supabase.

## Out of scope for this session

This document exists so the gap is on record. The other six privacy
bugs in the same Codex Validation Report (SM-159, SM-160, SM-164,
SM-165, SM-166, SM-167) were tractable as small, independent commits
and have been addressed; SM-162 needs its own change set.

---

Cross-references:
- `docs/telemetry-microserver-architecture.md` — current HMAC scheme
- `internal/telemetry/canonical.go` — Go canonical JSON
- `test/telemetry-validation.py` — Python reference implementation
- `worker/src/index.ts` — Cloudflare Worker
- `~/.claude/projects/C--SelectiveMirror/memory/reference_jsonb_canonicalization.md`
  — canonicalization gotcha that cost a debugging session in 2026-04-26
