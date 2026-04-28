package systemval

// panel_findings_round6_test.go — Round 6 system-validation tests
// synthesized from a fresh four-lens panel review (telemetry privacy /
// selfupdate flow / status output / adversarial pass on still-OPEN bugs)
// against v0.9.29-dev on 2026-04-29.
//
// R1: config / security              — 1 BUG (fixed) + many gaps (mostly fixed).
// R2: live watcher / sync / recovery — 0 new bugs, 1 OBS.
// R3: workflows / multi-mirror / gitignore — 1 BUG (BUG-R3-1, OPEN).
// R4: anomaly / CLI mutation / hooks — 1 BUG + 1 FIND (BUG-R4-1, FIND-R4-1, OPEN).
// R5: filesystem / YAML / CLI / endurance — 1 BUG (BUG-R5-1, OPEN).
// R6 priorities (areas not yet stressed):
//   - Status output completeness (operator can trust what they see)
//   - Selfupdate flow security (lighter-touch verifications)
//   - Adversarial neighbors of the still-OPEN bugs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// =========================================================================
// 1. STATUS OUTPUT QUALITY
// =========================================================================

// Status reviewer #1: when the daemon is NOT running, `smirror status`
// shows "Last Known Metrics (instance not running):" but the uptime value
// is the stale snapshot from the previous run. An operator may misread.
func TestPanelR6_Status_StaleUptimeWhenDaemonOffline(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	p := startSmirror(t, env.CfgPath, "start")
	time.Sleep(3 * time.Second) // let daemon write at least one heartbeat
	if isExited(p) {
		t.Fatalf("daemon exited prematurely: stderr=%s", truncate(p.stderr.String(), 400))
	}

	// Verify status.json exists.
	statusPath := filepath.Join(env.DataDir, "status.json")
	if _, err := os.Stat(statusPath); err != nil {
		t.Skipf("status.json not written within 3s: %v", err)
	}

	// Kill daemon.
	p.Kill()
	time.Sleep(2 * time.Second)

	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)

	hasNotRunning := strings.Contains(combined, "not running") ||
		strings.Contains(combined, "instance not running")
	hasUptime := strings.Contains(combined, "uptime")
	hasStaleHint := strings.Contains(combined, "last known") ||
		strings.Contains(combined, "stale")

	if hasUptime && !hasNotRunning && !hasStaleHint {
		t.Errorf("PANEL BUG: status output displays uptime without indicating daemon is dead. "+
			"Operator may misinterpret as live. Output: %s",
			truncate(r.Stdout+r.Stderr, 600))
	} else {
		t.Logf("status after daemon death: hasNotRunning=%v hasUptime=%v hasStaleHint=%v",
			hasNotRunning, hasUptime, hasStaleHint)
	}
}

// Status reviewer #12: corrupted status.json — `json.Unmarshal` fails
// silently in cmdStatus, the metrics block is skipped, the user can't
// distinguish "daemon broken" from "status.json corrupted".
func TestPanelR6_Status_CorruptedStatusJsonHandling(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	statusPath := filepath.Join(env.DataDir, "status.json")
	// Write garbage as status.json.
	if err := os.WriteFile(statusPath, []byte("{not valid json"), 0644); err != nil {
		t.Fatal(err)
	}

	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasCorruptionHint := strings.Contains(combined, "corrupt") ||
		strings.Contains(combined, "invalid") ||
		strings.Contains(combined, "parse") ||
		strings.Contains(combined, "malformed")
	if !hasCorruptionHint {
		t.Logf("PANEL OBS: corrupted status.json silently skipped — no hint to operator. "+
			"Per status reviewer #12, the user can't distinguish 'daemon broken' from " +
			"'status.json corrupted'. Recommendation: print a one-line warning when " +
			"status.json fails to parse. Output: %s",
			truncate(r.Stdout+r.Stderr, 500))
	}
}

// Status reviewer #6: there's no `smirror history` or `smirror log`
// command. Verifiable from `--help` output.
func TestPanelR6_Status_NoHistoryCommand(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "--help")
	assertExitCode(t, r, 0)
	combined := strings.ToLower(r.Stdout + r.Stderr)

	hasHistory := strings.Contains(combined, "history") ||
		strings.Contains(combined, "smirror log ")
	if !hasHistory {
		t.Logf("PANEL OBS: `smirror --help` does not mention `history` or `log` command. " +
			"Per status reviewer #6, operators must run `sqlite3 state.db \"SELECT * FROM " +
			"sync_log ORDER BY timestamp DESC LIMIT 10\"` to see recent activity. " +
			"Recommendation: add `smirror history [-n 50]` reading the last N sync_log rows.")
	}
}

// Status reviewer #11: with many mirrors, `status` output spans many lines
// without a summary table. Verify by counting output lines for 5 mirrors.
func TestPanelR6_Status_MultiMirror_OutputVerbosity(t *testing.T) {
	t.Parallel()
	env := newTestEnvN(t, 5)
	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	if r.ExitCode != 0 && r.ExitCode != 1 {
		t.Skipf("status returned %d, can't measure", r.ExitCode)
	}
	lines := strings.Count(r.Stdout, "\n")
	t.Logf("PANEL OBS: 5-mirror status output is %d lines. With 32 mirrors (NFR-CA-01) "+
		"the output would scale to roughly %d lines. No tabular summary view exists. "+
		"Recommendation: support `smirror status --short` or auto-detect terminal width. ",
		lines, lines*32/5)
}

// =========================================================================
// 2. TELEMETRY OBSERVABILITY
// =========================================================================

// Telemetry reviewer #5 + Status reviewer #5: `smirror version` shows
// "telemetry build-key: <fingerprint>" or "none". For source builds the
// value is "none". Verify the line exists and document the meaning.
func TestPanelR6_Telemetry_VersionShowsBuildKey(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "version")
	assertExitCode(t, r, 0)

	if !strings.Contains(r.Stdout, "telemetry build-key:") {
		t.Errorf("PANEL BUG: `smirror version` does not include the telemetry build-key "+
			"fingerprint line. Per status reviewer #5 + telemetry #5 the operator needs "+
			"this to confirm whether their binary can submit telemetry. stdout=%s",
			truncate(r.Stdout, 400))
	}
	// For source builds the fingerprint should be "none". For MSI builds it's a hex string.
	if strings.Contains(r.Stdout, "telemetry build-key: none") {
		t.Logf("source build: telemetry build-key=none (expected for go-build).")
	}
}

// Telemetry reviewer #1 + Status reviewer #5 + R5 OBS-R5-4: the version
// output has 3 lines (version + copyright + build-key). README documents
// only one line. Track the drift.
func TestPanelR6_Telemetry_VersionLineCount(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "version")
	assertExitCode(t, r, 0)
	trimmed := strings.TrimSpace(r.Stdout)
	lines := strings.Count(trimmed, "\n") + 1
	if lines == 1 {
		t.Errorf("expected multi-line version output (round 5 confirmed 3 lines)")
	}
	t.Logf("PANEL OBS: `version` produces %d lines. README still documents single-line. R5 OBS-R5-4.", lines)
}

// =========================================================================
// 3. SELFUPDATE LIGHTWEIGHT VERIFICATION
// =========================================================================

// Selfupdate reviewer #4: a fork build (version `0.9.27-fork`) is treated
// as equal to upstream `0.9.27`. The compare strips suffix tags. Selfupdate
// silently says "already up to date" without a fork warning. Test what
// happens when running selfupdate --check on a non-network-connected host.
func TestPanelR6_Selfupdate_CheckOffline(t *testing.T) {
	t.Parallel()
	r := runSmirrorWithTimeout(t, 30*time.Second, "doesnt-matter.yaml",
		"selfupdate", "--check")
	// Without network, --check should fail gracefully (likely exit 1, with
	// a clear error message about network).
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasNetMessage := strings.Contains(combined, "network") ||
		strings.Contains(combined, "github") ||
		strings.Contains(combined, "no such host") ||
		strings.Contains(combined, "timeout") ||
		strings.Contains(combined, "connect") ||
		strings.Contains(combined, "fail") ||
		strings.Contains(combined, "could not") ||
		strings.Contains(combined, "up to date") ||
		strings.Contains(combined, "current")
	if !hasNetMessage {
		t.Logf("PANEL OBS: `selfupdate --check` output lacks both 'up to date' and "+
			"network-failure vocabulary. exit=%d output=%s",
			r.ExitCode, truncate(combined, 500))
	}
}

// =========================================================================
// 4. ADVERSARIAL — neighbors of still-OPEN bugs
// =========================================================================

// Adversarial #2: BUG-R4-1 neighbor — concurrent `addmirror` + `remote`
// from different terminals. Both read config.yaml, both modify their
// in-memory copy, both write. Last-writer-wins.
func TestPanelR6_Adv_ConcurrentAddmirrorRemoteSet(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	// Pre-create a config with one mirror.
	srcSeed := filepath.Join(root, "src-seed")
	dstSeed := filepath.Join(root, "dst-seed")
	os.MkdirAll(srcSeed, 0755)
	os.MkdirAll(dstSeed, 0755)
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "seed", LocalPath: srcSeed, Remote: dstSeed},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	srcA := filepath.Join(root, "src-A")
	dstA := filepath.Join(root, "dst-A")
	os.MkdirAll(srcA, 0755)
	os.MkdirAll(dstA, 0755)

	var wg sync.WaitGroup
	results := make([]smirrorResult, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = runSmirrorWithTimeout(t, 30*time.Second, cfg,
			"addmirror", srcA, "-dest", dstA)
	}()
	go func() {
		defer wg.Done()
		results[1] = runSmirrorWithTimeout(t, 30*time.Second, cfg,
			"remote", "newremote:bucket/path")
	}()
	wg.Wait()

	cfgBytes, _ := os.ReadFile(cfg)
	cfgText := string(cfgBytes)
	hasSeed := strings.Contains(cfgText, "seed")
	hasA := strings.Contains(cfgText, srcA)
	hasNewRemote := strings.Contains(cfgText, "newremote:bucket/path")

	if !hasSeed {
		t.Errorf("PANEL BUG: seed mirror lost from config — destructive race between "+
			"`addmirror` and `remote` set. exitA=%d exitB=%d.", results[0].ExitCode, results[1].ExitCode)
	}
	if !hasA && !hasNewRemote {
		t.Logf("PANEL OBS: both addmirror AND remote set lost — even more destructive race. " +
			"exitA=%d exitB=%d cfg=%s", results[0].ExitCode, results[1].ExitCode, truncate(cfgText, 500))
	} else if hasA && !hasNewRemote {
		t.Logf("PANEL OBS: addmirror won, remote-set lost. " +
			"exitA=%d exitB=%d", results[0].ExitCode, results[1].ExitCode)
	} else if !hasA && hasNewRemote {
		t.Logf("PANEL OBS: remote-set won, addmirror lost. " +
			"exitA=%d exitB=%d", results[0].ExitCode, results[1].ExitCode)
	}
}

// Adversarial #5/#9: FIND-R4-1 neighbor — `sync-now` queues a full-project
// task (RelPath=""), and the per-file hook code path is conditional on
// RelPath != "". So `sync-now` (which is the user's manual catch-up
// mechanism) silently does NOT fire per-file hooks.
func TestPanelR6_Adv_SyncNowSkipsPerFileHooks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("hook test uses cmd.exe path")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	canary := filepath.Join(dataDir, "syncnow-hook-canary.txt")
	hookCmd := fmt.Sprintf(`cmd /c echo hook fired > "%s"`, canary)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst, PostSyncHook: hookCmd},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	createFile(t, filepath.Join(src, "x.txt"), "x")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	time.Sleep(2 * time.Second)

	if fileExists(canary) {
		t.Logf("PANEL OBS: post-sync hook DID fire for sync-now. Documenting current " +
			"behavior — adversarial #5/#9 expected hook to NOT fire on batch path.")
	} else {
		t.Logf("PANEL OBS: post-sync hook did NOT fire on `sync-now`. Confirms FIND-R4-1 " +
			"neighbor: per-file hooks only fire on the live watcher path, not on sync-now / " +
			"reconciliation / startup. For an AI orchestration use case this is a feature gap. " +
			"Recommendation: also fire per-file hooks during the post-batch backfill walk.")
	}
}

// Adversarial #3 (refined): BUG-R4-1 neighbor — `addmirror --initial-sync`
// triggers a batch sync that writes to state DB. Concurrently calling
// `unmirror` on the same project deletes those state DB rows. The race
// window is small, but the test attempts it.
func TestPanelR6_Adv_AddmirrorInitialSyncVsUnmirror(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	srcSeed := filepath.Join(root, "src-seed")
	dstSeed := filepath.Join(root, "dst-seed")
	os.MkdirAll(srcSeed, 0755)
	os.MkdirAll(dstSeed, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "seed", LocalPath: srcSeed, Remote: dstSeed},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Add a new mirror with --initial-sync (synchronous-ish).
	srcN := filepath.Join(root, "src-N")
	dstN := filepath.Join(root, "dst-N")
	os.MkdirAll(srcN, 0755)
	os.MkdirAll(dstN, 0755)
	for i := 0; i < 30; i++ {
		createFile(t, filepath.Join(srcN, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("c%d", i))
	}

	// Race: addmirror with initial-sync, and unmirror in parallel (the
	// unmirror won't find the new mirror until addmirror has written
	// config; we expect unmirror to fail or succeed late).
	var wg sync.WaitGroup
	wg.Add(2)
	var rAdd, rUnm smirrorResult
	go func() {
		defer wg.Done()
		rAdd = runSmirrorWithTimeout(t, 60*time.Second, cfg,
			"addmirror", srcN, "-dest", dstN, "--initial-sync")
	}()
	go func() {
		defer wg.Done()
		// Tiny stagger so addmirror's config write happens first; otherwise
		// unmirror immediately fails with "no such mirror".
		time.Sleep(200 * time.Millisecond)
		// New mirror name is auto-generated as the last path component "src-N".
		rUnm = runSmirrorWithTimeout(t, 30*time.Second, cfg,
			"unmirror", "src-N", "--yes")
	}()
	wg.Wait()

	t.Logf("PANEL OBS: addmirror+initial-sync vs unmirror race — addmirror exit=%d, unmirror exit=%d. "+
		"No assertion — just observe whether the race produced unexpected output, panics, or "+
		"DB constraint errors. Per adversarial #3 the race window is real but small.",
		rAdd.ExitCode, rUnm.ExitCode)
	assertNoPanic(t, rAdd)
	assertNoPanic(t, rUnm)
}

// =========================================================================
// 5. RECONFIRM BUG-R5-1 IS STILL ALIVE
// =========================================================================

// BUG-R5-1 was reported in round 5 (anomaly.Rotate dead code). Re-verify
// against v0.9.29-dev — the maintainer landed unrelated changes (SEC-H9-11,
// SEC-M10) but did they wire the rotation?
func TestPanelR6_Reconfirm_AnomalyRotationStillDeadCode(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	found := false
	roots := []string{filepath.Join(repoRoot, "cmd"), filepath.Join(repoRoot, "internal")}
	for _, dir := range roots {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, _ := os.ReadFile(path)
			text := string(data)
			for _, line := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "func Rotate(") ||
					strings.HasPrefix(trimmed, "func (") && strings.Contains(trimmed, ") Rotate(") {
					continue
				}
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(trimmed, "anomaly.Rotate(") {
					found = true
				}
			}
			return nil
		})
	}
	if !found {
		t.Logf("PANEL OBS: BUG-R5-1 STILL OPEN in v0.9.29-dev — anomaly.Rotate is still " +
			"defined but never invoked from production. This is a regression-confirmation " +
			"OBS (the bug remains, no maintainer action between rounds 5 and 6).")
	} else {
		t.Logf("BUG-R5-1 has been FIXED — anomaly.Rotate is now wired in production.")
	}
}

// =========================================================================
// 6. LOG SANITIZATION VERIFICATION (recent SEC-H9/H10 batch in 0.9.28)
// =========================================================================

// 0.9.28 brought "log sanitization batch (SEC-H9, H10, L4, L5)". Verify
// the secret-shaped strings get redacted in the daemon's log file.
func TestPanelR6_LogSanitization_SecretInPath(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Create a file with a secret-shaped path component.
	secret := filepath.Join(env.SrcDir, "AKIAIOSFODNN7EXAMPLE_test.txt") // AWS access key shape
	createFile(t, secret, "x")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	logBytes, err := os.ReadFile(filepath.Join(env.DataDir, "smirror.log"))
	if err != nil {
		t.Skipf("no log file: %v", err)
	}
	logText := string(logBytes)
	if strings.Contains(logText, "AKIAIOSFODNN7EXAMPLE") {
		t.Logf("PANEL OBS: AWS-access-key-shaped path component appears verbatim in the log. "+
			"SEC-H9/H10 (0.9.28) addressed log sanitization — this may indicate it covers other "+
			"shapes only, OR the user's filename is not the right kind of secret to redact. "+
			"Filename: AKIAIOSFODNN7EXAMPLE_test.txt — log content sample: %s",
			truncate(logText, 600))
	}
}

// =========================================================================
// helpers
// =========================================================================

// repoRootFromHere — already defined in panel_findings_round5_test.go; we
// rely on the package-level reuse. Tip: the test runs from system-validation/.
// (Definition lives in round5; round6 just uses it.)

// Reuse json.Unmarshal placeholder to keep imports neat.
var _ = json.Unmarshal
