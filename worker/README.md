# SelectiveMirror telemetry edge proxy

Cloudflare Worker that fronts the Supabase telemetry endpoint. Optional
infrastructure layer; the system works without it (clients can hit
Supabase directly via `qkspigvkniiiwxggdvbr.supabase.co`).

## What it adds

- **Per-IP rate limiting** (30 req/min per edge PoP) — defends against
  flood attacks. Keys are salted hashes of the IP (SM-163 fix);
  KV at rest is non-reversible without the deploy-time secret.
- **Body-size cap on actual bytes** (not just `Content-Length`) — a
  chunked-transfer client can no longer bypass the 100 KB cap.
- **Edge filtering** — blocks obvious garbage (wrong method, unknown path,
  oversized body) before reaching Supabase.
- **Alternative path** — clients on networks that block `*.supabase.co`
  may be able to reach `*.workers.dev`, and vice versa.
- **Free tier covers SM volume** — 100K req/day on Cloudflare's free
  Workers plan; SM telemetry is far below this even at scale.

## Routes

| Path | Target | Status |
|------|--------|--------|
| `POST /v1/contribute` | Supabase `telemetry.contribute()` RPC | **Active.** The only ingest path. Stream-aggregate-and-discard. |
| `POST /v1/forget`     | (none) — returns `410 Gone` | **Retired.** No server-side per-install record exists; nothing to delete. See `docs/PRIVACY.md`. |
| `POST /v1/bug-reports` | (none) — returns `410 Gone` | **Retired (legacy v1).** Never wired client-side after SM-160 (0.9.18-dev) deleted SendReport. Removed from Worker in 0.9.7x-dev. |
| `POST /v1/installations/report` | (none) — returns `410 Gone` | **Retired (legacy v1).** Same status as `/v1/bug-reports`. |

## What it does NOT do

- HMAC verification (server-side Postgres function does it; worker is
  a transparent proxy)
- Authentication beyond Supabase's anon key forwarding
- Caching (telemetry POSTs are write-only and not cacheable)

## Setup (one-time)

```bash
cd worker

# Install dependencies
npm install

# Authenticate wrangler with your Cloudflare account
npx wrangler login

# Create rate-limit KV namespace
npx wrangler kv:namespace create RATE_LIMIT_KV
# Copy the returned id; uncomment and fill in [[kv_namespaces]] in wrangler.toml

# Set the Supabase anon key as a secret
npx wrangler secret put SUPABASE_ANON_KEY
# Paste the anon public key when prompted

# Set the rate-limit salt secret (SM-163 fix)
npx wrangler secret put RATE_LIMIT_SALT_SECRET
# Paste any 32+ random bytes, e.g.:
#   python3 -c "import secrets; print(secrets.token_hex(32))"
# Rotate quarterly. If skipped, rate limiting is SKIPPED entirely
# (the Worker logs a warning) — by design, since falling back to
# raw-IP keys would defeat the SM-163 privacy fix. Always set this
# in production.

# Deploy
npx wrangler deploy
```

After deploy, the URLs are:

```
https://smirror-telemetry.selectivemirror.workers.dev/v1/contribute       (v2 — only ingest path)
https://smirror-telemetry.selectivemirror.workers.dev/v1/forget           (retired — 410 Gone)
https://smirror-telemetry.selectivemirror.workers.dev/v1/bug-reports      (legacy v1 — 410 Gone)
https://smirror-telemetry.selectivemirror.workers.dev/v1/installations/report  (legacy v1 — 410 Gone)
```

The retired paths intentionally return `410 Gone` with body `{"code":"endpoint_retired"}` so old binaries fail loudly rather than silently miscount; see `docs/PRIVACY.md` "Bug reports are not telemetry" for the rationale.

(Account subdomain = `selectivemirror`, fixed via Cloudflare API after a
one-time misconfiguration. Subdomain is changeable: DELETE then PUT on
`/accounts/{id}/workers/subdomain` with an API token having Workers
Scripts:Edit permission.)

Update the smirror Go client's endpoint constant to use this URL once
ready.

## Operational notes

- **Rate limit is a SOFT CAP, not a hard limit, and per-edge-PoP.**
  The documented `30 req/min/IP` is a soft-cap target. Under burst
  load the actual ceiling is ~2x because Cloudflare KV doesn't
  support atomic increment — the Worker's read-check-write sequence
  has a TOCTOU race that lets parallel requests slip through. A
  60-request burst at concurrency 30 was observed to land 55+
  successful POSTs in ~5 seconds (FINDING 14, round-5 validation
  memo 2026-05-03). For absolute-flood scenarios, Cloudflare's
  own DDoS protection is the floor. The real fix (Cloudflare
  Durable Objects per IP shard, atomic increment) is deferred to
  v1.0.x; threat-model impact today is low because replay can only
  over-count aggregate counters, never exfiltrate.
  Additionally, KV is regional so an attacker spreading across many
  IPs/regions gets up to ~2N req/min PER PoP. For SM's threat model
  this is acceptable; document it so operators planning capacity
  aren't surprised.
- **Rate-limit keys are salted hashes** (SM-163 fix). The salt is the
  `RATE_LIMIT_SALT_SECRET` mixed with the UTC date inside an HMAC; KV
  contents at rest cannot be reversed to IP addresses without the
  secret. Same IP within a UTC day → same key (counter accumulates);
  across days → different keys (linkability broken at the 24h boundary).
- **`/v1/forget` returns 410 Gone** with `code=endpoint_retired`. This
  is intentional and durable — under v2 there's no server-side record
  of an install to delete. See `docs/PRIVACY.md` "Your rights" and
  `docs/cli-telemetry-command.md` for the policy.
- **First deploy creates the workers.dev subdomain** if you don't have
  one. You're prompted to choose at the dashboard.
- **Logs**: `npx wrangler tail` streams live logs. The Worker emits
  `console.warn` if `RATE_LIMIT_SALT_SECRET` is missing.
- **Free tier limits**: 100K requests/day, 10ms CPU per request.
- **Two distinct 5xx sources** — important for triage:
    * *Worker rewrite* — when `fetch(upstream)` returns non-200 (PGRST
      4xx, Supabase 5xx, etc.), the Worker now rewrites to a generic
      `502 upstream_unavailable` with JSON body. This is the FINDING 4
      fix from the round-1 validation memo and is the path the
      probe / Go client / smoke test will see for upstream issues.
    * *Cloudflare edge* — when the Worker is throttled by Cloudflare's
      free-tier limits (CPU ceiling exceeded mid-burst, or the platform
      drops the request before the Worker runs), Cloudflare serves its
      own HTML error page with a 5xx status. This is rare-but-possible
      under burst load; it cannot be intercepted from the Worker side
      because the Worker code never runs.
    Distinguish at triage time by the response `Content-Type`: a
    Worker rewrite is `application/json` with a `code` field; a
    Cloudflare-edge 5xx is `text/html`. Clients should treat any
    non-2xx as transient and retry on their normal cadence
    (`internal/telemetry.Contribute` already wraps both as
    `ErrNetwork`).
- **Smoke testing after deploy**: `python3 scripts/telemetry-v2-smoke-test.py --via-worker`
  posts the four standard cases (bad HMAC, good HMAC, schema violation,
  unknown event) plus the retired-forget probe. See
  `docs/operations/deploy-telemetry-v2.md` for full procedure.
