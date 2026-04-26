// HMAC signing for SelectiveMirror telemetry payloads.
//
// Each release embeds a per-version derived key, computed by CI as
//   HMAC-SHA256(master_key, version)
// where master_key is the SMIRROR_TELEMETRY_MASTER_KEY GitHub Actions
// secret. The derived key (NOT the master) is injected at build time via
//   -ldflags "-X github.com/qraveh/SelectiveMirror/internal/telemetry.buildKey=<hex>"
//
// -dev / local builds without the ldflag get an empty buildKey and cannot
// sign telemetry. They will fail SignPayload with ErrNoBuildKey, which
// callers can detect to skip submission gracefully.
//
// See docs/telemetry-microserver-architecture.md "HMAC signing scheme".

package telemetry

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// buildKey is the per-version derived HMAC key, embedded at build time.
// Empty string in -dev / non-CI builds.
var buildKey = ""

// ErrNoBuildKey is returned by SignPayload when buildKey is unset
// (typically -dev builds, local `go build` without -ldflags, or release
// builds where SMIRROR_TELEMETRY_MASTER_KEY secret was not set).
var ErrNoBuildKey = errors.New("telemetry: build key not embedded; this build cannot sign telemetry")

// SignPayload computes the HMAC-SHA256 hex string over the canonical-JSON
// payload bytes (without the version_hmac field).
//
// The caller is expected to:
//  1. Compose the inner payload (without version_hmac).
//  2. Serialize it via CanonicalJSON.
//  3. Pass the canonical text here.
//  4. Add the returned hex string as the "version_hmac" field of the outer
//     payload before submission.
func SignPayload(canonicalText string) (string, error) {
	if buildKey == "" {
		return "", ErrNoBuildKey
	}
	keyBytes, err := hex.DecodeString(buildKey)
	if err != nil {
		return "", fmt.Errorf("telemetry: build key is not valid hex: %w", err)
	}
	if len(keyBytes) != 32 {
		return "", fmt.Errorf("telemetry: build key length is %d bytes; expected 32", len(keyBytes))
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte(canonicalText))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// HasBuildKey reports whether a non-empty buildKey was injected at build
// time. Use this for conditional behavior — e.g., skip telemetry submission
// in -dev builds without raising errors at every send attempt.
func HasBuildKey() bool {
	return buildKey != ""
}

// BuildKeyFingerprint returns a short hex fingerprint derived from the
// build key. Used for diagnostic display in `smirror version` to confirm
// telemetry signing is enabled. Never returns the raw key.
//
// Returns "none" when buildKey is empty, "invalid" when buildKey is set
// but malformed.
func BuildKeyFingerprint() string {
	if buildKey == "" {
		return "none"
	}
	keyBytes, err := hex.DecodeString(buildKey)
	if err != nil || len(keyBytes) != 32 {
		return "invalid"
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write([]byte("smirror-build-key-fingerprint"))
	return hex.EncodeToString(mac.Sum(nil))[:8]
}
