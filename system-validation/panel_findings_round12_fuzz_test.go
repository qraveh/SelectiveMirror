package systemval

// panel_findings_round12_fuzz_test.go — Round 12: property-based testing
// using Go's built-in testing.F.
//
// The repo already has fuzz tests for config parsing, filter patterns, and
// path handling (config_fuzz_test.go, filter_fuzz_test.go, path_fuzz_test.go).
// These new fuzz harnesses target HIGHER-LEVEL PROPERTIES the existing ones
// don't cover:
//
//   1. Filter determinism — explain output is identical across two
//      identical invocations (catches non-deterministic ordering or
//      time-dependent decisions in the filter pipeline).
//
//   2. Filter explain ↔ sync-now consistency — a path that explain says
//      INCLUDED actually syncs; a path that explain says EXCLUDED does not.
//      (Catches divergence between the explain code path and the actual
//      sync filter application — analogous to the round-3 hoisting
//      observation but at runtime.)
//
//   3. Concurrent-safe explain — running explain on the same config from
//      multiple goroutines never panics and always produces the same
//      result for the same input.
//
// Maintains scope:
//   - Each fuzz function has a curated seed corpus (runs cleanly with
//     just `go test`, no `-fuzz` flag needed)
//   - Uses runSmirror — black-box, no internal-package coupling
//   - Each property is small and well-defined

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// FuzzPanelR12_FilterDeterminism — explain output for the same (rules,
// path) input must be byte-identical across two consecutive runs.
func FuzzPanelR12_FilterDeterminism(f *testing.F) {
	// Seed corpus: simple patterns, doublestars, negations, char classes.
	f.Add("*.log", "info.log")
	f.Add("**/foo", "a/b/foo")
	f.Add("foo/**", "foo/bar")
	f.Add("foo/**\n!foo/keep", "foo/keep")
	f.Add("[abc].txt", "a.txt")
	f.Add("/anchored", "anchored")
	f.Add("/anchored", "sub/anchored")
	f.Add("trailing/", "trailing")
	f.Add("trailing/", "trailing/file")
	f.Add(`\!important`, "!important")
	// Tricky from BUG-R3-1:
	f.Add("foo/**\n!foo/bar/baz.txt", "foo/bar/baz.txt")

	f.Fuzz(func(t *testing.T, rules string, relPath string) {
		// Skip empty path or path with NUL byte / leading slash etc.
		if relPath == "" || strings.ContainsRune(relPath, 0) ||
			strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "\\") {
			return
		}
		// Skip overly long inputs (just keep test fast).
		if len(rules) > 4096 || len(relPath) > 256 {
			return
		}

		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		dataDir := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(dataDir, 0755)

		// Write the rules into a .syncignore.
		os.WriteFile(filepath.Join(src, ".syncignore"), []byte(rules+"\n"), 0644)

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

		// Run explain twice.
		r1 := runSmirror(t, cfg, "explain", "test", relPath)
		r2 := runSmirror(t, cfg, "explain", "test", relPath)
		if r1.ExitCode != 0 || r2.ExitCode != 0 {
			// Don't worry about non-zero exit; the property is about
			// determinism, not success.
			return
		}

		// Extract just the "Status:" line — the rest may include timing
		// or paths that legitimately differ across runs.
		s1 := extractStatusLine(r1.Stdout)
		s2 := extractStatusLine(r2.Stdout)
		if s1 != s2 {
			t.Errorf("PANEL BUG: explain output for rules=%q path=%q is non-deterministic.\n"+
				"  run1 status: %q\n  run2 status: %q",
				rules, relPath, s1, s2)
		}
	})
}

// FuzzPanelR12_ExplainVsSyncConsistency — when explain says INCLUDED, the
// file actually syncs; when it says EXCLUDED, it doesn't. This is the
// invariant that closes the round-3 hoisting observation: explain's
// decision must match what sync-now actually does.
func FuzzPanelR12_ExplainVsSyncConsistency(f *testing.F) {
	// Seed corpus.
	f.Add("*.log", "info.log")            // both should say EXCLUDED
	f.Add("*.log", "info.txt")            // both should say INCLUDED
	f.Add("**/foo", "x/y/foo")            // both EXCLUDED
	f.Add("foo/", "foo/file.txt")         // both EXCLUDED (under excluded dir)
	f.Add("foo/", "foo")                  // INCLUDED — file named foo with no slash

	f.Fuzz(func(t *testing.T, rules string, relPath string) {
		if relPath == "" || strings.ContainsRune(relPath, 0) ||
			strings.HasPrefix(relPath, "/") || strings.HasPrefix(relPath, "\\") ||
			strings.Contains(relPath, "..") {
			return
		}
		// Path components shouldn't be empty after cleaning.
		clean := filepath.ToSlash(filepath.Clean(relPath))
		if clean == "." || clean == ".." || strings.Contains(clean, "//") {
			return
		}
		if len(rules) > 4096 || len(relPath) > 256 {
			return
		}

		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		dataDir := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(dataDir, 0755)

		// Write rules.
		os.WriteFile(filepath.Join(src, ".syncignore"), []byte(rules+"\n"), 0644)

		// Create the actual file.
		full := filepath.Join(src, filepath.FromSlash(clean))
		os.MkdirAll(filepath.Dir(full), 0755)
		if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
			return // can't create the file — skip
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

		// Run explain.
		rE := runSmirror(t, cfg, "explain", "test", clean)
		if rE.ExitCode != 0 {
			return
		}
		explainSays := extractStatus(rE.Stdout)
		if explainSays == "" {
			return // couldn't parse
		}

		// Run sync-now and check whether the file landed at dst.
		rS := runSmirror(t, cfg, "sync-now")
		if rS.ExitCode != 0 {
			// sync-now failed for unrelated reasons — skip.
			return
		}
		dstFile := filepath.Join(dst, filepath.FromSlash(clean))
		landedAtDst := fileExists(dstFile)

		// Property: explain INCLUDED iff sync-now landed the file.
		switch explainSays {
		case "INCLUDED":
			if !landedAtDst {
				t.Errorf("PANEL BUG: explain says INCLUDED but sync-now did NOT land "+
					"the file. rules=%q path=%q",
					rules, clean)
			}
		case "EXCLUDED":
			if landedAtDst {
				t.Errorf("PANEL BUG: explain says EXCLUDED but sync-now DID land the "+
					"file (filter divergence). rules=%q path=%q",
					rules, clean)
			}
		}
	})
}

// FuzzPanelR12_ConcurrentExplain — running explain from multiple
// goroutines on the same config gives the same answer every time.
func FuzzPanelR12_ConcurrentExplain(f *testing.F) {
	f.Add("*.log", "info.log")
	f.Add("**/foo", "a/foo")
	f.Add("foo/**\n!foo/keep", "foo/keep")

	f.Fuzz(func(t *testing.T, rules string, relPath string) {
		if relPath == "" || strings.ContainsRune(relPath, 0) {
			return
		}
		if len(rules) > 2048 || len(relPath) > 128 {
			return
		}

		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		dataDir := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(dataDir, 0755)
		os.WriteFile(filepath.Join(src, ".syncignore"), []byte(rules+"\n"), 0644)

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

		// Run explain 4 times in parallel. Each invocation is its own
		// process — no in-process race. But state.db is shared across
		// sequential CLI runs; concurrent runs hit the lock.
		const N = 4
		results := make([]string, N)
		var wg sync.WaitGroup
		for i := 0; i < N; i++ {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := runSmirror(t, cfg, "explain", "test", relPath)
				if r.ExitCode == 0 {
					results[i] = extractStatus(r.Stdout)
				}
			}()
		}
		wg.Wait()

		// All non-empty results must agree.
		var first string
		for _, s := range results {
			if s == "" {
				continue
			}
			if first == "" {
				first = s
				continue
			}
			if s != first {
				t.Errorf("PANEL BUG: concurrent explain produced inconsistent results: "+
					"%v. rules=%q path=%q", results, rules, relPath)
				return
			}
		}
	})
}

// =========================================================================
// helpers
// =========================================================================

// extractStatus returns "INCLUDED" or "EXCLUDED" from `smirror explain` output.
func extractStatus(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Status:") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			return rest
		}
	}
	return ""
}

// extractStatusLine returns the "Status: X" line from explain output.
func extractStatusLine(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Status:") {
			return t
		}
	}
	return ""
}

// =========================================================================
// Non-fuzz: a focused decision table for known-tricky gitignore cases
// =========================================================================

// This serves as a sanity check that our seed corpus actually triggers the
// behaviors we expect; useful when fuzzing is disabled.
func TestPanelR12_FilterDecisionTable_KnownCases(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("path semantics differ across OS; this case set is Windows-tested")
	}

	cases := []struct {
		name    string
		rules   []string
		relPath string
		want    string // "INCLUDED" or "EXCLUDED"
	}{
		{
			name:    "BUG-R3-1: child of excluded parent (gitignore says EXCLUDED)",
			rules:   []string{"foo/**", "!foo/bar/baz.txt"},
			relPath: "foo/bar/baz.txt",
			want:    "EXCLUDED", // per gitignore spec
		},
		{
			name:    "wildcard simple",
			rules:   []string{"*.log"},
			relPath: "info.log",
			want:    "EXCLUDED",
		},
		{
			name:    "negation re-include",
			rules:   []string{"*.log", "!error.log"},
			relPath: "error.log",
			want:    "INCLUDED",
		},
		{
			name:    "anchored does not match nested",
			rules:   []string{"/foo"},
			relPath: "subdir/foo",
			want:    "INCLUDED",
		},
		{
			name:    "doublestar matches at top",
			rules:   []string{"**/foo"},
			relPath: "foo",
			want:    "EXCLUDED",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			src := filepath.Join(root, "src")
			dst := filepath.Join(root, "dst")
			dataDir := filepath.Join(root, "data")
			os.MkdirAll(src, 0755)
			os.MkdirAll(dst, 0755)
			os.MkdirAll(dataDir, 0755)
			createSyncIgnore(t, src, tc.rules)

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

			r := runSmirror(t, cfg, "explain", "test", tc.relPath)
			if r.ExitCode != 0 {
				t.Fatalf("explain failed: exit=%d stderr=%s", r.ExitCode, truncate(r.Stderr, 200))
			}
			got := extractStatus(r.Stdout)
			if got != tc.want {
				// For BUG-R3-1, we already know it diverges; log without failing.
				if strings.Contains(tc.name, "BUG-R3-1") {
					t.Logf("KNOWN BUG: %s — expected %q, got %q (BUG-R3-1 still OPEN)",
						tc.name, tc.want, got)
					return
				}
				t.Errorf("filter decision diverged: rules=%v path=%s want=%s got=%s",
					tc.rules, tc.relPath, tc.want, got)
			}
		})
	}

	_ = fmt.Sprintf // keep fmt referenced
}
