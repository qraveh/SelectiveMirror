package main

import (
	"reflect"
	"testing"
)

// when --config is given multiple times,
// last-wins. Previous behavior broke out of the parsing loop on the FIRST
// occurrence and left subsequent --config args in the stripped slice.
func TestExtractConfigPath_LastWins(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "no --config",
			in:       []string{"version"},
			wantPath: "default", // unchanged
			wantArgs: []string{"version"},
		},
		{
			name:     "single separate-form",
			in:       []string{"--config", "/tmp/c.yaml", "status"},
			wantPath: "/tmp/c.yaml",
			wantArgs: []string{"status"},
		},
		{
			name:     "single equals-form",
			in:       []string{"--config=/tmp/c.yaml", "status"},
			wantPath: "/tmp/c.yaml",
			wantArgs: []string{"status"},
		},
		{
			name:     "double --config: last wins (separate-form)",
			in:       []string{"--config", "/tmp/bogus.yaml", "--config", "/tmp/good.yaml", "version"},
			wantPath: "/tmp/good.yaml",
			wantArgs: []string{"version"},
		},
		{
			name:     "double --config: last wins (mixed forms)",
			in:       []string{"--config=/tmp/bogus.yaml", "--config", "/tmp/good.yaml", "version"},
			wantPath: "/tmp/good.yaml",
			wantArgs: []string{"version"},
		},
		{
			name:     "triple --config: still last wins",
			in:       []string{"--config", "/a", "--config", "/b", "--config=/c", "status"},
			wantPath: "/c",
			wantArgs: []string{"status"},
		},
		{
			name:     "--config interleaved with other args",
			in:       []string{"start", "--config", "/x", "--foo", "--config=/y"},
			wantPath: "/y",
			wantArgs: []string{"start", "--foo"},
		},
		// SM-187: trailing --config without a value used to be silently
		// dropped (path stays default). The new contract is to return
		// an error so the user gets an explicit "missing value" exit
		// rather than a surprise default-config invocation. Test moved
		// to TestExtractConfigPath_MissingValue below.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "default"
			gotArgs, err := extractConfigPathErr(tc.in, &path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

// SM-187: --config with no value or with next-token-is-a-flag must
// produce an error (caller exits with ExitConfigError) rather than
// silently dropping.
func TestExtractConfigPath_MissingValue(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"trailing --config without value", []string{"version", "--config"}},
		{"--config followed by another flag", []string{"status", "--config", "--foo"}},
		{"--config followed by --foo=bar form", []string{"status", "--config", "--foo=bar"}},
		{"empty --config= value", []string{"status", "--config="}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "default"
			_, err := extractConfigPathErr(tc.in, &path)
			if err == nil {
				t.Fatalf("expected error for %v, got none (path=%q)", tc.in, path)
			}
		})
	}
}
