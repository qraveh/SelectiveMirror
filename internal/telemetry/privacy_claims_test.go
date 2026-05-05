package telemetry

import (
	"runtime"
	"testing"
)

// Falsifiability tests for the headline privacy claims in `docs/PRIVACY.md`
// and the README opening paragraph. These are deliberately named with the
// `TestPrivacyClaim_*` prefix so that a future maintainer running
// `grep TestPrivacyClaim_ ./...` sees, in one place, every assertion the
// project makes to its users about telemetry default-off behavior.
//
// Panel review pre-public-flip 2026-05-04: prior to this file there were
// individual unit tests for ReadTier, AllowsNetwork, and IsValid, but
// no test labelled as "this is the README claim's load-bearing
// regression test." If any of these tests fails in a future cycle, the
// fix is NOT to update the test — it is to update the README to match
// the new behavior, OR to revert the regression.

// TestPrivacyClaim_DefaultIsNone — README says: "Privacy: opt-in
// telemetry, default off. The default consent tier is None — no startup
// pings, no version checks, no anonymous-counts traffic."
//
// Falsifying conditions:
//  1. A fresh install (no state-DB telemetry_tier key set) reads tier
//     = something OTHER than TierNone, and
//  2. That tier reports AllowsNetwork() == true.
//
// Either condition flipping breaks the README claim. Both must hold for
// the claim to remain accurate.
func TestPrivacyClaim_DefaultIsNone(t *testing.T) {
	// Test 1a: empty state DB → TierNone.
	got := ReadTier(&fakeMeta{values: map[string]string{}})
	if got != TierNone {
		t.Errorf("README claim broken: empty state DB → %q, expected %q (TierNone)", got, TierNone)
	}

	// Test 1b: nil meta (very early startup) → must still default safely.
	// On Windows the registry may carry an installer-time choice; on
	// non-Windows the registry stub returns empty so default is TierNone.
	// Either way, the result must be a VALID tier — not empty, not
	// arbitrary garbage.
	got = ReadTier(nil)
	if !got.IsValid() {
		t.Errorf("README claim broken: nil meta → %q, which is not a valid tier", got)
	}
	if runtime.GOOS != "windows" && got != TierNone {
		t.Errorf("README claim broken (non-Windows): nil meta → %q, expected %q", got, TierNone)
	}

	// Test 2: TierNone must NOT allow network.
	if TierNone.AllowsNetwork() {
		t.Errorf("README claim broken: TierNone.AllowsNetwork() == true — every claim about 'default off' fails if this is true")
	}
}

// TestPrivacyClaim_NoStartupVersionCheckAtTierNone — README says: "no
// startup pings, no version checks." The startup-version-check path
// (cmd/smirror/selfupdate.go::checkForUpdateOnStartup) is gated on
// telemetry.ReadTier(state).AllowsNetwork(). This test asserts the gate
// is in the right shape: TierNone never allows the gate to open.
//
// Falsifying conditions:
//  1. TierNone.AllowsNetwork() returns true (then the gate at
//     selfupdate.go:749 stops gating), OR
//  2. A new tier is added that defaults the unset case to allow-network
//     (then ReadTier with empty meta would return that new tier
//     instead of TierNone).
//
// This test is intentionally redundant with TestPrivacyClaim_DefaultIsNone —
// the redundancy is the point. The README makes the claim twice (once
// for "default off" and once for "no version checks"); regressions of
// either need their own loud failure.
func TestPrivacyClaim_NoStartupVersionCheckAtTierNone(t *testing.T) {
	// The version-check gate is `!ReadTier(state).AllowsNetwork() → return`.
	// We falsify by checking each tier's AllowsNetwork value matches
	// the documented behavior:
	//   None         → no network at all (incl. version check)
	//   Standard     → install-events allowed
	//   Reliability  → install-events + reliability snapshots allowed
	cases := []struct {
		tier       Tier
		allows     bool
		readmeSays string
	}{
		{TierNone, false, "no startup pings, no version checks, no anonymous-counts traffic"},
		{TierStandard, true, "Anonymous categorical counts: install / upgrade / bug-report bucket increments"},
		{TierReliability, true, "Standard plus operational-health bucket increments at upgrade events"},
	}
	for _, c := range cases {
		got := c.tier.AllowsNetwork()
		if got != c.allows {
			t.Errorf("README/PRIVACY.md claim broken for %q (%q): AllowsNetwork=%v, expected %v",
				c.tier, c.readmeSays, got, c.allows)
		}
	}
}

// TestPrivacyClaim_ReadTierFailsClosed — PRIVACY.md says: "If you don't
// know, send nothing." This is the SM-173 fail-closed contract: any
// state-DB read error returns TierNone, NOT a fall-through to the
// installer-time registry value (which the user may have opted DOWN
// from at runtime).
//
// Falsifying condition: a state-DB error returns anything other than
// TierNone, OR falls through to the registry.
func TestPrivacyClaim_ReadTierFailsClosed(t *testing.T) {
	got := ReadTier(&fakeMeta{err: sentinelError{}})
	if got != TierNone {
		t.Errorf("PRIVACY.md fail-closed claim broken: state-DB error → %q, expected %q", got, TierNone)
	}
}
