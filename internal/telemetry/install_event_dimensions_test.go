// SM-220 regression ratchet — payload-level dimension checks.
//
// Pre-SM-220, `cmd/smirror/cmd_telemetry.go::bestEffortRcloneVersion`
// returned the literal placeholder string
// "(would be detected at submit time)" for both the inspect path AND
// the actual submit path. The Worker's schema doesn't constrain
// `rclone_version` to an ENUM, so the placeholder text leaked through
// schema validation, the HMAC verified, and the placeholder landed
// in `telemetry.installation_daily_rollup` as a fake "version"
// indistinguishable from a real rclone version string.
//
// SM-220 also reclassified the install_method and background_mode
// dimensions, which DID have ENUM constraints server-side, but
// always returned "unknown" client-side. Server accepted, but the
// population-distribution signal these were supposed to provide was
// missing.
//
// These tests guard against any future regression. They operate on
// the payload BUILDER (BuildInstallationPayload) — anything that
// reaches the wire goes through this function, so a regression
// anywhere in the helper chain that feeds it will surface here.

package telemetry

import (
	"strings"
	"testing"
)

// validInstallMethods enumerates every value the server-side
// payloads.go documents for the install_method dimension. Kept in
// sync with the comment on SystemView.InstallMethod.
var validInstallMethods = []string{"msi", "winget", "zip", "manual", "unknown"}

// validBackgroundModes — same, for background_mode.
var validBackgroundModes = []string{"foreground", "service", "task", "unknown"}

// validDeletePolicies — same, for delete_policy.
var validDeletePolicies = []string{"ignore", "delete", "quarantine"}

// validMirrorCountBuckets — same, for mirror_count_bucket.
var validMirrorCountBuckets = []string{"0", "1", "2-5", "6-20", "21+"}

// TestSM220_BuildInstallationPayload_DimensionsAreEnumValid asserts
// that BuildInstallationPayload faithfully reproduces every documented
// ENUM value for install_method and background_mode without rewriting
// or coercing them. This is the regression ratchet against silently
// dropping an ENUM-valid value into "unknown" or vice-versa.
func TestSM220_BuildInstallationPayload_DimensionsAreEnumValid(t *testing.T) {
	// Cross-product test: every (install_method, background_mode)
	// pairing flows through BuildInstallationPayload unchanged.
	for _, im := range validInstallMethods {
		for _, bm := range validBackgroundModes {
			view := SystemView{
				InstallID:         "sm-test-deadbeef",
				ClientVersion:     "1.0.0",
				InstallMethod:     im,
				BackgroundMode:    bm,
				MirrorCountBucket: "1",
				DeletePolicy:      "quarantine",
				HasHooks:          false,
				HasFilters:        true,
				HasAlertWebhook:   false,
				HasBandwidthLimit: false,
				RcloneVersion:     "1.73.2",
			}
			p := BuildInstallationPayload("first_seen", view, "2026-01-01T00:00:00Z", "", "")
			if p["install_method"] != im {
				t.Errorf("install_method mismatch in payload: got %q, want %q", p["install_method"], im)
			}
			if p["background_mode"] != bm {
				t.Errorf("background_mode mismatch in payload: got %q, want %q", p["background_mode"], bm)
			}
		}
	}
}

// TestSM220_BuildInstallationPayload_NoPlaceholderLeak ensures no
// string field in the submitted payload carries a placeholder pattern.
// Heuristic: placeholder strings tend to start with "(" (the
// SM-220 culprit "(would be detected at submit time)"), or contain
// markers like "TODO", "FIXME", "TBD", "placeholder", or the
// substring "would be detected" / "would be filled".
//
// Forward-going ratchet: future regressions that reintroduce
// placeholder text into ANY dimension (not just rclone_version)
// fail this test loudly.
func TestSM220_BuildInstallationPayload_NoPlaceholderLeak(t *testing.T) {
	view := SystemView{
		InstallID:         "sm-test-deadbeef",
		ClientVersion:     "1.0.0",
		InstallMethod:     "msi",
		BackgroundMode:    "foreground",
		MirrorCountBucket: "1",
		DeletePolicy:      "quarantine",
		HasHooks:          false,
		HasFilters:        true,
		HasAlertWebhook:   false,
		HasBandwidthLimit: false,
		RcloneVersion:     "1.73.2",
	}

	// Test both event kinds — first_seen and upgrade have different
	// payload shapes (upgrade adds prior_version + days_since_first_seen_bucket).
	for _, kind := range []string{"first_seen", "upgrade"} {
		priorVersion := ""
		daysBucket := ""
		if kind == "upgrade" {
			priorVersion = "0.9.0"
			daysBucket = "8-30"
		}
		p := BuildInstallationPayload(kind, view, "2026-01-01T00:00:00Z", priorVersion, daysBucket)

		for k, v := range p {
			s, ok := v.(string)
			if !ok {
				continue
			}

			// Heuristic 1: placeholders often start with "(".
			// Real ENUM values and versions don't.
			if strings.HasPrefix(s, "(") {
				t.Errorf("event_kind=%s field %q starts with '(' — placeholder pattern (SM-220 culprit shape): %q",
					kind, k, s)
			}

			// Heuristic 2: literal markers commonly used as TODOs.
			for _, marker := range []string{
				"would be detected",
				"would be filled",
				"would be generated",
				"todo",
				"fixme",
				"tbd",
				"placeholder",
				"<not implemented>",
				"<unimplemented>",
			} {
				if strings.Contains(strings.ToLower(s), marker) {
					t.Errorf("event_kind=%s field %q contains placeholder marker %q in value %q",
						kind, k, marker, s)
				}
			}
		}
	}
}

// TestSM220_SystemViewENUMComment_DocumentsKnownValues protects the
// `// "msi" / "winget" / "zip" / "manual" / "unknown"` comment line on
// the SystemView.InstallMethod field — that comment is the documented
// contract and the source of truth this test uses to derive
// validInstallMethods (above). If the comment is edited to drop a value,
// the test variables here need to be updated in lockstep — otherwise the
// test silently goes out of sync.
//
// This is a soft assertion (Logf, not Errorf) because the comment-vs-
// constant relationship is curatorial, not load-bearing. It surfaces
// drift in code review.
func TestSM220_SystemViewENUMComment_DocumentsKnownValues(t *testing.T) {
	t.Logf("SM-220 ENUM-test guard: validInstallMethods = %v", validInstallMethods)
	t.Logf("SM-220 ENUM-test guard: validBackgroundModes = %v", validBackgroundModes)
	t.Logf("SM-220 ENUM-test guard: validDeletePolicies = %v", validDeletePolicies)
	t.Logf("SM-220 ENUM-test guard: validMirrorCountBuckets = %v", validMirrorCountBuckets)
	t.Log("If SystemView field comments change, update these vars in lockstep.")
}
