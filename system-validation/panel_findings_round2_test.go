package systemval

// panel_findings_round2_test.go — Round 2 system-validation tests
// synthesized from a fresh four-lens review (live-watcher / sync-correctness /
// concurrency / recovery) against v0.9.26-dev on 2026-04-28.
//
// Round 1 (panel_findings_test.go) was heavy on config-validation gaps and
// most have shipped. Round 2 prioritizes the MOST IMPORTANT FEATURES:
//   - the live watcher path (smirror start) which the existing suite barely
//     exercises (most tests use sync-now)
//   - data-integrity guarantees around ghost cleanup and quarantine retention
//   - crash / restart recovery and lock semantics
//   - rename + filter behaviours
//
// Convention: PANEL BUG = real defect (t.Errorf). PANEL OBS = observation
// only (t.Logf), surfaced for the bug report.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =========================================================================
// 1. DATA-LOSS RISK: ghost cleanup against a stale state.db
// =========================================================================

// Panel finding (Recovery #6, Critical): if a user restores state.db from
// an older backup, files that were uploaded between the backup and the
// restore look like ghost orphans. Under delete_policy=delete (the default),
// `sync-now` will DELETE those files from the remote.
//
// Test: simulate the scenario as black-box — sync 2 files (state DB
// records them), back up state.db, sync 2 more, restore the backup,
// run sync-now, observe whether the 2 newer files survive on the remote.
func TestPanelR2_Ghost_RestoreOldStateDB_DeletesNewFiles(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Sync first batch.
	createFile(t, filepath.Join(env.SrcDir, "first.txt"), "first")
	createFile(t, filepath.Join(env.SrcDir, "second.txt"), "second")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(env.DstDir, "first.txt"))
	assertFileExists(t, filepath.Join(env.DstDir, "second.txt"))

	// Snapshot state.db.
	stateDB := filepath.Join(env.DataDir, "state.db")
	stateBackup := filepath.Join(env.DataDir, "state.db.snapshot")
	if data, err := os.ReadFile(stateDB); err == nil {
		os.WriteFile(stateBackup, data, 0644)
	} else {
		t.Skipf("could not read state.db: %v", err)
	}

	// Add new files and sync them.
	createFile(t, filepath.Join(env.SrcDir, "newer.txt"), "newer")
	createFile(t, filepath.Join(env.SrcDir, "newest.txt"), "newest")
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(env.DstDir, "newer.txt"))
	assertFileExists(t, filepath.Join(env.DstDir, "newest.txt"))

	// Restore the snapshot — newer.txt and newest.txt are now "ghosts" from
	// the state DB's perspective.
	if data, err := os.ReadFile(stateBackup); err == nil {
		os.WriteFile(stateDB, data, 0644)
		// SQLite WAL/journal files may also need to go.
		os.Remove(stateDB + "-wal")
		os.Remove(stateDB + "-shm")
	}

	// Run sync-now. Expectation per "no surprise data loss": newer.txt and
	// newest.txt must survive — they exist locally and the filter doesn't
	// exclude them. They should be re-recorded into state, not deleted.
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	survivedNewer := fileExists(filepath.Join(env.DstDir, "newer.txt"))
	survivedNewest := fileExists(filepath.Join(env.DstDir, "newest.txt"))
	if !survivedNewer || !survivedNewest {
		t.Errorf("PANEL BUG: ghost cleanup deleted recently-uploaded files after a state.db restore. "+
			"newer.txt survived=%v, newest.txt survived=%v. This is a data-loss path: a user who "+
			"restores their state.db from backup will lose files added since the backup. "+
			"Recommendation: ghost cleanup should compare to LOCAL filesystem, not just state DB.",
			survivedNewer, survivedNewest)
	}
}

// =========================================================================
// 2. DELETE-POLICY DATA INTEGRITY
// =========================================================================

// Panel finding (Sync #6, High): quarantine_days is enforced only during
// reconciliation. If reconciliation is disabled (`verify_interval_sec: -1`,
// which the helpers set by default), quarantined files accumulate forever.
//
// Test: configure quarantine + verify_interval_sec=-1, delete a file (it
// goes to .quarantine/), set its mtime to "old", call test-mirrors which
// runs the reconcile path. Then check whether the quarantined file survived.
//
// Note: the existing default test config sets VerifyIntervalSec: -1, so
// the typical user calling sync-now never triggers PurgeExpiredQuarantine.
func TestPanelR2_Quarantine_RetentionNotEnforced_NoReconcile(t *testing.T) {
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
			{Name: "m", LocalPath: src, Remote: dst,
				DeletePolicy: "quarantine", QuarantineDays: 1},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1, // reconciliation/verify disabled
	})

	// Sync, then delete locally → goes to .quarantine/.
	createFile(t, filepath.Join(src, "doomed.txt"), "doomed")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	os.Remove(filepath.Join(src, "doomed.txt"))
	r = runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	// Find the quarantined file.
	qDir := filepath.Join(dst, ".quarantine")
	qFiles := listFiles(t, qDir)
	if len(qFiles) == 0 {
		t.Skip("file did not enter quarantine — separate bug, not the one we're testing")
	}

	// Backdate the quarantined file far past retention.
	for _, rel := range qFiles {
		full := filepath.Join(qDir, rel)
		oldTime := time.Now().AddDate(0, 0, -10) // 10 days ago, retention is 1
		os.Chtimes(full, oldTime, oldTime)
	}

	// Run sync-now AGAIN. With verify disabled, no PurgeExpiredQuarantine
	// should fire. Quarantined file should still be present.
	r = runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	stillThere := false
	for _, rel := range qFiles {
		if fileExists(filepath.Join(qDir, rel)) {
			stillThere = true
			break
		}
	}
	if stillThere {
		t.Logf("PANEL OBS: quarantined files past retention (%d days, file is %d days old) survived "+
			"because verify_interval_sec=-1 disables reconciliation, and sync-now never calls "+
			"PurgeExpiredQuarantine. quarantine_days becomes a promise without delivery in this config. "+
			"Recommendation: trigger PurgeExpiredQuarantine from sync-now too, or document that "+
			"verify must be enabled for quarantine_days to take effect.", 1, 10)
	}
}

// =========================================================================
// 3. LIVE WATCHER COVERAGE (largest gap in existing suite)
// =========================================================================

// All existing scenario tests use `sync-now` (a one-shot batch). The
// fsnotify event-driven path is barely exercised. Round 2 adds black-box
// daemon-mode tests.

// Latency baseline: file written → eventually appears on remote.
// NFR-TB-02 says < 5s p95 on broadband. Local-to-local should be much faster.
func TestPanelR2_Daemon_LiveSync_FileCreate(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()

	// Wait for daemon to be ready (prints version line then enters watch loop).
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited unexpectedly: stderr=%s", truncate(p.stderr.String(), 500))
	}

	createFile(t, filepath.Join(env.SrcDir, "live.txt"), "live")
	if !waitForFile(t, filepath.Join(env.DstDir, "live.txt"), 15*time.Second) {
		t.Errorf("PANEL BUG: file written under daemon was not synced within 15s. "+
			"Live watcher path is broken or has unexpected latency. "+
			"daemon stdout=%s stderr=%s",
			truncate(p.stdout.String(), 500), truncate(p.stderr.String(), 500))
	} else {
		assertFileContent(t, filepath.Join(env.DstDir, "live.txt"), "live")
	}
}

// Burst create: 50 files at once, all should sync.
func TestPanelR2_Daemon_LiveSync_BurstCreate(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	const n = 50
	for i := 0; i < n; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("burst%03d.txt", i)),
			fmt.Sprintf("content-%d", i))
	}

	if !waitForFileCount(t, env.DstDir, n, 30*time.Second) {
		got := fileCount(env.DstDir)
		t.Errorf("PANEL BUG: burst of %d files did not all sync within 30s — got %d. "+
			"Live watcher dropped events or queue is too slow. stderr=%s",
			n, got, truncate(p.stderr.String(), 500))
	}
}

// Subdirectory creation under daemon: FR-WATCH-03 promises automatic
// recursion. Verify a NEW subdirectory + file inside it both sync.
func TestPanelR2_Daemon_LiveSync_NewSubdirectory(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	subDir := filepath.Join(env.SrcDir, "newdir", "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Give the watcher a beat to register the new dir.
	time.Sleep(500 * time.Millisecond)
	createFile(t, filepath.Join(subDir, "deep.txt"), "deep-content")

	if !waitForFile(t, filepath.Join(env.DstDir, "newdir", "sub", "deep.txt"), 15*time.Second) {
		t.Errorf("PANEL BUG: file in newly-created subdirectory was not synced. "+
			"Watcher's recursive subdir registration may be racing with file create. stderr=%s",
			truncate(p.stderr.String(), 500))
	}
}

// Directory rename: rename a directory containing files; all children should
// be re-queued under the new path. Old remote path should be cleaned per
// delete_policy.
func TestPanelR2_Daemon_LiveSync_DirectoryRename(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	oldDir := filepath.Join(env.SrcDir, "olddir")
	os.MkdirAll(oldDir, 0755)
	createFile(t, filepath.Join(oldDir, "a.txt"), "a")
	createFile(t, filepath.Join(oldDir, "b.txt"), "b")

	// Wait for the initial syncs.
	if !waitForFileCount(t, env.DstDir, 2, 15*time.Second) {
		t.Skipf("initial sync didn't complete; can't test rename. dst contains: %v", listFiles(t, env.DstDir))
	}

	newDir := filepath.Join(env.SrcDir, "newdir")
	if err := os.Rename(oldDir, newDir); err != nil {
		t.Fatal(err)
	}

	// After rename, two files at new paths should exist; old paths should be cleaned.
	gotNewA := waitForFile(t, filepath.Join(env.DstDir, "newdir", "a.txt"), 20*time.Second)
	gotNewB := waitForFile(t, filepath.Join(env.DstDir, "newdir", "b.txt"), 20*time.Second)
	if !gotNewA || !gotNewB {
		t.Errorf("PANEL BUG: directory rename did not propagate to remote. "+
			"new/a=%v new/b=%v. The watcher should detect children of a renamed "+
			"directory and re-sync them under the new path. stderr=%s",
			gotNewA, gotNewB, truncate(p.stderr.String(), 500))
	}

	// Old directory contents should be cleaned (delete policy).
	time.Sleep(2 * time.Second) // allow clean-up to settle
	hasOldA := fileExists(filepath.Join(env.DstDir, "olddir", "a.txt"))
	hasOldB := fileExists(filepath.Join(env.DstDir, "olddir", "b.txt"))
	if hasOldA || hasOldB {
		t.Logf("PANEL OBS: after directory rename, old remote paths still exist (a=%v b=%v). "+
			"Whether this is a real bug depends on delete policy timing — they may be cleaned "+
			"at next reconciliation. Worth a longer-timeout test.", hasOldA, hasOldB)
	}
}

// File deletion under daemon (delete_policy=delete): file deleted locally
// should be deleted on remote.
func TestPanelR2_Daemon_LiveSync_DeletePropagates(t *testing.T) {
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
			{Name: "m", LocalPath: src, Remote: dst, DeletePolicy: "delete"},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	p := startSmirror(t, cfg, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	createFile(t, filepath.Join(src, "doomed.txt"), "doomed")
	if !waitForFile(t, filepath.Join(dst, "doomed.txt"), 15*time.Second) {
		t.Skip("initial sync didn't fire; can't test delete propagation")
	}

	os.Remove(filepath.Join(src, "doomed.txt"))
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !fileExists(filepath.Join(dst, "doomed.txt")) {
			return // success
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("PANEL BUG: file deleted locally was not deleted on remote within 15s. " +
		"Live delete propagation is broken under delete_policy=delete.")
}

// Filter hot-reload under daemon: a file that's currently being synced
// should respect the LATEST filter content.
func TestPanelR2_Daemon_FilterHotReload(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	// Create .syncignore that initially excludes nothing.
	createSyncIgnore(t, env.SrcDir, []string{"# initially empty"})
	time.Sleep(500 * time.Millisecond)

	createFile(t, filepath.Join(env.SrcDir, "should-sync.txt"), "first")
	if !waitForFile(t, filepath.Join(env.DstDir, "should-sync.txt"), 10*time.Second) {
		t.Skip("initial sync did not fire")
	}

	// Modify .syncignore to exclude *.exclude files. Wait for hot-reload.
	createSyncIgnore(t, env.SrcDir, []string{"*.exclude"})
	time.Sleep(2 * time.Second)

	createFile(t, filepath.Join(env.SrcDir, "filtered.exclude"), "should-not-sync")
	createFile(t, filepath.Join(env.SrcDir, "kept.txt"), "should-sync")

	// kept.txt must sync; filtered.exclude must NOT.
	if !waitForFile(t, filepath.Join(env.DstDir, "kept.txt"), 10*time.Second) {
		t.Errorf("PANEL BUG: kept.txt was not synced after filter hot-reload — non-excluded files broken.")
	}

	// Wait one full sync cycle then check that filtered.exclude is absent.
	time.Sleep(3 * time.Second)
	if fileExists(filepath.Join(env.DstDir, "filtered.exclude")) {
		t.Errorf("PANEL BUG: file matching newly-added exclude pattern was synced anyway. " +
			"Filter hot-reload did not take effect for events arriving after the .syncignore edit.")
	}
}

// =========================================================================
// 4. LOCK SEMANTICS UNDER REAL CRASH
// =========================================================================

// Panel finding (Recovery #9, Concurrency #11, Round-1 Sr Dev #2): a
// crashed daemon may leave a lock file with a stale PID. The next start
// should detect and recover.
//
// Round 1 used a synthetic lock file with a fake PID. Round 2 simulates
// a REAL crash: kill the daemon process, immediately try to restart.
func TestPanelR2_Lock_RealCrash_AllowsRestart(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Start daemon, give it time to acquire the lock.
	p := startSmirror(t, env.CfgPath, "start")
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely: stderr=%s", truncate(p.stderr.String(), 500))
	}

	// Force-kill (simulates power loss / SIGKILL — no graceful unlock).
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	<-p.done

	// Wait a beat for OS to release any kernel-level lock state.
	time.Sleep(1 * time.Second)

	// Start a fresh daemon with the same config.
	p2 := startSmirror(t, env.CfgPath, "start")
	defer p2.Kill()
	time.Sleep(3 * time.Second)

	// p2 should still be running; an exit-4 lock conflict means the stale
	// lock was not detected.
	if isExited(p2) {
		exitCode := waitDoneExitCode(p2)
		if exitCode == 4 {
			t.Errorf("PANEL BUG: post-crash daemon restart got exit 4 (lock conflict). "+
				"Stale lock from crashed prior instance was not auto-cleared. User has to "+
				"manually delete the lock file. stderr=%s", truncate(p2.stderr.String(), 500))
		} else {
			t.Logf("p2 exited with %d: stderr=%s", exitCode, truncate(p2.stderr.String(), 300))
		}
	}
}

func waitDoneExitCode(p *smirrorProcess) int {
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
	if exitErr, ok := p.err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	if p.err != nil {
		return -1
	}
	return 0
}

// =========================================================================
// 5. STALE STATUS.JSON AFTER DAEMON DEATH
// =========================================================================

// Panel finding (Recovery #3): `smirror status` reads status.json; if the
// daemon died, the file is stale but `status` doesn't warn. User believes
// the daemon is healthy.
func TestPanelR2_Status_StaleStatusJsonAfterDaemonDeath(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	p := startSmirror(t, env.CfgPath, "start")
	time.Sleep(3 * time.Second) // let daemon write at least one heartbeat
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	// Verify status.json exists (daemon is alive).
	statusPath := filepath.Join(env.DataDir, "status.json")
	if _, err := os.Stat(statusPath); err != nil {
		t.Skipf("status.json not written within 3s — heartbeat interval may be longer: %v", err)
	}

	// Kill daemon. status.json is now stale.
	p.Kill()
	time.Sleep(1 * time.Second)

	// Run `smirror status`. Output should ideally contain a hint that the
	// daemon is not running (compare lock-acquire test, or compare the
	// recorded heartbeat timestamp with now and warn if too old).
	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasStaleHint := strings.Contains(combined, "not running") ||
		strings.Contains(combined, "stale") ||
		strings.Contains(combined, "no instance") ||
		strings.Contains(combined, "no active") ||
		strings.Contains(combined, "dead")
	if !hasStaleHint {
		t.Logf("PANEL OBS: `smirror status` after daemon death did NOT indicate the daemon is " +
			"not running. Output reads as if everything is normal. Operator may believe sync " +
			"is healthy when it has stopped. Recommendation: compare last_heartbeat to now and " +
			"warn if older than 2× heartbeat interval, OR check for lock-file PID liveness.")
	}
}

// =========================================================================
// 6. FILTER TEMP FILE LEAK ON CRASH
// =========================================================================

// Panel finding (Sync #8): GenerateRcloneFilterFile creates a temp file in
// %TEMP% with `defer os.Remove`. A SIGKILL skips the defer, leaving the
// file behind. Over weeks of crashes this accumulates.
func TestPanelR2_Filter_TempFileLeakOnKill(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("temp dir scan is OS-specific; this test targets Windows TEMP env")
	}
	env := newTestEnv(t)

	tempDir := os.TempDir()
	beforeCount := countSmirrorFilterTempFiles(t, tempDir)

	// Seed many files so the filter file is sizable and the batch sync runs long.
	for i := 0; i < 100; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("seed%03d.txt", i)), strings.Repeat("x", 1024))
	}

	// Run several sync-now cycles, force-killing each to skip the defer.
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		full := append([]string{"--config", env.CfgPath}, "sync-now")
		cmd := exec.CommandContext(ctx, smirrorBin, full...)
		cmd.Stdin = strings.NewReader("")
		_ = cmd.Start()
		// Kill after a short delay so a filter file may have been written.
		time.Sleep(200 * time.Millisecond)
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
		cancel()
	}

	afterCount := countSmirrorFilterTempFiles(t, tempDir)
	leaked := afterCount - beforeCount
	if leaked > 0 {
		t.Logf("PANEL OBS: %d smirror-filter-* temp files leaked after kills (before=%d after=%d). "+
			"defer os.Remove doesn't run on SIGKILL. Recommendation: stamp temp files with PID + "+
			"clean up orphans on next startup, OR write to dataDir which has a startup sweep.",
			leaked, beforeCount, afterCount)
	}
}

func countSmirrorFilterTempFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.HasPrefix(name, "smirror-filter") || strings.Contains(name, "smirror-filter") {
			count++
		}
		// Be liberal — Go's ioutil.TempFile uses random suffix on prefix.
		if strings.HasPrefix(name, "filter") && strings.HasSuffix(name, ".txt") {
			count++
		}
	}
	return count
}

// =========================================================================
// 7. RENAME ACROSS MIRROR BOUNDARIES
// =========================================================================

// Panel finding (Watcher #11): renaming a file from one mirror's tree to
// another's. The "from" mirror sees a delete event; the "to" mirror sees a
// create event. Behavior should be: file deleted from A's remote, created on
// B's remote.
func TestPanelR2_Daemon_RenameAcrossMirrors(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// On Windows, os.Rename across directories may behave differently
		// for fsnotify event delivery. We still attempt the test.
	}
	env := newTestEnvN(t, 2)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited: stderr=%s", truncate(p.stderr.String(), 500))
	}

	srcA := filepath.Join(env.RootDir, "src0")
	srcB := filepath.Join(env.RootDir, "src1")
	dstA := filepath.Join(env.RootDir, "dst0")
	dstB := filepath.Join(env.RootDir, "dst1")

	createFile(t, filepath.Join(srcA, "moveme.txt"), "moving")
	if !waitForFile(t, filepath.Join(dstA, "moveme.txt"), 15*time.Second) {
		t.Skip("initial sync did not fire")
	}

	if err := os.Rename(filepath.Join(srcA, "moveme.txt"), filepath.Join(srcB, "moveme.txt")); err != nil {
		t.Fatal(err)
	}

	// Within 15s: dstB should have moveme.txt; dstA should not.
	gotB := waitForFile(t, filepath.Join(dstB, "moveme.txt"), 15*time.Second)
	if !gotB {
		t.Errorf("PANEL BUG: file renamed across mirror boundary did not appear on the destination "+
			"mirror's remote. The 'create' event in mirror B was missed. stderr=%s",
			truncate(p.stderr.String(), 500))
	}

	time.Sleep(3 * time.Second)
	stillA := fileExists(filepath.Join(dstA, "moveme.txt"))
	if stillA {
		t.Logf("PANEL OBS: file renamed out of mirror A is still on A's remote. "+
			"This is the documented delete-policy behavior on rename — verify the policy " +
			"intent (foreground/per-mirror policy=delete should clean up).")
	}
}

// =========================================================================
// 8. CONCURRENT STATUS.JSON READS DURING DAEMON WRITES
// =========================================================================

// Panel finding (Concurrency #1): heartbeatLoop writes status.json on a
// timer; concurrent reads via `smirror status` may see a torn or missing
// file (depends on whether write is atomic).
func TestPanelR2_Concurrent_StatusJsonReadDuringWrite(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(3 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	statusPath := filepath.Join(env.DataDir, "status.json")
	if _, err := os.Stat(statusPath); err != nil {
		t.Skipf("status.json not written: %v", err)
	}

	// Read status.json continuously for 5 seconds while heartbeat updates it.
	deadline := time.Now().Add(5 * time.Second)
	var torn, partial int64
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				data, err := os.ReadFile(statusPath)
				if err != nil {
					atomic.AddInt64(&torn, 1)
					continue
				}
				if !strings.HasSuffix(strings.TrimSpace(string(data)), "}") {
					atomic.AddInt64(&partial, 1)
				}
			}
		}()
	}
	wg.Wait()

	if torn > 0 || partial > 0 {
		t.Logf("PANEL OBS: concurrent reads of status.json during heartbeat writes: "+
			"%d ENOENT/error reads, %d apparently-truncated reads (no closing brace). "+
			"Recommendation: write to status.json.tmp + atomic rename so readers never see partial.",
			torn, partial)
	}
}

// =========================================================================
// 9. DAEMON GRACEFUL SHUTDOWN UNDER LOAD
// =========================================================================

// Panel finding (Concurrency #14, Recovery #16): daemon shutdown while
// queue has work. FR-SVC-05 promises 30s graceful shutdown.
func TestPanelR2_Daemon_GracefulShutdown_QueueDrains(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	// Queue many syncs.
	const n = 30
	for i := 0; i < n; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("q%03d.txt", i)),
			strings.Repeat("x", 64*1024)) // 64 KB each
	}

	// Ask for graceful stop almost immediately.
	time.Sleep(200 * time.Millisecond)
	stopCh := make(chan smirrorResult, 1)
	go func() {
		stopCh <- p.Stop() // Stop sends ctx cancel and waits up to 10s
	}()

	select {
	case r := <-stopCh:
		// Daemon exited within 10s of the stop signal.
		assertNoPanic(t, r)
		// Some files should have been processed.
		// We don't require all 30 — just that shutdown was clean.
		t.Logf("graceful stop completed: exit=%d, files synced before stop=%d / %d",
			r.ExitCode, fileCount(env.DstDir), n)
	case <-time.After(15 * time.Second):
		t.Errorf("PANEL BUG: daemon did not exit gracefully within 15s of Ctrl-C-equivalent. " +
			"FR-SVC-05 promises 30s but our context-cancel path should be much faster. " +
			"Possibly: in-flight rclone process is being waited on without bound.")
	}
}

// =========================================================================
// 10. CIRCUIT BREAKER UNDER REPEATED FAILURE
// =========================================================================

// Panel observation: circuit breaker should engage after 3+ failures and
// back off exponentially. Verify via dry-run + bogus remote (won't actually
// hit the breaker because dry-run doesn't sync, but let's see).
func TestPanelR2_CircuitBreaker_BogusRemote(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dataDir, 0755)
	createFile(t, filepath.Join(src, "x.txt"), "x")

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: "no-such-remote-xyz:bucket/path"},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Run 5 sync-now cycles. Each should fail. By the 3rd or later, the
	// circuit breaker should engage and short-circuit further syncs.
	timings := []time.Duration{}
	for i := 0; i < 5; i++ {
		start := time.Now()
		r := runSmirrorWithTimeout(t, 30*time.Second, cfg, "sync-now")
		dur := time.Since(start)
		timings = append(timings, dur)
		assertNoPanic(t, r)
		if r.ExitCode == 0 {
			t.Errorf("sync-now to bogus remote unexpectedly succeeded on iteration %d", i)
		}
	}
	t.Logf("PANEL OBS: 5 successive sync-now to bogus remote durations: %v. "+
		"If circuit-breaker engages, later iterations should be MUCH faster than "+
		"first (skipped not retried). Inspect for monotonically-decreasing or "+
		"step-function timing.", timings)
}

// =========================================================================
// helper: detect a started smirror process that has already exited
// =========================================================================

func isExited(p *smirrorProcess) bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
