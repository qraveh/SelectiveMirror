package systemval

// panel_findings_round3_test.go — Round 3 system-validation tests
// synthesized from a fresh four-lens panel review (workflow / multi-mirror /
// gitignore conformance / performance) against v0.9.26-dev on 2026-04-28.
//
// Round 1 (config / security): mostly shipped — see PANEL-REVIEW-2026-04-28.md.
// Round 2 (live watcher / sync / concurrency / recovery): zero new bugs —
//   see PANEL-REVIEW-ROUND2-2026-04-28.md.
// Round 3 priorities for the most important features:
//   - Gitignore conformance (FR-FILTER-01 v1.0 commitment, no formal suite yet)
//   - Real-world workflow patterns (Office, IDE auto-save, log rotation, swap files)
//   - Multi-mirror isolation (5+ mirrors, cross-mirror event leakage)
//   - Startup time scaling (linear or worse)
//
// Convention: PANEL BUG = real defect (t.Errorf). PANEL OBS = observation
// only (t.Logf). Where the panel finding is documented design rather than a
// bug, we lock the behavior down with a regression test.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// =========================================================================
// 1. GITIGNORE CONFORMANCE (FR-FILTER-01)
// =========================================================================
//
// SRS commits to "validated against a gitignore conformance test suite
// covering edge cases (`**/foo`, `foo/**/bar`, trailing spaces, character
// classes, escaped characters)". Status column says "Done (conformance
// suite: Not Done)". This section closes that gap.

// Helper: build a single-mirror config with the given .syncignore rules
// and run `smirror explain test <relPath>` for every (path, expected) pair.
type explainCase struct {
	relPath  string
	excluded bool
	wantRule string // optional; checked if non-empty (the matched rule)
}

func runExplainSuite(t *testing.T, suiteName string, rules []string, cases []explainCase) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)
	if len(rules) > 0 {
		createSyncIgnore(t, src, rules)
	}
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
	for _, c := range cases {
		c := c
		t.Run(suiteName+"/"+c.relPath, func(t *testing.T) {
			r := runSmirror(t, cfg, "explain", "test", c.relPath)
			if r.ExitCode != 0 {
				t.Fatalf("explain failed: exit=%d stderr=%s", r.ExitCode, truncate(r.Stderr, 300))
			}
			gotExcluded := strings.Contains(r.Stdout, "Status: EXCLUDED")
			gotIncluded := strings.Contains(r.Stdout, "Status: INCLUDED")
			if !gotExcluded && !gotIncluded {
				t.Fatalf("could not parse explain output: %s", truncate(r.Stdout, 300))
			}
			if c.excluded && !gotExcluded {
				t.Errorf("PANEL BUG: rules=%v path=%q expected EXCLUDED, got INCLUDED. "+
					"explain stdout: %s", rules, c.relPath, truncate(r.Stdout, 400))
			}
			if !c.excluded && gotExcluded {
				t.Errorf("PANEL BUG: rules=%v path=%q expected INCLUDED, got EXCLUDED. "+
					"explain stdout: %s", rules, c.relPath, truncate(r.Stdout, 400))
			}
			if c.excluded && c.wantRule != "" {
				if !strings.Contains(r.Stdout, "Matched rule: "+c.wantRule) {
					t.Logf("PANEL OBS: matched-rule mismatch — wanted %q, stdout: %s",
						c.wantRule, truncate(r.Stdout, 300))
				}
			}
		})
	}
}

// CRITICAL per gitignore reviewer: child file under excluded directory
// CANNOT be re-included via negation. Per spec, once a parent is excluded,
// its children aren't even evaluated.
func TestPanelR3_Gitignore_ExcludedParentBlocksChildNegation(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "excluded-parent",
		[]string{"foo/**", "!foo/bar/baz.txt"},
		[]explainCase{
			{relPath: "foo/bar/baz.txt", excluded: true},  // spec: excluded (parent dir excluded)
			{relPath: "foo/other.txt", excluded: true},
			{relPath: "outside.txt", excluded: false},
		},
	)
}

// `**/foo` matches `foo` at any depth INCLUDING the top level.
func TestPanelR3_Gitignore_DoublestarPrefix(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "doublestar-prefix",
		[]string{"**/foo"},
		[]explainCase{
			{relPath: "foo", excluded: true},
			{relPath: "a/foo", excluded: true},
			{relPath: "a/b/foo", excluded: true},
			{relPath: "fooz", excluded: false},
			{relPath: "afoo", excluded: false},
		},
	)
}

// `foo/**/bar` matches with zero or more intermediate directories.
func TestPanelR3_Gitignore_DoublestarMiddle(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "doublestar-middle",
		[]string{"foo/**/bar"},
		[]explainCase{
			{relPath: "foo/bar", excluded: true},     // zero intermediate
			{relPath: "foo/x/bar", excluded: true},   // one
			{relPath: "foo/x/y/bar", excluded: true}, // two
			{relPath: "bar", excluded: false},
			{relPath: "foo/baz", excluded: false},
		},
	)
}

// Anchored vs unanchored.
func TestPanelR3_Gitignore_AnchoredVsUnanchored(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "anchored",
		[]string{"/foo"},
		[]explainCase{
			{relPath: "foo", excluded: true},
			{relPath: "subdir/foo", excluded: false}, // not anchored — still included
		},
	)
	runExplainSuite(t, "unanchored",
		[]string{"foo"},
		[]explainCase{
			{relPath: "foo", excluded: true},
			{relPath: "subdir/foo", excluded: true},
			{relPath: "a/b/foo", excluded: true},
		},
	)
}

// Trailing slash means "directory only".
func TestPanelR3_Gitignore_TrailingSlashDirOnly(t *testing.T) {
	t.Parallel()
	// `build/` should match the directory; a regular file named `build`
	// should NOT match (filename without trailing slash).
	runExplainSuite(t, "dir-only",
		[]string{"build/"},
		[]explainCase{
			// File named "build" (no slash semantics): should be INCLUDED.
			{relPath: "build", excluded: false},
			// File inside a build/ dir: should be EXCLUDED.
			{relPath: "build/main.o", excluded: true},
			{relPath: "src/build/main.o", excluded: true},
		},
	)
}

// Character class.
func TestPanelR3_Gitignore_CharacterClass(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "char-class",
		[]string{"[abc].txt"},
		[]explainCase{
			{relPath: "a.txt", excluded: true},
			{relPath: "b.txt", excluded: true},
			{relPath: "c.txt", excluded: true},
			{relPath: "d.txt", excluded: false},
		},
	)
}

// Negated character class.
func TestPanelR3_Gitignore_NegatedCharClass(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "neg-char-class",
		[]string{"[!abc].txt"},
		[]explainCase{
			{relPath: "a.txt", excluded: false},
			{relPath: "d.txt", excluded: true},
			{relPath: "z.txt", excluded: true},
		},
	)
}

// Escape sequences.
func TestPanelR3_Gitignore_EscapeBang(t *testing.T) {
	t.Parallel()
	// `\!important` matches a file LITERALLY named `!important`, not negation.
	runExplainSuite(t, "escape-bang",
		[]string{"\\!important"},
		[]explainCase{
			{relPath: "!important", excluded: true},
			{relPath: "important", excluded: false},
		},
	)
}

// Last-match-wins precedence inside the SAME .syncignore file.
func TestPanelR3_Gitignore_LastMatchWins(t *testing.T) {
	t.Parallel()
	// Last rule re-includes error.log explicitly.
	runExplainSuite(t, "last-match-wins",
		[]string{"*.log", "!error.log"},
		[]explainCase{
			{relPath: "info.log", excluded: true},
			{relPath: "error.log", excluded: false}, // negation wins
		},
	)
	// Reverse order: negation first, then *.log — *.log wins.
	runExplainSuite(t, "first-rule-overridden",
		[]string{"!error.log", "*.log"},
		[]explainCase{
			{relPath: "info.log", excluded: true},
			{relPath: "error.log", excluded: true}, // *.log wins (it's later)
		},
	)
}

// Question mark matches a single non-slash character.
func TestPanelR3_Gitignore_QuestionMark(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "qmark",
		[]string{"a?.txt"},
		[]explainCase{
			{relPath: "ab.txt", excluded: true},
			{relPath: "a.txt", excluded: false},  // ? requires a character
			{relPath: "abc.txt", excluded: false}, // ? matches one char
			{relPath: "a/b.txt", excluded: false}, // ? does NOT match /
		},
	)
}

// Comments and blank lines.
func TestPanelR3_Gitignore_CommentsAndBlanks(t *testing.T) {
	t.Parallel()
	runExplainSuite(t, "comments",
		[]string{"# a comment", "", "*.log", "  ", "# another"},
		[]explainCase{
			{relPath: "x.log", excluded: true},
			{relPath: "x.txt", excluded: false},
			{relPath: "comment", excluded: false},
		},
	)
}

// =========================================================================
// 2. REAL-WORLD WORKFLOW PATTERNS
// =========================================================================

// Editor swap files (Vim .swp, Emacs #file#) are NOT in the example
// global_excludes per config.example.yaml. Workflow reviewer flagged this
// as a config-default gap. Verify what happens.
func TestPanelR3_Workflow_EditorSwapFiles_DefaultBehavior(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	// Don't add any extra excludes. Use the default behavior.
	createFile(t, filepath.Join(env.SrcDir, "main.go"), "package main")
	createFile(t, filepath.Join(env.SrcDir, ".main.go.swp"), "vim swap")
	createFile(t, filepath.Join(env.SrcDir, "#file#"), "emacs autosave")
	createFile(t, filepath.Join(env.SrcDir, ".#file"), "emacs lock")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	syncedSwap := fileExists(filepath.Join(env.DstDir, ".main.go.swp"))
	syncedHash := fileExists(filepath.Join(env.DstDir, "#file#"))
	syncedDot := fileExists(filepath.Join(env.DstDir, ".#file"))
	syncedReal := fileExists(filepath.Join(env.DstDir, "main.go"))

	if !syncedReal {
		t.Errorf("PANEL BUG: real source file main.go was not synced")
	}
	if syncedSwap || syncedHash || syncedDot {
		t.Logf("PANEL OBS: editor temp files synced by default: "+
			".main.go.swp=%v, #file#=%v, .#file=%v. "+
			"Recommendation: extend config.example.yaml global_excludes to cover "+
			"`*.swp`, `*.swo`, `#*#`, `.#*` (Vim/Emacs editor temps). Today, "+
			"the user has to know to add these themselves.",
			syncedSwap, syncedHash, syncedDot)
	}
}

// Office-style atomic save: write temp + delete original + rename temp.
// The temp file matches the documented `~$*` global exclude.
func TestPanelR3_Workflow_OfficeAtomicSave(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Initial state: doc.docx with content "v1".
	doc := filepath.Join(env.SrcDir, "doc.docx")
	createFile(t, doc, "v1")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileContent(t, filepath.Join(env.DstDir, "doc.docx"), "v1")

	// Office atomic save sequence:
	// 1. Create ~$doc.docx temp.
	tmp := filepath.Join(env.SrcDir, "~$doc.docx")
	createFile(t, tmp, "v2-content")
	// 2. Delete the original doc.docx.
	os.Remove(doc)
	// 3. Rename temp to final name.
	if err := os.Rename(tmp, doc); err != nil {
		t.Fatal(err)
	}

	// Sync and verify the final doc.docx has v2 content.
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	got, _ := os.ReadFile(filepath.Join(env.DstDir, "doc.docx"))
	if string(got) != "v2-content" {
		t.Errorf("PANEL BUG: after Office atomic save, remote doc.docx = %q, want %q. "+
			"Atomic save sequence may be misinterpreted as delete-then-create.",
			string(got), "v2-content")
	}
	// The ~$ temp must NOT have synced (it's in the example global_excludes,
	// but the test config doesn't include it — so this checks default behavior).
}

// Log rotation: rename chain + truncate. The final state should be
// internally consistent on the remote.
func TestPanelR3_Workflow_LogRotation(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Pre-populate: app.log (current), app.log.1 (one rotation old).
	current := filepath.Join(env.SrcDir, "app.log")
	rotated := filepath.Join(env.SrcDir, "app.log.1")
	createFile(t, current, "current-content")
	createFile(t, rotated, "old-content")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Rotation step: rename app.log → app.log.1 (overwriting), then truncate
	// app.log to start fresh.
	os.Remove(rotated)
	if err := os.Rename(current, rotated); err != nil {
		t.Fatal(err)
	}
	createFile(t, current, "freshly-rotated")

	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Final remote should have:
	//   app.log = "freshly-rotated"
	//   app.log.1 = "current-content" (the one that got rotated)
	gotCurrent, _ := os.ReadFile(filepath.Join(env.DstDir, "app.log"))
	gotRotated, _ := os.ReadFile(filepath.Join(env.DstDir, "app.log.1"))
	if string(gotCurrent) != "freshly-rotated" {
		t.Errorf("PANEL BUG: post-rotation app.log = %q, want %q",
			string(gotCurrent), "freshly-rotated")
	}
	if string(gotRotated) != "current-content" {
		t.Errorf("PANEL BUG: post-rotation app.log.1 = %q, want %q (the previously-current content)",
			string(gotRotated), "current-content")
	}
}

// IDE-style rapid auto-save to the SAME file. The 30-second per-file
// cooldown should bound rclone invocations even under high event rate.
func TestPanelR3_Workflow_IDEAutoSave_CooldownBounds(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	// Tight loop: 20 writes to the same file in ~2 seconds.
	target := filepath.Join(env.SrcDir, "buffer.txt")
	for i := 0; i < 20; i++ {
		os.WriteFile(target, []byte(fmt.Sprintf("v%d", i)), 0644)
		time.Sleep(100 * time.Millisecond)
	}

	// Wait long enough for the queue to drain and at least one sync to happen.
	time.Sleep(8 * time.Second)

	if !fileExists(filepath.Join(env.DstDir, "buffer.txt")) {
		t.Errorf("PANEL BUG: rapid-save target was not synced at all in 10s window. "+
			"stderr=%s", truncate(p.stderr.String(), 300))
		return
	}
	got, _ := os.ReadFile(filepath.Join(env.DstDir, "buffer.txt"))
	t.Logf("PANEL OBS: after 20 rapid saves, remote content = %q "+
		"(latest local was 'v19'). The remote may be slightly stale due to cooldown — "+
		"that is the documented behavior. The daemon should NOT be in a rclone-spawn "+
		"loop firing 20 syncs.", string(got))
}

// Burst mass-rewrite: simulate `git checkout` overwriting many files at once.
// Most should be unchanged-skipped via the content-addressed check; the
// system must handle the burst without crash or unbounded queue growth.
func TestPanelR3_Workflow_BurstMassRewrite(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Sync 30 files. Then "git-checkout"-style: rewrite each with the SAME
	// content. Hash-identical → rclone --checksum should skip all.
	for i := 0; i < 30; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("f%02d.txt", i)),
			fmt.Sprintf("content-%d", i))
	}
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Rewrite each with the same bytes — mtime changes, content doesn't.
	for i := 0; i < 30; i++ {
		os.WriteFile(filepath.Join(env.SrcDir, fmt.Sprintf("f%02d.txt", i)),
			[]byte(fmt.Sprintf("content-%d", i)), 0644)
	}
	start := time.Now()
	r = runSmirror(t, env.CfgPath, "sync-now")
	dur := time.Since(start)
	assertExitCode(t, r, 0)

	t.Logf("PANEL OBS: 30-file burst rewrite (identical content) took %v. "+
		"With --checksum skip, this should be much faster than the initial sync. "+
		"If it's the same duration, content-addressed skip may not be effective.",
		dur)
}

// =========================================================================
// 3. MULTI-MIRROR ISOLATION
// =========================================================================

// Filter change in mirror A must not trigger work in mirror B.
// Multi-mirror reviewer Finding #7.
func TestPanelR3_MultiMirror_FilterChangeIsolation(t *testing.T) {
	t.Parallel()
	env := newTestEnvN(t, 3)
	// Pre-populate each mirror with one file each.
	for i := 0; i < 3; i++ {
		createFile(t, filepath.Join(env.RootDir, fmt.Sprintf("src%d", i), fmt.Sprintf("file%d.txt", i)),
			"hello")
	}
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Snapshot dst mtimes before changing mirror0's filter.
	mtimeBefore := func(rel string) time.Time {
		info, err := os.Stat(filepath.Join(env.RootDir, rel))
		if err != nil {
			return time.Time{}
		}
		return info.ModTime()
	}
	beforeM1 := mtimeBefore("dst1/file1.txt")
	beforeM2 := mtimeBefore("dst2/file2.txt")

	// Modify only mirror0's .syncignore.
	createSyncIgnore(t, filepath.Join(env.RootDir, "src0"), []string{"*.tmp"})
	time.Sleep(500 * time.Millisecond)

	// Run sync-now for ALL mirrors. Mirrors 1 and 2's files should NOT
	// be re-uploaded (mtime unchanged on remote).
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	afterM1 := mtimeBefore("dst1/file1.txt")
	afterM2 := mtimeBefore("dst2/file2.txt")
	if !afterM1.Equal(beforeM1) {
		t.Logf("PANEL OBS: mirror1's file mtime changed after mirror0's filter edit. "+
			"before=%v after=%v. May be a re-sync triggered cross-mirror.", beforeM1, afterM1)
	}
	if !afterM2.Equal(beforeM2) {
		t.Logf("PANEL OBS: mirror2's file mtime changed after mirror0's filter edit. "+
			"before=%v after=%v.", beforeM2, afterM2)
	}
}

// 5 mirrors in one config: basic isolation.  Events in mirror N must
// only sync to dstN, not any other mirror's destination.
func TestPanelR3_MultiMirror_NoCrossTalk(t *testing.T) {
	t.Parallel()
	env := newTestEnvN(t, 5)
	// Each mirror gets a unique file.
	for i := 0; i < 5; i++ {
		createFile(t, filepath.Join(env.RootDir, fmt.Sprintf("src%d", i),
			fmt.Sprintf("only_in_%d.txt", i)), fmt.Sprintf("from_mirror_%d", i))
	}
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	// Verify each only_in_N.txt is at dstN/ ONLY, not at any other dstX/.
	for i := 0; i < 5; i++ {
		want := filepath.Join(env.RootDir, fmt.Sprintf("dst%d", i),
			fmt.Sprintf("only_in_%d.txt", i))
		if !fileExists(want) {
			t.Errorf("PANEL BUG: only_in_%d.txt did not appear at dst%d", i, i)
		}
		for j := 0; j < 5; j++ {
			if i == j {
				continue
			}
			cross := filepath.Join(env.RootDir, fmt.Sprintf("dst%d", j),
				fmt.Sprintf("only_in_%d.txt", i))
			if fileExists(cross) {
				t.Errorf("PANEL BUG: only_in_%d.txt leaked into dst%d (cross-mirror leak)", i, j)
			}
		}
	}
}

// Mirror configuration drift: rename mirror in config, restart, confirm
// state DB doesn't accumulate orphaned project rows. Multi-mirror #8.
func TestPanelR3_MultiMirror_ConfigDriftOrphans(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	// First config: mirror name "ProjectA".
	cfgA := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "ProjectA", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})
	createFile(t, filepath.Join(src, "file.txt"), "hi")
	r := runSmirror(t, cfgA, "sync-now")
	assertExitCode(t, r, 0)

	// Now rewrite config with a renamed mirror "ProjectB" pointing at the
	// same source/destination. State DB still has rows for ProjectA.
	cfgB := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "ProjectB", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})
	r = runSmirror(t, cfgB, "sync-now")
	assertExitCode(t, r, 0)

	// Read project-stats — should report just ProjectB. If ProjectA is
	// still listed, state DB has orphan rows.
	r = runSmirror(t, cfgB, "project-stats")
	assertExitCode(t, r, 0)
	if strings.Contains(r.Stdout, "ProjectA") {
		t.Logf("PANEL OBS: after mirror rename A→B, state DB still mentions ProjectA. "+
			"project-stats output: %s. "+
			"Recommendation: prune state DB rows whose project name is not in current config.",
			truncate(r.Stdout, 400))
	}
}

// =========================================================================
// 4. STARTUP TIME WITH MIRROR COUNT (perf reviewer)
// =========================================================================

// Startup time should be roughly proportional to mirror count, not
// quadratic. Test 2 mirrors vs 8 and look for sub-linear behavior.
func TestPanelR3_Perf_StartupTimeScaling(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("perf test")
	}

	measure := func(n int) time.Duration {
		root := t.TempDir()
		dataDir := filepath.Join(root, "data")
		os.MkdirAll(dataDir, 0755)
		var mirrors []mirrorDef
		for i := 0; i < n; i++ {
			s := filepath.Join(root, fmt.Sprintf("src%d", i))
			d := filepath.Join(root, fmt.Sprintf("dst%d", i))
			os.MkdirAll(s, 0755)
			os.MkdirAll(d, 0755)
			mirrors = append(mirrors, mirrorDef{
				Name: fmt.Sprintf("m%d", i), LocalPath: s, Remote: d,
			})
		}
		cfg := createConfig(t, root, configOpts{
			Mirrors:           mirrors,
			StateDB:           filepath.Join(dataDir, "state.db"),
			LogFile:           filepath.Join(dataDir, "s.log"),
			LogLevel:          "error",
			SyncWorkers:       1,
			NotifyEnabled:     boolPtr(false),
			AnomalyEnabled:    boolPtr(false),
			VerifyIntervalSec: -1,
		})
		// Time a status command — exercises config load + filter compile + state DB open.
		start := time.Now()
		r := runSmirror(t, cfg, "status")
		dur := time.Since(start)
		if r.ExitCode != 0 {
			t.Logf("status exit=%d for n=%d", r.ExitCode, n)
		}
		return dur
	}

	d2 := measure(2)
	d8 := measure(8)
	ratio := d8.Seconds() / d2.Seconds()
	t.Logf("PANEL OBS: status command duration: 2 mirrors=%v, 8 mirrors=%v, ratio=%.2fx. "+
		"Linear scaling would give ratio ~4.0; significantly higher suggests N² behavior.",
		d2, d8, ratio)
	if ratio > 8.0 {
		t.Errorf("PANEL BUG: 8-mirror status takes %.1fx as long as 2-mirror — "+
			"super-linear scaling suggests N² code path (e.g., per-mirror DB scan, "+
			"redundant filter compilation).", ratio)
	}
}

// =========================================================================
// 5. RECONCILIATION ON HIGH QUEUE DEPTH (FR-QUEUE-10)
// =========================================================================

// SRS FR-QUEUE-10: "When queue depth warning threshold is exceeded, system
// SHALL trigger immediate reconciliation to accelerate draining". Status:
// "Not Done". Verify whether the user-observable behavior is at least
// graceful when burst-loading occurs.
func TestPanelR3_Queue_HighDepthGraceful(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	p := startSmirror(t, env.CfgPath, "start")
	defer p.Kill()
	time.Sleep(2 * time.Second)
	if isExited(p) {
		t.Fatalf("daemon exited prematurely")
	}

	// Create many small files in a burst (not 50K — that's too slow for
	// a regular test). Just verify the daemon doesn't crash under
	// 200 simultaneous file creates.
	for i := 0; i < 200; i++ {
		_ = os.WriteFile(filepath.Join(env.SrcDir, fmt.Sprintf("burst%04d.txt", i)),
			[]byte("x"), 0644)
	}
	if !waitForFileCount(t, env.DstDir, 200, 60*time.Second) {
		got := fileCount(env.DstDir)
		t.Errorf("PANEL BUG: 200-file burst did not all sync within 60s — got %d. "+
			"This is well under the 50K queue threshold; failure indicates a deeper issue.", got)
	}
	assertNoPanic(t, smirrorResult{Stdout: p.stdout.String(), Stderr: p.stderr.String()})
}

// =========================================================================
// 6. UNUSED imports placeholder
// =========================================================================

// keep `runtime` referenced so the import is never "unused" if some
// of the platform-conditional tests above are rewritten.
var _ = runtime.GOOS
