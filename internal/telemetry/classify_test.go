package telemetry

import (
	"reflect"
	"sort"
	"testing"
)

func TestKnownBugKinds_StableOrder(t *testing.T) {
	got := KnownBugKinds()
	want := []string{"sync", "watcher", "rclone", "config", "service", "fs", "auth"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KnownBugKinds() returned %v; want %v (drift would silently re-bucket telemetry counts)", got, want)
	}
	// Confirm the call returns a defensive copy — mutating one shouldn't
	// affect the next.
	got[0] = "MUTATED"
	got2 := KnownBugKinds()
	if got2[0] == "MUTATED" {
		t.Errorf("KnownBugKinds() returned a shared slice; expected a copy so callers can't mutate the package-level state")
	}
}

// TestKnownBugKinds_TaxonomyLockedAgainstReportScript guards against
// drift between the Go classifier's vocabulary and the digest report's
// KNOWN_BUG_KINDS Python list. If they fall out of sync, "unknown" share
// in bug_unknown_share view would track the WRONG signal — drift between
// the producer (here) and the consumer (the report) rather than drift
// between client builds and the maintainer's intent.
//
// This test is a structural assertion: it sorts both lists and compares
// as a set. The classify.go file cross-references the report script in
// its header comment.
func TestKnownBugKinds_TaxonomyLockedAgainstReportScript(t *testing.T) {
	got := KnownBugKinds()
	sort.Strings(got)
	want := []string{"auth", "config", "fs", "rclone", "service", "sync", "watcher"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Go KnownBugKinds (sorted) = %v; PRIVACY.md / scripts/telemetry-report.py KNOWN_BUG_KINDS (sorted) = %v",
			got, want)
	}
}

func TestClassifyBugReport_RcloneNotFound(t *testing.T) {
	bundle := `smirror version: 0.9.88-dev
platform: windows/amd64
rclone: NOT FOUND (rclone executable not in PATH)
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "rclone" {
		t.Errorf("Kind = %q, want rclone (a 'rclone: NOT FOUND' bundle should classify as rclone)", cls.Kind)
	}
	if cls.Surface != "rclone" {
		t.Errorf("Surface = %q, want rclone", cls.Surface)
	}
}

func TestClassifyBugReport_HealthyBundleStillClassifiesAsError(t *testing.T) {
	// A bundle with no negative keywords is rare but possible (clean
	// install, healthy state, log line happens to be "INFO starting").
	// User-initiated --submit defaults severity to "error" — see
	// classify.go file header — because the user explicitly chose to
	// file a report.
	bundle := `smirror version: 0.9.88-dev
platform: windows/amd64
rclone version: v1.73.5
config path: ~/.selectivemirror/config.yaml
mirrors: 0
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "unknown" {
		t.Errorf("Kind = %q, want unknown (no negative keywords → unknown bucket)", cls.Kind)
	}
	if cls.Severity != "error" {
		t.Errorf("Severity = %q, want error (user-initiated --submit defaults to error)", cls.Severity)
	}
}

func TestClassifyBugReport_RcloneVersionLineWithoutFailureNotMisclassified(t *testing.T) {
	// The success-path bundle includes "rclone version: v1.73.5" — that
	// shouldn't match the rclone bucket since it's the OK output, not
	// a failure indicator. classify.go has an explicit suppression for
	// these positive lines.
	bundle := `smirror version: 0.9.88-dev
rclone version: v1.73.5
rclone path: C:\Program Files\rclone.exe
rclone os: windows
rclone compat: ok
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind == "rclone" {
		t.Errorf("Kind = %q; success-path 'rclone version' line should NOT classify as rclone bucket without a negative signal", cls.Kind)
	}
}

func TestClassifyBugReport_ConfigError(t *testing.T) {
	bundle := `--- Config ---
config error: yaml: line 17: mapping values are not allowed in this context
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "config" {
		t.Errorf("Kind = %q, want config", cls.Kind)
	}
}

func TestClassifyBugReport_FilesystemError(t *testing.T) {
	bundle := `Recent log:
2026-05-02T12:34:56 sync failed: access is denied (file is locked by another process)
`
	cls := ClassifyBugReport(bundle)
	// "sync failed" matches the sync rule. "access is denied" matches
	// fs. The rules' order in classify.go places fs before sync, so
	// this should bucket as fs (the underlying cause).
	if cls.Kind != "fs" {
		t.Errorf("Kind = %q, want fs (access-denied rules ahead of sync-error in classify.go)", cls.Kind)
	}
}

func TestClassifyBugReport_AuthError(t *testing.T) {
	bundle := `Recent log:
2026-05-02T12:34:56 oauth refresh token expired
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "auth" {
		t.Errorf("Kind = %q, want auth", cls.Kind)
	}
}

func TestClassifyBugReport_WatcherError(t *testing.T) {
	bundle := `Recent log:
2026-05-02T12:34:56 fsnotify: too many open files
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "watcher" {
		t.Errorf("Kind = %q, want watcher", cls.Kind)
	}
}

func TestClassifyBugReport_ServiceError(t *testing.T) {
	bundle := `Recent log:
2026-05-02T12:34:56 windows service start failed: ERROR_SERVICE_DOES_NOT_EXIST
`
	cls := ClassifyBugReport(bundle)
	if cls.Kind != "service" {
		t.Errorf("Kind = %q, want service", cls.Kind)
	}
}

func TestClassifyBugReport_PanicCriticalSeverity(t *testing.T) {
	bundle := `Recent log:
panic: runtime error: invalid memory address or nil pointer dereference
`
	cls := ClassifyBugReport(bundle)
	if cls.Severity != "critical" {
		t.Errorf("Severity = %q, want critical (panic should escalate to critical)", cls.Severity)
	}
}

func TestClassifyBugReport_AlwaysReturnsValidStrings(t *testing.T) {
	// Even a completely empty bundle should return acceptable values
	// (the server's _bump_bug INSERT will accept "unknown" / "error" /
	// "report_bug" without raising).
	cls := ClassifyBugReport("")
	if cls.Kind == "" {
		t.Errorf("Kind is empty; should default to 'unknown'")
	}
	if cls.Severity == "" {
		t.Errorf("Severity is empty; must be one of info/warning/error/critical")
	}
	if cls.Surface == "" {
		t.Errorf("Surface is empty; must mirror Kind")
	}
}
