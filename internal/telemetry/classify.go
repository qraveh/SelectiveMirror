// Bug-report classification — derive (bug_kind, bug_surface,
// severity_hint) from a sanitized bug-report bundle.
//
// SM-158. The categorical contribution travels through telemetry; the
// narrative travels through GitHub Issues. This file only produces the
// CATEGORICAL bucket. It never sees the narrative.
//
// Closed taxonomy. The known-good values for bug_kind are
// scripts/telemetry-report.py::KNOWN_BUG_KINDS:
//
//   sync watcher rclone config service fs auth
//
// Anything that doesn't match one of those falls into "unknown" — and
// the bug_unknown_share view will surface it for taxonomy review (the analyst's
// drift detection, A-10).
//
// bug_surface mirrors the kinds for now: the "where in the system" view
// converges with the "what subsystem" view for the bugs smirror users
// actually file. We keep the column separate (and identically populated)
// so a future split (e.g., kind=auth + surface=installer) doesn't need
// a schema migration.
//
// Heuristic order. Earlier patterns take precedence. The first match
// wins. This means classifications cascade from "specific" to "general":
// e.g. "rclone NOT FOUND" lands as rclone, not as fs, even though it
// involves a missing binary. Order chosen by triage-frequency, not by
// alphabetical tidiness.
//
// What this is NOT: a full NLP classifier. It's a keyword grep over the
// already-sanitized bundle text, designed to be readable, deterministic,
// and easy to extend by adding rules in the right place. False positives
// (a bug actually about config getting tagged "rclone" because the user
// mentioned rclone in passing) are acceptable — the GitHub-issue
// narrative is the source of truth for triage; the bucket is just a
// histogram bin.

package telemetry

import (
	"strings"
)

// Closed taxonomy. Keep in sync with KNOWN_BUG_KINDS in
// scripts/telemetry-report.py and PRIVACY.md "Closed-vocabulary fields".
// The TestClassify_TaxonomyLockedAgainstReport test in classify_test.go
// asserts this list does not drift from the report script.
var knownBugKinds = []string{
	"sync", "watcher", "rclone", "config", "service", "fs", "auth",
}

// KnownBugKinds returns the closed taxonomy of bug kinds. Exposed for
// test introspection and for the report-script taxonomy-lock check.
func KnownBugKinds() []string {
	out := make([]string, len(knownBugKinds))
	copy(out, knownBugKinds)
	return out
}

// BugClassification is the categorical result of inspecting a sanitized
// bug-report bundle. All three fields are server-accepted strings:
// bug_kind/bug_surface are TEXT (closed taxonomy); severity_hint is a
// PG ENUM with values info/warning/error/critical.
type BugClassification struct {
	Kind     string // one of knownBugKinds, or "unknown"
	Surface  string // mirrors Kind for now (see file header)
	Severity string // info / warning / error / critical
}

// ClassifyBugReport scans the sanitized bundle text and returns a
// best-effort bucket. Always returns valid server-acceptable strings;
// the worst case is BugClassification{Kind:"unknown", Surface:"unknown",
// Severity:"error"}. Never returns an empty string.
//
// The classifier is text-based and intentionally conservative — it does
// NOT crack open log lines for stack traces or error codes (that
// information stays out of telemetry). It looks for surface-level
// keywords on already-sanitized output. The contract is "produce a
// bucket the maintainer can graph"; it is not "diagnose the bug."
//
// User-initiated --submit defaults severity to "error" — the user
// explicitly chose to file, presumably because something is wrong.
// Crash-report flow (separate, deferred) should set severity to
// "critical" before calling this and pass through.
func ClassifyBugReport(bundle string) BugClassification {
	lower := strings.ToLower(bundle)

	kind := classifyKind(lower)
	severity := classifySeverity(lower)

	return BugClassification{
		Kind:     kind,
		Surface:  kind, // same vocabulary; column kept distinct for future split
		Severity: severity,
	}
}

// classifyKind walks an ordered list of patterns. First match wins.
// Each pattern is a short slice of substrings; matching one of them in
// the lower-cased bundle assigns that kind. Order matters — see file
// header.
func classifyKind(lower string) string {
	type rule struct {
		kind     string
		patterns []string
	}
	// Order: most-specific first, most-general last. A "service"
	// failure that's actually a Windows-service install issue should
	// classify before "config" even if the bundle also mentions the
	// config file path.
	rules := []rule{
		{"rclone", []string{
			"rclone: not found",
			"rclone version",      // matches both ok and error lines; we re-check below
			"rclone path",
			"rclone os",
			"rclone error",
			"rclone exit",
			"rclone failed",
		}},
		{"watcher", []string{
			"fsnotify",
			"readdirectorychangesw",
			"watcher",
			"watch error",
		}},
		{"service", []string{
			"windows service",
			"service install",
			"service start",
			"service stop",
			"scm ",
			"smirror service",
		}},
		{"auth", []string{
			"unauthorized",
			"401",
			"403 forbidden",
			"token expired",
			"credential",
			"oauth",
			"refresh token",
		}},
		{"fs", []string{
			"access is denied",
			"permission denied",
			"file is locked",
			"sharing violation",
			"path too long",
			"invalid filename",
			"disk full",
			"no space left",
		}},
		{"config", []string{
			"config error",
			"yaml:",
			"yaml error",
			"config validation",
			"validation failed",
			"invalid mirror",
			"invalid remote",
		}},
		{"sync", []string{
			"sync error",
			"sync failed",
			"sync_failure",
			"failed to sync",
			"copy error",
			"upload error",
		}},
	}

	// Special handling for the "rclone version" / "rclone path" / "rclone os"
	// lines that are part of the bundle's success output, not a failure
	// signal. If those appear without a NEGATIVE keyword, ignore them.
	rcloneNegative := containsAny(lower, []string{
		"rclone: not found", "rclone error", "rclone exit", "rclone failed",
	})

	for _, r := range rules {
		for _, p := range r.patterns {
			if strings.Contains(lower, p) {
				if r.kind == "rclone" {
					// Suppress the always-present "rclone version"-style
					// matches unless we also see a negative keyword.
					if (p == "rclone version" || p == "rclone path" || p == "rclone os") && !rcloneNegative {
						continue
					}
				}
				return r.kind
			}
		}
	}
	return "unknown"
}

// classifySeverity inspects the bundle for a severity hint. Defaults to
// "error" because user-initiated --submit means the user thinks
// something is wrong; only downgrade to "warning" or "info" if the
// bundle is unusually clean.
func classifySeverity(lower string) string {
	switch {
	case containsAny(lower, []string{"panic:", "fatal:", "fatal error", "critical"}):
		return "critical"
	case containsAny(lower, []string{"error", "failed", "denied"}):
		return "error"
	case containsAny(lower, []string{"warn", "deprecated"}):
		return "warning"
	default:
		// User-initiated --submit defaults to "error" — see file header.
		// A bundle with literally zero warning/error keywords is rare
		// (the recent-logs section almost always has at least an INFO
		// "starting" line). Returning "error" matches user intent.
		return "error"
	}
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}
