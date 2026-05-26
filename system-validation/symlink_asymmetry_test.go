// SM Symlink-asymmetry Medium close-out.
//
// Background: pre-close-out, foreground mode followed symlinks while
// service mode rejected them. A symlink planted in a watched directory
// could exfiltrate arbitrary readable files to the configured remote.
// Service mode closed this in v0.9.x via SEC-H5; aligns
// foreground.
//
// This file ratchets the foreground-rejects-by-default contract at
// two layers:
//
//   1. Source-property: `cmd/smirror/main.go` MUST set
//      `syncEngine.RejectSymlinkedFiles = !cfg.AllowSymlinks` in the
//      foreground startup path (not just service mode).
//
//   2. Config-shape: `internal/config/config.go::Global` MUST have an
//      `AllowSymlinks bool` field with yaml tag `allow_symlinks`.
//      Default-zero of bool = false = reject (correct posture).
//
// The behavioral test (do-foreground-actually-reject-a-planted-symlink-
// at-runtime) is deferred to a v1.0.x test pass — it needs subprocess
// orchestration + Windows-only mklink setup and the structural tests
// above are sufficient to catch the regression class that prompted
// the Medium.

package systemval

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestSymlinkAsymmetry_ForegroundDefaultsToReject is the close-
// out ratchet for the v1.0.0 Open Medium. Asserts the source-property
// shape that aligns foreground-mode default with the service-mode
// default-reject.
func TestSymlinkAsymmetry_ForegroundDefaultsToReject(t *testing.T) {
	t.Parallel()

	// Locate cmd/smirror/main.go by climbing from the systemval dir.
	mainGoPath := findRepoFile(t, "cmd", "smirror", "main.go")
	src, err := os.ReadFile(mainGoPath)
	if err != nil {
		t.Fatalf("could not read %s: %v", mainGoPath, err)
	}

	// MUST contain: syncEngine.RejectSymlinkedFiles = !cfg.AllowSymlinks
	// (whitespace-tolerant). This is the line that aligns foreground
	// behavior with service-mode SEC-H5.
	wantFG := regexp.MustCompile(`syncEngine\.RejectSymlinkedFiles\s*=\s*!cfg\.AllowSymlinks`)
	if !wantFG.Match(src) {
		t.Errorf("cmd/smirror/main.go MUST set "+
			"`syncEngine.RejectSymlinkedFiles = !cfg.AllowSymlinks` "+
			"in the foreground startup path (the close-out of "+
			"the Symlink-asymmetry Medium). Without this, foreground "+
			"mode reopens the pre-close-out default-follow behavior "+
			"that asymmetrically diverged from service-mode's "+
			"SEC-H5 default-reject.")
	}

	// ALSO MUST contain the service-mode unconditional reject (pre-existing).
	// If this line gets removed, the asymmetry would resurface in the
	// other direction (service mode following symlinks).
	wantSvc := regexp.MustCompile(`syncEngine\.RejectSymlinkedFiles\s*=\s*true`)
	if !wantSvc.Match(src) {
		t.Errorf("cmd/smirror/main.go MUST also retain the service-mode " +
			"`syncEngine.RejectSymlinkedFiles = true` (unconditional, " +
			"per SEC-H5 / PF-A3). If this line is gone, the service-mode " +
			"hardening regressed independently.")
	}
}

// TestSymlinkAsymmetry_ConfigFieldExists asserts that the AllowSymlinks
// field is declared on Global with the correct yaml tag. Catches a
// regression where the cmd/smirror startup line references cfg.AllowSymlinks
// but the field is silently removed from the struct (which would
// compile-fail loudly, but this gives a cleaner regression message
// in CI).
func TestSymlinkAsymmetry_ConfigFieldExists(t *testing.T) {
	t.Parallel()

	configGoPath := findRepoFile(t, "internal", "config", "config.go")
	src, err := os.ReadFile(configGoPath)
	if err != nil {
		t.Fatalf("could not read %s: %v", configGoPath, err)
	}

	// Declaration must be present, yaml tag must be `allow_symlinks`.
	want := regexp.MustCompile(`AllowSymlinks\s+bool\s+` + "`" + `yaml:"allow_symlinks"` + "`")
	if !want.Match(src) {
		t.Errorf("internal/config/config.go::Global MUST declare " +
			"`AllowSymlinks bool `yaml:\"allow_symlinks\"`` for the " +
			"Symlink-asymmetry close-out to compile and to " +
			"accept the documented config-file key. Did this get " +
			"renamed or removed?")
	}
}

// TestSymlinkAsymmetry_ConfigExampleDocumented asserts that
// `config.example.yaml` mentions `allow_symlinks` so users discover
// the option when they consult the canonical example.
func TestSymlinkAsymmetry_ConfigExampleDocumented(t *testing.T) {
	t.Parallel()

	examplePath := findRepoFile(t, "config.example.yaml")
	src, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatalf("could not read %s: %v", examplePath, err)
	}
	if !regexp.MustCompile(`allow_symlinks`).Match(src) {
		t.Errorf("config.example.yaml must mention `allow_symlinks` " +
			"so users discover the option. The Medium close-out " +
			"committed to documenting the new key here.")
	}
}

// findRepoFile climbs from the test's cwd upward to find a file
// relative to repo root. Same pattern as
// internal/telemetry/hmac_derivation_parity_test.go.
func findRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(append([]string{dir}, parts...)...)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate %v from cwd %s (climbed 8 levels)", parts, cwd)
	return ""
}
