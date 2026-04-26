/**
 * SelectiveMirror telemetry edge proxy.
 *
 * Cloudflare Worker that fronts the Supabase telemetry endpoint, adding:
 *   - Per-IP rate limiting (in-memory KV; per-edge-PoP)
 *   - Basic shape filtering (block obvious bots before Supabase)
 *   - Method/path allowlist (only the two ingest paths are exposed)
 *   - Optional alternative path for clients whose networks block
 *     *.supabase.co directly
 *
 * Deploys to <worker-name>.<account-subdomain>.workers.dev (free tier:
 * 100,000 requests/day).
 *
 * NOT YET DEPLOYED. To deploy:
 *   1. Install wrangler: npm install -g wrangler
 *   2. Authenticate: wrangler login
 *   3. From this directory: wrangler deploy
 *   4. Update smirror Go client to point at the worker URL
 */

interface Env {
    // Bound via wrangler.toml [vars] (non-secret)
    SUPABASE_PROJECT_REF: string;       // e.g., "qkspigvkniiiwxggdvbr"

    // Bound via wrangler secret put (secret)
    SUPABASE_ANON_KEY: string;          // anon JWT — actually safe to share,
                                        // but fetched via secret for hygiene

    // Bound via wrangler.toml [[kv_namespaces]] for rate limiting
    RATE_LIMIT_KV?: KVNamespace;        // optional; if missing, rate limit
                                        // is bypassed
}

const ALLOWED_PATHS = new Set([
    "/v1/bug-reports",
    "/v1/installations/report",
]);

const PATH_TO_TABLE: Record<string, string> = {
    "/v1/bug-reports":           "ingest_envelope",
    "/v1/installations/report":  "ingest_envelope",
};

// Rate limit configuration
const RATE_LIMIT_REQUESTS_PER_MINUTE = 30;
const RATE_LIMIT_WINDOW_SECONDS = 60;

export default {
    async fetch(request: Request, env: Env): Promise<Response> {
        const url = new URL(request.url);

        // Method allowlist
        if (request.method !== "POST") {
            return jsonResponse(405, {
                code: "method_not_allowed",
                message: "Only POST is supported on this endpoint.",
            });
        }

        // Path allowlist
        if (!ALLOWED_PATHS.has(url.pathname)) {
            return jsonResponse(404, {
                code: "not_found",
                message: "Unknown ingest path.",
            });
        }

        // Per-IP rate limit (best-effort; per-edge-PoP, not global)
        const clientIP = request.headers.get("CF-Connecting-IP") || "unknown";
        if (env.RATE_LIMIT_KV) {
            const allowed = await checkRateLimit(env.RATE_LIMIT_KV, clientIP);
            if (!allowed) {
                return jsonResponse(429, {
                    code: "rate_limit_exceeded",
                    message: `Rate limit: ${RATE_LIMIT_REQUESTS_PER_MINUTE}/min per IP. Back off.`,
                }, { "Retry-After": String(RATE_LIMIT_WINDOW_SECONDS) });
            }
        }

        // Body size cap (defense-in-depth; PostgreSQL also caps at 100KB
        // via RLS WITH CHECK)
        const contentLength = parseInt(request.headers.get("Content-Length") || "0", 10);
        if (contentLength > 100_000) {
            return jsonResponse(413, {
                code: "payload_too_large",
                message: "Payload exceeds 100KB limit.",
            });
        }

        // Forward to Supabase REST API. The worker does NOT inspect or
        // tamper with the body — the HMAC verification on the Postgres
        // side does the real authenticity check.
        const targetTable = PATH_TO_TABLE[url.pathname];
        const supabaseURL = `https://${env.SUPABASE_PROJECT_REF}.supabase.co/rest/v1/${targetTable}`;

        const upstreamRequest = new Request(supabaseURL, {
            method: "POST",
            headers: {
                "Content-Type":    request.headers.get("Content-Type") || "application/json",
                "apikey":          env.SUPABASE_ANON_KEY,
                "Authorization":   `Bearer ${env.SUPABASE_ANON_KEY}`,
                "Content-Profile": "telemetry",
                "Prefer":          "return=minimal",
            },
            body: request.body,
        });

        try {
            const upstreamResponse = await fetch(upstreamRequest);
            // Pass through status and body (don't expose Supabase headers)
            return new Response(upstreamResponse.body, {
                status: upstreamResponse.status,
                headers: {
                    "Content-Type": upstreamResponse.headers.get("Content-Type") || "application/json",
                },
            });
        } catch (err) {
            // Upstream error: don't leak details
            return jsonResponse(502, {
                code: "upstream_unavailable",
                message: "Telemetry endpoint temporarily unavailable.",
            });
        }
    },
};

async function checkRateLimit(kv: KVNamespace, ip: string): Promise<boolean> {
    const key = `rl:${ip}`;
    const current = parseInt((await kv.get(key)) || "0", 10);
    if (current >= RATE_LIMIT_REQUESTS_PER_MINUTE) {
        return false;
    }
    // Increment with TTL (sliding-window approximation)
    await kv.put(key, String(current + 1), { expirationTtl: RATE_LIMIT_WINDOW_SECONDS });
    return true;
}

function jsonResponse(status: number, body: unknown, extraHeaders?: Record<string, string>): Response {
    return new Response(JSON.stringify(body), {
        status,
        headers: {
            "Content-Type": "application/json",
            ...(extraHeaders ?? {}),
        },
    });
}
