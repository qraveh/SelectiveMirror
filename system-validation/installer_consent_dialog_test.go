// Structural tests for installer/TelemetryConsent.wxi — the v1.0.1+
// binary-consent dialog (PROPOSAL-2026-05-03-msi-binary-consent.md).
//
// These tests are STRUCTURAL — they read the WiX source as text and
// assert invariants on its content. Behavioral tests against the
// built MSI would be stronger but require either Windows runners with
// full MSI tooling or a parser for the WiX object model. The
// structural approach catches the regression class that matters
// (someone re-adds Reliability as a fresh-install dialog choice;
// someone removes the upgrade-preserve guard) without that cost.

package systemval

import (
	"regexp"
	"strings"
	"testing"
)

func readTelemetryConsentWxi(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "installer", "TelemetryConsent.wxi")
}

// ---------------------------------------------------------------------------
// PROPOSAL-2026-05-03 — dialog presents exactly two RadioButtons
// (none + standard); reliability does NOT appear as a fresh-install
// radio choice.
// ---------------------------------------------------------------------------

func TestInstallerConsentDialog_HasExactlyTwoRadios(t *testing.T) {
	t.Parallel()
	coverage.Record("installer_consent_dialog_binary")

	wxi := readTelemetryConsentWxi(t)

	// Find the RadioButtonGroup body and count <RadioButton ... /> tags.
	// The WiX 4 schema uses self-closing radio buttons; we count any
	// occurrence of `<RadioButton ` (with trailing space) to be safe
	// against attribute-order variation.
	re := regexp.MustCompile(`(?s)<RadioButtonGroup\s+Property="INSTALL_TELEMETRY_TIER"\s*>(.*?)</RadioButtonGroup>`)
	m := re.FindStringSubmatch(wxi)
	if m == nil {
		t.Fatalf("could not locate <RadioButtonGroup Property=\"INSTALL_TELEMETRY_TIER\"> in TelemetryConsent.wxi")
	}
	body := m[1]

	radioCount := strings.Count(body, "<RadioButton ")
	if radioCount != 2 {
		t.Errorf("RadioButtonGroup contains %d RadioButton elements; PROPOSAL-2026-05-03 requires exactly 2 (none + standard). Reliability tier must NOT appear as a fresh-install dialog choice — it's a CLI opt-up only. The CLI three-tier surface in cmd/smirror/cmd_telemetry.go is unchanged.", radioCount)
	}

	// Required values: "none" and "standard". Reliability MUST NOT
	// appear as a Value= on a RadioButton inside the group.
	if !strings.Contains(body, `Value="none"`) {
		t.Errorf("RadioButtonGroup is missing Value=\"none\" — the privacy-honest default must be present and visually first")
	}
	if !strings.Contains(body, `Value="standard"`) {
		t.Errorf("RadioButtonGroup is missing Value=\"standard\" — the affirmative opt-in is the second radio")
	}
	if strings.Contains(body, `Value="reliability"`) {
		t.Errorf("RadioButtonGroup contains Value=\"reliability\" — PROPOSAL-2026-05-03 explicitly removes Reliability from the fresh-install dialog (it remains reachable via CLI and silent-install property). Re-adding it would re-introduce the middle-option-default + 'more is better' anchoring effects this proposal closes.")
	}
}

// ---------------------------------------------------------------------------
// Migration story — RegistrySearch reads the existing tier; SetProperty
// preserves it into INSTALL_TELEMETRY_TIER; conditional Publish skips
// the dialog when an existing tier is present.
// ---------------------------------------------------------------------------

func TestInstallerConsentDialog_PreservesExistingTierOnUpgrade(t *testing.T) {
	t.Parallel()
	coverage.Record("installer_consent_preserves_existing_tier")

	wxi := readTelemetryConsentWxi(t)

	// (a) RegistrySearch on HKLM\Software\<ProductName>\TelemetryTier
	//     populates an EXISTING_TELEMETRY_TIER property.
	if !strings.Contains(wxi, `Id="EXISTING_TELEMETRY_TIER"`) {
		t.Errorf("Missing EXISTING_TELEMETRY_TIER property — required by PROPOSAL-2026-05-03 to detect prior tier choice on upgrade")
	}
	if !strings.Contains(wxi, `<RegistrySearch`) {
		t.Errorf("Missing <RegistrySearch> — without it, EXISTING_TELEMETRY_TIER cannot be populated from HKLM at install time")
	}
	if !strings.Contains(wxi, `Name="TelemetryTier"`) {
		t.Errorf("RegistrySearch does not target the TelemetryTier registry value")
	}

	// (b) SetProperty action propagates EXISTING_TELEMETRY_TIER into
	//     INSTALL_TELEMETRY_TIER so the registry component (which
	//     writes back) doesn't clobber the user's prior choice with
	//     the default "none".
	if !strings.Contains(wxi, `<SetProperty`) {
		t.Errorf("Missing <SetProperty> — without it, the registry write would clobber the prior tier choice with INSTALL_TELEMETRY_TIER's default")
	}
	// The SetProperty must target INSTALL_TELEMETRY_TIER and source
	// from EXISTING_TELEMETRY_TIER. Use a multi-substring match so we
	// don't depend on attribute ordering.
	//
	// SM-218 close-out: WiX v6 changed the attribute layout from
	// v5's `Property="<target>"` to v6's `Id="<target>"`. The regex
	// below matches the v6 form (which is what the WiX v6 build
	// requires). If we ever go back to a WiX-v5 schema, this regex
	// must be updated to match `Property="..."` instead.
	setPropRe := regexp.MustCompile(`(?s)<SetProperty\b[^>]*?Id="INSTALL_TELEMETRY_TIER"[^>]*?Value="\[EXISTING_TELEMETRY_TIER\]"`)
	if !setPropRe.MatchString(wxi) {
		t.Errorf("SetProperty does not propagate EXISTING_TELEMETRY_TIER → INSTALL_TELEMETRY_TIER (expected WiX-v6 form `Id=\"INSTALL_TELEMETRY_TIER\"` post-SM-218). Without that propagation, an upgrade install with a prior Reliability choice would silently downgrade to the dialog's default (none) when the registry component fires.")
	}

	// (c) Conditional Publish: LicenseAgreementDlg.Next routes to
	//     TelemetryTierDlg ONLY when EXISTING_TELEMETRY_TIER is empty.
	//     The complementary route (existing tier present → straight to
	//     InstallDirDlg) skips the dialog entirely.
	//
	// Match for the "skip dialog when existing tier is set" branch.
	skipRe := regexp.MustCompile(`(?s)Dialog="LicenseAgreementDlg"[^>]*Control="Next"[^>]*Value="InstallDirDlg".*?EXISTING_TELEMETRY_TIER`)
	if !skipRe.MatchString(wxi) {
		t.Errorf("Missing the upgrade-skip Publish: LicenseAgreementDlg.Next → InstallDirDlg conditional on EXISTING_TELEMETRY_TIER. Without this, an upgrade install would re-prompt the user — violating the PROPOSAL's 'don't re-prompt; preserve their choice' migration story.")
	}

	// And the fresh-install branch (existing tier empty → show dialog).
	freshRe := regexp.MustCompile(`(?s)Dialog="LicenseAgreementDlg"[^>]*Control="Next"[^>]*Value="TelemetryTierDlg"`)
	if !freshRe.MatchString(wxi) {
		t.Errorf("Missing the fresh-install Publish: LicenseAgreementDlg.Next → TelemetryTierDlg. Without this, the dialog never appears for first-time users.")
	}
}

// ---------------------------------------------------------------------------
// The `none` radio MUST be first in source order (visually first in
// the dialog) AND MUST be the property's default value. The proposal's
// "privacy-honest default" framing depends on this.
// ---------------------------------------------------------------------------

func TestInstallerConsentDialog_NoneRadioIsFirst(t *testing.T) {
	t.Parallel()

	wxi := readTelemetryConsentWxi(t)

	noneIdx := strings.Index(wxi, `<RadioButton Value="none"`)
	standardIdx := strings.Index(wxi, `<RadioButton Value="standard"`)

	if noneIdx == -1 {
		t.Fatalf(`could not locate <RadioButton Value="none" — required by PROPOSAL-2026-05-03 as the visually-first / default radio`)
	}
	if standardIdx == -1 {
		t.Fatalf(`could not locate <RadioButton Value="standard"`)
	}
	if noneIdx >= standardIdx {
		t.Errorf("RadioButton ordering: <none> appears at offset %d, <standard> at %d. PROPOSAL-2026-05-03 requires <none> to be visually first (source order = display order in WiX RadioButtonGroup). Reversing them produces middle-option-effect equivalents on a binary surface.",
			noneIdx, standardIdx)
	}

	// The Property declaration must default to "none" (the proposal's
	// privacy-honest default). Find the INSTALL_TELEMETRY_TIER Property
	// element and verify Value="none".
	propRe := regexp.MustCompile(`(?s)<Property\s+Id="INSTALL_TELEMETRY_TIER"[^>]*Value="([^"]+)"`)
	m := propRe.FindStringSubmatch(wxi)
	if m == nil {
		t.Fatalf(`could not locate <Property Id="INSTALL_TELEMETRY_TIER" Value="..."> declaration`)
	}
	if m[1] != "none" {
		t.Errorf(`INSTALL_TELEMETRY_TIER default Value=%q; want "none". The proposal's privacy-honest default depends on this — silent installs without an explicit property override fall back to None, which is the consent-honest behavior.`, m[1])
	}
}

// ---------------------------------------------------------------------------
// Sanity: the WixUI_TelemetryTier UI element exists and is referenced
// by the bridge that links the dialog into the WixUI_InstallDir chain.
// (Catches a regression where someone deletes the dialog wholesale.)
// ---------------------------------------------------------------------------

func TestInstallerConsentDialog_UIElementExists(t *testing.T) {
	t.Parallel()

	wxi := readTelemetryConsentWxi(t)

	if !strings.Contains(wxi, `<UI Id="WixUI_TelemetryTier">`) {
		t.Errorf(`<UI Id="WixUI_TelemetryTier"> missing — PROPOSAL-2026-05-03 keeps the dialog wired into the WixUI_InstallDir chain via the existing UIRef in Package.wxs`)
	}
	if !strings.Contains(wxi, `<Dialog Id="TelemetryTierDlg"`) {
		t.Errorf(`<Dialog Id="TelemetryTierDlg"> missing — without the dialog the binary-consent surface degrades to "no consent surface at all"`)
	}
}
