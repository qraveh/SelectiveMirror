package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMetaStore is an in-memory MetaStore for tests. Mirrors the
// state.Store API surface used by install_events.go.
type fakeMetaStore struct {
	mu   sync.Mutex
	data map[string]string
}

func newFakeMetaStore() *fakeMetaStore {
	return &fakeMetaStore{data: map[string]string{}}
}

func (f *fakeMetaStore) GetMeta(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data[key], nil
}

func (f *fakeMetaStore) SetMeta(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = value
	return nil
}

// makeView returns a SystemView with all fields populated to ENUM-
// valid values, suitable for any payload-builder test.
func makeView(version string) SystemView {
	return SystemView{
		InstallID:         "sm-test-deadbeef",
		ClientVersion:     version,
		InstallMethod:     "msi",
		BackgroundMode:    "service",
		MirrorCountBucket: "1",
		DeletePolicy:      "delete",
		HasHooks:          false,
		HasFilters:        true,
		HasAlertWebhook:   false,
		HasBandwidthLimit: false,
		RcloneVersion:     "v1.73.5-test",
	}
}

// withTestBuildKey sets the package-level buildKey so SignPayload
// works during the test. Restores the original value on cleanup.
func withTestBuildKey(t *testing.T, version string) {
	t.Helper()
	saved := buildKey
	mac := hmac.New(sha256.New, []byte("install-events-test-master"))
	mac.Write([]byte(version))
	buildKey = hex.EncodeToString(mac.Sum(nil))
	t.Cleanup(func() { buildKey = saved })
}

// startMockWorker returns an httptest server that records every
// request body and returns the next configured response.
func startMockWorker(responses []mockResponse) (*httptest.Server, *[]mockReceived) {
	idx := 0
	var mu sync.Mutex
	received := []mockReceived{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 4096)
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body = append(body, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		var parsed map[string]any
		_ = json.Unmarshal(body, &parsed)
		mu.Lock()
		received = append(received, mockReceived{
			path:   r.URL.Path,
			method: r.Method,
			body:   parsed,
		})
		i := idx
		if idx < len(responses)-1 {
			idx++
		}
		resp := responses[i]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}))
	return srv, &received
}

type mockResponse struct {
	status int
	body   string
}

type mockReceived struct {
	path   string
	method string
	body   map[string]any
}

// payloadOf extracts the inner payload object from a wire-shaped
// request body {"payload": {...}, "claimed_version": ..., "claimed_hmac_hex": ...}.
func payloadOf(t *testing.T, m mockReceived) map[string]any {
	t.Helper()
	if m.body == nil {
		t.Fatalf("request body was not parseable JSON")
	}
	p, ok := m.body["payload"].(map[string]any)
	if !ok {
		t.Fatalf("body[payload] is not a JSON object: %v", m.body)
	}
	return p
}

// ---------------------------------------------------------------------------
// SendInstallEventsIfDue gates: tier, buildKey, install_id
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_TierNone_NoEvents(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{{status: 200, body: `{"ok":true}`}})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierNone,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	if len(*received) != 0 {
		t.Errorf("TierNone should not POST anything; got %d requests", len(*received))
	}
}

func TestSendInstallEventsIfDue_NoBuildKey_NoEvents(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{{status: 200, body: `{"ok":true}`}})
	defer srv.Close()

	saved := buildKey
	buildKey = ""
	t.Cleanup(func() { buildKey = saved })

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil (skip-with-WARN, not error)", err)
	}
	if len(*received) != 0 {
		t.Errorf("no buildKey should not POST anything; got %d requests", len(*received))
	}
}

func TestSendInstallEventsIfDue_NoInstallID_NoEvents(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{{status: 200, body: `{"ok":true}`}})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	view.InstallID = ""
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	if len(*received) != 0 {
		t.Errorf("empty install_id should not POST; got %d requests", len(*received))
	}
}

// ---------------------------------------------------------------------------
// First-seen happy path
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_FirstSeen_HappyPath(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	if len(*received) != 1 {
		t.Fatalf("expected 1 request (first_seen); got %d", len(*received))
	}
	p := payloadOf(t, (*received)[0])
	if p["event_kind"] != "first_seen" {
		t.Errorf("event_kind = %v; want first_seen", p["event_kind"])
	}
	// State DB should have first_seen_at set.
	if v, _ := st.GetMeta(MetaFirstSeenAt); v == "" {
		t.Errorf("MetaFirstSeenAt is empty after success")
	}
	// last_recorded_version should also be set (upgrade branch's
	// "first run after first_seen" path writes it).
	if v, _ := st.GetMeta(MetaLastRecordedVersion); v != "0.0.0-test" {
		t.Errorf("MetaLastRecordedVersion = %q; want 0.0.0-test", v)
	}
}

// ---------------------------------------------------------------------------
// First-seen idempotency: second call doesn't fire
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_FirstSeen_AlreadyRecorded(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	// Pre-set first_seen_at + last_recorded_version as if a previous
	// successful run had recorded them.
	_ = st.SetMeta(MetaFirstSeenAt, "2026-01-01T00:00:00Z")
	_ = st.SetMeta(MetaLastRecordedVersion, "0.0.0-test")

	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	if len(*received) != 0 {
		t.Errorf("first_seen + upgrade already recorded; should not POST. Got %d", len(*received))
	}
}

// ---------------------------------------------------------------------------
// Upgrade detection: version transition fires upgrade event
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_UpgradeFires_OnVersionChange(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.1-test")

	st := newFakeMetaStore()
	// Existing install: first_seen_at recorded, last version 0.0.0
	firstSeen := time.Now().UTC().AddDate(0, 0, -50).Format(time.RFC3339)
	_ = st.SetMeta(MetaFirstSeenAt, firstSeen)
	_ = st.SetMeta(MetaLastRecordedVersion, "0.0.0-test")

	view := makeView("0.0.1-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	if len(*received) != 1 {
		t.Fatalf("expected 1 request (upgrade); got %d", len(*received))
	}
	p := payloadOf(t, (*received)[0])
	if p["event_kind"] != "upgrade" {
		t.Errorf("event_kind = %v; want upgrade", p["event_kind"])
	}
	if p["prior_version"] != "0.0.0-test" {
		t.Errorf("prior_version = %v; want 0.0.0-test", p["prior_version"])
	}
	if p["client_version"] != "0.0.1-test" {
		t.Errorf("client_version = %v; want 0.0.1-test", p["client_version"])
	}
	if p["days_since_first_seen_bucket"] != "31-90" {
		t.Errorf("days_since_first_seen_bucket = %v; want 31-90 (50 days)",
			p["days_since_first_seen_bucket"])
	}
	if v, _ := st.GetMeta(MetaLastRecordedVersion); v != "0.0.1-test" {
		t.Errorf("MetaLastRecordedVersion = %q; want 0.0.1-test", v)
	}
}

// ---------------------------------------------------------------------------
// Upgrade does NOT fire on first run with this binary (no prior version yet)
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_Upgrade_FirstRunSilent(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`}, // first_seen
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	// Fresh install: no first_seen, no last_recorded_version.

	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil", err)
	}
	// One request: first_seen. No upgrade because there's no prior version.
	if len(*received) != 1 {
		t.Fatalf("expected 1 request (first_seen only); got %d", len(*received))
	}
	p := payloadOf(t, (*received)[0])
	if p["event_kind"] != "first_seen" {
		t.Errorf("event_kind = %v; want first_seen", p["event_kind"])
	}
}

// ---------------------------------------------------------------------------
// Failure: HMAC reject doesn't write meta key (will retry next startup)
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_FirstSeen_HMACRejected_RetriesNextTime(t *testing.T) {
	srv, _ := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":false,"error":"rejected"}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil (errors are informational)", err)
	}
	// first_seen_at must NOT be set (the contract: meta key only set on success).
	if v, _ := st.GetMeta(MetaFirstSeenAt); v != "" {
		t.Errorf("MetaFirstSeenAt = %q; should be empty after failed POST", v)
	}
	// Attempts counter incremented to 1.
	if v, _ := st.GetMeta(MetaFirstSeenAttempts); v != "1" {
		t.Errorf("MetaFirstSeenAttempts = %q; want 1", v)
	}
}

// ---------------------------------------------------------------------------
// Dead-letter after max attempts
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_FirstSeen_DeadLettersAfterMaxAttempts(t *testing.T) {
	// All responses reject — simulate a persistent failure.
	srv, _ := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":false,"error":"rejected"}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	dir := t.TempDir()
	view := makeView("0.0.0-test")

	// Run up to maxInstallEventAttempts+1 times.
	for i := 0; i < maxInstallEventAttempts+1; i++ {
		_ = SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
			dir, SendOptions{Endpoint: srv.URL, DeadLetterDir: filepath.Join(dir, "dl")})
	}

	// After reaching max, attempts is exactly max (not max+1; the
	// max-th attempt dead-letters but doesn't increment further on
	// subsequent calls because the gate skips them).
	v, _ := st.GetMeta(MetaFirstSeenAttempts)
	if n, _ := strconv.Atoi(v); n < maxInstallEventAttempts {
		t.Errorf("MetaFirstSeenAttempts = %q; want >= %d", v, maxInstallEventAttempts)
	}

	// A dead-letter file must exist.
	matches, _ := filepath.Glob(filepath.Join(dir, "dl", "first_seen-*.json"))
	if len(matches) == 0 {
		t.Errorf("no dead-letter file created; expected at least one under %s/dl", dir)
	}
}

// ---------------------------------------------------------------------------
// HMAC chain integrity: server-recomputed HMAC matches client-sent HMAC
// (the same round-trip test the bug-report path uses, applied to install)
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_HMACRoundTrip(t *testing.T) {
	const masterKey = "install-events-roundtrip-master"
	const ver = "0.0.0-rt"

	saved := buildKey
	mac := hmac.New(sha256.New, []byte(masterKey))
	mac.Write([]byte(ver))
	buildKey = hex.EncodeToString(mac.Sum(nil))
	t.Cleanup(func() { buildKey = saved })

	var serverMismatch error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 0, 4096)
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body = append(body, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
		var wire struct {
			Payload      map[string]any `json:"payload"`
			ClaimedVer   string         `json:"claimed_version"`
			ClaimedHMAC  string         `json:"claimed_hmac_hex"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			serverMismatch = err
			http.Error(w, "bad json", 400)
			return
		}
		// Server-side recompute. Mirrors what telemetry.contribute()
		// does: strip event_kind + version_hmac from payload,
		// canonicalize, HMAC with derived key.
		signing := map[string]any{}
		for k, v := range wire.Payload {
			if k == "event_kind" || k == "version_hmac" {
				continue
			}
			signing[k] = v
		}
		canonical, err := CanonicalJSON(signing)
		if err != nil {
			serverMismatch = err
			http.Error(w, "canonical err", 500)
			return
		}
		dh := hmac.New(sha256.New, []byte(masterKey))
		dh.Write([]byte(ver))
		derived := dh.Sum(nil)
		eh := hmac.New(sha256.New, derived)
		eh.Write([]byte(canonical))
		expected := hex.EncodeToString(eh.Sum(nil))
		if wire.ClaimedHMAC != expected {
			serverMismatch = errors.New("hmac mismatch: client " + wire.ClaimedHMAC + " server " + expected)
			http.Error(w, "hmac mismatch", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	st := newFakeMetaStore()
	view := makeView(ver)
	err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL})
	if err != nil {
		t.Errorf("got %v; want nil (HMAC round-trip should succeed)", err)
	}
	if serverMismatch != nil {
		t.Errorf("server-side HMAC recompute mismatch: %v", serverMismatch)
	}
}

// ---------------------------------------------------------------------------
// Privacy invariant: install-event payload contains NO forbidden fields
// (FINDING 3 generalized to install events; FINDING R9 from the
// rounds-4-and-5 panel)
// ---------------------------------------------------------------------------

func TestSendInstallEventsIfDue_FirstSeen_NoForbiddenFields(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.0-test")

	st := newFakeMetaStore()
	view := makeView("0.0.0-test")
	if err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(*received) != 1 {
		t.Fatal("expected 1 request")
	}
	p := payloadOf(t, (*received)[0])

	// Required fields (installation_daily_rollup bucket-key columns + envelope).
	required := []string{
		"event_kind", "schema_version", "install_id", "client_version", "reported_at",
		"install_method", "os_family", "mirror_count_bucket", "background_mode",
		"delete_policy", "has_hooks", "has_filters", "has_alert_webhook",
		"has_bandwidth_limit", "rclone_version",
	}
	for _, f := range required {
		if _, ok := p[f]; !ok {
			t.Errorf("first_seen payload missing required field %q", f)
		}
	}

	// Forbidden fields. These ARE NOT in installation_daily_rollup
	// and would represent leakage if the binary transmitted them.
	forbidden := []string{
		"os_detail",     // FINDING 3 (round-3 panel)
		"prior_version", // upgrade-only field; first_seen must not carry it
		"days_since_first_seen_bucket", // upgrade-only
		"hostname", "user", "username",
		"path", "cwd", "exe_path",
		"backend_types", "remote_path",
	}
	for _, f := range forbidden {
		if _, ok := p[f]; ok {
			t.Errorf("first_seen payload contains forbidden field %q (privacy invariant)", f)
		}
	}
}

func TestSendInstallEventsIfDue_Upgrade_NoForbiddenFields(t *testing.T) {
	srv, received := startMockWorker([]mockResponse{
		{status: 200, body: `{"ok":true}`},
	})
	defer srv.Close()
	withTestBuildKey(t, "0.0.1-test")

	st := newFakeMetaStore()
	_ = st.SetMeta(MetaFirstSeenAt, time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339))
	_ = st.SetMeta(MetaLastRecordedVersion, "0.0.0-test")

	view := makeView("0.0.1-test")
	if err := SendInstallEventsIfDue(context.Background(), view, st, TierStandard,
		t.TempDir(), SendOptions{Endpoint: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(*received) != 1 {
		t.Fatal("expected 1 request (upgrade)")
	}
	p := payloadOf(t, (*received)[0])

	// Upgrade adds prior_version + days_since_first_seen_bucket on
	// top of the install fields.
	required := []string{
		"event_kind", "schema_version", "install_id", "client_version", "reported_at",
		"install_method", "os_family", "mirror_count_bucket", "background_mode",
		"delete_policy", "has_hooks", "has_filters", "has_alert_webhook",
		"has_bandwidth_limit", "rclone_version",
		"prior_version", "days_since_first_seen_bucket",
	}
	for _, f := range required {
		if _, ok := p[f]; !ok {
			t.Errorf("upgrade payload missing required field %q", f)
		}
	}
	// Same forbidden list as first_seen (the architectural invariant
	// is that the install payload doesn't widen between event types).
	forbidden := []string{
		"os_detail", "hostname", "user", "username",
		"path", "cwd", "exe_path", "backend_types", "remote_path",
	}
	for _, f := range forbidden {
		if _, ok := p[f]; ok {
			t.Errorf("upgrade payload contains forbidden field %q", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Transition notice (FINDING R12/R14 light-touch consent)
// ---------------------------------------------------------------------------

func TestShouldShowTransitionNotice_FiresOnceThenStops(t *testing.T) {
	st := newFakeMetaStore()
	if !ShouldShowTransitionNotice(st, TierStandard) {
		t.Errorf("first call should return true (notice unshown)")
	}
	if ShouldShowTransitionNotice(st, TierStandard) {
		t.Errorf("second call should return false (notice already shown)")
	}
}

func TestShouldShowTransitionNotice_TierNoneNeverFires(t *testing.T) {
	st := newFakeMetaStore()
	if ShouldShowTransitionNotice(st, TierNone) {
		t.Errorf("TierNone should never trigger the notice")
	}
}

func TestTransitionNoticeMessage_PointsAtPolicyAndOptOut(t *testing.T) {
	// The user-facing notice must mention `smirror telemetry policy`
	// (so they can read the contract) and `smirror telemetry none`
	// (so they can opt out). FINDING R14 (ISO 27701 §7.2.6).
	if !strings.Contains(TransitionNoticeMessage, "smirror telemetry policy") {
		t.Errorf("TransitionNoticeMessage doesn't point at telemetry policy: %q", TransitionNoticeMessage)
	}
	if !strings.Contains(TransitionNoticeMessage, "smirror telemetry none") {
		t.Errorf("TransitionNoticeMessage doesn't point at telemetry none (opt-out): %q", TransitionNoticeMessage)
	}
}

// ---------------------------------------------------------------------------
// ComputeDaysSinceFirstSeenBucket — every bucket reachable
// ---------------------------------------------------------------------------

func TestComputeDaysSinceFirstSeenBucket_AllBuckets(t *testing.T) {
	now := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		days int
		want string
	}{
		{0, "1-7"},
		{1, "1-7"},
		{7, "1-7"},
		{8, "8-30"},
		{30, "8-30"},
		{31, "31-90"},
		{90, "31-90"},
		{91, "91-365"},
		{365, "91-365"},
		{366, ">365"},
		{10000, ">365"},
	}
	for _, c := range cases {
		first := now.AddDate(0, 0, -c.days)
		got := ComputeDaysSinceFirstSeenBucket(first, now)
		if got != c.want {
			t.Errorf("days=%d: got %q, want %q", c.days, got, c.want)
		}
	}
	// Zero-value first_seen returns empty (caller must check).
	if got := ComputeDaysSinceFirstSeenBucket(time.Time{}, now); got != "" {
		t.Errorf("zero firstSeenAt: got %q, want empty", got)
	}
}
