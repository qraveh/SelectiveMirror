package telemetry

import (
	"strings"
	"testing"
)

// The test cases in this file mirror what PostgreSQL's
// `'<json>'::jsonb::text` would produce for the same inputs. If a future
// PG version changes JSONB normalization, these tests catch the drift.

func TestCanonicalJSON_LengthFirstSort(t *testing.T) {
	// The exact bug that broke SelectiveMirror's first end-to-end test:
	// keys of different lengths sort differently in PG vs naive
	// alphabetical.
	input := map[string]any{
		"hello":       "world",
		"test":        "valid",
		"reported_at": "2026-04-26T10:00:00Z",
	}
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	// PG length-first ordering: test (4), hello (5), reported_at (11)
	want := `{"test": "valid", "hello": "world", "reported_at": "2026-04-26T10:00:00Z"}`
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

func TestCanonicalJSON_TieBreakOnCodepoint(t *testing.T) {
	// Same length → fall back to codepoint (lexicographic byte) order.
	input := map[string]any{
		"abc": float64(1),
		"abd": float64(2),
		"abb": float64(3),
	}
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"abb": 3, "abc": 1, "abd": 2}`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCanonicalJSON_Primitives(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", `"hello"`},
		{"empty string", "", `""`},
		{"true", true, `true`},
		{"false", false, `false`},
		{"null", nil, `null`},
		{"int", 42, `42`},
		{"float-whole", float64(1), `1`},
		{"float-fractional", 3.14, `3.14`},
		{"negative", -7, `-7`},
		{"escape-quote", `say "hi"`, `"say \"hi\""`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalJSON(c.in)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestCanonicalJSON_Array(t *testing.T) {
	input := []any{"a", "b", "c"}
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `["a", "b", "c"]`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestCanonicalJSON_NestedObject(t *testing.T) {
	input := map[string]any{
		"outer": map[string]any{
			"abc": float64(1),
			"z":   float64(2),
		},
	}
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	// Outer has one key. Inner uses length-first: z (1), abc (3).
	want := `{"outer": {"z": 2, "abc": 1}}`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// TestCanonicalJSON_RejectsStruct guards against the silent-misordering
// bug: passing a struct (rather than map[string]any) used to fall through
// to encoding/json with alphabetical sort. That would HMAC-mismatch with
// the PG length-first canonicalization and fail RLS silently. The
// updated implementation returns a descriptive error instead.
func TestCanonicalJSON_RejectsStruct(t *testing.T) {
	type bugReport struct {
		Hello       string `json:"hello"`
		ReportedAt  string `json:"reported_at"`
		Test        string `json:"test"`
	}
	_, err := CanonicalJSON(bugReport{Hello: "world", ReportedAt: "2026-04-26T10:00:00Z", Test: "valid"})
	if err == nil {
		t.Fatal("expected error for struct input; got nil (would silently mis-canonicalize and HMAC-fail in production)")
	}
	if !strings.Contains(err.Error(), "unsupported value type") {
		t.Errorf("error message should mention unsupported type; got: %v", err)
	}
}

// TestCanonicalJSON_NoHTMLEscape asserts that the canonical form does
// NOT escape '<', '>', '&' to their \uXXXX forms. Go's encoding/json
// HTML-escapes those by default; PostgreSQL JSONB::text does not.
// Diverging on this would silently break HMAC verification for any
// payload whose strings contain those characters (sanitized log lines
// with shell pipes, HTML snippets, "X & Y", etc.).
//
// This is SM-167. The expected output below matches what
//
//	SELECT '{"k":"<a> & <b>"}'::jsonb::text
//
// returns from PostgreSQL.
func TestCanonicalJSON_NoHTMLEscape(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "lt-gt-amp in string",
			in:   map[string]any{"k": "<a> & <b>"},
			want: `{"k": "<a> & <b>"}`,
		},
		{
			name: "ampersand only",
			in:   map[string]any{"x": "A & B"},
			want: `{"x": "A & B"}`,
		},
		{
			name: "lt only",
			in:   map[string]any{"x": "< then more"},
			want: `{"x": "< then more"}`,
		},
		{
			name: "gt only",
			in:   map[string]any{"x": "more then >"},
			want: `{"x": "more then >"}`,
		},
		{
			name: "log-line-with-pipes-and-redirects",
			in: map[string]any{
				"line": "rclone copy src:foo dst:bar 2>&1 | grep ERROR",
			},
			want: `{"line": "rclone copy src:foo dst:bar 2>&1 | grep ERROR"}`,
		},
		{
			name: "bare primitive string with HTML chars",
			in:   "<html>&copy;</html>",
			want: `"<html>&copy;</html>"`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CanonicalJSON(c.in)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			if got != c.want {
				t.Errorf("got\n  %s\nwant\n  %s", got, c.want)
			}
			// Defense-in-depth: explicitly assert that the FORBIDDEN
			// \u-escaped forms (which Go's default json.Marshal emits)
			// are absent. The literal characters '<', '>', '&' are
			// the correct, expected output — what we don't want is the
			// 6-byte escape sequences PG JSONB never produces.
			//
			// We construct the forbidden patterns with string
			// concatenation so the file's source bytes can't get
			// auto-decoded by editors / Edit tools and silently turn
			// the test into a no-op.
			bs := string([]byte{'\\'})
			forbidden := []string{
				bs + "u003c", // '<'
				bs + "u003C", // alternate-case hex
				bs + "u003e", // '>'
				bs + "u003E",
				bs + "u0026", // '&'
			}
			for _, esc := range forbidden {
				if strings.Contains(got, esc) {
					t.Errorf("output contains forbidden \\u escape %q (Go default; PG JSONB does not produce this): %s", esc, got)
				}
			}
		})
	}
}

func TestCanonicalJSON_KeysWithHTMLChars(t *testing.T) {
	// We don't expect these in practice (snake_case schema), but the
	// encoder must NOT HTML-escape them for keys either, so a future
	// schema can't silently break verification.
	in := map[string]any{
		"a<b": float64(1),
		"a&b": float64(2),
	}
	got, err := CanonicalJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	// length-first equal (both 3); codepoint: '&' (0x26) < '<' (0x3C),
	// so "a&b" sorts first.
	want := `{"a&b": 2, "a<b": 1}`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	bs := string([]byte{'\\'})
	for _, esc := range []string{bs + "u003c", bs + "u003C", bs + "u0026"} {
		if strings.Contains(got, esc) {
			t.Errorf("key contains forbidden \\u escape %q (Go default; PG does not produce): %s", esc, got)
		}
	}
}

func TestCanonicalJSON_EmptyObjectAndArray(t *testing.T) {
	got1, _ := CanonicalJSON(map[string]any{})
	if got1 != `{}` {
		t.Errorf("empty map: got %q want {}", got1)
	}
	got2, _ := CanonicalJSON([]any{})
	if got2 != `[]` {
		t.Errorf("empty array: got %q want []", got2)
	}
}

func TestCanonicalJSON_RealisticBugReportPayload(t *testing.T) {
	// What an actual report-bug --submit might serialize:
	input := map[string]any{
		"schema_version": float64(1),
		"install_id":     "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		"source":         "report_bug",
		"client_version": "0.9.4",
		"reported_at":    "2026-04-26T13:00:00+00:00",
		"report_format":  "text_bundle",
		"report_text":    "smirror crashed during sync",
	}
	got, err := CanonicalJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	// Length-first key order, tie-broken by codepoint:
	//   source          (6)
	//   install_id      (10)
	//   report_text     (11)  - tied with reported_at; comparing codepoints
	//   reported_at     (11)    at position 6: '_' (0x5F) < 'e' (0x65),
	//                           so report_text < reported_at.
	//   report_format   (13)
	//   client_version  (14)  - c (0x63) < s (0x73), so client < schema
	//   schema_version  (14)
	want := `{"source": "report_bug", "install_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479", "report_text": "smirror crashed during sync", "reported_at": "2026-04-26T13:00:00+00:00", "report_format": "text_bundle", "client_version": "0.9.4", "schema_version": 1}`
	if got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}
