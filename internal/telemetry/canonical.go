// Canonical JSON serialization for HMAC compatibility with PostgreSQL JSONB.
//
// PostgreSQL JSONB sorts object keys by length first, then by Unicode
// codepoint. Standard JSON serializers (Go's encoding/json with
// MarshalIndent disabled, Python's json.dumps(sort_keys=True)) sort by
// codepoint only. The two diverge whenever keys in an object have
// different lengths.
//
// Server-side HMAC verification uses (payload - 'version_hmac')::text::bytea
// for canonical bytes — that's PG's length-first JSONB::text. For client-
// computed HMACs to verify, the client MUST produce the same byte sequence.
//
// This file's CanonicalJSON does that. It must be kept in sync with:
//   - test/telemetry-validation.py:canonical_json (Python reference)
//   - PostgreSQL's jsonb::text output for representative payloads
//
// See docs/telemetry-microserver-architecture.md "Canonical JSON for HMAC".

package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// jsonEncodeNoHTMLEscape serializes a primitive (string / number / bool /
// null) using encoding/json with HTML escaping DISABLED. This is the
// critical correctness knob for HMAC parity with PostgreSQL JSONB:
//
//   - Go's json.Marshal escapes '<', '>', and '&' as <, >, &.
//     This is a defense-in-depth feature aimed at HTML/XML embedding.
//   - PostgreSQL JSONB::text does NOT escape those characters; they
//     appear literally in the canonical text.
//
// If any payload field's string value contains any of those three
// characters (e.g., a sanitized bug-report log line containing "&" or
// an HTML snippet), the client-computed HMAC and the server-recomputed
// HMAC diverge — and RLS rejects the row with no recovery path. The
// fix is to use json.Encoder with SetEscapeHTML(false), which produces
// the literal-character form that matches PG.
//
// json.Encoder.Encode appends a trailing newline; we trim it.
func jsonEncodeNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	// Trim trailing newline that Encoder appends after every value.
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// CanonicalJSON serializes value to a string matching PostgreSQL JSONB::text
// byte-for-byte for the supported value types (objects, arrays, strings,
// numbers, booleans, null).
//
// Object keys are sorted by (len(key), key) — length first, then codepoint.
// Output uses ": " between key and value, ", " between items.
//
// Returns an error only if encoding/json fails on a primitive (extremely
// rare; would indicate something like an unsupported type or a NaN/Inf
// float).
func CanonicalJSON(value any) (string, error) {
	var b strings.Builder
	if err := writeCanonical(&b, value); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeCanonical(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		// PG length-first, then codepoint (lexicographic byte comparison
		// of the UTF-8 encoding equals codepoint order for valid UTF-8).
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			// Keys: must NOT HTML-escape (PG JSONB doesn't). Snake-case
			// identifiers like our schema uses don't contain '<', '>',
			// '&' — but use the safe encoder anyway so a future schema
			// addition can't silently break HMAC parity.
			kJSON, err := jsonEncodeNoHTMLEscape(k)
			if err != nil {
				return err
			}
			b.Write(kJSON)
			b.WriteString(": ")
			if err := writeCanonical(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	case nil, bool, string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64,
		json.Number:
		// Allowed primitives. We MUST disable HTML escaping here:
		// json.Marshal would emit "<" / ">" / "&" for
		// '<' / '>' / '&', which PG JSONB::text does NOT. The result
		// would be byte-mismatched bytes the server's HMAC verifier
		// can't reproduce. SM-167.
		bs, err := jsonEncodeNoHTMLEscape(v)
		if err != nil {
			return err
		}
		b.Write(bs)
		return nil
	default:
		// IMPORTANT: do NOT silently json.Marshal arbitrary types here.
		// A struct passed in would marshal via reflection with
		// encoding/json's default alphabetical key ordering, NOT the
		// PG length-first ordering CanonicalJSON guarantees. The
		// resulting HMAC would silently fail server-side verification.
		// Force the caller to convert to map[string]any first.
		return fmt.Errorf(
			"telemetry.CanonicalJSON: unsupported value type %T; convert to map[string]any first (cannot rely on encoding/json key order, which differs from PG JSONB length-first ordering)",
			v,
		)
	}
}
