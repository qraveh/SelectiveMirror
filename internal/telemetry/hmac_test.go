package telemetry

import (
	"strings"
	"testing"
)

const testKey32 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSignPayload_NoBuildKey(t *testing.T) {
	saved := buildKey
	buildKey = ""
	t.Cleanup(func() { buildKey = saved })

	_, err := SignPayload("anything")
	if err != ErrNoBuildKey {
		t.Errorf("expected ErrNoBuildKey, got %v", err)
	}
}

func TestSignPayload_Deterministic(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	sig1, err := SignPayload("hello world")
	if err != nil {
		t.Fatal(err)
	}
	sig2, err := SignPayload("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if sig1 != sig2 {
		t.Errorf("HMAC not deterministic: %s vs %s", sig1, sig2)
	}
	if len(sig1) != 64 {
		t.Errorf("HMAC hex length = %d, want 64", len(sig1))
	}
	for _, c := range sig1 {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("HMAC contains non-hex char %q in %s", c, sig1)
			break
		}
	}
}

func TestSignPayload_DifferentInputDifferentSig(t *testing.T) {
	saved := buildKey
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	sig1, _ := SignPayload("hello")
	sig2, _ := SignPayload("hellp")
	if sig1 == sig2 {
		t.Error("different inputs produced same HMAC")
	}
}

func TestSignPayload_InvalidHexKey(t *testing.T) {
	saved := buildKey
	buildKey = "not-a-hex-string"
	t.Cleanup(func() { buildKey = saved })

	_, err := SignPayload("anything")
	if err == nil {
		t.Error("expected error for non-hex buildKey")
	}
	if err == ErrNoBuildKey {
		t.Error("got ErrNoBuildKey for invalid-hex case; should be a different error")
	}
}

func TestSignPayload_WrongKeyLength(t *testing.T) {
	saved := buildKey
	// 16 hex chars = 8 bytes — too short
	buildKey = "0123456789abcdef"
	t.Cleanup(func() { buildKey = saved })

	_, err := SignPayload("anything")
	if err == nil {
		t.Error("expected error for short key")
	}
}

func TestHasBuildKey(t *testing.T) {
	saved := buildKey
	t.Cleanup(func() { buildKey = saved })

	buildKey = ""
	if HasBuildKey() {
		t.Error("HasBuildKey() = true for empty buildKey")
	}
	buildKey = testKey32
	if !HasBuildKey() {
		t.Error("HasBuildKey() = false for non-empty buildKey")
	}
}

func TestBuildKeyFingerprint(t *testing.T) {
	saved := buildKey
	t.Cleanup(func() { buildKey = saved })

	buildKey = ""
	if got := BuildKeyFingerprint(); got != "none" {
		t.Errorf("got %q, want 'none'", got)
	}

	buildKey = "not-hex"
	if got := BuildKeyFingerprint(); got != "invalid" {
		t.Errorf("got %q, want 'invalid'", got)
	}

	buildKey = testKey32
	fp := BuildKeyFingerprint()
	if len(fp) != 8 {
		t.Errorf("fingerprint length = %d, want 8", len(fp))
	}
	if fp2 := BuildKeyFingerprint(); fp != fp2 {
		t.Errorf("fingerprint not deterministic: %s vs %s", fp, fp2)
	}
}

// TestSignPayload_MatchesPythonReference verifies that this Go implementation
// produces the same HMAC as the reference Python implementation in
// test/telemetry-validation.py for a known input. Hardcoded expected value
// computed offline from the same test inputs.
func TestSignPayload_MatchesPythonReference(t *testing.T) {
	saved := buildKey
	// Use the test key that the reference Python implementation uses
	// for cross-language verification. This test also confirms the
	// hex.DecodeString path works correctly.
	buildKey = testKey32
	t.Cleanup(func() { buildKey = saved })

	canonical := `{"hello": "world"}`
	sig, err := SignPayload(canonical)
	if err != nil {
		t.Fatal(err)
	}
	// Computed once via:
	//   python -c "import hmac, hashlib; print(hmac.new(bytes.fromhex('0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'), b'{\"hello\": \"world\"}', hashlib.sha256).hexdigest())"
	want := "0c735f42c86c08d9a5e4b9a548fa712431aa15a10edf7e1fc2e220812a089b00"
	if sig != want {
		t.Errorf("got\n  %s\nwant\n  %s\n(if want is wrong, recompute via Python with the test key)", sig, want)
	}
}
