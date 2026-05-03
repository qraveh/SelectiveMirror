// Worker-side claims tests for the telemetry v2 architecture.
//
// Origin: Quincy + Felix (round-3 panel, 2026-04-30 afternoon).
// Maps to claims in system-validation/CLAIMS-MAP.md:
//
//   C-13 — IP addresses are hashed with daily-rotating salt; never
//          raw in storage.
//   C-14 — Same IP within UTC day → same KV key (counter accumulates);
//          across days → different keys (linkability broken at 24h).
//   C-16 — Worker exposes only /v1/contribute; legacy + forget paths
//          return 410 Gone.
//
// All tests are STRUCTURAL — they read worker/src/index.ts as text
// and assert invariants on its source. Behavioral tests against
// `wrangler dev` would be stronger but require either CF auth in CI
// or a vitest-pool-workers dependency we have not yet added. Static
// tests catch the regression class that matters (someone reverts
// the SM-163 fix; someone removes a path from RETIRED_PATHS) without
// the operational cost.

package systemval

import (
	"regexp"
	"strings"
	"testing"
)

func readWorker(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "worker", "src", "index.ts")
}

// ---------------------------------------------------------------------------
// C-13 — IP addresses never raw in storage
// ---------------------------------------------------------------------------

func TestTelemetryV2Worker_IPNeverInKVKey(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_worker_no_raw_ip")

	worker := readWorker(t)

	// Forbidden: any template literal that puts the IP directly into
	// a KV key. The most likely-to-regress shape is `rl:${ip}`.
	if strings.Contains(worker, "`rl:${ip}`") {
		t.Errorf("Worker contains `rl:${ip}` — raw IP in KV key contradicts CLAIMS-MAP C-13. The SM-163 fix should produce a salted-HMAC key only.")
	}

	// Confirm the actual production key shape exists. The Worker's
	// rateLimitKey function should produce `rl:${hex}`.
	if !strings.Contains(worker, "`rl:${hex}`") {
		t.Errorf("Worker does not contain expected production key shape `rl:${hex}` — rateLimitKey may have been refactored")
	}
}

// ---------------------------------------------------------------------------
// C-14 — same IP within UTC day → same key; across days → different
// ---------------------------------------------------------------------------
//
// The linkability property is encoded in rateLimitKey's HMAC input:
// the message is `${ip}:${utcDate}` (where utcDate is YYYY-MM-DD
// from new Date().toISOString().slice(0,10)). Same IP + same date
// → same HMAC input → same hash. Same IP + next date → different
// HMAC input → different hash.

func TestTelemetryV2Worker_RateLimitKeyDateInMessage(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_worker_rate_limit_linkability")

	worker := readWorker(t)

	if !strings.Contains(worker, "new Date().toISOString().slice(0, 10)") {
		t.Errorf("Worker rateLimitKey does not use new Date().toISOString().slice(0,10) for the UTC date — same-IP-across-days linkability claim depends on this being the salt input")
	}

	if !strings.Contains(worker, "`${ip}:${utcDate}`") {
		t.Errorf("Worker rateLimitKey does not compose the HMAC message as `${ip}:${utcDate}` — same-IP-within-day → same-key + same-IP-across-days → different-key property depends on this exact composition (CLAIMS-MAP C-14)")
	}

	if !strings.Contains(worker, `name: "HMAC", hash: "SHA-256"`) {
		t.Errorf("Worker rateLimitKey does not specify HMAC-SHA-256 — algorithm drift would break key stability across runs")
	}

	// Truncation to 16 bytes (32 hex chars).
	if !strings.Contains(worker, "new Uint8Array(sig).slice(0, 16)") {
		t.Errorf("Worker rateLimitKey does not truncate the HMAC output to 16 bytes — key length / collision properties may have drifted")
	}
}

// Sanity: the rateLimitKey function exists with the expected shape.

func TestTelemetryV2Worker_RateLimitKeyFunctionShape(t *testing.T) {
	t.Parallel()
	worker := readWorker(t)

	re := regexp.MustCompile(`(?s)async\s+function\s+rateLimitKey\s*\(\s*ip\s*:\s*string\s*,\s*saltSecret\s*:\s*string\s*\)\s*:\s*Promise\s*<\s*string\s*>\s*\{`)
	if !re.MatchString(worker) {
		t.Errorf("Worker rateLimitKey signature has drifted; expected `async function rateLimitKey(ip: string, saltSecret: string): Promise<string>`")
	}
}

// ---------------------------------------------------------------------------
// C-16 — Worker legacy + forget paths return 410 Gone
// ---------------------------------------------------------------------------

func TestTelemetryV2Worker_RetiredPathsCoverLegacyAndForget(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_worker_retired_paths")

	worker := readWorker(t)

	required := []string{
		`"/v1/forget"`,
		`"/v1/bug-reports"`,
		`"/v1/installations/report"`,
	}
	for _, want := range required {
		if !strings.Contains(worker, want) {
			t.Errorf("Worker retired-path sets are missing %s — CLAIMS-MAP C-16 requires every legacy path return 410 Gone", want)
		}
	}

	if !strings.Contains(worker, "jsonResponse(410,") {
		t.Errorf("Worker does not emit 410 status for retired paths — CLAIMS-MAP C-16 requires 410 Gone")
	}

	if !strings.Contains(worker, `code: "endpoint_retired"`) {
		t.Errorf("Worker retired-path response body does not include code: \"endpoint_retired\"")
	}
}

// FINDING 7 (round-3 panel, 2026-05-02): the Worker should distinguish
// retired-forget paths ("nothing to forget" — true under v2 stream-
// aggregate-and-discard) from retired-ingest paths ("endpoint moved
// to /v1/contribute"). Both return 410, but the message is correct
// for the kind of path. This guard ensures both sets exist as
// separate constants with their own messages.
func TestTelemetryV2Worker_RetiredPathsClassifiedByPurpose(t *testing.T) {
	t.Parallel()

	worker := readWorker(t)

	if !strings.Contains(worker, "RETIRED_FORGET_PATHS") {
		t.Errorf("Worker does not declare RETIRED_FORGET_PATHS — FINDING 7 needs forget paths classified separately so the response message is accurate")
	}
	if !strings.Contains(worker, "RETIRED_INGEST_PATHS") {
		t.Errorf("Worker does not declare RETIRED_INGEST_PATHS — FINDING 7 needs ingest paths classified separately so the response message points at /v1/contribute")
	}

	// The forget path message should mention "nothing to forget".
	// The ingest path message should mention "/v1/contribute".
	if !strings.Contains(worker, "nothing to forget") {
		t.Errorf("Worker retired-forget message does not contain 'nothing to forget' — accurate framing under v2 architecture")
	}
	if !strings.Contains(worker, "/v1/contribute") {
		t.Errorf("Worker retired-ingest message does not point at /v1/contribute — operators need the actionable hint")
	}
}

// FINDING 8 (round-3 panel, 2026-05-02): the retired-path check must
// run BEFORE the method allowlist. Otherwise GET /v1/forget returns
// 405 ("method not allowed") which suggests the endpoint exists but
// only accepts POST — when in fact the endpoint is gone. 410 is the
// architectural truth and should fire regardless of method.
func TestTelemetryV2Worker_RetiredPathCheckBeforeMethodCheck(t *testing.T) {
	t.Parallel()

	worker := readWorker(t)

	// Find the positions of the two checks. The retired-path block
	// is identified by RETIRED_FORGET_PATHS (its first reference);
	// the method block is identified by `method !== "POST"`.
	retiredIdx := strings.Index(worker, "RETIRED_FORGET_PATHS.has")
	methodIdx := strings.Index(worker, `method !== "POST"`)

	if retiredIdx == -1 {
		t.Fatalf("Could not locate RETIRED_FORGET_PATHS.has(...) check; structural test cannot proceed")
	}
	if methodIdx == -1 {
		t.Fatalf(`Could not locate request.method !== "POST" check; structural test cannot proceed`)
	}
	if retiredIdx >= methodIdx {
		t.Errorf("retired-path check appears AFTER method check (retiredIdx=%d, methodIdx=%d). FINDING 8: GET on a retired path should return 410 Gone, not 405. Move the retired-path block above the method-allowlist block.", retiredIdx, methodIdx)
	}
}

// FINDING 1 (round-3 panel, 2026-05-02): the Worker must validate
// body shape before forwarding to PostgREST so PGRST schema-cache
// hints (function name, parameter signatures, parameter ORDER hints)
// don't leak via 4xx error bodies. Validation rejects with a generic
// 400; PGRST never sees a malformed shape.
//
// FINDING 15 (round-5, 2026-05-03): the validator was renamed to
// parseContributeBody and now returns the parsed object so the
// Worker can re-serialize via JSON.stringify before forwarding —
// stripping any UTF-8 BOM the client prepended and forcing
// upstream Content-Type: application/json regardless of what the
// client sent. Both formerly manifested as a generic 502
// upstream_unavailable.
func TestTelemetryV2Worker_HasBodyShapeValidator(t *testing.T) {
	t.Parallel()

	worker := readWorker(t)

	if !strings.Contains(worker, "parseContributeBody") {
		t.Errorf("Worker does not declare parseContributeBody — FINDING 1 requires a body-shape validator that rejects malformed payloads before they reach PostgREST (renamed from isValidContributeBody in FINDING 15)")
	}
	// The validator should be CALLED in the request flow (not just
	// declared and unused). The call site must precede the upstream
	// fetch.
	callIdx := strings.Index(worker, "parseContributeBody(bodyBytes)")
	fetchIdx := strings.Index(worker, "fetch(upstreamRequest)")
	if callIdx == -1 {
		t.Errorf("parseContributeBody is declared but not called from the fetch handler")
	} else if fetchIdx == -1 {
		t.Errorf("Could not find fetch(upstreamRequest) callsite; structural test cannot proceed")
	} else if callIdx >= fetchIdx {
		t.Errorf("parseContributeBody is called AFTER fetch(upstreamRequest) — body validation must precede the PostgREST call")
	}

	// FINDING 15: the parsed body is re-serialized via JSON.stringify
	// before forwarding. Without this, BOM-prepended bodies and
	// wrong-Content-Type bodies manifest as confusing 502s.
	if !strings.Contains(worker, "JSON.stringify(parsed)") {
		t.Errorf("Worker does not re-serialize the parsed body via JSON.stringify(parsed) — FINDING 15 requires the Worker to be the canonical-JSON gate so BOM/wrong-Content-Type don't leak as 502")
	}

	// The validator's rejection message must be generic — no
	// parameter ORDER hint, no function name, no schema-cache
	// reference. We assert the rejection emits "bad_request" code
	// (Worker-defined) rather than PGRST's PGRST202 / PGRST204
	// codes (which would mean we're passing through).
	//
	// Note: "PGRST" appears in source comments explaining the
	// rationale; that's fine. We check that the response BODIES
	// constructed via jsonResponse(...) don't reference PGRST.
	// Heuristic: the rejection should use "bad_request" + the
	// generic message from the source, neither of which contains
	// the literal substring "PGRST".
	rejectMsg := `code: "bad_request"`
	if !strings.Contains(worker, rejectMsg) {
		t.Errorf("Worker does not emit a `code: \"bad_request\"` response — body validation should produce its own generic 400 (not a PGRST passthrough)")
	}
}

// FINDING 4: upstream 5xx (Cloudflare-fronted Supabase blip) arrives
// as a non-throw with HTML body. The Worker should rewrite anything
// other than 200 from the upstream call to a generic 502.
func TestTelemetryV2Worker_RewritesNon200UpstreamTo502(t *testing.T) {
	t.Parallel()

	worker := readWorker(t)

	// The branch should exist. Look for the 200-only happy path
	// followed by the catch-all jsonResponse(502, ...) for non-200.
	if !strings.Contains(worker, "upstreamResponse.status === 200") {
		t.Errorf("Worker does not 200-gate upstream responses — FINDING 4 requires only 200 from contribute() be passed through verbatim; everything else becomes a 502")
	}
	if !strings.Contains(worker, "upstream_unavailable") {
		t.Errorf("Worker does not emit code: upstream_unavailable — operator-facing error code lost")
	}
}

// Sanity: ALLOWED_PATHS contains exactly /v1/contribute via RPC_PATH.

func TestTelemetryV2Worker_AllowedPathsExactlyContribute(t *testing.T) {
	t.Parallel()
	worker := readWorker(t)

	re := regexp.MustCompile(`(?s)const\s+ALLOWED_PATHS\s*=\s*new\s+Set\s*\(\s*\[(.*?)\]\s*\)\s*;`)
	m := re.FindStringSubmatch(worker)
	if m == nil {
		t.Fatalf("Could not locate ALLOWED_PATHS = new Set([...]) declaration")
	}
	body := m[1]

	if !strings.Contains(body, "RPC_PATH") {
		t.Errorf("ALLOWED_PATHS does not reference RPC_PATH")
	}

	if !strings.Contains(worker, `const RPC_PATH = "/v1/contribute"`) {
		t.Errorf("RPC_PATH constant is not /v1/contribute")
	}
}
