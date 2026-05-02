// Telemetry v2 contribute() RPC client.
//
// SM-158 ships the bug-report --submit pipeline; this file is the HTTP
// client smirror.exe uses to POST a single bug_report (or any other
// event_kind) into telemetry.contribute() on the Cloudflare Worker.
//
// The client:
//
//   1. Composes the canonical payload (CanonicalJSON, length-first key
//      ordering — matches PostgreSQL JSONB::text byte-for-byte).
//   2. Computes HMAC-SHA256 over the canonical bytes using the per-version
//      derived buildKey (see hmac.go::SignPayload). On a -dev build with
//      no buildKey injected, returns ErrNoBuildKey so callers can degrade
//      gracefully rather than 500-ing the user.
//   3. POSTs {payload, claimed_version, claimed_hmac_hex} to
//      <endpoint>/v1/contribute. The endpoint defaults to the production
//      Worker URL but can be overridden via SMIRROR_TELEMETRY_ENDPOINT
//      for testing.
//   4. Decodes the {ok, error?} response. ok=true → nil. ok=false →
//      ErrRejected/ErrSchemaViolation/ErrUnknownEvent depending on the
//      server's reason. Network/HTTP errors wrap into ErrNetwork.
//
// What this file deliberately does NOT do:
//
//   - It does not retry. The bug-report --submit path is interactive and
//      the user is waiting at the terminal; failing fast with a clear
//      message + the GitHub-issue URL is better UX than a silent retry
//      loop. The on-disk queue (queue.go) exists for non-interactive
//      paths (first_seen, upgrade) where retry-later is appropriate.
//   - It does not enforce tier gating. That's the caller's job. This
//      file's preconditions: HasBuildKey() returns true, and the caller
//      has decided (per tier + --one-shot + interactive consent) that a
//      contribution is appropriate.
//   - It does not classify the bug. classify.go owns that mapping.
//
// Privacy note. The "payload" passed to Contribute() is the SAME jsonb
// value the server will UPSERT into the rollup table — bucketed,
// classified, and structurally low-cardinality by construction. There
// is no narrative, no log lines, no install_id, no remote URLs. The
// rule from docs/PRIVACY.md "Bug reports are not telemetry" holds: the
// narrative path is GitHub Issues, not this RPC.

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultContributeEndpoint is the production Cloudflare Worker URL.
// Override per-call by setting SMIRROR_TELEMETRY_ENDPOINT in the
// environment. The endpoint MUST NOT include a trailing slash and MUST
// NOT include the /v1/contribute path component — Contribute appends
// that itself so the same env var works for the smoke test, the live
// endpoint, and any future dev deployment.
const DefaultContributeEndpoint = "https://smirror-telemetry.selectivemirror.workers.dev"

// Contribute errors. Callers can errors.Is against these to discriminate.
var (
	// ErrRejected — the server's HMAC check failed. Either the build's
	// per-version key is wrong (CI misconfiguration) or the payload was
	// tampered with in transit. Not retryable.
	ErrRejected = errors.New("telemetry contribute: HMAC rejected by server")

	// ErrSchemaViolation — the payload's structural shape didn't match
	// the server-side schema for the declared event_kind. Indicates a
	// client bug (missing field, bad enum value, type mismatch). Not
	// retryable; needs a code change.
	ErrSchemaViolation = errors.New("telemetry contribute: payload failed server-side schema validation")

	// ErrUnknownEvent — event_kind is not one of {first_seen, upgrade,
	// bug_report, reliability_snapshot}. Client bug. Not retryable.
	ErrUnknownEvent = errors.New("telemetry contribute: server does not recognize event_kind")

	// ErrNetwork — transport or HTTP-status failure (non-2xx, timeout,
	// DNS, etc.). The caller should report it to the user with the
	// GitHub-issue URL fallback so the narrative still has somewhere
	// to land.
	ErrNetwork = errors.New("telemetry contribute: network failure")

	// ErrServerMalformed — the server returned 2xx but a body we can't
	// parse, or returned ok=false with no recognizable reason. Treated
	// as a bug in the contract; user gets the URL fallback.
	ErrServerMalformed = errors.New("telemetry contribute: server returned an unexpected response shape")
)

// ContributeOptions tunes one Contribute call. The zero value is the
// production default (live Worker, 10s timeout, no extra headers).
type ContributeOptions struct {
	// Endpoint overrides DefaultContributeEndpoint and the env var.
	// Used by tests with httptest.NewServer.
	Endpoint string

	// Timeout caps the entire HTTP call (Dial + TLS + Request + Body
	// read). Defaults to 10 seconds.
	Timeout time.Duration

	// HTTPClient lets tests inject a custom transport. If nil, the
	// function builds a transport bounded by Timeout.
	HTTPClient *http.Client
}

// Contribute signs and posts a single payload. version MUST match the
// build's actual version string (the same one used to derive buildKey
// at CI time); contribute() server-side will refuse the HMAC otherwise.
//
// payload is a map with the dimension fields the server expects for
// the declared event_kind (see _bump_install / _bump_bug /
// _bump_reliability in docs/telemetry-v2.sql). The "event_kind" field
// MUST be in payload — Contribute does not derive it.
//
// Returns nil on a successful contribution. On failure, returns one of
// the sentinel errors above wrapped with a context-rich message.
func Contribute(ctx context.Context, version string, payload map[string]any, opts ContributeOptions) error {
	if !HasBuildKey() {
		return fmt.Errorf("%w: this build was not signed at CI time (HasBuildKey=false)", ErrNoBuildKey)
	}
	if version == "" {
		return errors.New("telemetry contribute: version must be non-empty (server requires it for per-version HMAC verification)")
	}
	if payload == nil {
		return errors.New("telemetry contribute: payload must be non-nil")
	}
	if _, ok := payload["event_kind"]; !ok {
		return errors.New("telemetry contribute: payload must include event_kind")
	}

	// 1. Canonicalize the inner payload. The server strips event_kind
	//    and version_hmac before recomputing the HMAC (see
	//    telemetry-v2.sql contribute() body), so we sign over the
	//    payload MINUS those fields. event_kind we omit from the
	//    signing-input copy; version_hmac was never in payload (we add
	//    it post-sign on the wire only — actually: we never add it. The
	//    wire shape is {payload, claimed_version, claimed_hmac_hex}.
	//    The server recomputes HMAC over (payload - 'version_hmac' -
	//    'event_kind') so the same canonical bytes used to sign must
	//    be used by the server to verify. We achieve this by signing
	//    over payload-with-event_kind-removed, but sending the full
	//    payload-WITH-event_kind on the wire so the server's strip-and-
	//    reverify routine produces the same bytes).
	signingPayload := make(map[string]any, len(payload))
	for k, v := range payload {
		if k == "event_kind" || k == "version_hmac" {
			continue
		}
		signingPayload[k] = v
	}
	canonical, err := CanonicalJSON(signingPayload)
	if err != nil {
		return fmt.Errorf("telemetry contribute: canonical JSON: %w", err)
	}

	// 2. Sign.
	hmacHex, err := SignPayload(canonical)
	if err != nil {
		return fmt.Errorf("telemetry contribute: sign: %w", err)
	}

	// 3. Build the wire body. The Worker forwards this directly to
	//    PostgREST as RPC params for telemetry.contribute(payload,
	//    claimed_version, claimed_hmac_hex).
	body := map[string]any{
		"payload":          payload,
		"claimed_version":  version,
		"claimed_hmac_hex": hmacHex,
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telemetry contribute: marshal wire body: %w", err)
	}

	// 4. Resolve endpoint.
	endpoint := opts.Endpoint
	if endpoint == "" {
		endpoint = strings.TrimRight(os.Getenv("SMIRROR_TELEMETRY_ENDPOINT"), "/")
	}
	if endpoint == "" {
		endpoint = DefaultContributeEndpoint
	}
	url := strings.TrimRight(endpoint, "/") + "/v1/contribute"

	// 5. HTTP client.
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	// 6. POST.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyJSON))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrNetwork, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", version))

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return fmt.Errorf("%w: read response: %v", ErrNetwork, err)
	}

	// 7. Decode.
	if resp.StatusCode == http.StatusGone {
		// 410 — endpoint retired (a v1 client hitting the v2 Worker).
		// Surface as a clear network-level error that hints at the
		// architectural issue.
		return fmt.Errorf("%w: server returned 410 Gone — endpoint retired (this build may be hitting a v1 path)", ErrNetwork)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: HTTP %d: %s", ErrNetwork, resp.StatusCode, summarize(respBody))
	}

	var parsed struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("%w: %v (body: %s)", ErrServerMalformed, err, summarize(respBody))
	}
	if parsed.OK {
		return nil
	}

	switch {
	case parsed.Error == "rejected":
		return ErrRejected
	case strings.HasPrefix(parsed.Error, "schema_violation"):
		return fmt.Errorf("%w: %s", ErrSchemaViolation, parsed.Error)
	case parsed.Error == "unknown_event":
		return ErrUnknownEvent
	case parsed.Error == "":
		return fmt.Errorf("%w: ok=false with no error string", ErrServerMalformed)
	default:
		return fmt.Errorf("%w: server reason: %s", ErrServerMalformed, parsed.Error)
	}
}

// summarize trims a response body to a short diagnostic snippet, safe
// to put in an error message that may bubble up to the user.
func summarize(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
