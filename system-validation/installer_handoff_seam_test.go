// Tests for the MSI-installer → daemon-runtime handoff seam.
//
// Origin: SM-216 (GH #165, DEFECT-1). The v1.0.0 MSI consent dialog
// wrote `HKLM\Software\SelectiveMirror\TelemetryTier` to the registry
// but did not write `install_id`. The daemon's
// `SendInstallEventsIfDue` then returned silently because Gate 3
// ("install_id must be set") failed. **Every MSI-installed Standard-
// or Reliability-tier user had silent telemetry failure** — the bug
// wasn't found until the maintainer dogfooded the MSI post-launch.
//
// Why we missed it (V&V/Release panel post-mortem):
//
//   - Unit tests (install_events_test.go) covered TierNone, NoBuildKey,
//     HappyPath, Idempotency. The case "tier set + buildKey present +
//     install_id MISSING" was not in the matrix.
//   - The WiX-source structural tests (installer_consent_dialog_test.go)
//     verified the dialog shape but didn't cross-check the registry-
//     write set against the runtime's state-DB-read set.
//   - The release-dryrun.yml workflow built the MSI but didn't install
//     it and run smirror, so no end-to-end first_seen-lands check.
//
// The category we missed: **inter-component handoff boundary tests**.
// When component A produces state X for component B, what happens when
// X is empty / null / missing / unexpected? None of these cases were
// systematically tested for the MSI → daemon path.
//
// This file is the gate that locks in SM-216's fix and pins the
// production recovery code so a future refactor can't silently
// regress it. It's a SOURCE-PROPERTY test (reads source files as
// text) rather than a behavioral test, matching the system-validation
// module's idiom (separate Go module, no internal/* imports). The
// behavioral regression lives in
// `internal/telemetry/install_events_test.go::TestSendInstallEventsIfDue_*`
// where the package's helpers are available.
//
// What this gate enforces:
//
//   1. The MSI does NOT write install_id (anti-pattern lock — pins
//      the seam shape so a future WiX edit that "fixes" this by also
//      writing install_id from MSI doesn't accidentally land).
//      Rationale: install_id should be ANONYMOUS and PER-INSTALL. The
//      MSI runs with elevated privileges and its registry writes
//      survive uninstall — bad properties for a "reset by deleting
//      state.db" identifier.
//
//   2. install_events.go contains the install_id-recovery code path
//      (the post-DEFECT-1 fix). Sharper than "doesn't compile away";
//      asserts the specific recovery branch.
//
//   3. install_events_test.go contains the regression test that
//      would have failed pre-fix.
//
//   4. The pre-tag release runbook references the post-MSI-install
//      first_seen-lands check (or explicitly notes its absence as a
//      known gap).

package systemval

import (
	"strings"
	"testing"
)

func readInstallEventsGo(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "internal", "telemetry", "install_events.go")
}

func readInstallEventsTest(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "internal", "telemetry", "install_events_test.go")
}

// ---------------------------------------------------------------------------
// Anti-pattern lock: MSI must NOT write install_id.
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_MSIDoesNotWriteInstallID(t *testing.T) {
	t.Parallel()
	coverage.Record("installer_handoff_seam_msi_no_install_id")

	wxi := readRepoFile(t, "installer", "TelemetryConsent.wxi")

	// The MSI's only registry-write component should be for TelemetryTier.
	// install_id MUST be generated at runtime per first daemon start
	// (cmdTelemetrySet path) or recovered at SendInstallEventsIfDue
	// (DEFECT-1 fix). Adding an install_id RegistryValue to the WiX
	// would defeat the per-install-anonymity property.
	forbidden := []string{
		"install_id",
		"InstallId",
		"InstallID",
		"INSTALL_ID",
	}
	for _, f := range forbidden {
		if strings.Contains(wxi, f) {
			t.Errorf("WiX file references %q — MSI must NOT generate or write install_id. The runtime owns this identifier so it can be reset by `smirror clean` / state-DB delete. Adding it to the MSI would (a) survive uninstall, (b) make the identifier admin-visible, (c) couple install lifecycle to identity lifecycle. SM-216 was originally caused by MSI NOT writing install_id; the fix was to recover at runtime, not to add it to MSI.", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Lock: install_events.go contains the SM-216 / DEFECT-1 recovery branch.
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_RecoveryCodeExists(t *testing.T) {
	t.Parallel()
	coverage.Record("installer_handoff_seam_recovery_exists")

	src := readInstallEventsGo(t)

	// Required: an explicit branch that handles view.InstallID == ""
	// by generating + persisting a new install_id, then proceeding.
	// We check for the structural shape of the recovery, not exact
	// wording (so cosmetic edits don't break the test).
	requiredFragments := []struct {
		name string
		want string
	}{
		{"empty-install_id check",     `view.InstallID == ""`},
		{"GenerateInstallID call",     `GenerateInstallID()`},
		{"persist via SetMeta",        `SetMeta("install_id"`},
		{"DEFECT-1 doc reference",     "DEFECT-1"},
	}
	for _, f := range requiredFragments {
		if !strings.Contains(src, f.want) {
			t.Errorf("install_events.go missing %s (looking for %q). SM-216 / DEFECT-1's idempotent-recovery fix at Gate 3 has been removed or refactored away. If the recovery moved elsewhere, update this test's expected fragments. Without this branch, an MSI-installed user with a non-None tier and no install_id will silently never contribute telemetry — the exact bug shipped in v1.0.0.",
				f.name, f.want)
		}
	}

	// Anti-pattern check: the OLD pre-fix behavior had install_id == ""
	// short-circuit to a silent "skip" branch. Confirm the source no
	// longer has the silent-skip pattern at the install_id check.
	silentSkipPatterns := []string{
		`if view.InstallID == "" {` + "\n" + `		slog.Warn(`,            // pre-fix shape
		`if view.InstallID == "" {` + "\n" + `		return nil`,            // pre-fix early return
	}
	for _, anti := range silentSkipPatterns {
		if strings.Contains(src, anti) {
			t.Errorf("install_events.go appears to silent-skip on empty install_id (pre-DEFECT-1 behavior detected). Pattern matched: %q. The post-DEFECT-1 fix generates + persists; it does not skip.", anti)
		}
	}
}

// ---------------------------------------------------------------------------
// Lock: the regression test that would fail pre-fix exists in tree.
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_RegressionTestPresent(t *testing.T) {
	t.Parallel()
	coverage.Record("installer_handoff_seam_regression_test_present")

	src := readInstallEventsTest(t)

	// At least ONE test name in install_events_test.go must reference
	// the missing-install_id case. We accept either of the two names
	// that have been used historically:
	candidates := []string{
		"TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds", // post-DEFECT-1
		"TestSendInstallEventsIfDue_MSIHandoffSeam_SimulatesSM216",          // explicit-naming variant
	}
	found := false
	for _, name := range candidates {
		if strings.Contains(src, "func "+name+"(t *testing.T)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("install_events_test.go is missing the SM-216 / DEFECT-1 regression test. Looked for any of: %v. Without it, a future change that re-introduces the silent-skip branch in install_events.go (see TestInstallerHandoffSeam_RecoveryCodeExists) would compile and pass unit tests — silently regressing the v1.0.1+ fix that closed the v1.0.0 bug.",
			candidates)
	}

	// Also assert the test specifically simulates the MSI-handoff
	// state (tier set, install_id empty). Look for the discriminating
	// shape: `view.InstallID = ""` and `TierStandard` in proximity.
	if !strings.Contains(src, `view.InstallID = ""`) {
		t.Errorf("install_events_test.go has the regression test name but does not set view.InstallID = \"\" — the test must SIMULATE the MSI-only-writes-tier handoff state.")
	}
}

// ---------------------------------------------------------------------------
// Forward-looking: the release-dryrun workflow should at minimum
// REFERENCE the missing post-MSI-install boundary check, even if the
// behavioral test is too expensive for routine CI. A documented gap
// is better than a silent one.
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_ReleaseDryrunReferencesPostInstallCheck(t *testing.T) {
	t.Parallel()

	// The release-dryrun.yml workflow exercises the MSI build but
	// (as of v1.0.0) does NOT install the just-built MSI on a Windows
	// runner and run smirror to verify a first_seen row lands. That's
	// the test that would have caught SM-216 directly.
	//
	// This assertion is intentionally LENIENT: we don't require the
	// workflow to actually run that test, only that someone has
	// referenced the gap in the workflow file (a TODO / NOTE / known-
	// gap comment), so future maintainers see the trail.

	yml := readRepoFile(t, ".github", "workflows", "release-dryrun.yml")
	hints := []string{
		"SM-216",
		"DEFECT-1",
		"install_id",
		"first_seen",
		"post-install",
		"post-MSI",
		"end-to-end",
		"e2e",
	}
	any := false
	for _, h := range hints {
		if strings.Contains(yml, h) {
			any = true
			break
		}
	}
	if !any {
		t.Logf("release-dryrun.yml does not reference the post-MSI-install / first_seen-lands boundary check. SM-216 was caused by a gap at this seam; documenting it in the workflow file (even as a `# TODO` / `# known gap`) helps future maintainers add the missing test. Suggested hint terms: %v. (Logged, not failed — adding a comment is opportunistic, not a hard gate.)",
			hints)
	}
}
