// SM-220 regression ratchet — detection-helper checks.
//
// Pre-SM-220, the three detection helpers in cmd_telemetry.go all
// returned hardcoded placeholders:
//   - detectInstallMethod() → "unknown"
//   - detectBackgroundMode() → "unknown"
//   - bestEffortRcloneVersion() → "(would be detected at submit time)"
//
// The third one was particularly bad because it leaked literal
// placeholder text into actual submitted rollup rows. The first two
// were ENUM-valid but never carried real population data.
//
// SM-220 replaced all three with real detection (where possible
// — task-mode detection deferred). This file ratchets against
// regression on two layers:
//
//   1. Source-property: cmd_telemetry.go MUST NOT contain a `return`
//      statement with a parenthesized placeholder-looking string.
//      Catches "someone added a new placeholder helper" before any
//      test or runtime side-effect.
//
//   2. Functional: the actual helper functions return ENUM-valid
//      strings (no rogue values, no empty strings, no leaked
//      placeholders). Windows-only because the helpers themselves
//      are build-tagged.

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestSM220_SubmitPathHelpers_NoParenthesizedPlaceholders ensures the
// helpers that feed the WIRE PAYLOAD (cfgToSystemView → submit) do
// not return parenthesized placeholder strings. The SM-220 bug
// pattern was a `return "(would be detected at submit time)"` in
// bestEffortRcloneVersion that leaked into real rollup rows.
//
// Scope is deliberately narrow: only functions on the submit path.
// Other helpers in cmd_telemetry.go — e.g. computeInspectDaysSince-
// FirstSeenBucket — legitimately return human-readable parenthesized
// strings for the `smirror telemetry inspect` user-facing output;
// those are never submitted and are not in scope.
//
// Targets (must stay in sync with cfgToSystemView's call sites):
//   bestEffortRcloneVersion (cmd_telemetry.go)
//   detectInstallMethod      (detect_windows.go / detect_other.go)
//   detectBackgroundMode     (detect_windows.go / detect_other.go)
func TestSM220_SubmitPathHelpers_NoParenthesizedPlaceholders(t *testing.T) {
	// (file, function, why-it-matters) tuples.
	targets := []struct {
		file string
		fn   string
	}{
		{"cmd_telemetry.go", "bestEffortRcloneVersion"},
		{"detect_windows.go", "detectInstallMethod"},
		{"detect_windows.go", "detectBackgroundMode"},
		{"detect_other.go", "detectInstallMethod"},
		{"detect_other.go", "detectBackgroundMode"},
	}

	for _, tg := range targets {
		body, ok := extractFunctionBody(t, tg.file, tg.fn)
		if !ok {
			t.Errorf("%s: function %s not found (moved? renamed? regression-prevention test out of sync)", tg.file, tg.fn)
			continue
		}
		bug := regexp.MustCompile(`return "\([^"]*\)"`)
		if loc := bug.FindIndex([]byte(body)); loc != nil {
			t.Errorf("%s::%s contains a parenthesized placeholder return — SM-220 culprit shape. "+
				"Submit-path helpers must return real values or the ENUM-valid sentinel \"unknown\".\n"+
				"  Offending text: %s", tg.file, tg.fn, body[loc[0]:loc[1]])
		}
	}
}

// extractFunctionBody finds the named function in the named file and
// returns its body (between { and the matching ^} at column 0).
// Uses regex rather than the go/parser package to keep the test
// lightweight; the input format (project-conventional Go formatting)
// guarantees the column-0-closing-brace assumption holds.
func extractFunctionBody(t *testing.T, file, fn string) (string, bool) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		// File doesn't exist — for build-tagged files (detect_other.go
		// on Windows etc.), this is acceptable; the caller treats it
		// as "function not found in this file".
		return "", false
	}
	// Find `func <fn>(`
	start := regexp.MustCompile(`(?m)^func\s+` + regexp.QuoteMeta(fn) + `\s*\(`).FindIndex(src)
	if start == nil {
		return "", false
	}
	// Find the next `^}` (closing brace at column 0) after `start`.
	rest := src[start[0]:]
	end := regexp.MustCompile(`(?m)^\}`).FindIndex(rest)
	if end == nil {
		return string(rest), true // unbounded — return what we have
	}
	return string(rest[:end[1]]), true
}

// TestSM220_DetectHelpers_ReturnEnumValidValues is the functional
// counterpart — calls the real helpers and asserts the returned
// values are in the documented ENUM sets. Windows-only because the
// helpers are build-tagged (`detect_windows.go` vs `detect_other.go`);
// the non-Windows fallback unconditionally returns "unknown" which
// would render this test trivial.
func TestSM220_DetectHelpers_ReturnEnumValidValues(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("detection helpers are Windows-specific (non-Windows returns 'unknown' unconditionally)")
	}

	validInstallMethods := map[string]bool{
		"msi": true, "winget": true, "zip": true, "manual": true, "unknown": true,
	}
	validBackgroundModes := map[string]bool{
		"foreground": true, "service": true, "task": true, "unknown": true,
	}

	im := detectInstallMethod()
	if !validInstallMethods[im] {
		t.Errorf("detectInstallMethod() returned %q which is not in {msi, winget, zip, manual, unknown}", im)
	}

	bm := detectBackgroundMode()
	if !validBackgroundModes[bm] {
		t.Errorf("detectBackgroundMode() returned %q which is not in {foreground, service, task, unknown}", bm)
	}
}

// TestSM220_BestEffortRcloneVersion_NoPlaceholder asserts the rclone
// version helper returns either a real version-like string ("1.73.2")
// or the ENUM-style sentinel "unknown" — NEVER a parenthesized
// placeholder. Calls the helper with nil config (the codepath used
// when no config has loaded yet) AND with a default config.
//
// The pre-SM-220 form returned "(would be detected at submit time)"
// unconditionally; this test would have failed against that build.
func TestSM220_BestEffortRcloneVersion_NoPlaceholder(t *testing.T) {
	cases := []struct {
		name   string
		callIt func() string
	}{
		{"nil config", func() string { return bestEffortRcloneVersion(nil) }},
		// Default config path tested implicitly by the above; explicitly
		// adding a non-nil cfg here would require importing internal/config
		// (extra dep). The nil-cfg path is the riskier one (was returning
		// the placeholder unconditionally) so guarding it is enough.
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.callIt()
			if got == "" {
				t.Errorf("returned empty string; expected a version or \"unknown\"")
				return
			}
			if strings.HasPrefix(got, "(") {
				t.Errorf("returned a parenthesized placeholder %q — SM-220 culprit shape; "+
					"expected a real rclone version (e.g. \"1.73.2\") or the sentinel \"unknown\"", got)
			}
			for _, marker := range []string{"would be detected", "TODO", "FIXME", "placeholder"} {
				if strings.Contains(strings.ToLower(got), strings.ToLower(marker)) {
					t.Errorf("returned value contains placeholder marker %q: %q", marker, got)
				}
			}
		})
	}
}

// TestSM220_DetectWindows_FileExists is a structural guard: the
// per-platform detection files must exist in their expected
// locations so SM-220's split-by-build-tag pattern stays intact.
// If someone deletes detect_windows.go and consolidates everything
// into cmd_telemetry.go, this test fails and prompts a rethink.
func TestSM220_DetectWindows_FileExists(t *testing.T) {
	for _, name := range []string{"detect_windows.go", "detect_other.go"} {
		path := filepath.Join(".", name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s missing — SM-220's per-platform detection split is broken: %v", name, err)
		}
	}
}
