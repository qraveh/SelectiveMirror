/**
 * SelectiveMirror telemetry edge proxy.
 *
 * Cloudflare Worker that fronts the Supabase telemetry endpoint, adding:
 *   - Per-IP rate limiting (KV namespace; per-edge-PoP) — keys are
 *     salted hashes of the IP, so the KV at rest is non-reversible
 *     (SM-163 partial fix)
 *   - Body size cap enforced on actual bytes, not just Content-Length
 *   - Method/path allowlist (only the documented ingest paths exposed)
 *   - Optional alternative path for clients whose networks block
 *     *.supabase.co directly
 *
 * Paths:
 *   - /v1/bug-reports          → POST to Supabase ingest_envelope (v1)
 *   - /v1/installations/report → POST to Supabase ingest_envelope (v1)
 *   - /v1/contribute           → RPC to telemetry.contribute()       (v2)
 *
 * v1 paths remain operational during the v2 migration window (Phases
 * A-C of docs/telemetry-architecture-v2.md). After Phase D they will
 * be removed.
 *
 * Deploy:
 *   1. npm install -g wrangler   (one-time)
 *   2. npx wrangler login
 *   3. npx wrangler secret put SUPABASE_ANON_KEY
 *   4. npx wrangler secret put RATE_LIMIT_SALT_SECRET
 *      (any 32+ random bytes; rotate quarterly)
 *   5. From this directory: npx wrangler deploy
 */

interface Env {
    // Bound via wrangler.toml [vars] (non-secret)
    SUPABASE_PROJECT_REF: string;       // e.g., "qkspigvkniiiwxggdvbr"

    // Bound via wrangler secret put (secret)
    SUPABASE_ANON_KEY: string;          // anon JWT — actually safe to share,
                                        // but fetched via secret for hygiene

    // SM-163 partial fix: a per-deploy random secret used to derive the
    // rate-limit KV key from the client IP. Without this secret, an
    // attacker who reads the KV cannot brute-force IP addresses out of
    // the keys (the IP space is small enough — 4 billion v4 addresses
    // — that an unsalted hash would be reversible).
    //
    // Rotate quarterly with `wrangler secret put RATE_LIMIT_SALT_SECRET`.
    // Rotation invalidates any in-flight rate-limit windows (KV TTL is
    // 60s, so the disruption is negligible). If the secret is missing,
    // the worker still enforces rate limits but using the legacy
    // raw-IP-prefixed key — emit a warning to the deploy log.
    RATE_LIMIT_SALT_SECRET?: string;

    // Bound via wrangler.toml [[kv_namespaces]] for rate limiting
    RATE_LIMIT_KV?: KVNamespace;        // optional; if missing, rate limit
                                        // is bypassed
}

// Body size cap (defense-in-depth; PostgreSQL also caps at 100KB
// via RLS WITH CHECK).
const BODY_SIZE_CAP_BYTES = 100_000;

// Rate limit configuration
const RATE_LIMIT_REQUESTS_PER_MINUTE = 30;
const RATE_LIMIT_WINDOW_SECONDS = 60;

// v1 ingest paths (deprecated; remain operational during the v2
// migration). All map to the same ingest_envelope table.
const V1_PATHS_TO_TABLE: Record<string, string> = {
    "/v1/bug-reports":           "ingest_envelope",
    "/v1/installations/report":  "ingest_envelope",
};

// v2 ingest path — calls the telemetry.contribute() RPC. The function
// verifies the HMAC, dispatches by event_kind, UPSERTs the matching
// rollup counter, and returns. No raw event row is ever persisted.
const V2_RPC_PATH = "/v1/contribute";
const V2_RPC_FUNCTION = "contribute";

// Forget endpoint — explicitly NOT supported under v2. The CLI design
// removed `smirror telemetry forget` (see docs/cli-telemetry-command.md);
// any client still attempting the legacy path receives 410 Gone with
// a pointer to the new policy. This is intentional and durable.
const RETIRED_PATHS = new Set([
    "/v1/forget",
]);

const ALLOWED_PATHS = new Set([
    ...Object.keys(V1_PATHS_TO_TABLE),
    V2_RPC_PATH,
]);

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

        // Retired path: 410 Gone with policy pointer.
        if (RETIRED_PATHS.has(url.pathname)) {
            return jsonResponse(410, {
                code: "endpoint_retired",
                message:
                    "Under SelectiveMirror v2 (stream-aggregate-and-" +
                    "discard), no per-install server data exists; there " +
                    "is nothing to forget. Run `smirror telemetry none` " +
                    "to stop contributing. See docs/PRIVACY.md.",
            });
        }

        // Path allowlist
        if (!ALLOWED_PATHS.has(url.pathname)) {
            return jsonResponse(404, {
                code: "not_found",
                message: "Unknown ingest path.",
            });
        }

        // Per-IP rate limit (best-effort; per-edge-PoP, not global). The
        // KV key is a salted hash of the IP — see SM-163 fix in the
        // header comment.
        const clientIP = request.headers.get("CF-Connecting-IP") || "unknown";
        if (env.RATE_LIMIT_KV) {
            const allowed = await checkRateLimit(
                env.RATE_LIMIT_KV,
                clientIP,
                env.RATE_LIMIT_SALT_SECRET,
            );
            if (!allowed) {
                return jsonResponse(429, {
                    code: "rate_limit_exceeded",
                    message: `Rate limit: ${RATE_LIMIT_REQUESTS_PER_MINUTE}/min per IP. Back off.`,
                }, { "Retry-After": String(RATE_LIMIT_WINDOW_SECONDS) });
            }
        }

        // Body size cap on actual bytes (NOT the Content-Length header,
        // which a chunked-transfer client can omit). We clone the
        // request, drain the body once to count bytes, then forward
        // the original body. This is SM-163's other half: the
        // Content-Length-only check that was bypassable by chunked
        // requests.
        let bodyBytes: ArrayBuffer;
        try {
            bodyBytes = await request.arrayBuffer();
        } catch (err) {
            return jsonResponse(400, {
                code: "bad_request",
                message: "Could not read request body.",
            });
        }
        if (bodyBytes.byteLength > BODY_SIZE_CAP_BYTES) {
            return jsonResponse(413, {
                code: "payload_too_large",
                message: `Payload exceeds ${BODY_SIZE_CAP_BYTES} byte limit.`,
            });
        }

        // Dispatch to v1 vs v2 upstream.
        const supabaseBase = `https://${env.SUPABASE_PROJECT_REF}.supabase.co`;

        let upstreamURL: string;
        if (url.pathname === V2_RPC_PATH) {
            // v2: call PostgREST RPC. The body is forwarded verbatim;
            // the Postgres function expects parameter-bound JSONB.
            upstreamURL = `${supabaseBase}/rest/v1/rpc/${V2_RPC_FUNCTION}`;
        } else {
            // v1: forward to ingest_envelope as before.
            const targetTable = V1_PATHS_TO_TABLE[url.pathname];
            upstreamURL = `${supabaseBase}/rest/v1/${targetTable}`;
        }

        const upstreamRequest = new Request(upstreamURL, {
            method: "POST",
            headers: {
                "Content-Type":    request.headers.get("Content-Type") || "application/json",
                "apikey":          env.SUPABASE_ANON_KEY,
                "Authorization":   `Bearer ${env.SUPABASE_ANON_KEY}`,
                "Content-Profile": "telemetry",
                "Prefer":          "return=minimal",
            },
            body: bodyBytes,
        });

        try {
            const upstreamResponse = await fetch(upstreamRequest);
            // Pass through status and body (don't expose Supabase
            // headers). For v2, the function returns 200 with
            // {"ok": false, ...} on rejection — we forward that
            // verbatim so the client can read the rejection reason.
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

/**
 * Rate-limit check with salted-IP-hash key (SM-163 partial fix).
 *
 * The key shape is:
 *   rl:HMAC-SHA256(salt_secret, ip + ":" + utc_date_yyyymmdd)[:32]
 *
 * Properties:
 *   - Same IP within the same UTC day → same key → counter accumulates
 *   - Same IP across days → different keys (linkability broken at 24h)
 *   - Compromise of KV at rest → keys are non-reversible without
 *     RATE_LIMIT_SALT_SECRET
 *   - No rotating-salt storage required; unlinkability comes from the
 *     date in the HMAC input
 *
 * If the secret is missing (deployed without it), we fall back to
 * the legacy raw-IP key shape with a console warning so the
 * regression is observable.
 */
async function checkRateLimit(
    kv: KVNamespace,
    ip: string,
    saltSecret: string | undefined,
): Promise<boolean> {
    const key = await rateLimitKey(ip, saltSecret);
    const current = parseInt((await kv.get(key)) || "0", 10);
    if (current >= RATE_LIMIT_REQUESTS_PER_MINUTE) {
        return false;
    }
    // Increment with TTL (sliding-window approximation)
    await kv.put(key, String(current + 1), { expirationTtl: RATE_LIMIT_WINDOW_SECONDS });
    return true;
}

async function rateLimitKey(
    ip: string,
    saltSecret: string | undefined,
): Promise<string> {
    if (!saltSecret) {
        // Fallback to the legacy shape so a misconfigured deploy still
        // rate-limits, just without the SM-163 protection.
        console.warn("RATE_LIMIT_SALT_SECRET is not set; using raw-IP key (SM-163 protection disabled)");
        return `rl:${ip}`;
    }
    const utcDate = new Date().toISOString().slice(0, 10); // YYYY-MM-DD
    const message = `${ip}:${utcDate}`;
    const enc = new TextEncoder();
    const cryptoKey = await crypto.subtle.importKey(
        "raw",
        enc.encode(saltSecret),
        { name: "HMAC", hash: "SHA-256" },
        false,
        ["sign"],
    );
    const sig = await crypto.subtle.sign("HMAC", cryptoKey, enc.encode(message));
    // First 16 bytes (32 hex chars) is plenty of unique-key space and
    // keeps the KV key short.
    const hex = Array.from(new Uint8Array(sig).slice(0, 16))
        .map((b) => b.toString(16).padStart(2, "0"))
        .join("");
    return `rl:${hex}`;
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
