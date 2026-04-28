// Tier reading for the three-tier telemetry consent model.
//
// The user's chosen tier governs whether smirror sends ANY network traffic
// — including the GitHub release-check ping that selfupdate uses on
// startup. At tier None (the default), nothing leaves the machine. This
// is the contractual promise in docs/PRIVACY.md:
//
//   "Nothing leaves your machine — not a heartbeat, not a version check."
//
// Source-of-truth ordering (matches docs/cli-telemetry-command.md
// "First run after install"):
//
//   1. State DB meta.telemetry_tier (runtime CLI writes here)
//   2. Windows registry HKLM\Software\SelectiveMirror\TelemetryTier
//      (MSI installer writes here on install)
//   3. Default: "none"
//
// Once the state DB has been populated (either by first-run migration
// from the registry, or by `smirror telemetry <tier>`), the registry is
// no longer consulted at runtime. This avoids requiring admin to flip
// tiers, and keeps user-mode runtime authoritative over machine-wide
// install-time defaults.
//
// Backward-compat with v0.9.4 telemetry: the previous design used a
// boolean opt-in flag (registry value `TelemetryOptIn`, REG_DWORD 0/1).
// If `TelemetryTier` is missing but `TelemetryOptIn` is present, we
// translate it: 1 → "standard", 0 → "none". The translation is
// transparent — the next state-DB write will persist the tier string
// and stop consulting the legacy value.

package telemetry

// Tier identifies the user's chosen telemetry consent tier.
type Tier string

const (
	// TierNone — no telemetry. The default. Honors PRIVACY.md's promise
	// of zero outbound traffic.
	TierNone Tier = "none"

	// TierStandard — bug reports (per-event approval) plus the install
	// census (first_seen + upgrade events with bucketed structural
	// fields). No accumulated metrics.
	TierStandard Tier = "standard"

	// TierReliability — Standard plus bucketed reliability deltas
	// attached to upgrade events. Sent only at upgrade; no schedule, no
	// heartbeat.
	TierReliability Tier = "reliability"
)

// IsValid reports whether t is one of the three accepted tier strings.
// Any other value (including the empty string) should be treated as
// TierNone by callers.
func (t Tier) IsValid() bool {
	switch t {
	case TierNone, TierStandard, TierReliability:
		return true
	default:
		return false
	}
}

// AllowsNetwork reports whether this tier permits any outbound network
// traffic at all. At TierNone, even the startup release-check ping must
// be skipped.
func (t Tier) AllowsNetwork() bool {
	return t == TierStandard || t == TierReliability
}

// MetaReader is the minimal interface ReadTier needs to consult state
// DB metadata. *state.Store satisfies this without import cycles.
type MetaReader interface {
	GetMeta(key string) (string, error)
}

// ReadTier returns the user's current telemetry tier, consulting state
// DB metadata first and the Windows registry as fallback. Returns
// TierNone for any unset / invalid / non-Windows configuration. Never
// returns an error: the caller's contract is "if you don't know, send
// nothing."
//
// Pass a nil MetaReader to skip state-DB lookup (e.g., very early in
// startup before the DB is open). The function then falls through to
// the registry check.
func ReadTier(meta MetaReader) Tier {
	// 1. State DB is authoritative once populated.
	if meta != nil {
		if v, err := meta.GetMeta("telemetry_tier"); err == nil && v != "" {
			t := Tier(v)
			if t.IsValid() {
				return t
			}
			// Any other value (corruption, manual edit) → fail closed.
			return TierNone
		}
	}

	// 2. Registry fallback (Windows-only; stub on other platforms).
	if v := readTierFromRegistry(); v != "" {
		t := Tier(v)
		if t.IsValid() {
			return t
		}
	}

	// 3. Default.
	return TierNone
}
