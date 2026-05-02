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
 *   - POST /v1/contribute  → telemetry.contribute() RPC (the only ingest)
 *   - POST /v1/forget      → 410 Gone (intentional; v2 has no per-install
 *                            record to delete; see docs/PRIVACY.md)
 *
 * Background:
 *   v1 endpoints (/v1/bug-reports, /v1/installations/report) existed in
 *   earlier deploys (0.9.4-dev … 0.9.18-dev). They were never wired to a
 *   live client posting path because SM-160 (0.9.18-dev) deleted the
 *   client-side SendReport before the server was ready. Under v2
 *   (stream-aggregate-and-discard) the architecture has converged on
 *   one ingest function and no per-event tables; the v1 paths were
 *   removed from the Worker surface in 0.9.7x-dev.
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

// The single ingest path — calls the telemetry.contribute() RPC. The
// function verifies the HMAC, dispatches by event_kind, UPSERTs the
// matching rollup counter, and returns. No raw event row is ever
// persisted.
const RPC_PATH = "/v1/contribute";
const RPC_FUNCTION = "contribute";

// Retired paths fall into two semantic groups so the response message
// matches what the path actually was:
//
// RETIRED_FORGET_PATHS — explicitly removed under v2 because there is
// no per-install server data to delete (stream-aggregate-and-discard).
// "Nothing to forget" is the correct framing for these.
//
// RETIRED_INGEST_PATHS — v1 ingest paths retired in 0.9.7x-dev when
// the v1 schema was dropped. The correct framing is "endpoint moved";
// the substitute is /v1/contribute. Same 410 status, different
// message so a confused operator sees the actionable hint.
const RETIRED_FORGET_PATHS = new Set([
    "/v1/forget",
]);

const RETIRED_INGEST_PATHS = new Set([
    "/v1/bug-reports",
    "/v1/installations/report",
]);

// Combined for any callsite that just needs "is this a retired path".
const RETIRED_PATHS = new Set<string>([
    ...RETIRED_FORGET_PATHS,
    ...RETIRED_INGEST_PATHS,
]);

const ALLOWED_PATHS = new Set([
    RPC_PATH,
]);

export default {
    async fetch(request: Request, env: Env): Promise<Response> {
        const url = new URL(request.url);

        // Retired path: 410 Gone with the appropriate message.
        // CHECKED FIRST — before method allowlist — so a GET on a
        // retired endpoint returns 410 Gone (the endpoint is gone),
        // not 405 Method Not Allowed (the endpoint exists but
        // doesn't accept GET). Either is technically correct, but
        // 410 communicates the architectural truth.
        if (RETIRED_FORGET_PATHS.has(url.pathname)) {
            return jsonResponse(410, {
                code: "endpoint_retired",
                message:
                    "Under SelectiveMirror v2 (stream-aggregate-and-" +
                    "discard), no per-install server data exists; " +
                    "there is nothing to forget. Run `smirror telemetry " +
                    "none` to stop contributing. See docs/PRIVACY.md.",
            });
        }
        if (RETIRED_INGEST_PATHS.has(url.pathname)) {
            return jsonResponse(410, {
                code: "endpoint_retired",
                message:
                    "Endpoint removed in v2. Use POST /v1/contribute " +
                    "instead. See docs/telemetry-architecture-v2.md.",
            });
        }

        // Method allowlist (after retired-path check, so retired
        // paths return 410 regardless of method).
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

        // Per-IP rate limit (best-effort; per-edge-PoP, not global). The
        // KV key is a salted hash of the IP — see SM-163 fix in the
        // header comment. If RATE_LIMIT_SALT_SECRET is not configured,
        // rate-limiting is SKIPPED rather than fall back to a raw-IP
        // key (which would violate the privacy claim). Cloudflare's
        // built-in DDoS protection still applies as the floor.
        const clientIP = request.headers.get("CF-Connecting-IP") || "unknown";
        if (env.RATE_LIMIT_KV && env.RATE_LIMIT_SALT_SECRET) {
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
        } else if (env.RATE_LIMIT_KV && !env.RATE_LIMIT_SALT_SECRET) {
            // KV is bound but the salt secret is missing. Don't fall
            // back to raw-IP keys — that would store IPs in KV. Log
            // and skip rate-limiting; operator must set the secret.
            console.warn(
                "RATE_LIMIT_SALT_SECRET is not set; per-IP rate limiting is DISABLED to avoid storing raw IPs in KV. Set the secret to enable it.",
            );
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

        // Body shape validation — FINDING 1 from the 2026-05-02
        // validation pass. Without this, a malformed body would reach
        // PostgREST and PGRST202 would echo the function name, parameter
        // names, and parameter ORDER hints back to the client. That
        // contradicts the architectural posture: regulators / auditors
        // / curious users should learn the schema only by reading
        // docs/telemetry-v2.sql, not by probing the API. Validate here
        // so PGRST never sees a malformed shape and never has reason
        // to surface its schema-cache hints.
        if (!isValidContributeBody(bodyBytes)) {
            return jsonResponse(400, {
                code: "bad_request",
                message: "Body must be a JSON object with exactly the keys " +
                         "'payload' (object), 'claimed_version' (string), " +
                         "and 'claimed_hmac_hex' (string).",
            });
        }

        // Forward to PostgREST RPC. The body is forwarded verbatim;
        // the Postgres function expects parameter-bound JSONB.
        const supabaseBase = `https://${env.SUPABASE_PROJECT_REF}.supabase.co`;
        const upstreamURL = `${supabaseBase}/rest/v1/rpc/${RPC_FUNCTION}`;

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

            // Happy path: contribute() ran (it always returns 200 with
            // its own JSON, even on rejection — that's part of the
            // contract so callers can read the reason). Pass through.
            if (upstreamResponse.status === 200) {
                return new Response(upstreamResponse.body, {
                    status: 200,
                    headers: {
                        "Content-Type": upstreamResponse.headers.get("Content-Type") || "application/json",
                    },
                });
            }

            // Anything other than 200 from PostgREST means a server
            // / config issue, not a legitimate contribute() outcome.
            // FINDING 1: PGRST 4xx bodies expose schema cache details
            // (function name, parameter signatures, parameter-order
            // hints). Don't pass them through.
            // FINDING 4: an upstream Cloudflare-fronted Supabase 5xx
            // arrives as a non-throw with text/html error page —
            // don't pass that through either.
            // Either way: log the actual status for ops debugging,
            // return a generic 502 to the client.
            console.warn(
                `Worker: upstream returned ${upstreamResponse.status} ` +
                `(non-200). Returning generic 502 to client.`,
            );
            return jsonResponse(502, {
                code: "upstream_unavailable",
                message: "Telemetry endpoint temporarily unavailable.",
            });
        } catch (err) {
            // Upstream throw (DNS, connection reset, etc.). Same
            // resolution: don't leak details.
            return jsonResponse(502, {
                code: "upstream_unavailable",
                message: "Telemetry endpoint temporarily unavailable.",
            });
        }
    },
};

// isValidContributeBody — FINDING 1 from the 2026-05-02 validation
// pass. Returns true iff the body parses to a JSON object with exactly
// the three documented keys, each with the expected type. Any other
// shape (extra keys, missing keys, wrong types, non-object payload,
// non-string claimed_version, etc.) returns false and the caller
// emits a generic 400. Keeping the validation rules minimal here
// since the server-side function (`telemetry.contribute()`) does the
// substantive checks (HMAC, schema_violation, unknown_event).
function isValidContributeBody(bodyBytes: ArrayBuffer): boolean {
    let parsed: unknown;
    try {
        parsed = JSON.parse(new TextDecoder().decode(bodyBytes));
    } catch {
        return false;
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        return false;
    }
    const obj = parsed as Record<string, unknown>;

    // Required keys — exactly these three.
    const expected = new Set(["payload", "claimed_version", "claimed_hmac_hex"]);
    for (const k of Object.keys(obj)) {
        if (!expected.has(k)) {
            return false;
        }
    }
    for (const k of expected) {
        if (!(k in obj)) {
            return false;
        }
    }

    // Type checks. payload must be an object (not an array, not a
    // primitive). claimed_version + claimed_hmac_hex must be
    // non-empty strings.
    if (
        typeof obj.payload !== "object" ||
        obj.payload === null ||
        Array.isArray(obj.payload)
    ) {
        return false;
    }
    if (typeof obj.claimed_version !== "string" || obj.claimed_version.length === 0) {
        return false;
    }
    if (typeof obj.claimed_hmac_hex !== "string" || obj.claimed_hmac_hex.length === 0) {
        return false;
    }
    return true;
}

/**
 * Rate-limit check with salted-IP-hash key (SM-163 fix).
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
 * Caller must verify saltSecret is non-empty before calling — if it
 * isn't, rate-limiting should be skipped entirely (see fetch handler
 * above) rather than fall back to a raw-IP key, which would violate
 * the "no IPs in storage" privacy claim.
 */
async function checkRateLimit(
    kv: KVNamespace,
    ip: string,
    saltSecret: string,
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

async function rateLimitKey(ip: string, saltSecret: string): Promise<string> {
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
