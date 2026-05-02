package main

import (
	"strings"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

func TestBuildBugReportPayload_OneShot(t *testing.T) {
	cls := telemetry.BugClassification{Kind: "rclone", Surface: "rclone", Severity: "error"}
	p := buildBugReportPayload(cls, "0.9.88-dev", telemetry.TierNone, true)

	if got := p["event_kind"]; got != "bug_report" {
		t.Errorf("event_kind = %v; want bug_report", got)
	}
	if got := p["bug_kind"]; got != "rclone" {
		t.Errorf("bug_kind = %v; want rclone", got)
	}
	if got := p["bug_surface"]; got != "rclone" {
		t.Errorf("bug_surface = %v; want rclone", got)
	}
	if got := p["severity_hint"]; got != "error" {
		t.Errorf("severity_hint = %v; want error", got)
	}
	if got := p["source"]; got != "report_bug" {
		t.Errorf("source = %v; want report_bug", got)
	}
	if got := p["submitted_tier"]; got != "one_shot" {
		t.Errorf("submitted_tier = %v; want one_shot (oneShot=true overrides tier)", got)
	}
	if got := p["client_version"]; got != "0.9.88-dev" {
		t.Errorf("client_version = %v; want 0.9.88-dev", got)
	}
	// reported_at is a current timestamp; just confirm it's present + non-empty.
	if r, _ := p["reported_at"].(string); r == "" {
		t.Errorf("reported_at empty; want ISO timestamp")
	}
}

func TestBuildBugReportPayload_StandardTier(t *testing.T) {
	cls := telemetry.BugClassification{Kind: "config", Surface: "config", Severity: "warning"}
	p := buildBugReportPayload(cls, "0.9.88-dev", telemetry.TierStandard, false)

	if got := p["submitted_tier"]; got != "standard" {
		t.Errorf("submitted_tier = %v; want standard", got)
	}
}

func TestBuildBugReportPayload_ReliabilityTier(t *testing.T) {
	cls := telemetry.BugClassification{Kind: "sync", Surface: "sync", Severity: "critical"}
	p := buildBugReportPayload(cls, "0.9.88-dev", telemetry.TierReliability, false)

	if got := p["submitted_tier"]; got != "reliability" {
		t.Errorf("submitted_tier = %v; want reliability", got)
	}
}

func TestBuildBugReportPayload_NoNarrativeFields(t *testing.T) {
	// Critical privacy invariant: the payload MUST NOT contain any
	// narrative or free-text from the bundle. Only closed-vocabulary
	// dimensions + version + timestamp.
	cls := telemetry.BugClassification{Kind: "fs", Surface: "fs", Severity: "error"}
	p := buildBugReportPayload(cls, "0.9.88-dev", telemetry.TierStandard, false)

	forbidden := []string{
		"report_text", "narrative", "log_lines", "bundle", "stack_trace",
		"install_id", "machine_id", "user", "username", "hostname",
		"path", "remote", "config", "raw_log",
	}
	for _, f := range forbidden {
		if _, ok := p[f]; ok {
			t.Errorf("payload contains forbidden field %q (PRIVACY.md: bug-report payload is dimensions-only)", f)
		}
	}
	// Sanity: the dimensions we DO want are all present.
	required := []string{
		"event_kind", "schema_version", "reported_at", "client_version",
		"bug_kind", "bug_surface", "severity_hint", "source", "submitted_tier",
	}
	for _, r := range required {
		if _, ok := p[r]; !ok {
			t.Errorf("payload missing required field %q", r)
		}
	}
}

// TestPrefilledIssueURL_BasicShape confirms the URL has the expected
// structure: bug_report.yml template, title, environment, optionally
// logs.
func TestPrefilledIssueURL_BasicShape(t *testing.T) {
	bundle := `smirror version: 0.9.88-dev
platform: windows/amd64
rclone: NOT FOUND

--- Recent Logs (last 30 lines) ---
2026-05-02T12:34:56 starting
`
	u := prefilledIssueURL(bundle)
	if !strings.HasPrefix(u, issueBugURL) {
		t.Errorf("URL does not start with issueBugURL = %q\n  got: %s", issueBugURL, u)
	}
	if !strings.Contains(u, "title=") {
		t.Errorf("URL is missing title= field")
	}
	if !strings.Contains(u, "environment=") {
		t.Errorf("URL is missing environment= field")
	}
	if !strings.Contains(u, "logs=") {
		t.Errorf("URL is missing logs= field (bundle had a Recent Logs section)")
	}
}

func TestPrefilledIssueURL_NoLogsSection(t *testing.T) {
	bundle := `smirror version: 0.9.88-dev
platform: windows/amd64
`
	u := prefilledIssueURL(bundle)
	if strings.Contains(u, "logs=") {
		t.Errorf("URL contains logs= but bundle had no logs section")
	}
}

func TestPrefilledIssueURL_TruncatesAtCap(t *testing.T) {
	// Build a bundle larger than 8KB to force truncation. The function
	// should still produce a URL <= maxIssueURL chars.
	huge := strings.Repeat("X", 20000)
	bundle := huge + "\n--- Recent Logs (last 30 lines) ---\n" + huge
	u := prefilledIssueURL(bundle)
	if len(u) > maxIssueURL {
		t.Errorf("URL length = %d; want <= %d (truncation should kick in)", len(u), maxIssueURL)
	}
	// Truncated URL must still be well-formed (start with bug template).
	if !strings.HasPrefix(u, issueBugURL) {
		t.Errorf("truncated URL does not start with issueBugURL")
	}
}

func TestPrintSubmitOutcome_AlwaysPrintsURL(t *testing.T) {
	// We can't easily capture stderr without stubbing os.Stderr or
	// dup-fd-ing. The contract is asserted at the source level: the
	// function ALWAYS calls fmt.Fprintln(os.Stderr, prefilledURL).
	// This test is a structural smoke check that prefilledURL is
	// non-empty for representative inputs and feeds through.
	cls := telemetry.BugClassification{Kind: "fs", Surface: "fs", Severity: "error"}
	urlStr := prefilledIssueURL("smirror version: 0.9.88-dev\n")
	if urlStr == "" {
		t.Error("prefilledIssueURL returned empty string; printSubmitOutcome would have nothing to print")
	}
	// printSubmitOutcome itself can't be tested without redirecting
	// stderr; tested behaviorally in end-to-end harness.
	_ = cls
}
