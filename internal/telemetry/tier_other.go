//go:build !windows

package telemetry

// readTierFromRegistry is a Windows-only concept. On other platforms,
// installer-side tier persistence is not implemented (no MSI), so this
// always returns the empty string and ReadTier falls through to its
// "none" default. Runtime CLI tier changes are still honored via the
// state DB path in ReadTier.
func readTierFromRegistry() string {
	return ""
}
