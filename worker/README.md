# SelectiveMirror telemetry edge proxy

Cloudflare Worker that fronts the Supabase telemetry endpoint. Optional
infrastructure layer; the system works without it (clients can hit
Supabase directly via `qkspigvkniiiwxggdvbr.supabase.co`).

## What it adds

- **Per-IP rate limiting** (30 req/min per edge PoP) — defends against
  flood attacks
- **Edge filtering** — blocks obvious garbage (wrong method, unknown path,
  oversized body) before reaching Supabase
- **Alternative path** — clients on networks that block `*.supabase.co`
  may be able to reach `*.workers.dev`, and vice versa
- **Free tier covers SM volume** — 100K req/day on Cloudflare's free
  Workers plan; SM telemetry is far below this even at scale

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

# Deploy
npx wrangler deploy
```

After deploy, the URL is:

```
https://smirror-telemetry.<your-account-subdomain>.workers.dev/v1/bug-reports
https://smirror-telemetry.<your-account-subdomain>.workers.dev/v1/installations/report
```

Update the smirror Go client's endpoint constant to use this URL once
ready.

## Operational notes

- **Rate limit is per-edge-PoP, not global.** Cloudflare KV is regional;
  a determined attacker spreading across many IPs/regions could each
  get 30 req/min. For SM's threat model this is acceptable.
- **First deploy creates the workers.dev subdomain** if you don't have
  one. You're prompted to choose at the dashboard.
- **Logs**: `npx wrangler tail` streams live logs.
- **Free tier limits**: 100K requests/day, 10ms CPU per request.
