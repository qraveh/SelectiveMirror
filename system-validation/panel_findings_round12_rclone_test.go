package systemval

// panel_findings_round12_rclone_test.go — Round 12: multi-rclone-version
// compatibility matrix.
//
// SelectiveMirror's pinned minimum is rclone v1.73+. Older versions get
// "partial" (1.50–1.72) or "incompatible" (<1.50) classification per
// internal/rclone/detect.go::CompatCheck. The maintainer's CI tests with
// one rclone version. A future rclone release that changes argument
// syntax or output format would silently break smirror in production.
//
// This round uses **fake rclone wrapper scripts** (Windows .bat) that
// print a configurable version. We point smirror's rclone_path at each
// wrapper and verify smirror's classification + behavior.
//
// Maintains scope:
//   - Only tests version detection / classification (the surface most
//     likely to break across rclone versions)
//   - Doesn't try to fake actual sync behavior (would require a complete
//     rclone re-implementation)
//   - Uses Windows .bat for the wrappers (project's primary platform)

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRcloneBat creates a Windows .bat wrapper that prints a configurable
// rclone version output and accepts any arguments. Returns the path to the
// .bat file. The wrapper:
//   - For "version" command: prints rclone version output with the given
//     version string
//   - For other commands: exits 0 silently (just enough for smirror's
//     test-mirrors / sync-now to think rclone responded)
//
// Note: Go's exec.Command on Windows can invoke .bat files directly only
// if they end in .bat or .cmd; we use .bat so resolve() in detect.go
// accepts the path.
func fakeRcloneBat(t *testing.T, dir, versionLine, osLine string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("fake rclone wrapper uses .bat (Windows-specific)")
	}
	path := filepath.Join(dir, "rclone.bat")
	body := fmt.Sprintf(`@echo off
if "%%1"=="version" (
  echo %s
  echo - os/version: Fake test environment
  echo - os/kernel: %s
  echo - os/type: windows
  echo - os/arch: amd64
  echo - go/version: go1.26.2
  exit /b 0
)
rem any other command — exit 0 silently
exit /b 0
`, versionLine, osLine)
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeCfgWithFakeRclone creates a smirror config pointing at a fake rclone.
func makeCfgWithFakeRclone(t *testing.T, root, rclonePath string) string {
	t.Helper()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)
	return createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		RclonePath:        rclonePath,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})
}

// =========================================================================
// Pinned-minimum (v1.73) — smirror should accept with "full compatibility"
// =========================================================================

func TestPanelR12_Rclone_PinnedMinimum_v1_73_0(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only fake rclone")
	}
	root := t.TempDir()
	rclone := fakeRcloneBat(t, root, "rclone v1.73.0", "windows (amd64)")
	cfg := makeCfgWithFakeRclone(t, root, rclone)

	r := runSmirror(t, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasFullCompat := strings.Contains(combined, "full compatibility") ||
		strings.Contains(combined, "1.73")
	if !hasFullCompat {
		t.Logf("PANEL OBS: rclone v1.73.0 (the pinned minimum) is not classified as 'full' "+
			"in test-mirrors output. exit=%d output=%s",
			r.ExitCode, truncate(r.Stdout+r.Stderr, 500))
	}
}

// =========================================================================
// Older partial-compat (v1.50 – v1.72) — should accept with "partial"
// =========================================================================

func TestPanelR12_Rclone_PartialCompat_v1_68(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only fake rclone")
	}
	root := t.TempDir()
	rclone := fakeRcloneBat(t, root, "rclone v1.68.2", "windows (amd64)")
	cfg := makeCfgWithFakeRclone(t, root, rclone)

	r := runSmirror(t, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasPartial := strings.Contains(combined, "partial") ||
		strings.Contains(combined, "skip-links unavailable") ||
		strings.Contains(combined, "1.68")
	if !hasPartial {
		t.Logf("PANEL OBS: rclone v1.68.2 (within partial-compat range) is not flagged as "+
			"'partial' or noted in test-mirrors output. "+
			"Per CompatCheck (detect.go:62), 1.50..1.72 should produce: "+
			"'rclone X.Y.Z — partial: --skip-links unavailable...'. "+
			"output: %s", truncate(r.Stdout+r.Stderr, 500))
	}
}

// =========================================================================
// Below minimum (<v1.50) — should refuse with "incompatible"
// =========================================================================

func TestPanelR12_Rclone_TooOld_v1_30(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only fake rclone")
	}
	root := t.TempDir()
	rclone := fakeRcloneBat(t, root, "rclone v1.30.0", "windows (amd64)")
	cfg := makeCfgWithFakeRclone(t, root, rclone)

	r := runSmirror(t, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasIncompatible := strings.Contains(combined, "incompatible") ||
		strings.Contains(combined, "missing critical") ||
		strings.Contains(combined, "requires 1.50")

	// Per CompatCheck, anything <1.50 should be classified incompatible.
	// test-mirrors should produce a clear error.
	if !hasIncompatible {
		t.Errorf("PANEL BUG: rclone v1.30.0 (below the documented 1.50 minimum) was not "+
			"flagged 'incompatible' in test-mirrors output. CompatCheck (detect.go:65) "+
			"explicitly says <1.50 is incompatible. exit=%d output=%s",
			r.ExitCode, truncate(r.Stdout+r.Stderr, 500))
	}
}

// =========================================================================
// Future-version handling (v2.0) — what does smirror do?
// =========================================================================

func TestPanelR12_Rclone_FutureMajor_v2_0(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only fake rclone")
	}
	root := t.TempDir()
	rclone := fakeRcloneBat(t, root, "rclone v2.0.0", "windows (amd64)")
	cfg := makeCfgWithFakeRclone(t, root, rclone)

	r := runSmirror(t, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasFullCompat := strings.Contains(combined, "full compatibility")
	hasNote := strings.Contains(combined, "2.0") ||
		strings.Contains(combined, "future") ||
		strings.Contains(combined, "untested")

	t.Logf("PANEL OBS: fake rclone v2.0.0 — smirror behavior:\n"+
		"  hasFullCompat=%v hasNote=%v\n"+
		"  Per CompatCheck (detect.go:57-66), v2.0 satisfies AtLeast(1,73,0) and is\n"+
		"  classified as 'full compatibility'. But rclone 2.x may have breaking\n"+
		"  argument syntax changes. R7 rclone reviewer #8 flagged this gap.\n"+
		"  output: %s",
		hasFullCompat, hasNote, truncate(r.Stdout+r.Stderr, 400))
}

// =========================================================================
// Garbage output — graceful failure
// =========================================================================

func TestPanelR12_Rclone_GarbageOutput(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only fake rclone")
	}
	root := t.TempDir()
	// Wrapper that prints garbage and exits 0.
	path := filepath.Join(root, "rclone.bat")
	body := `@echo off
echo this is not a version
echo nor is this
exit /b 0
`
	os.WriteFile(path, []byte(body), 0755)
	cfg := makeCfgWithFakeRclone(t, root, path)

	r := runSmirror(t, cfg, "test-mirrors")
	assertNoPanic(t, r)
	combined := strings.ToLower(r.Stdout + r.Stderr)

	// Per parseVersionOutput, no match → ver stays at zero value (0.0.0).
	// 0.0.0 < 1.50, so CompatCheck should classify as "incompatible".
	hasIncompat := strings.Contains(combined, "incompatible") ||
		strings.Contains(combined, "0.0.0") ||
		strings.Contains(combined, "could not parse") ||
		strings.Contains(combined, "version")

	if !hasIncompat {
		t.Logf("PANEL OBS: garbage rclone output produces silent zero-version. "+
			"CompatCheck classifies 0.0.0 as 'incompatible' (<1.50). User should see "+
			"that classification. output: %s", truncate(r.Stdout+r.Stderr, 400))
	}
}

// =========================================================================
// Missing rclone binary
// =========================================================================

func TestPanelR12_Rclone_MissingBinary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	// Point at a path that doesn't exist.
	bogus := filepath.Join(root, "does-not-exist", "rclone.bat")

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		RclonePath:        bogus,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "test-mirrors")
	// Per FR-CLI-07: exit 3 = rclone error.
	if r.ExitCode != 3 && r.ExitCode != 2 {
		t.Logf("PANEL OBS: missing rclone produces exit=%d (expected 3 = rclone error, "+
			"or 2 = config error). FR-CLI-07 commits to specific exit codes for scripting.",
			r.ExitCode)
	}
	combined := strings.ToLower(r.Stdout + r.Stderr)
	hasHelpfulMessage := strings.Contains(combined, "rclone not found") ||
		strings.Contains(combined, "does not exist") ||
		strings.Contains(combined, "cannot find") ||
		strings.Contains(combined, "rclone") && strings.Contains(combined, "path")
	if !hasHelpfulMessage {
		t.Logf("PANEL OBS: missing rclone error message lacks 'rclone not found' / "+
			"'does not exist' vocabulary. Per NFR-OP-01 errors should be actionable. "+
			"output: %s", truncate(r.Stdout+r.Stderr, 400))
	}
}
