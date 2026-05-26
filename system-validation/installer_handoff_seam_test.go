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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		t.Errorf("install_events_test.go is missing the SM-216 / DEFECT-1 regression test. Looked for any of: %v. Without it, a future change that re-introduces the silent-skip branch in install_events.go (see TestInstallerHandoffSeam_RecoveryCodeExists) would compile and pass unit tests — silently regressing the post-SM-217 fix that closed the v1.0.0 bug.",
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

// ---------------------------------------------------------------------------
// BEHAVIORAL gate #1 — actually run the canonical SM-216 regression test.
//
// The four tests above are SOURCE-PROPERTY: they grep for shapes in
// install_events.go, install_events_test.go, TelemetryConsent.wxi,
// release-dryrun.yml. None of them EXECUTE code. So a future change
// that (a) keeps the surface shape but (b) breaks the recovery's
// observable behavior would slip past all four.
//
// This test answers "Can you reproduce SM-216 in system verification?"
// by shelling out to:
//
//     go test -run TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds
//             -count=1 -v ./internal/telemetry/
//
// from the system-validation module's working directory. The system-
// validation module is intentionally a separate Go module (no
// internal/* imports) — but it CAN invoke `go test` against the
// parent module the same way it invokes the freshly-built smirror.exe.
// That gives us behavioral coverage without dissolving the module
// boundary.
//
// Failure semantics:
//   - Subprocess exits non-zero → SV gate fails.
//   - Subprocess exits 0 but the PASS line is not in the output → SV
//     gate fails (defensive: catches a name change or `-run` mismatch
//     that would otherwise silently pass).
//   - Subprocess exits 0 and PASS is observed but the recovery's
//     INFO log line ("generated install_id on first daemon start")
//     is missing → SV gate fails (defensive: catches a stub-recovery
//     that returns nil without doing the work).
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_BehavioralRegressionPasses(t *testing.T) {
	coverage.Record("installer_handoff_seam_behavioral_regression_passes")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"go", "test",
		"-run", "TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds",
		"-count=1",
		"-v",
		"./internal/telemetry/",
	)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	s := string(out)

	if err != nil {
		t.Errorf(
			"SM-216 regression test FAILED on current source tree:\n%s\nerror: %v\n\n"+
				"This means install_events.go's install_id-recovery branch has regressed (deleted, modified, or moved). "+
				"An MSI-installed Standard/Reliability-tier user will silently never contribute first_seen telemetry — exactly the v1.0.0 ship-bug. "+
				"FIX BEFORE TAGGING: restore the recovery branch in internal/telemetry/install_events.go (see commit 8e82d40).",
			s, err)
		return
	}
	if !strings.Contains(s, "--- PASS: TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds") {
		t.Errorf(
			"regression-test subprocess exited 0 but PASS line not found.\n"+
				"Output:\n%s\n\n"+
				"Possible causes: test name changed; -run filter mismatched; output truncated.",
			s)
		return
	}
	// The recovery branch's INFO log fires only when the recovery
	// CODE PATH actually executed. If a future refactor stub-recovers
	// (returns nil without doing the work), the assertion line in the
	// unit test could still pass while the user-visible behavior is
	// gone. This catches that.
	if !strings.Contains(s, "generated install_id on first daemon start") {
		t.Errorf(
			"regression test PASSED but the install_id-recovery INFO log was not observed.\n"+
				"Output:\n%s\n\n"+
				"The recovery branch's `slog.Info(\"telemetry: generated install_id on first daemon start ...\")` did not fire. "+
				"This means the test passed for the wrong reason — possibly a stub recovery that returns nil without actually generating an install_id.",
			s)
	}
}

// ---------------------------------------------------------------------------
// BEHAVIORAL gate #2 — mutation test: the regression test must DETECT
// the pre-fix code shape that shipped in v1.0.0.
//
// `go test -overlay <json>` lets us swap a source file's content for
// the duration of a single `go test` invocation, without touching
// disk. We use it to:
//
//   1. Read the current install_events.go.
//   2. Locate the recovery block by structural markers.
//   3. Splice in the pre-fix silent-skip block (the exact shape that
//      shipped in v1.0.0).
//   4. Write the mutated source + an overlay JSON to a temp dir.
//   5. Run the regression test against the mutated source.
//   6. Assert FAIL — the test must catch the mutation.
//
// What this proves:
//   - The recovery branch is the load-bearing piece of the fix
//     (mutation kills the test → recovery branch is doing the work).
//   - The regression test in install_events_test.go would have shipped
//     a CI failure pre-fix — i.e. it would have caught SM-216 before
//     v1.0.0 tag, given the chance to run.
//
// What this does NOT prove:
//   - That the test catches OTHER pre-fix mutations. (A mutation
//     framework like go-mutesting could enumerate, but pre-tag we just
//     need the canonical "v1.0.0 shape" mutation pinned.)
//
// If this test fails (mutation survives), the regression test is
// weak — a future SM-216-class regression could ship to users.
// ---------------------------------------------------------------------------

func TestInstallerHandoffSeam_MutationConfirmsRegressionTestCatchesBug(t *testing.T) {
	coverage.Record("installer_handoff_seam_mutation_confirms_test_catches")

	origPath := filepath.Join(repoRoot, "internal", "telemetry", "install_events.go")
	origBytes, err := os.ReadFile(origPath)
	if err != nil {
		t.Fatalf("read install_events.go: %v", err)
	}
	orig := string(origBytes)

	// Locate the recovery block. Markers are chosen to be specific
	// (won't match elsewhere in the file) and stable across cosmetic
	// edits to logs / comments / variable names. Drift in either
	// marker is a signal that the source has moved enough that the
	// mutation harness needs an update.
	startMarker := `if view.InstallID == "" {`
	endMarker := `view.InstallID = newID`
	startIdx := strings.Index(orig, startMarker)
	if startIdx < 0 {
		t.Fatalf(
			"could not find recovery-block start marker %q in install_events.go.\n"+
				"The recovery branch may have been moved or rewritten. "+
				"Update this test's markers to match the new shape, OR if the recovery has been fundamentally restructured, file an SM-NNN to rewrite the mutation harness.",
			startMarker)
	}
	endIdx := strings.Index(orig[startIdx:], endMarker)
	if endIdx < 0 {
		t.Fatalf(
			"could not find recovery-block end marker %q after start marker.\n"+
				"The recovery branch's tail has been rewritten. Update this test's markers.",
			endMarker)
	}
	afterEnd := startIdx + endIdx + len(endMarker)
	closeIdx := strings.Index(orig[afterEnd:], "}")
	if closeIdx < 0 {
		t.Fatalf("could not find closing brace after %q in install_events.go", endMarker)
	}
	blockEnd := afterEnd + closeIdx + 1

	// The pre-fix v1.0.0 shape: silent-skip with a WARN, no
	// install_id generation, no recovery. This is the EXACT shape
	// that shipped and caused SM-216.
	preFixBlock := `if view.InstallID == "" {
		slog.Warn("install-event submit skipped: install_id is empty (state DB inconsistent?)")
		return nil
	}`
	mutated := orig[:startIdx] + preFixBlock + orig[blockEnd:]

	// Sanity: confirm the splice removed GenerateInstallID() from
	// install_events.go. (It's only called inside the recovery block.
	// If it remains, the splice missed and the mutation isn't real.)
	if strings.Contains(mutated, "GenerateInstallID()") {
		t.Fatalf(
			"mutated source still contains GenerateInstallID() — splice missed.\n"+
				"The recovery branch may have multiple call sites or has been refactored to a helper. Update the splice.")
	}
	// Sanity: confirm the splice introduced the pre-fix WARN line.
	if !strings.Contains(mutated, `install-event submit skipped: install_id is empty`) {
		t.Fatalf("mutated source does not contain the pre-fix WARN string — splice produced a non-pre-fix variant")
	}

	// Write the mutated source + the -overlay JSON to a temp dir.
	overlayDir := t.TempDir()
	mutatedPath := filepath.Join(overlayDir, "install_events_mutated.go")
	if err := os.WriteFile(mutatedPath, []byte(mutated), 0644); err != nil {
		t.Fatalf("write mutated source: %v", err)
	}
	overlay := struct {
		Replace map[string]string
	}{
		Replace: map[string]string{origPath: mutatedPath},
	}
	overlayJSON, err := json.Marshal(overlay)
	if err != nil {
		t.Fatalf("marshal overlay: %v", err)
	}
	overlayPath := filepath.Join(overlayDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlayJSON, 0644); err != nil {
		t.Fatalf("write overlay JSON: %v", err)
	}

	// Run the regression test against the mutated source. EXPECT FAIL.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"go", "test",
		"-overlay", overlayPath,
		"-run", "TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds",
		"-count=1",
		"-v",
		"./internal/telemetry/",
	)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	s := string(out)

	if err == nil {
		t.Errorf(
			"MUTATION SURVIVED: regression test PASSED against pre-fix code.\n"+
				"Output:\n%s\n\n"+
				"This means TestSendInstallEventsIfDue_MissingInstallID_GeneratesAndProceeds is NOT actually exercising the install_id-recovery branch. "+
				"Either the recovery is masked by other code, or the test's assertions are too lax to discriminate. "+
				"Strengthen the test (assert install_id was persisted to MetaStore AND that the mock telemetry endpoint received exactly one POST AND that view.InstallID is non-empty after the call).",
			s)
		return
	}
	if !strings.Contains(s, "FAIL") {
		t.Errorf(
			"mutation subprocess exited non-zero but no FAIL line was emitted — possible compile error against the mutated source.\n"+
				"Output:\n%s",
			s)
		return
	}
	// Confirm the failure is for the RIGHT REASON — one of the
	// discriminating assertion messages from the unit test, not a
	// random side-effect of the splice.
	expectedHints := []string{
		"missing install_id should be auto-generated",
		"install_id was not persisted",
		"view.InstallID still empty",
	}
	foundHint := false
	for _, h := range expectedHints {
		if strings.Contains(s, h) {
			foundHint = true
			break
		}
	}
	if !foundHint {
		t.Errorf(
			"mutation FAIL did not include any of the expected discriminating-failure hints %v.\n"+
				"The test failed for a different reason than the recovery-branch absence — possibly a panic, a compile error in mutated test code, or a flake.\n"+
				"Output:\n%s",
			expectedHints, s)
	}
}
