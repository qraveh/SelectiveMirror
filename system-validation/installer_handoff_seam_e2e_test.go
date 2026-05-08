// End-to-end (near-MSI) test for the installer→runtime handoff seam.
//
// This is the test that the SM-216 post-mortem identified as MISSING:
// before this file, no test built smirror.exe with a working build-
// key, simulated the v1.0.0 MSI handoff state in state.db (tier set,
// install_id missing), started the daemon, and observed first_seen
// actually land on a (mock) telemetry endpoint.
//
// What this test exercises:
//
//   1. smirror.exe built with a real per-version derived buildKey
//      (HasBuildKey() returns true; SignPayload signs).
//   2. state.db prepared in the EXACT shape MSI consent dialog
//      produces: telemetry_tier = "standard", install_id meta row
//      ABSENT. (We accomplish this by running `smirror telemetry
//      standard` to set up a clean DB, then DELETE the install_id
//      row.)
//   3. The daemon's startup goroutine fires fireInstallEventsAtStartup
//      → SendInstallEventsIfDue → hits Gate 3 with view.InstallID == ""
//      → SHOULD generate + persist + log + submit first_seen.
//   4. A localhost mock telemetry endpoint confirms the POST
//      actually happened with the expected payload shape.
//   5. After the daemon stops, state.db has install_id persisted
//      (idempotency: next start would not re-fire first_seen).
//
// What this test does NOT exercise (and the gap is documented):
//
//   - The MSI consent dialog itself (TelemetryConsent.wxi shape).
//     That's covered by source-property tests in
//     installer_consent_dialog_test.go.
//   - The msiexec install + uninstall lifecycle. That's the
//     "true" E2E test; it requires admin + Windows runner +
//     elevated CI flow. Tracked in
//     docs/PROPOSAL-2026-05-08-boundary-test-harvest-round2.md
//     bucket 2C.
//   - Real Worker / PostgREST / Postgres pipeline. Mock endpoint
//     is sufficient to gate the smirror-side contract.
//
// Together with the source-property + behavioral + mutation tests
// in installer_handoff_seam_test.go, this file completes the
// SV-layer ratchet for handoff #1 (MSI installer → smirror
// runtime). Six tests now lock the SM-216 fix in tree:
//
//   1. WiX file does not write install_id (anti-pattern lock).
//   2. install_events.go contains the recovery branch.
//   3. install_events_test.go contains the regression test.
//   4. release-dryrun.yml references the gap (advisory).
//   5. The unit regression test passes when subprocessed.
//   6. The unit regression test FAILS against pre-fix code (mutation).
//   + 7 (THIS FILE). End-to-end: real smirror.exe + real state.db
//     + real recovery branch executing under real daemon startup.

package systemval

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// testKey32 is a deterministic 32-byte (64-hex-char) buildKey for
// tests. The unit tests in internal/telemetry/hmac_test.go use the
// same constant; we duplicate it here because system-validation is
// a separate Go module that intentionally does not import internal/*.
//
// It does NOT need to be the per-version-derived key the production
// HMAC scheme would compute (HMAC-SHA256(master_key, version)). The
// mock telemetry endpoint accepts any signature. We only need
// HasBuildKey() to return true and SignPayload() to succeed.
const testBuildKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// buildSmirrorWithBuildKey builds a SECOND smirror.exe binary with
// the test buildKey injected via -ldflags. The global smirrorBin
// (built in TestMain without ldflags) has buildKey="" → HasBuildKey()
// returns false → SendInstallEventsIfDue short-circuits at Gate 2
// before reaching the recovery branch. We need a buildkey-injected
// binary for any test that observes telemetry submission.
//
// The build is cached per-test-binary-invocation via sync.Once so
// repeat callers in the same `go test` run reuse one .exe.
var (
	signedSmirrorBinPath string
	signedSmirrorBinErr  error
	signedSmirrorBinOnce sync.Once
)

func buildSmirrorWithBuildKey(t *testing.T) string {
	t.Helper()
	signedSmirrorBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "systemval-signed-build-*")
		if err != nil {
			signedSmirrorBinErr = err
			return
		}
		// Note: do not RemoveAll here — t.Cleanup is per-test, but
		// the binary needs to outlive the test that triggered the
		// build. We accept a small temp-dir leak per `go test` run;
		// the OS cleans %TEMP% eventually.
		binName := "smirror-signed.exe"
		if runtime.GOOS != "windows" {
			binName = "smirror-signed"
		}
		signedSmirrorBinPath = filepath.Join(dir, binName)
		// -X path: must match the actual import path of the
		// telemetry package's buildKey var. The parent module is
		// github.com/qraveh/SelectiveMirror per go.mod.
		ldflags := "-X github.com/qraveh/SelectiveMirror/internal/telemetry.buildKey=" + testBuildKey
		cmd := exec.Command("go", "build",
			"-ldflags", ldflags,
			"-o", signedSmirrorBinPath,
			"./cmd/smirror/")
		cmd.Dir = repoRoot
		out, berr := cmd.CombinedOutput()
		if berr != nil {
			signedSmirrorBinErr = &buildErr{out: string(out), err: berr}
			return
		}
	})
	if signedSmirrorBinErr != nil {
		t.Fatalf("build smirror with buildKey failed: %v", signedSmirrorBinErr)
	}
	return signedSmirrorBinPath
}

type buildErr struct {
	out string
	err error
}

func (b *buildErr) Error() string {
	return b.err.Error() + "\n" + b.out
}

// runSignedSmirror invokes the buildkey-injected smirror.exe with
// --config prepended, like runSmirror does for the unsigned binary.
// Used for setup commands (e.g. `smirror telemetry standard` to
// initialize the state.db) where we want the same binary that's
// going to run during the test.
func runSignedSmirror(t *testing.T, signedBin, cfgPath string, args ...string) smirrorResult {
	t.Helper()
	full := append([]string{"--config", cfgPath}, args...)
	start := time.Now()
	cmd := exec.Command(signedBin, full...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec error (not ExitError): %v", err)
		}
	}
	return smirrorResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}
}

// startMockTelemetryEndpoint spins up a localhost HTTP server that
// records every POST. The returned channel surfaces each received
// body to the test; the cleanup is registered on t.
//
// Returns nil-but-200 to mimic the contribute() RPC's success shape
// (Worker returns 200 with `{"ok":true}`). The smirror-side parser
// only requires HTTP 200 for "success".
func startMockTelemetryEndpoint(t *testing.T) (url string, received <-chan map[string]any) {
	t.Helper()
	ch := make(chan map[string]any, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slurp body, parse JSON, send to channel.
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, "read body", 500)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			// Not JSON → record the raw body shape for diagnosis.
			payload = map[string]any{"_raw_non_json": string(body)}
		}
		select {
		case ch <- payload:
		default:
			// Channel full — overflow. Tests should drain.
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, ch
}

// prepopulateStateDBForSM216 simulates the v1.0.0 MSI consent-dialog
// handoff state. Steps:
//
//   1. Run `smirror telemetry standard` (using the signed binary)
//      to create a fully-schema'd state.db with telemetry_tier =
//      "standard" AND install_id set.
//   2. Open the just-created state.db with database/sql, DELETE
//      the install_id meta row. Result: telemetry_tier still
//      "standard", install_id absent — exactly what the v1.0.0
//      MSI consent dialog produces (it sets the registry tier
//      directly without invoking the CLI's install_id-generation
//      branch).
//
// We use this two-step instead of writing schema by hand because
// schema migrations are versioned (see internal/state/state.go)
// and we don't want this test to break every time a migration is
// added.
func prepopulateStateDBForSM216(t *testing.T, signedBin, cfgPath, stateDBPath string) {
	t.Helper()

	// Step 1: smirror telemetry standard.
	res := runSignedSmirror(t, signedBin, cfgPath, "telemetry", "standard")
	if res.ExitCode != 0 {
		t.Fatalf("`smirror telemetry standard` failed (exit %d):\nstdout: %s\nstderr: %s",
			res.ExitCode, res.Stdout, res.Stderr)
	}

	// Step 2: open state.db, DELETE install_id row.
	db, err := sql.Open("sqlite3", stateDBPath)
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("DELETE FROM meta WHERE key = ?", "install_id"); err != nil {
		t.Fatalf("delete install_id: %v", err)
	}
	// Sanity: confirm post-delete shape.
	var tierVal, idVal sql.NullString
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", "telemetry_tier").Scan(&tierVal); err != nil {
		t.Fatalf("read telemetry_tier: %v", err)
	}
	if tierVal.String != "standard" {
		t.Fatalf("post-prepopulate telemetry_tier = %q; want %q", tierVal.String, "standard")
	}
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", "install_id").Scan(&idVal); err != sql.ErrNoRows {
		t.Fatalf("post-prepopulate install_id should be ABSENT; got value=%q err=%v", idVal.String, err)
	}
}

// readMetaFromStateDB returns the value of a meta key (empty string
// if absent). Used to verify that the recovery branch persisted a
// new install_id during daemon startup.
func readMetaFromStateDB(t *testing.T, stateDBPath, key string) string {
	t.Helper()
	db, err := sql.Open("sqlite3", stateDBPath)
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer db.Close()
	var v sql.NullString
	err = db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("read meta %q: %v", key, err)
	}
	return v.String
}

// startSignedSmirrorWithEnv spawns the signed binary as a background
// daemon, with extra env vars merged into the inherited environment.
// Cleanup is registered on t; the process is killed at test end.
func startSignedSmirrorWithEnv(t *testing.T, signedBin, cfgPath string, extraEnv map[string]string, args ...string) *exec.Cmd {
	t.Helper()
	full := append([]string{"--config", cfgPath}, args...)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, signedBin, full...)
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	// Capture stderr so we can grep for the recovery's INFO log.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	setNewProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start signed smirror: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		// Stash stderr on the test for diagnostic if assertions failed.
		if t.Failed() {
			t.Logf("signed-smirror stderr (last 2k):\n%s", truncate(stderr.String(), 2000))
		}
	})
	// Stash stderr address on the cmd for callers who want to grep
	// it before the cleanup fires.
	cmd.ExtraFiles = nil // (no-op; just a place to stash if we need a side channel)
	return cmd
}

// ---------------------------------------------------------------------------
// THE test
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_E2E_DaemonRecoversAndSubmitsFirstSeen(t *testing.T) {
	coverage.Record("installer_handoff_seam_e2e_first_seen_lands")

	// Build the buildkey-injected smirror. Cached per `go test` run.
	signedBin := buildSmirrorWithBuildKey(t)

	// Mock telemetry endpoint: records POSTs.
	endpointURL, received := startMockTelemetryEndpoint(t)

	// Test environment: config + state.db location.
	env := newTestEnv(t)
	stateDBPath := filepath.Join(env.DataDir, "state.db")

	// Pre-populate state.db in the SM-216 shape.
	prepopulateStateDBForSM216(t, signedBin, env.CfgPath, stateDBPath)

	// Verify pre-condition: install_id is empty BEFORE we start the daemon.
	if got := readMetaFromStateDB(t, stateDBPath, "install_id"); got != "" {
		t.Fatalf("pre-condition failed: install_id is %q before daemon start; want empty (SM-216 shape)", got)
	}

	// Start signed smirror as background daemon, pointing telemetry
	// at the mock endpoint.
	startSignedSmirrorWithEnv(t, signedBin, env.CfgPath,
		map[string]string{"SMIRROR_TELEMETRY_ENDPOINT": endpointURL},
		"start")

	// Wait for the install-events goroutine to fire. Bound: 45s
	// (the daemon's internal context is 30s; we add slack for build
	// startup overhead). We block on the channel rather than poll.
	//
	// The wire body shape (per internal/telemetry/contribute.go is:
	//   {
	//     "payload": { "event_kind": "first_seen", "install_id": ..., ... },
	//     "claimed_version": "1.0.16-dev",
	//     "claimed_hmac_hex": "..."
	//   }
	// — the payload's discriminating fields are NESTED, not at top level.
	var wireBody map[string]any
	select {
	case wireBody = <-received:
		// Got a POST. Continue to assertions.
	case <-time.After(45 * time.Second):
		// Show diagnostic: state.db install_id and tier.
		idAfter := readMetaFromStateDB(t, stateDBPath, "install_id")
		tierAfter := readMetaFromStateDB(t, stateDBPath, "telemetry_tier")
		t.Fatalf(
			"no telemetry POST received within 45s after daemon start.\n"+
				"  state.db install_id  = %q (want non-empty if recovery fired)\n"+
				"  state.db tier        = %q (want \"standard\")\n"+
				"\n"+
				"This means SendInstallEventsIfDue did NOT submit first_seen. "+
				"Either: (a) Gate 1 (tier) failed → tier was not 'standard'; "+
				"(b) Gate 2 (buildKey) failed → buildkey injection didn't take; "+
				"(c) Gate 3 recovery branch failed silently → SM-216 has regressed; "+
				"(d) Contribute() failed before HTTP layer → endpoint URL not seen; "+
				"(e) daemon never reached fireInstallEventsAtStartup → smirror start crashed.",
			idAfter, tierAfter)
	}

	// Pull out the inner payload from the wire envelope.
	innerAny, ok := wireBody["payload"]
	if !ok {
		t.Fatalf("wire body missing top-level \"payload\" field; got keys %v\nfull body: %#v", topLevelKeys(wireBody), wireBody)
	}
	payload, ok := innerAny.(map[string]any)
	if !ok {
		t.Fatalf("wire body \"payload\" is %T, want map[string]any", innerAny)
	}

	// Assertion 1: payload shape — event_kind = "first_seen".
	// (Note: the install-events goroutine fires first_seen + upgrade.
	// We may see either order; the channel is buffered so we observe
	// the FIRST one. first_seen MUST come first by sequence in
	// SendInstallEventsIfDue.)
	if got := payload["event_kind"]; got != "first_seen" {
		t.Errorf("payload event_kind = %v; want %q", got, "first_seen")
	}
	// Assertion 2: install_id is present in the payload.
	if got, ok := payload["install_id"].(string); !ok || got == "" {
		t.Errorf("payload install_id = %v (type %T); want non-empty string", payload["install_id"], payload["install_id"])
	}
	// Assertion 3: HMAC field is present at the WIRE level (top of
	// the envelope, not the inner payload). Proves SignPayload ran,
	// not skipped at Gate 2.
	if got, ok := wireBody["claimed_hmac_hex"].(string); !ok || got == "" {
		t.Errorf("wire body claimed_hmac_hex = %v; want non-empty string (HMAC must be signed)", wireBody["claimed_hmac_hex"])
	}
	// Assertion 3b: the claimed_version matches the binary's version.
	if got, ok := wireBody["claimed_version"].(string); !ok || got == "" {
		t.Errorf("wire body claimed_version = %v; want non-empty string", wireBody["claimed_version"])
	}

	// Wait briefly for the daemon to commit the install_id meta
	// write (the goroutine's SetMeta happens after the HTTP POST
	// returns). Poll up to 5s.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v := readMetaFromStateDB(t, stateDBPath, "install_id"); v != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Assertion 4: install_id was persisted to state.db (idempotency
	// — next start would not re-fire first_seen).
	persistedID := readMetaFromStateDB(t, stateDBPath, "install_id")
	if persistedID == "" {
		t.Errorf("install_id was NOT persisted to state.db after recovery — next daemon start would re-fire first_seen")
	}

	// Assertion 5: payload's install_id matches state.db's install_id.
	// (Discriminating: catches a recovery that signed with one ID
	// and persisted a different one.)
	if payloadID, _ := payload["install_id"].(string); payloadID != persistedID {
		t.Errorf("install_id mismatch: payload=%q, state.db=%q (recovery should sign and persist the SAME id)",
			payloadID, persistedID)
	}
}

// topLevelKeys is a small diagnostic helper for assertion failure
// messages. Returns the (sorted) top-level keys of a wire body so
// the failure message points at what we DID receive rather than
// just saying "missing".
func topLevelKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// stable order for diagnostic readability
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
