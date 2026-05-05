// CLI + artifact tests for the telemetry v2 architecture.
//
// Origin: System Validation review,
// 2026-04-30. Maps to claims in system-validation/CLAIMS-MAP.md:
//
//   C-15 — bug-report narratives stay on GitHub (no quoted issue
//          text in published artifacts: CHANGELOG, README, weekly
//          digests, telemetry-architecture-v2.md, etc.)
//   C-17 — `smirror telemetry forget` rejected with v2 migration
//          message (no network call attempted)
//   C-18 — `smirror version` prints the build-key fingerprint line
//          (already shipped; this test ratifies it under v2's claim
//          IDs)
//
// All three tests are static / black-box against the built binary;
// none require live infrastructure. Safe to run in CI.

package systemval

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// C-15 — published artifacts contain no quoted bug-report narratives
// ---------------------------------------------------------------------------
//
// Rule from PRIVACY.md "What is no longer telemetry": the maintainer
// never quotes user-submitted GitHub Issue text in changelogs,
// digests, or other artifacts the project publishes. Rationale: any
// copy makes the maintainer a controller for that copy, defeating
// the v2 architecture's "no copies, not a controller" stance.
//
// This test catches accidents — it does not catch deliberate quoting
// (a hostile maintainer could code-launder around a grep). The intent
// is "if you typed the user's exact words into CHANGELOG by mistake,
// CI catches it."

func TestTelemetryV2Artifacts_NoQuotedNarrativeFragments(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_artifacts_no_narrative_quotes")

	// A list of phrase fragments that are PLAUSIBLE in a user's
	// natural-language bug report and HIGHLY UNLIKELY in legitimate
	// project documentation. If any of these appear in a published
	// artifact, it almost certainly came from a copy-paste of an
	// issue body.
	//
	// We err on the side of conservative — fragments that are
	// distinctively first-person bug-report voice. Generic developer
	// prose ("goroutine spawn", panel-quotation blockquotes, etc.)
	// has been deliberately excluded because it produces false
	// positives on legitimate technical writing. The list is meant
	// to grow as the maintainer notices patterns; this is a
	// forcing-function checklist, not a completeness claim.
	suspiciousFragments := []string{
		// Distinctive first-person bug-report voice
		"i tried to install",
		"i was trying to",
		"when i ran smirror",
		"in my case smirror",
		"on my machine smirror",
		"my error message was",
		"please help me ",
		// Direct cut-and-paste give-aways
		"reproduce: i ",
		"steps i tried:",
		"hi maintainer ",
		"hi maintainers",
		"thanks for your project",
		// Forms that are almost-always issue-template artifacts
		"## what happened\ni ",
		"## expected behavior\ni ",
	}

	// Files that are part of the project's published surface and
	// MUST NOT contain quoted narrative.
	publishedFiles := []string{
		"CHANGELOG.md",
		"README.md",
		filepath.Join("docs", "PRIVACY.md"),
		filepath.Join("docs", "telemetry-architecture-v2.md"),
		filepath.Join("docs", "cli-telemetry-command.md"),
		filepath.Join("docs", "operations", "telemetry-ops.md"),
		filepath.Join("docs", "operations", "deploy-telemetry-v2.md"),
	}

	// Plus: every weekly digest under docs/telemetry/.
	digestDir := filepath.Join(repoRoot, "docs", "telemetry")
	if entries, err := os.ReadDir(digestDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			publishedFiles = append(publishedFiles, filepath.Join("docs", "telemetry", e.Name()))
		}
	}

	for _, rel := range publishedFiles {
		full := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			// Missing file is a different problem — don't fail the
			// narrative test for it. (CHANGELOG.md may not exist
			// pre-1.0 in some forks, e.g.)
			continue
		}
		lc := strings.ToLower(string(data))
		for _, frag := range suspiciousFragments {
			if strings.Contains(lc, frag) {
				t.Errorf("published artifact %s contains suspicious narrative fragment %q — bug-report content must stay on GitHub Issues, not be quoted into project docs (CLAIMS-MAP C-15). If this is a genuine project-doc usage, rephrase to avoid the fragment.", rel, frag)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// C-17 — `smirror telemetry forget` rejected with v2 migration message
// ---------------------------------------------------------------------------

func TestTelemetryV2CLI_ForgetSubcommandRejected(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_cli_forget_rejected")

	env := newTestEnv(t)
	r := runSmirror(t, env.CfgPath, "telemetry", "forget")

	assertNoPanic(t, r)
	if r.ExitCode == 0 {
		t.Errorf("smirror telemetry forget should exit non-zero under v2 (CLAIMS-MAP C-17)\nstdout: %s\nstderr: %s", truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}

	combined := strings.ToLower(r.Stdout + r.Stderr)
	// The message must point the user at the v2 model and at policy /
	// telemetry none. Different phrasings are OK; we just want the
	// concepts present.
	if !strings.Contains(combined, "stream-aggregate-and-discard") &&
		!strings.Contains(combined, "no per-install server") {
		t.Errorf("forget rejection should explain the v2 architecture; got:\n%s%s",
			truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}
	if !strings.Contains(combined, "telemetry none") {
		t.Errorf("forget rejection should point at `smirror telemetry none` as the opt-out path; got:\n%s%s",
			truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}
}

// ---------------------------------------------------------------------------
// C-18 — `smirror version` prints the build-key fingerprint line
// ---------------------------------------------------------------------------
//
// SM-168 is shipped (verified at 0.9.82-dev). This test ratifies the
// claim under CLAIMS-MAP's C-18 ID so the map can be marked GREEN
// against an explicitly named test rather than the older
// `TestTelemetryVersionReportsBuildKeyFingerprint` which lived in
// telemetry_test.go without the claim mapping.

func TestTelemetryV2CLI_VersionReportsBuildKeyFingerprint(t *testing.T) {
	t.Parallel()
	// Reuse existing coverage ID; this test exists for CLAIMS-MAP
	// traceability and to keep the SM-168 ratification adjacent to
	// the v2 schema-claims tests.
	coverage.Record("telemetry_build_key_diag")

	r := runSmirrorRaw(t, "version")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)

	if !strings.Contains(r.Stdout, "telemetry build-key:") {
		t.Errorf("smirror version should print 'telemetry build-key:' line (CLAIMS-MAP C-18)\nstdout:\n%s", truncate(r.Stdout, 600))
	}
}

// ---------------------------------------------------------------------------
// Bonus: `smirror telemetry inspect` produces structured JSON
// ---------------------------------------------------------------------------
//
// the FAE-role diagnostic for "I need to see we collect everything."
// Black-box ratification of the inspect subcommand at the binary
// level (the Go-level unit tests already cover the field-by-field
// presence check; this just confirms the plumbing through the CLI
// dispatcher works).

func TestTelemetryV2CLI_InspectProducesJSONForFirstSeen(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_cli_inspect_works")

	env := newTestEnv(t)
	r := runSmirror(t, env.CfgPath, "telemetry", "inspect", "first_seen")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)

	// Inspect prints the JSON to stdout. We don't unmarshal here;
	// just confirm the structural markers + a few mandatory fields
	// are present. The Go-level unit test covers full parsing.
	if !strings.Contains(r.Stdout, `"event_kind"`) {
		t.Errorf("inspect output should include event_kind field; stdout:\n%s", truncate(r.Stdout, 600))
	}
	if !strings.Contains(r.Stdout, `"first_seen"`) {
		t.Errorf("inspect output for first_seen should reference the kind by name; stdout:\n%s", truncate(r.Stdout, 600))
	}
	for _, mandatoryField := range []string{`"client_version"`, `"mirror_count_bucket"`, `"delete_policy"`, `"has_hooks"`} {
		if !strings.Contains(r.Stdout, mandatoryField) {
			t.Errorf("inspect output is missing mandatory PRIVACY.md field %s\nstdout:\n%s", mandatoryField, truncate(r.Stdout, 600))
		}
	}
}

// ---------------------------------------------------------------------------
// Bonus: ratify that printUsage lists the new telemetry command
// ---------------------------------------------------------------------------

func TestTelemetryV2CLI_HelpListsTelemetryCommand(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "--help")
	combined := r.Stdout + r.Stderr
	if !strings.Contains(combined, "telemetry") {
		t.Errorf("smirror --help should list the telemetry command; output:\n%s", truncate(combined, 800))
	}
	// Best-effort: top-level help should mention what 'telemetry' does.
	if !bytes.Contains([]byte(combined), []byte("tier")) {
		t.Errorf("smirror --help should briefly describe what telemetry does (mention 'tier'); output:\n%s", truncate(combined, 800))
	}
}
