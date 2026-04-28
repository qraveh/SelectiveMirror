//go:build windows

package telemetry

import (
	"golang.org/x/sys/windows/registry"
)

// readTierFromRegistry reads HKLM\Software\SelectiveMirror\TelemetryTier
// (REG_SZ, written by the MSI installer). Falls back to the legacy
// REG_DWORD `TelemetryOptIn` value from pre-v0.9.16 installs:
//   1 → "standard"
//   0 → "none"
//
// Returns the empty string if no value is found (or the registry key
// itself doesn't exist), in which case the caller falls through to the
// "none" default.
func readTierFromRegistry() string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`Software\SelectiveMirror`,
		registry.QUERY_VALUE|registry.WOW64_64KEY,
	)
	if err != nil {
		return ""
	}
	defer k.Close()

	// Modern (v0.9.16+) value: REG_SZ.
	if v, _, err := k.GetStringValue("TelemetryTier"); err == nil && v != "" {
		return v
	}

	// Legacy (v0.9.4–v0.9.15) value: REG_DWORD opt-in flag.
	if v, _, err := k.GetIntegerValue("TelemetryOptIn"); err == nil {
		if v == 1 {
			return string(TierStandard)
		}
		return string(TierNone)
	}

	return ""
}
