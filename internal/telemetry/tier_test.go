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
