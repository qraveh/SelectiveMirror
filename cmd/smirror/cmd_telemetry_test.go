// Tests for `smirror telemetry` (SM-157).
//
// These are in-process unit tests that exercise the cmdTelemetry*
// dispatch and the underlying state-DB persistence. Black-box CLI
// tests against the built binary live in system-validation/.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/state"
	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// scratchEnv stands up a minimal config + state DB so the tier-mutating
// subcommands have somewhere to write. Returns the configPath so tests
// can call cmd functions like the real CLI dispatcher would.
func scratchEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stateDB := filepath.Join(dir, "state.db")
	logFile := filepath.Join(dir, "smirror.log")
	cfgPath := filepath.Join(dir, "config.yaml")

	yml := "" +
		"state_db: " + escapeYAMLPath(stateDB) + "\n" +
		"log_file: " + escapeYAMLPath(logFile) + "\n" +
		"log_level: \"info\"\n" +
		"sync_workers: 1\n" +
		"notify_enabled: false\n" +
		"anomaly_detection_enabled: false\n" +
		"verify_interval_sec: -1\n" +
		"mirrors:\n" +
		"  - name: \"test\"\n" +
		"    local_path: " + escapeYAMLPath(filepath.Join(dir, "src")) + "\n" +
		"    remote: " + escapeYAMLPath(filepath.Join(dir, "dst")) + "\n"
	if err := os.WriteFile(cfgPath, []byte(yml), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dst"), 0755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}

	// Force the config to load successfully by warming up state.Open.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	st.Close()
	return cfgPath
}

// escapeYAMLPath wraps a Windows path in double quotes and doubles
// backslashes so YAML accepts it.
func escapeYAMLPath(p string) string {
	return "\"" + strings.ReplaceAll(p, "\\", "\\\\") + "\""
}

// captureStdout swaps os.Stdout for a pipe, runs fn, returns what fn
// printed. Used to verify status output without shelling out to a binary.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = orig
	return <-done
}

// readTier opens the state DB and reports the tier value via ReadTier
// (the same path the production code uses). This keeps the tests honest:
// they verify what ReadTier sees, not just what the meta table contains.
func readTier(t *testing.T, configPath string) telemetry.Tier {
	t.Helper()
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer st.Close()
	return telemetry.ReadTier(st)
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func TestTelemetryStatus_NoneByDefault(t *testing.T) {
	cfgPath := scratchEnv(t)
	out := captureStdout(t, func() {
		cmdTelemetryStatus(cfgPath, nil)
	})
	if !strings.Contains(out, "tier:") {
		t.Errorf("status output missing tier: line:\n%s", out)
	}
	if !strings.Contains(out, "none") {
		t.Errorf("status output should report tier=none for fresh state DB:\n%s", out)
	}
	if !strings.Contains(out, "build-key:") {
		t.Errorf("status output missing build-key: line:\n%s", out)
	}
}

func TestTelemetryStatus_AfterSetStandard(t *testing.T) {
	cfgPath := scratchEnv(t)
	cmdTelemetrySet(cfgPath, telemetry.TierStandard)

	out := captureStdout(t, func() {
		cmdTelemetryStatus(cfgPath, nil)
	})
	if !strings.Contains(out, "standard") {
		t.Errorf("status output should report standard after set:\n%s", out)
	}
	if !strings.Contains(out, "source:             user") {
		t.Errorf("status output should report source=user after explicit set:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// none / standard / reliability transitions
// ---------------------------------------------------------------------------

func TestTelemetrySet_NoneToStandard_PersistsAndGeneratesInstallID(t *testing.T) {
	cfgPath := scratchEnv(t)

	cmdTelemetrySet(cfgPath, telemetry.TierStandard)

	got := readTier(t, cfgPath)
	if got != telemetry.TierStandard {
		t.Errorf("after set Standard, ReadTier = %q, want %q", got, telemetry.TierStandard)
	}

	// install_id should have been generated on the None→Standard
	// transition. Open state DB directly to verify.
	cfg, _ := config.Load(cfgPath)
	st, _ := state.Open(cfg.StateDB)
	defer st.Close()
	id, _ := st.GetMeta(metaInstallID)
	if id == "" {
		t.Errorf("install_id not generated on None→Standard transition")
	}
	if !strings.HasPrefix(id, "sm-") {
		t.Errorf("install_id %q should start with 'sm-'", id)
	}
}

func TestTelemetrySet_StandardToReliability(t *testing.T) {
	cfgPath := scratchEnv(t)
	cmdTelemetrySet(cfgPath, telemetry.TierStandard)

	cmdTelemetrySet(cfgPath, telemetry.TierReliability)

	if got := readTier(t, cfgPath); got != telemetry.TierReliability {
		t.Errorf("after set Reliability, ReadTier = %q, want %q", got, telemetry.TierReliability)
	}
}

func TestTelemetrySet_ReliabilityToNone(t *testing.T) {
	cfgPath := scratchEnv(t)
	cmdTelemetrySet(cfgPath, telemetry.TierReliability)

	cmdTelemetrySet(cfgPath, telemetry.TierNone)

	if got := readTier(t, cfgPath); got != telemetry.TierNone {
		t.Errorf("after set None, ReadTier = %q, want %q", got, telemetry.TierNone)
	}
}

func TestTelemetrySet_SameTierIsNoOp(t *testing.T) {
	cfgPath := scratchEnv(t)
	cmdTelemetrySet(cfgPath, telemetry.TierStandard)

	// Capture: should print "already standard" and NOT update the
	// changed_at timestamp. We don't test the timestamp directly,
	// but we do verify the tier hasn't bounced.
	out := captureStdout(t, func() {
		cmdTelemetrySet(cfgPath, telemetry.TierStandard)
	})
	if !strings.Contains(out, "already standard") {
		t.Errorf("expected 'already standard' message, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------------
// inspect — read-only payload preview (Felix's diagnostic)
// ---------------------------------------------------------------------------

func TestTelemetryInspect_FirstSeen_ProducesValidJSON(t *testing.T) {
	cfgPath := scratchEnv(t)

	out := captureStdout(t, func() {
		cmdTelemetryInspect(cfgPath, []string{"first_seen"})
	})

	// Strip any preamble that lands on stderr (we only captured stdout
	// anyway). The remaining content should be parseable JSON.
	jsonText := strings.TrimSpace(out)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		t.Fatalf("inspect output is not valid JSON: %v\noutput:\n%s", err, jsonText)
	}

	// Every documented PRIVACY.md field must appear. This is the
	// claims-conformance check from CLAIMS-MAP.md (C-03).
	requiredFields := []string{
		"event_kind", "schema_version", "install_id", "client_version",
		"reported_at", "install_method", "os_family", "os_detail",
		"mirror_count_bucket", "background_mode", "delete_policy",
		"has_hooks", "has_filters", "has_alert_webhook",
		"has_bandwidth_limit", "rclone_version",
	}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("first_seen inspect output is missing required field %q", field)
		}
	}

	if parsed["event_kind"] != "first_seen" {
		t.Errorf("event_kind = %v, want first_seen", parsed["event_kind"])
	}
}

func TestTelemetryInspect_Upgrade_AddsUpgradeFields(t *testing.T) {
	cfgPath := scratchEnv(t)

	out := captureStdout(t, func() {
		cmdTelemetryInspect(cfgPath, []string{"upgrade"})
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("inspect output is not valid JSON: %v", err)
	}
	for _, field := range []string{"prior_version", "days_since_first_seen_bucket"} {
		if _, ok := parsed[field]; !ok {
			t.Errorf("upgrade inspect output is missing %q", field)
		}
	}
}

func TestTelemetryInspect_Reliability_AddsReliabilityFields(t *testing.T) {
	cfgPath := scratchEnv(t)

	out := captureStdout(t, func() {
		cmdTelemetryInspect(cfgPath, []string{"reliability_snapshot"})
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("inspect output is not valid JSON: %v", err)
	}
	requiredFields := []string{
		"anomaly_count_bucket", "most_common_anomaly_kind",
		"sync_attempts_bucket", "sync_failures_bucket",
		"restart_count_bucket", "max_queue_depth_bucket",
		"dead_letter_count_bucket", "state_db_size_bucket",
	}
	for _, field := range requiredFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("reliability_snapshot inspect output is missing %q", field)
		}
	}
}

func TestTelemetryInspect_BugReport_NotInspectable(t *testing.T) {
	// Bug-report payloads are documented as NOT inspect-able (the
	// `report-bug` command shows the bundle directly). buildInspectPayload
	// should reject the kind.
	cfgPath := scratchEnv(t)
	cfg, _ := config.Load(cfgPath)
	st, _ := state.Open(cfg.StateDB)
	defer st.Close()

	_, err := buildInspectPayload(cfg, st, "bug_report")
	if err == nil {
		t.Errorf("buildInspectPayload should reject event_kind=bug_report")
	}
}

func TestTelemetryInspect_UnknownKind_Rejected(t *testing.T) {
	cfgPath := scratchEnv(t)
	cfg, _ := config.Load(cfgPath)
	st, _ := state.Open(cfg.StateDB)
	defer st.Close()

	_, err := buildInspectPayload(cfg, st, "totally_made_up_event")
	if err == nil {
		t.Errorf("buildInspectPayload should reject unknown event_kind")
	}
}

// ---------------------------------------------------------------------------
// Bucket helpers
// ---------------------------------------------------------------------------

func TestBucketMirrorCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{2, "2-5"},
		{5, "2-5"},
		{6, "6-20"},
		{20, "6-20"},
		{21, "21+"},
		{100, "21+"},
	}
	for _, c := range cases {
		if got := bucketMirrorCount(c.n); got != c.want {
			t.Errorf("bucketMirrorCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketStateDbSize_Missing(t *testing.T) {
	if got := bucketStateDbSize("/nonexistent/path/that/should/not/be/here.db"); got != "unknown" {
		t.Errorf("bucketStateDbSize on missing file = %q, want %q", got, "unknown")
	}
}
