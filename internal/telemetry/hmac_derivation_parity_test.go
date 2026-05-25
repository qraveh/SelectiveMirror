// SM-219 regression ratchet — the per-version HMAC key is derived in
// THREE separate code bases, and pre-SM-219 they didn't all do the same
// thing:
//
//   1. Go (this package + .github/workflows/release.yml's PowerShell
//      derivation that feeds the binary's buildKey ldflag):
//          derived = HMAC-SHA256(bytes.fromhex(MASTER), VERSION)
//
//   2. Python (scripts/telemetry-v2-smoke-test.py):
//      Pre-SM-219:  HMAC-SHA256(MASTER.encode("utf-8"), VERSION)  ← BUG
//      Post-SM-219: HMAC-SHA256(bytes.fromhex(MASTER), VERSION)   ← matches Go
//
//   3. SQL (docs/telemetry-v2.sql verify_versioned_hmac):
//      Pre-SM-219:  hmac(version::BYTEA, master_key::BYTEA, 'sha256')   ← BUG
//      Post-SM-219: hmac(version::BYTEA, decode(master_key, 'hex'), 'sha256')
//
// All three sides MUST produce byte-identical derived keys for a given
// (master, version) pair, otherwise the HMAC on the canonical payload
// won't verify and CI-built binaries will be silently rejected (which
// is exactly what happened in v1.0.0 — telemetry rollup tables stayed
// empty for the entire v1.0.0 cycle because of this mismatch).
//
// This file guards against any single side drifting back. The Go test
// computes the canonical derivation against a fixed test vector; the
// Python and SQL tests are source-property regexes that assert the
// correct form is in the source files (the form-equivalence implies
// byte-equivalence because the inputs are byte-identical strings and
// the algorithm is HMAC-SHA256).

package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Fixed test vector used across all three derivation sides. The master
// key is a 32-byte random-looking value encoded as 64 hex chars. The
// version is a typical SemVer string.
const (
	parityTestMasterHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	parityTestVersion   = "1.2.3"

	// Golden expected derived-key hex, computed via:
	//   python3 -c "import hmac, hashlib; print(hmac.new(
	//       bytes.fromhex('0123456789abcdef...cdef'),
	//       b'1.2.3', hashlib.sha256).hexdigest())"
	parityTestExpectedDerivedHex = "46a1787f9d61137e6b5905667f193a02960fff985d83ceddedb566c9435b7f06"
)

// TestHMACDerivationParity_Go asserts that the Go derivation pattern
// — the same pattern release.yml's PowerShell uses to compute the
// derived key it embeds in the binary's buildKey — produces the
// golden expected output for the fixed test vector.
//
// If this fails, either:
//   - release.yml has diverged from the convention (unlikely; would
//     require a corresponding fix to .github/workflows/release.yml).
//   - The expected golden value was computed wrong (unlikely if the
//     Python smoke-test still passes its own end-to-end check against
//     the Worker).
//   - The HMAC-SHA256 stdlib has somehow drifted (impossible).
//
// If this passes but the smoke test breaks, the divergence is on the
// Python or SQL side — see the source-property tests below.
func TestHMACDerivationParity_Go(t *testing.T) {
	masterBytes, err := hex.DecodeString(parityTestMasterHex)
	if err != nil {
		t.Fatalf("test vector master hex is malformed: %v", err)
	}

	mac := hmac.New(sha256.New, masterBytes)
	mac.Write([]byte(parityTestVersion))
	got := hex.EncodeToString(mac.Sum(nil))

	if got != parityTestExpectedDerivedHex {
		t.Errorf("Go HMAC derivation produced unexpected output:\n  got:  %s\n  want: %s\n"+
			"This means release.yml's derivation or the convention has drifted "+
			"from the Python smoke-test / SQL function expectations. The CI-built "+
			"binary would be silently rejected by the Worker.",
			got, parityTestExpectedDerivedHex)
	}
}

// TestHMACDerivationParity_PythonSourceProperty asserts that
// scripts/telemetry-v2-smoke-test.py uses the correct
// `bytes.fromhex(master_key)` form, not the pre-SM-219 broken
// `master_key.encode("utf-8")` form.
//
// Source-property tests don't actually run Python; they assert the
// source-code shape that implies byte-equivalence with Go. The shape
// constraint is: HMAC's key argument must be `bytes.fromhex(<master>)`
// — anything else (UTF-8 of hex string, raw string passed directly,
// base64 decode, etc.) breaks parity with Go and SQL.
func TestHMACDerivationParity_PythonSourceProperty(t *testing.T) {
	src, err := readRepoFile(t, "scripts", "telemetry-v2-smoke-test.py")
	if err != nil {
		t.Skipf("Python smoke-test source not found from %s: %v", whereAmI(), err)
	}

	// Must contain bytes.fromhex(master_key) inside the derive function.
	mustHaveHexDecode := regexp.MustCompile(`hmac\.new\(\s*bytes\.fromhex\(master_key\)`)
	if !mustHaveHexDecode.Match(src) {
		t.Errorf("scripts/telemetry-v2-smoke-test.py must derive the per-version key " +
			"via `hmac.new(bytes.fromhex(master_key), ...)` to match Go and SQL; " +
			"could not find this pattern.")
	}

	// MUST NOT contain master_key.encode( anywhere inside an hmac.new(
	// call — that's the SM-219 bug form.
	bugForm := regexp.MustCompile(`hmac\.new\(\s*master_key\.encode\(`)
	if bugForm.Match(src) {
		t.Errorf("scripts/telemetry-v2-smoke-test.py contains `hmac.new(master_key.encode(...)` — " +
			"this is the pre-SM-219 broken derivation. Use `bytes.fromhex(master_key)` instead.")
	}
}

// TestHMACDerivationParity_SQLSourceProperty asserts that
// docs/telemetry-v2.sql derives the per-version key by hex-decoding
// master_key, not by casting it to BYTEA (which gives UTF-8 of the
// hex string — pre-SM-219 form).
//
// This is the most critical of the three checks because the SQL
// function is what the live Worker calls; a regression on the SQL
// side requires a database migration to fix, not just a code edit.
func TestHMACDerivationParity_SQLSourceProperty(t *testing.T) {
	src, err := readRepoFile(t, "docs", "telemetry-v2.sql")
	if err != nil {
		t.Skipf("telemetry SQL source not found from %s: %v", whereAmI(), err)
	}

	// Must contain decode(master_key, 'hex') inside an hmac(...) call.
	mustHaveHexDecode := regexp.MustCompile(`hmac\([^)]*decode\(master_key,\s*'hex'\)`)
	if !mustHaveHexDecode.Match(src) {
		t.Errorf("docs/telemetry-v2.sql must derive the per-version key via " +
			"`hmac(..., decode(master_key, 'hex'), 'sha256')` to match Go and Python; " +
			"could not find this pattern. Pre-SM-219 used `master_key::BYTEA` which " +
			"hashes the literal hex characters of the key and breaks parity with " +
			"every CI-built binary.")
	}

	// MUST NOT contain master_key::BYTEA inside the verify function —
	// that's the SM-219 bug form.
	bugForm := regexp.MustCompile(`hmac\([^)]*master_key::BYTEA`)
	if bugForm.Match(src) {
		t.Errorf("docs/telemetry-v2.sql contains `hmac(..., master_key::BYTEA, ...)` — " +
			"this is the pre-SM-219 broken derivation. The cast UTF-8-encodes the " +
			"hex characters instead of hex-decoding them. Use `decode(master_key, 'hex')`.")
	}
}

// whereAmI returns the absolute path of the current working directory,
// for use in skip messages. Tests sometimes run from the package dir
// or from the repo root depending on the runner.
func whereAmI() string {
	p, err := os.Getwd()
	if err != nil {
		return "(cwd: unknown)"
	}
	return p
}

// readRepoFile climbs the directory tree from the test's working
// directory until it finds the named file, then reads it. The Go
// test working directory is the package dir (`internal/telemetry`),
// which is two levels below the repo root.
func readRepoFile(t *testing.T, parts ...string) ([]byte, error) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// Walk up to find a directory that contains the requested file.
	// Bounded to 8 levels — generous but stops a runaway loop.
	dir := cwd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(append([]string{dir}, parts...)...)
		if data, err := os.ReadFile(candidate); err == nil {
			return data, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, os.ErrNotExist
}
