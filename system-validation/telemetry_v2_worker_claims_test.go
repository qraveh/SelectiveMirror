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
			t.Errorf("Worker RETIRED_PATHS is missing %s — CLAIMS-MAP C-16 requires every legacy path return 410 Gone", want)
		}
	}

	if !strings.Contains(worker, "jsonResponse(410,") {
		t.Errorf("Worker does not emit 410 status for retired paths — CLAIMS-MAP C-16 requires 410 Gone")
	}

	if !strings.Contains(worker, `code: "endpoint_retired"`) {
		t.Errorf("Worker retired-path response body does not include code: \"endpoint_retired\"")
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
