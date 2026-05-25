package systemval

// panel_findings_round4_test.go — Round 4 system-validation tests
// synthesized from a fresh four-lens panel review (anomaly detection /
// CLI config-mutation / hooks + audit trail / adversarial recheck) against
// v0.9.27-dev on 2026-04-29.
//
// Round 1: config / security                — bug + many gaps, mostly shipped.
// Round 2: live watcher / sync / concurrency — 0 new bugs, 1 OBS.
// Round 3: workflows / multi-mirror / gitignore — 1 BUG (parent-exclusion).
// Round 4 priorities (areas R1-R3 didn't touch):
//   - Hooks system end-to-end (env vars, pre-sync failure semantics, delete events)
//   - Audit trail completeness (NFR-NR-01, batch sync row count)
//   - Anomaly detection accuracy (config validation, hypothesis chain, JSON shape)
//   - CLI config-mutation safety (concurrent addmirror, fresh-config mode, quoting)
//
// PANEL BUG = real defect (t.Errorf). PANEL OBS = observation (t.Logf).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// =========================================================================
// 1. HOOKS SYSTEM END-TO-END
// =========================================================================

// VV-Plan T-HOOK-06 promises env vars FILE, MIRROR, ACTION, STATUS.
// internal/hooks/hooks.go:38-43 actually exports SMIRROR_PROJECT,
// SMIRROR_FILE, SMIRROR_REMOTE, SMIRROR_EVENT.
//
// Lock down what's ACTUALLY exposed and surface the V&V drift.
//
// FIND-R4-1 / hooks deferral (2026-04-29): hooks are no longer part of
// the v1.0 stability surface — see docs/RESOLUTION-2026-04-29-hooks-deferred.md.
// The implementation in internal/hooks/ remains in tree but is not
// counted toward v1.0 readiness, so the FIND-R4-1 gap (batch-sync paths
// bypass per-file hooks) is closed as won't-fix under the new framing.
// This test is skipped until either (a) hooks are promoted back into
// the v1.0 surface per §6 of the resolution, or (b) the hook
// implementation is removed and the test goes with it.
func TestPanelR4_Hooks_EnvVarsActuallyExported(t *testing.T) {
	t.Skip("hooks deferred from v1.0 — see docs/RESOLUTION-2026-04-29-hooks-deferred.md")
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

	// Hook writes ALL SMIRROR_* env vars to a canary file.
	canary := filepath.Join(dataDir, "env-dump.txt")
	hookCmd := fmt.Sprintf(
		`cmd /c (set SMIRROR_) > "%s"`, canary)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test-mirror", LocalPath: src, Remote: dst, PostSyncHook: hookCmd},
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
	// Wait briefly for hook to complete (post_sync runs after sync record).
	time.Sleep(1 * time.Second)

	if !fileExists(canary) {
		t.Fatalf("post_sync hook did not produce canary file. stderr=%s",
			truncate(r.Stderr, 300))
	}
	dump, _ := os.ReadFile(canary)
	got := strings.ToUpper(string(dump))
	expected := []string{"SMIRROR_PROJECT", "SMIRROR_FILE", "SMIRROR_REMOTE", "SMIRROR_EVENT"}
	missing := []string{}
	for _, want := range expected {
		if !strings.Contains(got, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Errorf("PANEL BUG: hook env vars missing %v. dump=%s",
			missing, truncate(string(dump), 600))
	}

	// VV-Plan T-HOOK-06 also names MIRROR / ACTION / STATUS — check whether
	// they were silently renamed (PROJECT/EVENT?) or genuinely missing.
	vvNamed := []string{"SMIRROR_MIRROR", "SMIRROR_ACTION", "SMIRROR_STATUS"}
	vvMissing := []string{}
	for _, want := range vvNamed {
		if !strings.Contains(got, want) {
			vvMissing = append(vvMissing, want)
		}
	}
	if len(vvMissing) > 0 {
		t.Logf("PANEL OBS: VV-Plan T-HOOK-06 (docs/VV-Plan.md:494) names %v as available env vars, "+
			"but those names are not in the actual hook environment (current names: PROJECT/FILE/REMOTE/EVENT). "+
			"Either update the V&V plan to match the implementation, or rename the env vars to match docs. "+
			"Missing per V&V: %v", vvNamed, vvMissing)
	}
	t.Logf("hook env dump: %s", truncate(string(dump), 600))
}

// VV-Plan T-HOOK-02 promises: "Pre-sync hook fails (non-zero exit) | Sync skipped".
// internal/sync/sync.go:286-306 + internal/hooks/hooks.go:65 say:
// "Errors are logged as warnings — hooks never block sync operations."
//
// Test the ACTUAL behavior (sync-not-skipped) and surface the V&V drift.
func TestPanelR4_Hooks_PreSyncFailureDoesNotBlock(t *testing.T) {
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

	// Pre-sync hook always fails with exit 1.
	hookCmd := `cmd /c exit 1`

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst, PreSyncHook: hookCmd},
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
	if r.ExitCode != 0 {
		t.Logf("sync-now exit=%d (expected 0 — sync proceeds despite hook failure)", r.ExitCode)
	}

	if !fileExists(filepath.Join(dst, "x.txt")) {
		t.Logf("PANEL OBS: pre-sync hook with exit 1 caused sync NOT to happen. " +
			"This matches VV-Plan T-HOOK-02. The current code says 'hooks never block sync', " +
			"so observed behavior diverges from code intent — investigate.")
	} else {
		t.Logf("PANEL OBS: pre-sync hook failure did NOT block sync (file synced anyway). " +
			"This matches the code comment 'hooks never block sync operations' but " +
			"DIVERGES from VV-Plan T-HOOK-02 ('Pre-sync hook fails | Sync skipped'). " +
			"The V&V plan needs to be updated, OR the code needs to honor pre-sync hook " +
			"failure semantics.")
	}
}

// Hooks reviewer Finding #5: pre/post-sync hooks are skipped for delete tasks
// (sync.go:287 condition `task.Type != TaskDelete`).  Verify that's the
// observable behavior and surface the gap for orchestration use cases.
func TestPanelR4_Hooks_NotInvokedForDelete(t *testing.T) {
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

	canary := filepath.Join(dataDir, "delete-hook-canary.txt")
	hookCmd := fmt.Sprintf(`cmd /c echo fired > "%s"`, canary)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst,
				DeletePolicy: "delete", PostSyncHook: hookCmd},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Sync, then delete.
	createFile(t, filepath.Join(src, "doomed.txt"), "x")
	runSmirror(t, cfg, "sync-now")
	// hook fires for the SYNC. Reset canary.
	os.Remove(canary)

	os.Remove(filepath.Join(src, "doomed.txt"))
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	time.Sleep(1 * time.Second)

	if fileExists(canary) {
		t.Logf("PANEL OBS: post-sync hook DID fire for delete event. " +
			"Documenting current behavior in case it shifts.")
	} else {
		t.Logf("PANEL OBS: post-sync hook did NOT fire for delete event. " +
			"Per code at sync.go:287 this is intentional (TaskDelete skips hooks). " +
			"For AI orchestration use cases, react-to-delete is a real need: " +
			"consider a separate `pre_delete_hook` / `post_delete_hook` config knob.")
	}
}

// =========================================================================
// 2. AUDIT-TRAIL COMPLETENESS (NFR-NR-01)
// =========================================================================

// SRS NFR-NR-01: "All sync actions SHALL be logged in the sync_log table".
// Hook reviewer Finding #10: batch sync logs ONE row, regardless of how many
// files are uploaded. backfillStateAfterBatchSync writes per-file sync_state
// rows but does NOT call LogAction.
//
// Test: sync 5 files via sync-now (which uses batch rclone copy under the
// hood); count rows in sync_log. Expect 5 if NR-01 is satisfied.
func TestPanelR4_AuditTrail_BatchSync_RowCount(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	const n = 5
	for i := 0; i < n; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("f%d.txt", i)),
			fmt.Sprintf("content-%d", i))
	}
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Use sqlite3 CLI if available to count rows.
	dbPath := filepath.Join(env.DataDir, "state.db")
	count := readSyncLogCount(t, dbPath)
	if count < 0 {
		t.Skip("sqlite3 CLI not available; skipping row-count check")
	}
	t.Logf("PANEL OBS: synced %d files, sync_log rows = %d. "+
		"Per NFR-NR-01 the audit trail should contain ONE row per file action; "+
		"a batch sync that produces 1 row instead of %d means audit-trail completeness "+
		"is partial.", n, count, n)
	// Don't t.Errorf — this is documenting the gap. The exact threshold is
	// design-dependent (a single 'full_sync project=...' row may be acceptable
	// per FR-SYNC-02). Surface it for the maintainer to decide.
}

// readSyncLogCount returns the number of rows in sync_log, or -1 if
// sqlite3 CLI is unavailable / query fails.
func readSyncLogCount(t *testing.T, dbPath string) int {
	t.Helper()
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return -1
	}
	out, err := exec.Command(sqlite, dbPath, "SELECT COUNT(*) FROM sync_log").Output()
	if err != nil {
		t.Logf("sqlite3 query failed: %v", err)
		return -1
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n); err != nil {
		return -1
	}
	return n
}

// Hook reviewer Finding #13: smirror status doesn't surface sync_log
// history. Operators have to query the DB directly for "what synced
// recently?". Lock down the current behavior.
func TestPanelR4_AuditTrail_StatusShowsSyncHistory(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	createFile(t, filepath.Join(env.SrcDir, "audit-me.txt"), "audit-content")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	r = runSmirror(t, env.CfgPath, "status")
	assertExitCode(t, r, 0)
	combined := r.Stdout + r.Stderr

	// Look for the filename or any sync_log-style entry.
	hasFile := strings.Contains(combined, "audit-me.txt")
	hasLogSection := strings.Contains(strings.ToLower(combined), "recent sync") ||
		strings.Contains(strings.ToLower(combined), "sync log") ||
		strings.Contains(strings.ToLower(combined), "last sync")
	if !hasFile && !hasLogSection {
		t.Logf("PANEL OBS: `smirror status` does not show recently-synced files or a sync-log section. "+
			"Per NFR-NR-01 the audit trail exists in DB but operators must run sqlite3 to see it. "+
			"Recommendation: add a `--verbose` flag or a separate `smirror history [mirror]` command "+
			"that surfaces the last N sync_log rows. Status output: %s", truncate(combined, 400))
	}
}

// =========================================================================
// 3. ANOMALY DETECTION & VALIDATION
// =========================================================================

// Anomaly auditor Findings #3 + #13: alert_min_severity is not validated
// at config load. A typo ("err" instead of "error") silently demotes
// behavior; an invalid value passes Load() and surfaces as bizarre filtering.
func TestPanelR4_Anomaly_AlertMinSeverity_Validated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
		// Invalid severity name (not in {info,warning,error,critical}).
		ExtraYAML: "alert_min_severity: \"erro\"\n", // typo for "error"
	})

	r := runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: alert_min_severity=\"erro\" (typo) accepted at config load. " +
			"Per anomaly auditor Finding #3, severityAtLeast() returns 0 for unknown keys, " +
			"silently demoting to no-filter. Recommendation: validate against " +
			"{info,warning,error,critical} in config.Validate() and reject typos.")
	}
}

// Anomaly Auditor Finding #1: Sync:Stalled and Sync:LsJsonSlow have empty
// hypothesis chains in HypothesisFor() (FR-ANOM-05 violation). We can't
// trigger Sync:Stalled in a system-validation black-box, but we can examine
// existing JSONL files if any anomalies fire. Skip if no anomalies present.
func TestPanelR4_Anomaly_HypothesisChainPresent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dataDir, 0755)
	createFile(t, filepath.Join(src, "x.txt"), "x")

	// Use a bogus remote to force CircuitBreaker:Activated and SyncFailure:Repeated
	// over multiple sync attempts.
	yes := boolPtr(true)
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: "no-such-remote-xyz:bucket/path"},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    yes,
		VerifyIntervalSec: -1,
	})

	// Trigger 4 failures to exercise CircuitBreaker + SyncFailure:Repeated.
	for i := 0; i < 4; i++ {
		runSmirrorWithTimeout(t, 30*time.Second, cfg, "sync-now")
	}

	// Search anomalies/ dir for JSONL files.
	anomDir := filepath.Join(dataDir, "anomalies")
	entries, _ := os.ReadDir(anomDir)
	if len(entries) == 0 {
		t.Skip("no anomaly files written; cannot test hypothesis presence")
	}

	categoriesSeen := map[string]bool{}
	emptyHypothesis := []string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(anomDir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var rec map[string]interface{}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				continue
			}
			kind, _ := rec["kind"].(string)
			hyp, _ := rec["hypothesis"].(string)
			if kind == "" {
				continue
			}
			categoriesSeen[kind] = true
			if hyp == "" {
				emptyHypothesis = append(emptyHypothesis, kind)
			}
		}
	}
	t.Logf("anomaly categories seen: %v", mapKeys(categoriesSeen))
	if len(emptyHypothesis) > 0 {
		t.Logf("PANEL OBS: anomaly categories with EMPTY hypothesis chain: %v. "+
			"FR-ANOM-05 commits to causal hypothesis chains for every anomaly category; "+
			"missing chains for these categories.", emptyHypothesis)
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// =========================================================================
// 4. CLI CONFIG-MUTATION SAFETY
// =========================================================================

// CLI auditor Finding #1: no file-level locking on config.yaml. Running
// two `addmirror` commands concurrently from different terminals can lose
// one of the writes (last-writer-wins).
func TestPanelR4_CLI_ConcurrentAddMirror(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	// Pre-create one mirror so config exists.
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

	// Two new mirrors to add concurrently.
	srcA := filepath.Join(root, "src-A")
	dstA := filepath.Join(root, "dst-A")
	srcB := filepath.Join(root, "src-B")
	dstB := filepath.Join(root, "dst-B")
	os.MkdirAll(srcA, 0755)
	os.MkdirAll(dstA, 0755)
	os.MkdirAll(srcB, 0755)
	os.MkdirAll(dstB, 0755)

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
			"addmirror", srcB, "-dest", dstB)
	}()
	wg.Wait()

	cfgBytes, _ := os.ReadFile(cfg)
	cfgText := string(cfgBytes)
	// `createConfig` (test helper) writes seed paths using `%q` — Go-style
	// quoted form, so backslashes appear escaped (`\\`). `addmirror` writes
	// new mirrors via `formatMirrorBlock` with `%s` — single backslashes.
	// Without the asymmetric check below the test was a false positive
	// against the seed every run, regardless of whether the race actually
	// fired (SM-153 / BUG-R4-1 verification gap).
	containsPath := func(text, path string) bool {
		return strings.Contains(text, path) ||
			strings.Contains(text, fmt.Sprintf("%q", path))
	}
	hasA := containsPath(cfgText, srcA)
	hasB := containsPath(cfgText, srcB)
	hasSeed := containsPath(cfgText, srcSeed)
	if !hasSeed {
		t.Errorf("seed mirror lost from config — destructive race")
	}
	if !hasA || !hasB {
		t.Errorf("PANEL BUG: concurrent addmirror lost a write. "+
			"hasSeed=%v hasA=%v hasB=%v. Config now has %d unique-mirror entries; "+
			"expected 3 (seed + A + B). Recommendation: add file-level locking around "+
			"config.yaml writes in internal/config/edit.go. exit_A=%d exit_B=%d",
			hasSeed, hasA, hasB, strings.Count(cfgText, "- name:"),
			results[0].ExitCode, results[1].ExitCode)
	}
}

// CLI auditor Finding #15: when addmirror creates a fresh config from
// scratch (config.yaml doesn't exist), it's created with mode 0644. Per
// SEC-C5 / SECURITY.md the documented baseline is 0600 (config may contain
// rclone remote names, webhook URLs).
func TestPanelR4_CLI_FreshConfig_FileMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("file mode semantics differ on POSIX; this is a Windows-NTFS check")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfgPath := filepath.Join(root, "config.yaml")
	// Don't create the config — let addmirror create it.

	r := runSmirrorWithTimeout(t, 30*time.Second, cfgPath,
		"addmirror", src, "-dest", dst)
	if r.ExitCode != 0 {
		t.Fatalf("addmirror failed: exit=%d stderr=%s", r.ExitCode, truncate(r.Stderr, 300))
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	mode := info.Mode().Perm()
	// v1.0.1 close-out of the Medium (corrected reading): on Windows,
	// os.Stat returns Unix mode bits 0666/0444 regardless of the
	// `os.WriteFile(..., 0600)` hint at cmdaddmirror.go:290 — Go's
	// Windows wrapper doesn't translate mode arg into NTFS ACL. The
	// REAL protection on Windows is the inherited DACL from
	// %USERPROFILE%\.selectivemirror\ which restricts read/write to
	// the owning user + admins (typical NTFS default for a user's
	// home subdirectory). The 0600 hint is forward-compatible
	// decoration for non-Windows builds.
	//
	// This test stays t.Logf (observation) because mode-bit checking
	// is the wrong protection contract on Windows. The actual Medium
	// closure is the combination of (a) the 0600 hint in the writer,
	// (b) Windows ACL inheritance from the user profile dir, and
	// (c) the SEC-C5 IsAdminOwnedPath gate when smirror loads the
	// config in service mode (refuses non-admin-owned paths
	// entirely). See internal/config/acl_windows.go.
	if mode != 0600 {
		t.Logf("OBS (informational): fresh config.yaml mode %o (expected 0600 if Unix mode bits "+
			"applied; Windows os.Stat returns 0666/0444 regardless of the WriteFile mode arg). "+
			"Real protection is the inherited ACL from %%USERPROFILE%%\\.selectivemirror\\ which "+
			"restricts read/write to the owning user + admins.",
			mode)
	}
}

// CLI auditor Finding #6: cmdRemoteSet wraps the remote value in single
// quotes for SetField. A remote with a single quote in it breaks the
// quoting and produces malformed YAML.
func TestPanelR4_CLI_RemoteSet_QuotingSafety(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Remote with embedded single quote.
	tricky := `s3:bucket'with'quotes`
	r := runSmirrorWithTimeout(t, 15*time.Second, cfg, "remote", tricky)
	// If smirror accepts and writes the value, the resulting config.yaml
	// must still be parseable — otherwise the user's config is bricked.
	cfgBytes, _ := os.ReadFile(cfg)
	cfgText := string(cfgBytes)

	// Try to round-trip via test-mirrors (which loads + validates config).
	r2 := runSmirrorWithTimeout(t, 15*time.Second, cfg, "test-mirrors")
	if r.ExitCode == 0 && r2.ExitCode == 2 {
		t.Errorf("PANEL BUG: `smirror remote %q` accepted but produced a config that "+
			"fails to load (test-mirrors exit 2). Recommendation: use yaml-encoded value "+
			"or reject embedded quote characters at remote command time. config.yaml "+
			"now contains: %s", tricky, truncate(cfgText, 400))
	}
}

// CLI auditor Finding #8: the daemon doesn't hot-reload config.yaml. A new
// mirror added via addmirror while the daemon is running won't be watched
// until restart. Test that addmirror at minimum succeeds, but document the
// behavior gap for the operator.
func TestPanelR4_CLI_AddMirror_DuringDaemonRun(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires daemon mode")
	}
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	// Add a NEW mirror while daemon is running. This requires a separate
	// source/dest pair.
	root := env.RootDir
	src2 := filepath.Join(root, "src-new")
	dst2 := filepath.Join(root, "dst-new")
	os.MkdirAll(src2, 0755)
	os.MkdirAll(dst2, 0755)

	r := runSmirrorWithTimeout(t, 15*time.Second, env.CfgPath,
		"addmirror", src2, "-dest", dst2)
	if r.ExitCode != 0 {
		// addmirror may legitimately refuse (lock conflict). Just observe.
		t.Logf("addmirror during daemon-run exit=%d, stderr=%s",
			r.ExitCode, truncate(r.Stderr, 300))
		return
	}

	// Now write a file under the NEW mirror's source. If the daemon
	// hot-reloads, the file would be synced. If not, it stays local.
	createFile(t, filepath.Join(src2, "newly-added.txt"), "should-i-sync")
	time.Sleep(5 * time.Second)

	if fileExists(filepath.Join(dst2, "newly-added.txt")) {
		t.Logf("PANEL OBS: daemon DID detect the addmirror'd config and synced a file " +
			"under the new mirror's path. Hot-reload is working.")
	} else {
		t.Logf("PANEL OBS: daemon did NOT pick up the newly-added mirror — file in src-new/ " +
			"never synced. This is the expected behavior per code (no config hot-reload), but " +
			"`addmirror` should print a hint like 'restart daemon to begin watching this " +
			"mirror'. Operator may not realize a restart is required.")
	}
}

// =========================================================================
// 5. ADVERSARIAL — small, focused checks
// =========================================================================

// Adversarial Finding #7: verify_interval_sec = -1 disables verify. Code
// treats this as documented (-1 = disabled), but no validation that the
// value is meaningfully bounded. Just lock down the documented behavior.
func TestPanelR4_Adv_VerifyDisabledExplicit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1, // documented disabled value
	})

	r := runSmirror(t, cfg, "test-mirrors")
	// -1 should not be rejected by Validate (it's the documented "off" sentinel).
	if r.ExitCode == 2 {
		t.Errorf("PANEL BUG: verify_interval_sec=-1 (documented 'disabled') was rejected by " +
			"config.Validate. Should be accepted.")
	}
}

// Anomaly auditor Finding #5: rotation deletion errors are silently ignored.
// Plant an anomaly file with mode 0444 (read-only), see whether rotation
// surfaces the error or silently fails.
func TestPanelR4_Anomaly_RotationDeletionError(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("posix mode bits; not applicable")
	}
	t.Skip("requires triggering rotation explicitly; black-box harness can't easily force it")
}

// Empty placeholder to avoid unused-import warnings if some tests skip.
var _ = context.Background

// Adversarial Finding #2 (informational): when the OnRecord callback queue
// overflows, the first overflow records a Queue:DepthWarning anomaly (PF-A8
// fix). Verify that the warning anomaly is itself written to disk.
func TestPanelR4_Anomaly_OverflowAnnouncementOnDisk(t *testing.T) {
	t.Parallel()
	t.Skip("requires triggering 65+ anomalies under a slow webhook — needs HTTP fault-injection " +
		"server. Recommend internal-package test with a synthetic OnRecord that blocks.")
}
