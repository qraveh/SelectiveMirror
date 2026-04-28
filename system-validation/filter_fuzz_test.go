package systemval

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFuzz_FilterPatterns generates random gitignore patterns and file paths,
// writes them as .syncignore rules, and verifies via `smirror explain` that:
//  1. No panic/crash occurs
//  2. Same input always gives same answer (deterministic)
//  3. All 6 pattern classes are exercised
func TestFuzz_FilterPatterns(t *testing.T) {
	t.Parallel()

	// Pattern generators by class.
	type patternGen struct {
		classID string
		gen     func(rng *rand.Rand) string
	}

	generators := []patternGen{
		{"filter_wildcard", func(rng *rand.Rand) string {
			exts := []string{".txt", ".log", ".go", ".py", ".js", ".md", ".yaml", ".json"}
			return "*" + exts[rng.Intn(len(exts))]
		}},
		{"filter_negation", func(rng *rand.Rand) string {
			names := []string{"important.log", "keep.txt", "README.md", "main.go"}
			return "!" + names[rng.Intn(len(names))]
		}},
		{"filter_directory", func(rng *rand.Rand) string {
			dirs := []string{"build", "dist", "node_modules", "vendor", ".cache", "tmp", "__pycache__"}
			return dirs[rng.Intn(len(dirs))] + "/"
		}},
		{"filter_doublestar", func(rng *rand.Rand) string {
			patterns := []string{"**/*.pyc", "**/test_*.py", "src/**/generated", "**/.DS_Store",
				"**/*.min.js", "**/node_modules/**", "docs/**/*.draft"}
			return patterns[rng.Intn(len(patterns))]
		}},
		{"filter_anchored", func(rng *rand.Rand) string {
			files := []string{"/config.yaml", "/Makefile", "/.env", "/go.sum", "/LICENSE"}
			return files[rng.Intn(len(files))]
		}},
		{"filter_charclass", func(rng *rand.Rand) string {
			patterns := []string{"[0-9]*.dat", "[a-z]*.tmp", "test[0-9].go", "log[0-9][0-9].txt",
				"[A-Z]*.bak"}
			return patterns[rng.Intn(len(patterns))]
		}},
	}

	// Random file path generator.
	randomPath := func(rng *rand.Rand) string {
		segments := rng.Intn(4) + 1
		parts := make([]string, segments)
		names := []string{"src", "build", "test", "docs", "lib", "vendor", "pkg", "cmd",
			"internal", "config", "data", "tmp", "cache", "output", "gen", "sub"}
		exts := []string{".txt", ".log", ".go", ".py", ".js", ".md", ".yaml", ".json",
			".pyc", ".dat", ".tmp", ".bak", ".min.js", ""}
		for i := 0; i < segments-1; i++ {
			parts[i] = names[rng.Intn(len(names))]
		}
		basename := names[rng.Intn(len(names))]
		// Sometimes add a number prefix.
		if rng.Intn(3) == 0 {
			basename = fmt.Sprintf("%d%s", rng.Intn(100), basename)
		}
		ext := exts[rng.Intn(len(exts))]
		parts[segments-1] = basename + ext
		return strings.Join(parts, "/")
	}

	rng := rand.New(rand.NewSource(42)) // Deterministic seed.

	// Track which pattern classes we've exercised.
	classHit := map[string]int{}

	iterations := 200
	for i := 0; i < iterations; i++ {
		// Pick a random pattern class.
		gen := generators[rng.Intn(len(generators))]
		pattern := gen.gen(rng)
		path := randomPath(rng)

		t.Run(fmt.Sprintf("iter%d_%s", i, gen.classID), func(t *testing.T) {
			// Create a temp env with the pattern as .syncignore.
			root := t.TempDir()
			src := filepath.Join(root, "src")
			dst := filepath.Join(root, "dst")
			data := filepath.Join(root, "data")
			os.MkdirAll(src, 0755)
			os.MkdirAll(dst, 0755)
			os.MkdirAll(data, 0755)
			noN := boolPtr(false)

			cfg := createConfig(t, root, configOpts{
				Mirrors:    []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
				StateDB:    filepath.Join(data, "state.db"),
				LogFile:    filepath.Join(data, "s.log"),
				SyncWorkers: 1,
				NotifyEnabled:  noN,
				AnomalyEnabled: noN,
				VerifyIntervalSec: -1,
			})
			createSyncIgnore(t, src, []string{pattern})

			// Run explain.
			r := runSmirror(t, cfg, "explain", "m", path)
			assertNoPanic(t, r)

			// Determinism: run again, should get same result.
			r2 := runSmirror(t, cfg, "explain", "m", path)
			if r.Stdout != r2.Stdout || r.ExitCode != r2.ExitCode {
				t.Errorf("non-deterministic: pattern=%q path=%q\nfirst=%q\nsecond=%q",
					pattern, path, r.Stdout, r2.Stdout)
			}

			coverage.Record(gen.classID)
			classHit[gen.classID]++
		})
	}

	// Verify all classes were hit.
	for _, gen := range generators {
		if classHit[gen.classID] == 0 {
			t.Errorf("pattern class %q was never exercised", gen.classID)
		}
	}
}

// TestFuzz_FilterCombinations tests multiple patterns combined in a single
// .syncignore file.
func TestFuzz_FilterCombinations(t *testing.T) {
	t.Parallel()

	combos := []struct {
		name    string
		rules   []string
		include []string
		exclude []string
	}{
		{
			name:    "wildcard+negation",
			rules:   []string{"*.log", "!error.log"},
			include: []string{"error.log", "main.go"},
			exclude: []string{"debug.log", "access.log"},
		},
		{
			name:    "directory+wildcard",
			rules:   []string{"build/", "*.tmp"},
			include: []string{"src/main.go"},
			exclude: []string{"build/output.exe", "scratch.tmp"},
		},
		{
			name:    "doublestar+negation",
			rules:   []string{"**/*.pyc", "!tests/**/*.pyc"},
			include: []string{"tests/unit/cache.pyc", "src/main.py"},
			exclude: []string{"src/module.pyc"},
		},
		{
			name:    "anchored+wildcard",
			rules:   []string{"/config.yaml", "*.bak"},
			include: []string{"sub/config.yaml", "main.go"},
			exclude: []string{"config.yaml", "old.bak"},
		},
		{
			name:    "complex_mix",
			rules:   []string{".git/", "node_modules/", "*.log", "*.tmp", "!important.log", "/README.md"},
			include: []string{"src/app.js", "important.log"},
			exclude: []string{".git/config", "node_modules/pkg/index.js", "debug.log", "README.md"},
		},
	}

	for _, tc := range combos {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t)
			createSyncIgnore(t, env.SrcDir, tc.rules)

			for _, path := range tc.include {
				r := runSmirror(t, env.CfgPath, "explain", "mirror0", path)
				assertExitCode(t, r, 0)
				assertNoPanic(t, r)
				if strings.Contains(r.Stdout, "EXCLUDE") {
					t.Errorf("expected INCLUDE for %q with rules %v, got EXCLUDE", path, tc.rules)
				}
			}
			for _, path := range tc.exclude {
				r := runSmirror(t, env.CfgPath, "explain", "mirror0", path)
				assertExitCode(t, r, 0)
				assertNoPanic(t, r)
				if strings.Contains(r.Stdout, "INCLUDE") && !strings.Contains(r.Stdout, "EXCLUDE") {
					t.Errorf("expected EXCLUDE for %q with rules %v, got INCLUDE", path, tc.rules)
				}
			}
		})
	}
}

// TestFuzz_FilterEdgeCasePatterns tests patterns that are syntactically unusual.
func TestFuzz_FilterEdgeCasePatterns(t *testing.T) {
	t.Parallel()

	edgePatterns := []string{
		"",                  // empty line (should be ignored)
		"#",                 // comment only
		"# comment",         // comment with text
		"\\#not-a-comment",  // escaped hash (literal)
		"\\!not-a-negation", // escaped bang (literal)
		"*",                 // match everything
		"**",                // double star alone
		"***",               // triple star
		"*.*.bak",           // double extension pattern
		".[a-z]*",           // dotfile with char class
		"a b c.txt",         // spaces in pattern
		"path/to/specific.txt", // exact path
	}

	env := newTestEnv(t)
	for i, pat := range edgePatterns {
		t.Run(fmt.Sprintf("edge_%d", i), func(t *testing.T) {
			// Create a fresh syncignore for each pattern.
			createSyncIgnore(t, env.SrcDir, []string{pat})
			r := runSmirror(t, env.CfgPath, "explain", "mirror0", "test.txt")
			assertNoPanic(t, r)
		})
	}
}
