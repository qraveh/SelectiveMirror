package main

import (
	"reflect"
	"testing"
)

// GAP-6 (panel review 2026-04-28): when --config is given multiple times,
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
		{
			name:     "trailing --config without value",
			in:       []string{"version", "--config"},
			wantPath: "default", // unchanged
			wantArgs: []string{"version"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := "default"
			gotArgs := extractConfigPath(tc.in, &path)
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}
