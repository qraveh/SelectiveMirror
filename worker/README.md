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
# Rotate quarterly. If skipped, rate limiting still works but uses
# raw-IP keys (worker logs a warning).

# Deploy
npx wrangler deploy
```

After deploy, the URLs are:

```
https://smirror-telemetry.selectivemirror.workers.dev/v1/contribute       (v2 — current)
https://smirror-telemetry.selectivemirror.workers.dev/v1/bug-reports      (v1 — deprecated)
https://smirror-telemetry.selectivemirror.workers.dev/v1/installations/report  (v1 — deprecated)
```

(Account subdomain = `selectivemirror`, fixed via Cloudflare API after a
one-time misconfiguration. Subdomain is changeable: DELETE then PUT on
`/accounts/{id}/workers/subdomain` with an API token having Workers
Scripts:Edit permission.)

Update the smirror Go client's endpoint constant to use this URL once
ready.

## Operational notes

- **Rate limit is per-edge-PoP, not global.** Cloudflare KV is regional;
  a determined attacker spreading across many IPs/regions could each
  get 30 req/min. For SM's threat model this is acceptable.
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
- **Smoke testing after deploy**: `python3 scripts/telemetry-v2-smoke-test.py --via-worker`
  posts the four standard cases (bad HMAC, good HMAC, schema violation,
  unknown event) plus the retired-forget probe. See
  `docs/operations/deploy-telemetry-v2.md` for full procedure.
