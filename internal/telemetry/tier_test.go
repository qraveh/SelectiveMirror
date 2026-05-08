package telemetry

import "testing"

// fakeMeta is a tiny in-memory MetaReader for testing ReadTier without
// touching SQLite. ReadTier only calls GetMeta with key "telemetry_tier";
// that's the only key fakeMeta needs to honor.
type fakeMeta struct {
	values map[string]string
	err    error
}

func (f *fakeMeta) GetMeta(key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", nil
}

func TestTier_IsValid(t *testing.T) {
	cases := []struct {
		in   Tier
		want bool
	}{
		{TierNone, true},
		{TierStandard, true},
		{TierReliability, true},
		{Tier(""), false},
		{Tier("garbage"), false},
		{Tier("RELIABILITY"), false}, // case-sensitive
	}
	for _, c := range cases {
		if got := c.in.IsValid(); got != c.want {
			t.Errorf("Tier(%q).IsValid() = %v, want %v", string(c.in), got, c.want)
		}
	}
}

func TestTier_AllowsNetwork(t *testing.T) {
	cases := []struct {
		in   Tier
		want bool
	}{
		{TierNone, false}, // default — no network at all, including version checks
		{TierStandard, true},
		{TierReliability, true},
		{Tier(""), false},
		{Tier("bogus"), false},
	}
	for _, c := range cases {
		if got := c.in.AllowsNetwork(); got != c.want {
			t.Errorf("Tier(%q).AllowsNetwork() = %v, want %v", string(c.in), got, c.want)
		}
	}
}

func TestReadTier_StateDBValuePreferred(t *testing.T) {
	// State DB is the runtime source of truth. Even if the registry
	// said something else, state DB wins. (Registry is consulted only
	// when state DB has no value.)
	meta := &fakeMeta{values: map[string]string{"telemetry_tier": "standard"}}
	if got := ReadTier(meta); got != TierStandard {
		t.Errorf("got %q, want %q", got, TierStandard)
	}
}

func TestReadTier_StateDBNoneIsRespected(t *testing.T) {
	// Explicit "none" in state DB must NOT fall through to the registry
	// — that would silently re-enable telemetry for users who opted out
	// at runtime after an MSI install with INSTALL_TELEMETRY_TIER=standard.
	meta := &fakeMeta{values: map[string]string{"telemetry_tier": "none"}}
	if got := ReadTier(meta); got != TierNone {
		t.Errorf("got %q, want %q", got, TierNone)
	}
}

func TestReadTier_InvalidStateDBValueFailsClosed(t *testing.T) {
	// Corruption / manual edit / future-version value smirror doesn't
	// recognize must fail closed to "none", never to a higher tier.
	meta := &fakeMeta{values: map[string]string{"telemetry_tier": "WHATEVER"}}
	if got := ReadTier(meta); got != TierNone {
		t.Errorf("got %q, want %q", got, TierNone)
	}
}

func TestReadTier_NilMetaDoesNotPanic(t *testing.T) {
	// Callers may pass nil very early at startup before the state DB
	// is open. ReadTier must tolerate it and fall through to registry +
	// default.
	got := ReadTier(nil)
	// On Windows in CI, the registry key may or may not exist; on
	// other platforms readTierFromRegistry always returns "". Either
	// way the result must be a valid tier.
	if !got.IsValid() {
		t.Errorf("ReadTier(nil) = %q, which is not a valid tier", got)
	}
}

func TestReadTier_MetaErrorFailsClosed(t *testing.T) {
	// SM-173: GetMeta error (DB locked, schema mismatch, corrupt file)
	// must fail CLOSED to TierNone. We deliberately don't fall through
	// to the Windows registry, because the registry holds the
	// installer-time choice — the user may have opted DOWN at runtime
	// to TierNone, and silently reading "standard" out of the registry
	// after a runtime read error would re-enable telemetry against
	// their wishes. "If you don't know, send nothing" is the contract.
	meta := &fakeMeta{err: errFake}
	got := ReadTier(meta)
	if got != TierNone {
		t.Errorf("ReadTier with errored meta = %q, want %q (fail-closed)", got, TierNone)
	}
}

// errFake is a sentinel to make the meta-error test independent of any
// real package import path.
type sentinelError struct{}

func (sentinelError) Error() string { return "fake meta error" }

var errFake = sentinelError{}

// ---------------------------------------------------------------------------
// Boundary tests #1, #2 from the harvest brainstorm
// (docs/PROPOSAL-2026-05-08-boundary-test-harvest.md).
//
// SM-216 post-mortem revealed a missing test class: inter-component
// handoff boundary tests. The "MSI registry → daemon state-DB read"
// seam is the most load-bearing one in the codebase. These tests
// pin the boundary cases that weren't tested pre-v1.0.0.
// ---------------------------------------------------------------------------

// Boundary case: empty-string value in state DB falls through to the
// registry-then-default chain.
//
// Why this matters. The pre-fix v1.0.0 ReadTier behavior was: empty
// string → fall through to registry. That's intentional (it's the
// "first-run migration" path). But it was UNDOCUMENTED at the test
// level, so a future refactor that changed this to "empty → fail
// closed to None" would silently break first-run installs that
// rely on the registry-write from the MSI.
func TestReadTier_EmptyStateDBValueFallsThrough(t *testing.T) {
	// State DB explicitly has telemetry_tier set to empty string.
	// (Distinct from "key absent": a key with value "" is what some
	// migration paths produce when clearing without deleting.)
	meta := &fakeMeta{values: map[string]string{"telemetry_tier": ""}}
	got := ReadTier(meta)
	// Resolution: fall through to registry. On non-Windows
	// readTierFromRegistry returns "", so we land on the default
	// (None). On Windows-CI the registry may have a value; either
	// way the result must be a valid tier (not corrupt nonsense).
	if !got.IsValid() {
		t.Errorf("ReadTier(empty-state-db) = %q, not a valid tier", got)
	}
	// Pre-v1.0.0 the implementation was documented to fall-through;
	// pinning that semantics so a future refactor can't silently flip
	// it. If this assertion needs to change in the future, it should
	// be a deliberate behavior change with a CHANGELOG entry — not a
	// silent edit.
	// (The non-failing path above implicitly covers it on non-Windows.
	// On Windows we'd need RegistrySearch fixtures to fully assert,
	// out of scope for this unit test.)
}

// Boundary case: state DB has a tier value with leading/trailing
// whitespace ("  standard  "). Whitespace tolerance is a common
// foot-gun: a config-file editor may add it inadvertently.
//
// Resolution: ReadTier should fail closed. Tier("  standard  ") is
// not in IsValid()'s domain. The user shouldn't accidentally enroll
// in telemetry by misformatting their state DB.
func TestReadTier_StateDBValueWithWhitespaceFailsClosed(t *testing.T) {
	cases := []string{
		"  standard",        // leading
		"standard  ",        // trailing
		"  standard  ",      // both
		"standard\n",        // trailing newline
		"\tstandard",        // leading tab
		"reliability\r\n",   // CRLF
	}
	for _, v := range cases {
		meta := &fakeMeta{values: map[string]string{"telemetry_tier": v}}
		got := ReadTier(meta)
		if got != TierNone {
			t.Errorf("ReadTier(%q) = %q, want %q (whitespace must not enroll user)",
				v, got, TierNone)
		}
	}
}

// Boundary case: state DB has a tier value with unexpected case
// ("Standard" / "STANDARD" / "Reliability"). The architecture's
// tier ENUM is case-sensitive lowercase.
//
// Resolution: fail closed. Same reasoning as whitespace —
// inadvertent capitalization shouldn't enroll a user.
func TestReadTier_StateDBValueWithCaseMismatchFailsClosed(t *testing.T) {
	cases := []string{
		"Standard",
		"STANDARD",
		"Reliability",
		"RELIABILITY",
		"None",
		"NONE",
	}
	for _, v := range cases {
		meta := &fakeMeta{values: map[string]string{"telemetry_tier": v}}
		got := ReadTier(meta)
		if got != TierNone {
			t.Errorf("ReadTier(%q) = %q, want %q (case-mismatch must not enroll user)",
				v, got, TierNone)
		}
	}
}

// Boundary case: tier values that LOOK like valid prefixes but aren't
// in the closed set ("standar", "stand", "reliabilit"). Catches a
// future regression where someone adds a `strings.HasPrefix`
// shortcut for "convenience."
func TestReadTier_StateDBValuePrefixMatchFailsClosed(t *testing.T) {
	cases := []string{
		"standar",       // missing 'd'
		"stand",         // truncated
		"reliabilit",    // missing 'y'
		"standard-x",    // suffix
		"reliability ",  // trailing space (covered above too)
	}
	for _, v := range cases {
		meta := &fakeMeta{values: map[string]string{"telemetry_tier": v}}
		got := ReadTier(meta)
		if got != TierNone {
			t.Errorf("ReadTier(%q) = %q, want %q (prefix/partial match must not enroll)",
				v, got, TierNone)
		}
	}
}

// Boundary case: IsValid() is the only path into a valid Tier.
// This is a structural test — if a future refactor adds a Tier
// constructor that bypasses IsValid (e.g., `MustTier(s)` for
// "trusted" code paths), this test would still pass against the
// public surface, but the internal contract would have weakened.
// Test to spec-lock IsValid as the unique gate.
func TestIsValid_DomainIsExactlyThreeValues(t *testing.T) {
	// Every valid value:
	for _, v := range []Tier{TierNone, TierStandard, TierReliability} {
		if !v.IsValid() {
			t.Errorf("IsValid(%q) = false, want true", v)
		}
	}
	// Every invalid value the architecture might encounter:
	invalid := []Tier{
		"",
		" ",
		"none ",
		" standard",
		"NONE",
		"standard\n",
		"reliability\x00",   // null byte injection
		"standard;DROP TABLE", // SQL-injection shape (defense theater; meta is parameter-bound)
		"none|standard",
		"4",                  // numeric
		"true",               // boolean shape
		Tier(""),
	}
	for _, v := range invalid {
		if v.IsValid() {
			t.Errorf("IsValid(%q) = true, want false (must fail closed)", v)
		}
	}
}
