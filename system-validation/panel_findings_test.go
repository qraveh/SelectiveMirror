package systemval

// panel_findings_test.go — system-validation tests synthesized from a
// multi-role review panel (architect / senior dev / edge-case / adversarial)
// run on 2026-04-28 against v0.9.17-dev.
//
// Each test targets a specific gap or suspected defect surfaced by the panel.
// Tests are designed to:
//   - run black-box via smirror.exe (no internal-package imports)
//   - be self-contained (per-test temp env, no shared state)
//   - distinguish FAIL (real bug) from t.Logf-only signals (informational
//     observations for the bug report)
//
// When a test FAILS, that's a candidate bug. When a test logs an
// observation without t.Errorf, the operator should grep "PANEL OBS:" in
// the output to find behavioural notes that warrant a follow-up.

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// =========================================================================
// 1. CONFIG VALIDATION GAPS
// =========================================================================

// Panel finding (Edge-case #11, Adversarial #6 corollary): Validate() uses
// a case-sensitive map for mirror name dedup — `Mirror` and `mirror` both
// pass, even on case-insensitive Windows filesystems where state-DB lookups
// would race.
//
// Expected: Validate() should reject names that collide case-insensitively.
func TestPanel_Config_CaseOnlyDuplicateNames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "WorkProject", LocalPath: src, Remote: dst},
			{Name: "workproject", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Errorf("PANEL BUG: Validate() accepted case-only duplicate names (`WorkProject` and `workproject`); on Windows these resolve to the same state-DB key and racey writes are possible. exit=%d", r.ExitCode)
	} else {
		t.Logf("Validate rejected case-only duplicates as expected: exit=%d", r.ExitCode)
	}
}

// Panel finding (Edge-case #9): Two mirrors with overlapping local_path
// (one is parent of the other) are accepted by Validate(). A single file
// under the child path triggers events on both watchers and double-syncs.
//
// Expected: Validate() should reject overlapping mirror paths.
func TestPanel_Config_OverlappingLocalPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent := filepath.Join(root, "Project")
	child := filepath.Join(parent, "SubProject")
	dst1 := filepath.Join(root, "dst1")
	dst2 := filepath.Join(root, "dst2")
	os.MkdirAll(child, 0755)
	os.MkdirAll(dst1, 0755)
	os.MkdirAll(dst2, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "outer", LocalPath: parent, Remote: dst1},
			{Name: "inner", LocalPath: child, Remote: dst2},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: Validate() accepted overlapping mirror paths (parent watches child). " +
			"This will double-sync any file under the child. " +
			"Recommendation: detect and reject parent/child overlap at config load.")
	}
	assertNoPanic(t, r)
}

// Panel finding (Edge-case #10, Adversarial paragraph on watcher scaling):
// local_path: drive root (C:\) is accepted. Watcher recurses through every
// directory on the drive — fsnotify resource exhaustion likely.
func TestPanel_Config_DriveRootAsLocalPath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("drive-root semantics are Windows-specific")
	}
	root := t.TempDir()
	dst := filepath.Join(root, "dst")
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "wholedrive", LocalPath: `C:\`, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Just validate — don't actually start. We only want to know whether
	// a drive root is rejected. test-mirrors is a no-side-effect check.
	r := runSmirrorWithTimeout(t, 30*time.Second, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: Validate() accepted local_path=C:\\ — watching an entire drive is " +
			"likely to exhaust fsnotify watch handles and is almost never user intent. " +
			"Recommendation: warn or reject when local_path is a drive root or %%SystemDrive%%.")
	}
	assertNoPanic(t, r)
}

// Panel finding (Edge-case #3): No syntax pre-check on `remote`. Path-traversal-
// looking values like `local:../../etc` pass Validate() and only fail at sync
// time inside rclone, leaving misconfiguration silent in `status`.
func TestPanel_Config_RemoteAcceptsTraversalSyntax(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "weird", LocalPath: src, Remote: `local:../../etc`},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: Validate() accepted remote=`local:../../etc` (traversal-shaped). " +
			"Failure deferred to first sync attempt; status may show 'OK' until then.")
	}
	assertNoPanic(t, r)
}

// Panel finding (Adversarial #6, CRITICAL): rclone_extra_flags is appended
// verbatim — no allowlist. A user (or anyone with write access to config)
// can inject `--rc --rc-addr 0.0.0.0:5572 --rc-no-auth` to expose an
// unauthenticated rclone control plane, or `--log-file=...` to overwrite
// arbitrary files writable by the smirror principal (LocalSystem under
// service mode).
//
// Test: dry-run a config with dangerous extra flags and check whether
// smirror raises any objection or strips/warns about them.
func TestPanel_Config_RcloneExtraFlags_DangerousFlagsAccepted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	createFile(t, filepath.Join(src, "x.txt"), "x")

	dangerous := [][]string{
		{"--rc", "--rc-addr", "127.0.0.1:5572", "--rc-no-auth"},
		{"--log-file", filepath.Join(root, "out.log"), "--log-level", "DEBUG"},
		{"--config", filepath.Join(root, "rogue-rclone.conf")},
	}

	for i, flags := range dangerous {
		flags := flags
		i := i
		t.Run(fmt.Sprintf("dangerous_flags_%d", i), func(t *testing.T) {
			t.Parallel()
			subroot := t.TempDir()
			subsrc := filepath.Join(subroot, "src")
			subdst := filepath.Join(subroot, "dst")
			os.MkdirAll(subsrc, 0755)
			os.MkdirAll(subdst, 0755)
			createFile(t, filepath.Join(subsrc, "x.txt"), "x")

			cfg := createConfig(t, subroot, configOpts{
				Mirrors: []mirrorDef{
					{Name: "victim", LocalPath: subsrc, Remote: subdst, RcloneExtra: flags},
				},
				StateDB:           filepath.Join(subroot, "state.db"),
				LogFile:           filepath.Join(subroot, "s.log"),
				LogLevel:          "error",
				SyncWorkers:       1,
				NotifyEnabled:     boolPtr(false),
				AnomalyEnabled:    boolPtr(false),
				VerifyIntervalSec: -1,
			})

			r := runSmirrorWithTimeout(t, 30*time.Second, cfg, "test-mirrors")
			combined := r.Stdout + r.Stderr
			rejected := strings.Contains(strings.ToLower(combined), "reject") ||
				strings.Contains(strings.ToLower(combined), "not allowed") ||
				strings.Contains(strings.ToLower(combined), "disallowed") ||
				strings.Contains(strings.ToLower(combined), "unsafe flag")
			if !rejected && r.ExitCode == 0 {
				t.Logf("PANEL OBS: rclone_extra_flags=%v passed validation with no warning/rejection. "+
					"Sub-flags like --rc-no-auth, --log-file, and --config can be abused by anyone "+
					"who can edit config.yaml. Recommendation: allowlist or reject dangerous flags.", flags)
			}
			assertNoPanic(t, r)
		})
	}
}

// Panel finding (Adversarial #14): rclone_config path is appended to every
// rclone invocation but its existence/ownership is not validated. Combined
// with the SEC-C5 service-mode requirement that the *config.yaml* be
// admin-owned, an attacker could still pivot via rclone_config pointing at
// a user-writable file.
func TestPanel_Config_RcloneConfigPathNotValidated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	bogus := filepath.Join(root, "does-not-exist.conf")
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
		ExtraYAML:         fmt.Sprintf("rclone_config: %q\n", bogus),
	})

	r := runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: rclone_config=%q (does not exist) accepted by Validate(). " +
			"Recommendation: stat the rclone_config path at config-load and reject missing/unreadable files.", bogus)
	}
}

// =========================================================================
// 2. REPORT-BUG OUTPUT GAPS (cross-checks against existing failing test)
// =========================================================================

// Panel observation (was BUG, now reclassified to TEST-DEFECT after reading
// SM-164 in main.go ~L2082): report-bug --stdout uses `mirror_N:` placeholder
// labels and intentionally omits status.json metrics for privacy (the report
// is destined for a public GitHub issue). The existing
// TestCLI_ReportBug_FailureScenario/{VerifyContent,VerifyURLPrefill} tests in
// cli_test.go assert presence of `working-mirror`, `broken-mirror`, and
// `sync_errors: 17` in the env section — those expectations are stale and
// were not updated when SM-164 landed.
//
// This panel test confirms the behavior is INTENTIONAL but flags the trade-off:
// without mirror names in the report, an operator triaging a multi-mirror
// failure cannot tell which mirror is producing which counts.
func TestPanel_ReportBug_EnvMissingMirrorNamesAndMetrics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "alpha-project", LocalPath: src, Remote: dst},
			{Name: "beta-project", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Pre-seed status.json so we can check whether report-bug surfaces it.
	statusJSON := `{"version":"0.9.17-dev","sync_errors":42,"queue_depth":7,"files_synced":99}`
	os.WriteFile(filepath.Join(dataDir, "status.json"), []byte(statusJSON), 0644)

	r := runSmirror(t, cfg, "report-bug", "--stdout")
	assertExitCode(t, r, 0)

	report := r.Stdout

	// Split env from logs the same way main.go --open does.
	env := report
	if idx := strings.Index(report, "\n--- Recent Logs"); idx >= 0 {
		env = report[:idx]
	}

	missing := []string{}
	for _, want := range []string{"alpha-project", "beta-project", "sync_errors: 42", "queue_depth: 7"} {
		if !strings.Contains(env, want) {
			missing = append(missing, want)
		}
	}
	t.Logf("PANEL OBS: report-bug --stdout env omits %v. Per SM-164 (main.go ~L2082) this is "+
		"INTENTIONAL — the env section uses `mirror_N:` placeholder labels and the Live Metrics "+
		"block was removed because reports go to public GitHub issues. Trade-off: operator triaging a "+
		"multi-mirror failure cannot tell *which* mirror has which counts. Update "+
		"TestCLI_ReportBug_FailureScenario/{VerifyContent,VerifyURLPrefill} to match SM-164.", missing)
}

// =========================================================================
// 3. STATE DB & LOCK ROBUSTNESS
// =========================================================================

// Panel finding (Senior dev #2, Adversarial #7): IsLocked() does not verify
// the recorded PID is alive. A crashed previous instance may leave a stale
// lock that blocks a clean restart.
//
// Test: simulate a pre-existing lock file with a PID that is guaranteed not
// to be a running smirror, then run a non-mutating command and observe.
func TestPanel_Lock_StalePIDNotDetected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Touch a stale lock file containing a PID that's never alive (PID 1
	// on Windows is the System idle process and reserved). We can't be
	// certain about Windows PID conventions, so try a clearly-bogus PID.
	lockPath := filepath.Join(dataDir, "smirror.lock")
	os.WriteFile(lockPath, []byte("99999999\n"), 0644)

	// status is read-only; should still succeed even if a stale lock is
	// observed (lock is only required for mutating commands).
	r := runSmirror(t, cfg, "status")
	if r.ExitCode == 4 {
		t.Errorf("PANEL OBS: status returned exit 4 (lock conflict) for a stale lock file with bogus PID. "+
			"Either status should not require the lock, or stale-PID detection should clear the lock. exit=%d", r.ExitCode)
	}
	assertNoPanic(t, r)
}

// Panel finding (Edge-case #18): zero-byte state.db is not gracefully
// rebuilt. db.Exec(baseSchema) fails with "file is not a database" and the
// daemon exits with no recovery path.
func TestPanel_StateDB_ZeroByteFileHandling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Create a 0-byte state DB so smirror is forced to deal with it.
	f, _ := os.Create(filepath.Join(dataDir, "state.db"))
	f.Close()

	r := runSmirrorWithTimeout(t, 20*time.Second, cfg, "status")
	combined := strings.ToLower(r.Stdout + r.Stderr)
	if strings.Contains(combined, "panic") {
		t.Errorf("PANEL BUG: zero-byte state.db caused a panic (no graceful recovery). exit=%d", r.ExitCode)
	}
	if r.ExitCode == 0 {
		t.Logf("PANEL OBS: smirror tolerated a zero-byte state.db on `status`; verify no silent corruption.")
	} else {
		t.Logf("zero-byte state.db rejected with exit=%d; combined stderr/out: %s", r.ExitCode, truncate(combined, 200))
	}
	assertNoPanic(t, r)
}

// Panel finding (Edge-case #18 variant): garbled state DB. We write
// non-SQLite junk and check that smirror handles it without panic and with
// a clear error message.
func TestPanel_StateDB_CorruptedFileHandling(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Write 200 bytes of garbage as the state DB.
	garbage := []byte(strings.Repeat("X", 200))
	os.WriteFile(filepath.Join(dataDir, "state.db"), garbage, 0644)

	r := runSmirrorWithTimeout(t, 20*time.Second, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	mentions := strings.Contains(combined, "database") || strings.Contains(combined, "sqlite") ||
		strings.Contains(combined, "corrupt") || strings.Contains(combined, "integrity")
	if r.ExitCode == 0 {
		t.Errorf("PANEL BUG: garbage state.db produced exit 0; corruption was not detected.")
	} else if !mentions {
		t.Logf("PANEL OBS: garbage state.db rejected with exit=%d but error message lacks DB/SQLite/integrity vocabulary. Operator may not know what to fix.", r.ExitCode)
	}
}

// Panel finding (Architect #6): schema_version is unconditionally set to
// len(migrations). A downgrade scenario (newer binary writes a higher
// version, then older binary runs) makes the older binary skip migrations
// and assume the schema is up-to-date.
//
// Test: pre-create a state DB with a forward-dated schema_version and run
// status against it. The expected safe behavior is to refuse to run with a
// clear "schema too new" error rather than silently proceeding.
func TestPanel_StateDB_ForwardSchemaVersion_SilentDowngrade(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// First, let smirror create a healthy DB.
	if r := runSmirror(t, cfg, "status"); r.ExitCode != 0 && r.ExitCode != 1 {
		t.Skipf("baseline status returned %d; cannot establish DB", r.ExitCode)
	}

	// Now poke a high schema_version into the meta table directly.
	// We use sqlite3 if available; otherwise skip with a panel observation.
	dbPath := filepath.Join(dataDir, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("state DB not created: %v", err)
	}
	// Without a sqlite CLI we can't reliably tamper. Just record an
	// observation so the operator knows this needs manual confirmation.
	t.Logf("PANEL OBS: forward-dated schema_version handling cannot be black-box-tested without sqlite3 CLI. " +
		"Manual reproduction: open state.db, run `UPDATE meta SET value='999' WHERE key='schema_version'`, " +
		"then run `smirror status`. If exit is 0 with no warning, the downgrade gap (Architect finding #6) is real.")
}

// =========================================================================
// 4. FILTER & SYNCIGNORE EDGE CASES
// =========================================================================

// Panel finding (Edge-case #4): BOM-prefixed .syncignore with CRLF line
// endings. Common when users edit on Windows (Notepad, Visual Studio).
// Likely behavior: BOM eats the first pattern; CRLF preserved as part of
// the pattern, breaking matches.
func TestPanel_Filter_BOMAndCRLFInSyncIgnore(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// BOM + CRLF + a clean filter.
	bom := "\xEF\xBB\xBF"
	contents := bom + "*.log\r\n*.tmp\r\n"
	createFile(t, filepath.Join(env.SrcDir, ".syncignore"), contents)

	createFile(t, filepath.Join(env.SrcDir, "keep.txt"), "keep")
	createFile(t, filepath.Join(env.SrcDir, "skip.log"), "skip")
	createFile(t, filepath.Join(env.SrcDir, "skip.tmp"), "skip")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)

	if !fileExists(filepath.Join(env.DstDir, "keep.txt")) {
		t.Logf("PANEL OBS: BOM+CRLF .syncignore caused keep.txt to be skipped (filter parser confused by BOM).")
	}
	if fileExists(filepath.Join(env.DstDir, "skip.log")) {
		t.Errorf("PANEL BUG: BOM-prefixed .syncignore did not exclude *.log — first pattern eaten by BOM. " +
			"Recommendation: strip BOM at .syncignore parse time.")
	}
	if fileExists(filepath.Join(env.DstDir, "skip.tmp")) {
		t.Errorf("PANEL BUG: CRLF-terminated *.tmp pattern did not exclude skip.tmp. " +
			"Recommendation: strip \\r in .syncignore parser.")
	}
}

// Panel finding (Edge-case): negation re-include of a globally-excluded path.
// global_excludes contains `.git/`. Per-mirror .syncignore re-includes one
// .git file via `!.git/special-keep`. Verify gitignore-spec behavior: this
// IS NOT supported by gitignore (you cannot re-include a file under an
// excluded directory) — but smirror's filter chain may accidentally allow
// it. Either way, behavior should be DEFINED.
func TestPanel_Filter_NegateUnderExcludedDir(t *testing.T) {
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
			{Name: "m", LocalPath: src, Remote: dst},
		},
		GlobalExcludes:    []string{".git/"},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	createSyncIgnore(t, src, []string{"!.git/special-keep"})
	createFile(t, filepath.Join(src, ".git", "config"), "must-skip")
	createFile(t, filepath.Join(src, ".git", "special-keep"), "wanted")
	createFile(t, filepath.Join(src, "code.go"), "package main")

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	// .git/config must NEVER sync; that part is unambiguous.
	if fileExists(filepath.Join(dst, ".git", "config")) {
		t.Errorf("PANEL BUG: .git/config synced despite global_excludes=[.git/]. Filter precedence broken.")
	}
	if !fileExists(filepath.Join(dst, "code.go")) {
		t.Errorf("PANEL BUG: code.go did not sync — unrelated files broken by negation pattern.")
	}
	specialSynced := fileExists(filepath.Join(dst, ".git", "special-keep"))
	t.Logf("PANEL OBS: negation under excluded directory: special-keep synced=%v. "+
		"Document and lock down whichever behavior — don't leave it to per-version drift.", specialSynced)
}

// Panel finding (Edge-case #8): trailing-escaped-space pattern. The
// gitignore spec says trailing spaces are stripped UNLESS escaped with
// backslash. Implementations differ.
func TestPanel_Filter_TrailingEscapedSpace(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	createSyncIgnore(t, env.SrcDir, []string{`*.log\ `})
	createFile(t, filepath.Join(env.SrcDir, "with-trailing-space.log "), "x")
	createFile(t, filepath.Join(env.SrcDir, "no-trailing.log"), "y")

	r := runSmirror(t, env.CfgPath, "sync-now")
	if r.ExitCode != 0 {
		t.Logf("sync-now non-zero exit: %d (acceptable for some filesystems that reject trailing-space names).", r.ExitCode)
		return
	}
	withSpace := fileExists(filepath.Join(env.DstDir, "with-trailing-space.log "))
	noSpace := fileExists(filepath.Join(env.DstDir, "no-trailing.log"))
	t.Logf("PANEL OBS: pattern `*.log\\ ` — file with trailing space synced=%v, file without synced=%v. "+
		"Document the chosen interpretation (gitignore is ambiguous here).", withSpace, noSpace)
}

// =========================================================================
// 5. FILENAME EDGE CASES
// =========================================================================

// Windows-reserved names. Windows refuses to create these; smirror should
// not crash if the user somehow supplies them via a non-Windows source or
// network share that doesn't enforce the restriction.
func TestPanel_Filename_WindowsReservedNames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific")
	}
	env := newTestEnv(t)

	// The Windows NTFS layer rejects file creation under reserved names,
	// so we cannot actually create them. Instead, we test that smirror
	// gracefully handles a config-path or .syncignore reference to a
	// reserved name without crashing.
	createSyncIgnore(t, env.SrcDir, []string{"CON", "NUL", "AUX", "COM1.txt", "PRN"})
	createFile(t, filepath.Join(env.SrcDir, "ok.txt"), "ok")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertNoPanic(t, r)
	if r.ExitCode != 0 {
		t.Errorf("sync-now failed (%d) with Windows-reserved names in .syncignore: stderr=%s",
			r.ExitCode, truncate(r.Stderr, 300))
	}
}

// Trailing-dot filenames are valid on POSIX, invalid on NTFS. If smirror is
// asked to sync a file with a trailing dot (e.g., from a network share),
// what happens?
func TestPanel_Filename_TrailingDotSpace(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	// On Windows, os.WriteFile with trailing dot will silently strip the
	// dot. That's fine for this test — we just want no panic.
	tricky := []string{"trailing.dot.", "trailing.space ", "lots...", "weird name."}
	for _, n := range tricky {
		// Skip on Windows where os.Create rejects the name.
		_ = os.WriteFile(filepath.Join(env.SrcDir, n), []byte("x"), 0644)
	}

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)
	t.Logf("PANEL OBS: trailing-dot/space filenames handled without crash. " +
		"Verify cross-FS roundtripping if/when source is a network share that preserves them.")
}

// Unicode normalization: NFC vs NFD. macOS HFS+ stores filenames as NFD;
// Windows NTFS as opaque UTF-16. Same logical filename can have two byte
// representations. State DB lookups will miss the second.
func TestPanel_Filename_UnicodeNormalization(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	// "café" — NFC form (composed)
	nfc := "café.txt"
	// "café" — NFD form (decomposed: e + combining acute)
	nfd := "café.txt"

	createFile(t, filepath.Join(env.SrcDir, nfc), "n-f-c")
	if err := os.WriteFile(filepath.Join(env.SrcDir, nfd), []byte("n-f-d"), 0644); err != nil {
		// On Windows the FS may collapse NFC and NFD to the same file; that's fine.
		t.Logf("could not write NFD variant: %v (may be FS-coalesced)", err)
	}

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)

	files := listFiles(t, env.DstDir)
	t.Logf("PANEL OBS: synced filenames after Unicode NFC/NFD test: %v", files)
}

// =========================================================================
// 6. FILE-SIZE BOUNDARY CASES
// =========================================================================

// Panel finding (Edge-case #6): off-by-one at max_file_size_mb boundary.
// A file at exactly the boundary should be treated consistently across
// re-syncs.
func TestPanel_Sync_MaxFileSizeBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	// 1 MB limit, then files at 1 MB - 1, exact 1 MB, 1 MB + 1.
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst, MaxFileSizeMB: 1},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	mb := 1024 * 1024
	cases := map[string]int{
		"under.bin": mb - 1,
		"exact.bin": mb,
		"over.bin":  mb + 1,
	}
	for name, size := range cases {
		createBinaryFile(t, filepath.Join(src, name), size)
	}

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	got := map[string]bool{}
	for name := range cases {
		got[name] = fileExists(filepath.Join(dst, name))
	}
	t.Logf("PANEL OBS: max_file_size_mb=1 boundary — under=%v exact=%v over=%v. "+
		"Document whether `exact` is included (per FR-SYNC-05 it is not — `>=` boundary).",
		got["under.bin"], got["exact.bin"], got["over.bin"])

	if !got["under.bin"] {
		t.Errorf("file just under boundary unexpectedly skipped")
	}
	if got["over.bin"] {
		t.Errorf("file just over boundary unexpectedly synced")
	}
}

// =========================================================================
// 7. ZERO-BYTE & EMPTY-CONFIG SCENARIOS
// =========================================================================

func TestPanel_Config_EmptyMirrorsListWithDefaultRemote(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	os.WriteFile(cfgPath, []byte("default_remote: \"gdrive:smirror\"\n"), 0644)

	r := runSmirror(t, cfgPath, "status")
	assertNoPanic(t, r)
	// Should be exit 2 (config error: no mirrors).
	if r.ExitCode != 2 {
		t.Logf("PANEL OBS: empty mirrors list with default_remote returned exit %d (expected 2 = config error). stderr=%s",
			r.ExitCode, truncate(r.Stderr, 200))
	}
}

func TestPanel_Config_ZeroByteFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	f, _ := os.Create(cfgPath)
	f.Close()

	r := runSmirror(t, cfgPath, "status")
	assertNoPanic(t, r)
	if r.ExitCode != 2 {
		t.Errorf("PANEL BUG: zero-byte config produced exit %d, expected 2 (config error). " +
			"stderr=%s", r.ExitCode, truncate(r.Stderr, 200))
	}
}

// =========================================================================
// 8. CLI ARGUMENT EDGE CASES
// =========================================================================

func TestPanel_CLI_ConfigFlagWithEqualsForm(t *testing.T) {
	// Both `--config foo` and `--config=foo` should work consistently.
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Space form
	rSpace := runSmirrorRaw(t, "--config", cfg, "version")
	if rSpace.ExitCode != 0 {
		t.Errorf("`--config %s version` failed: exit=%d stderr=%s", cfg, rSpace.ExitCode, truncate(rSpace.Stderr, 200))
	}
	// Equals form
	rEq := runSmirrorRaw(t, "--config="+cfg, "version")
	if rEq.ExitCode != 0 {
		t.Errorf("`--config=%s version` failed: exit=%d stderr=%s", cfg, rEq.ExitCode, truncate(rEq.Stderr, 200))
	}
}

func TestPanel_CLI_DoubleConfigFlag_LastWins(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	good := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: root, Remote: root},
		},
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})
	bogus := filepath.Join(root, "not-a-config.yaml")

	// Two --config flags. Behavior is unspecified; we just check no panic and
	// a clear error if it fails.
	r := runSmirrorRaw(t, "--config", bogus, "--config", good, "version")
	assertNoPanic(t, r)
	t.Logf("PANEL OBS: double --config flag exit=%d (the parser should pick a documented winner — first or last — and not silently use the wrong one).", r.ExitCode)
}

// =========================================================================
// 9. URL / WEBHOOK SECURITY
// =========================================================================

// Panel finding (Adversarial #3): webhook URL host is checked at config
// load only. Hostnames that resolve to private IPs at SEND TIME bypass.
// We can't fully exercise DNS-rebind from black-box, but we can test that
// the static checks are at least correct.
func TestPanel_Webhook_StaticIPRejection(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"https://127.0.0.1/x":           true, // loopback (must reject)
		"https://10.0.0.1/x":            true, // RFC1918 (must reject)
		"https://192.168.1.1/x":         true, // RFC1918 (must reject)
		"https://169.254.169.254/x":     true, // link-local AWS IMDS (must reject)
		"https://[::1]/x":               true, // IPv6 loopback (must reject)
		"http://example.com/x":          true, // plaintext (must reject)
		"https://example.com/x":         false, // legitimate
	}

	for raw, mustReject := range cases {
		raw := raw
		mustReject := mustReject
		t.Run(url.QueryEscape(raw), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			src := filepath.Join(root, "src")
			dst := filepath.Join(root, "dst")
			os.MkdirAll(src, 0755)
			os.MkdirAll(dst, 0755)

			cfg := createConfig(t, root, configOpts{
				Mirrors: []mirrorDef{
					{Name: "m", LocalPath: src, Remote: dst},
				},
				StateDB:           filepath.Join(root, "state.db"),
				LogFile:           filepath.Join(root, "s.log"),
				LogLevel:          "error",
				SyncWorkers:       1,
				NotifyEnabled:     boolPtr(false),
				AnomalyEnabled:    boolPtr(false),
				VerifyIntervalSec: -1,
				ExtraYAML:         fmt.Sprintf("alert_webhook_url: %q\n", raw),
			})

			r := runSmirror(t, cfg, "test-mirrors")
			assertNoPanic(t, r)
			rejected := r.ExitCode == 2 // config validation error
			if mustReject && !rejected {
				t.Errorf("PANEL BUG: alert_webhook_url=%q must be rejected at config load (SEC-C4) but exit=%d", raw, r.ExitCode)
			}
			if !mustReject && rejected {
				t.Errorf("legitimate alert_webhook_url=%q rejected: exit=%d stderr=%s", raw, r.ExitCode, truncate(r.Stderr, 200))
			}
		})
	}
}

// =========================================================================
// 10. SHELL-METACHAR INJECTION VIA HOOKS
// =========================================================================

// Panel finding (Senior dev #4, Adversarial corollary): hook commands are
// run via cmd.exe /C / sh -c. Even if the SMIRROR_* env values are
// validated, the hookCmd ITSELF (the config string) is shell-interpreted.
// Verify that smirror at least does not silently swallow obviously-malicious
// hook strings — and that no command-injection path bypasses validation.
func TestPanel_Hooks_ConfigStringIsShellInterpreted(t *testing.T) {
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

	canary := filepath.Join(dataDir, "canary.txt")

	// A hook that always succeeds but writes a canary file as a side-effect.
	// If the hook executes, canary will exist after sync.
	hookCmd := fmt.Sprintf(`cmd /c echo hooked > "%s"`, canary)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: dst, PostSyncHook: hookCmd},
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
	t.Logf("sync-now with post_sync_hook: exit=%d stdout=%s stderr=%s",
		r.ExitCode, truncate(r.Stdout, 200), truncate(r.Stderr, 200))
	assertNoPanic(t, r)
	t.Logf("PANEL OBS: canary exists=%v — confirms hook execution path. "+
		"If a config-write attacker can edit post_sync_hook, they execute as the smirror principal. "+
		"This is by design; SEC-C5 says service-mode requires admin-owned config.", fileExists(canary))
}

// =========================================================================
// 11. UNKNOWN YAML KEYS (silent typo trap)
// =========================================================================

// Panel observation: yaml.v3 silently ignores unknown keys by default.
// `mirrior:` (typo for `mirrors:`) → empty mirrors list → exit 2 with
// generic message; nothing tells the user about the typo.
func TestPanel_Config_TypoInTopLevelKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	body := `# Typo: 'mirrior' instead of 'mirrors'
mirrior:
  - name: m
    local_path: /tmp
    remote: /tmp
`
	os.WriteFile(cfgPath, []byte(body), 0644)

	r := runSmirror(t, cfgPath, "status")
	assertNoPanic(t, r)
	t.Logf("PANEL OBS: typo'd top-level key produced exit=%d, message=%q. "+
		"Without strict YAML decoding, users debugging 'no mirrors defined' won't know they typed `mirrior`.",
		r.ExitCode, truncate(r.Stderr+r.Stdout, 300))
}

// =========================================================================
// 12. INFORMATIONAL: rclone subprocess stderr surfacing
// =========================================================================

// When sync fails because of a misconfigured remote, the user must see a
// useful error in stderr — not just exit code 3.
func TestPanel_Sync_BogusRemote_ErrorIsInformative(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dataDir, 0755)
	createFile(t, filepath.Join(src, "x.txt"), "x")

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "m", LocalPath: src, Remote: "no-such-remote:bucket/path"},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	r := runSmirrorWithTimeout(t, 60*time.Second, cfg, "sync-now")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hint := strings.Contains(combined, "remote") || strings.Contains(combined, "rclone") ||
		strings.Contains(combined, "config")
	if r.ExitCode == 0 {
		t.Errorf("PANEL BUG: sync-now to bogus remote exited 0 — failure was masked.")
	}
	if !hint {
		t.Logf("PANEL OBS: sync-now exit=%d to bogus remote, but combined stdout/stderr lacks 'remote'/'rclone'/'config' vocabulary. " +
			"Operator may not know what to fix. Output: %s", r.ExitCode, truncate(combined, 200))
	}
}
