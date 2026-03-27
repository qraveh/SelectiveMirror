package rclone

import (
	"testing"
)

func TestParseVersionOutput_Standard(t *testing.T) {
	output := `rclone v1.68.2
- os/version: Microsoft Windows 11 Home 10.0.26200.5603 (64 bit)
- os/kernel: windows (amd64)
- os/type: windows
- os/arch: amd64 (Intel(R) Core(TM) Ultra 9 275HX)
- go/version: go1.23.4
- go/linking: static
- go/tags: cmount`

	ver, osArch := parseVersionOutput(output)
	if ver.Major != 1 || ver.Minor != 68 || ver.Patch != 2 {
		t.Errorf("version = %d.%d.%d, want 1.68.2", ver.Major, ver.Minor, ver.Patch)
	}
	if ver.String() != "1.68.2" {
		t.Errorf("String() = %q, want %q", ver.String(), "1.68.2")
	}
	if osArch != "windows (amd64)" {
		t.Errorf("osArch = %q, want %q", osArch, "windows (amd64)")
	}
}

func TestParseVersionOutput_NoPrefix(t *testing.T) {
	output := "rclone 1.73.0\n"
	ver, _ := parseVersionOutput(output)
	if ver.Major != 1 || ver.Minor != 73 || ver.Patch != 0 {
		t.Errorf("version = %d.%d.%d, want 1.73.0", ver.Major, ver.Minor, ver.Patch)
	}
}

func TestParseVersionOutput_Empty(t *testing.T) {
	ver, _ := parseVersionOutput("")
	if ver.Major != 0 || ver.Minor != 0 || ver.Patch != 0 {
		t.Errorf("empty output should give 0.0.0, got %s", ver)
	}
}

func TestVersion_AtLeast(t *testing.T) {
	tests := []struct {
		ver     Version
		major   int
		minor   int
		patch   int
		want    bool
	}{
		{Version{1, 73, 0, ""}, 1, 73, 0, true},  // exact match
		{Version{1, 73, 1, ""}, 1, 73, 0, true},  // patch higher
		{Version{1, 74, 0, ""}, 1, 73, 0, true},  // minor higher
		{Version{2, 0, 0, ""}, 1, 73, 0, true},   // major higher
		{Version{1, 72, 9, ""}, 1, 73, 0, false},  // minor lower
		{Version{1, 73, 0, ""}, 1, 73, 1, false},  // patch lower
		{Version{0, 99, 9, ""}, 1, 0, 0, false},   // major lower
	}

	for _, tt := range tests {
		got := tt.ver.AtLeast(tt.major, tt.minor, tt.patch)
		if got != tt.want {
			t.Errorf("%s.AtLeast(%d,%d,%d) = %v, want %v",
				tt.ver, tt.major, tt.minor, tt.patch, got, tt.want)
		}
	}
}

func TestCompatCheck_Full(t *testing.T) {
	info := &Info{Version: Version{1, 73, 0, ""}}
	compat, msg := info.CompatCheck()
	if compat != CompatFull {
		t.Errorf("1.73.0 should be CompatFull, got %d", compat)
	}
	if msg == "" {
		t.Error("message should not be empty")
	}
}

func TestCompatCheck_Partial(t *testing.T) {
	info := &Info{Version: Version{1, 68, 2, ""}}
	compat, _ := info.CompatCheck()
	if compat != CompatPartial {
		t.Errorf("1.68.2 should be CompatPartial, got %d", compat)
	}
}

func TestCompatCheck_None(t *testing.T) {
	info := &Info{Version: Version{1, 40, 0, ""}}
	compat, _ := info.CompatCheck()
	if compat != CompatNone {
		t.Errorf("1.40.0 should be CompatNone, got %d", compat)
	}
}

func TestDetect_SystemRclone(t *testing.T) {
	// This test requires rclone to be installed on the system.
	// Skip if not available.
	info, err := Detect("")
	if err != nil {
		t.Skipf("rclone not found on system: %v", err)
	}

	if info.Path == "" {
		t.Error("path should not be empty")
	}
	if info.Version.Major == 0 && info.Version.Minor == 0 {
		t.Error("version should be parsed")
	}

	t.Logf("found rclone %s at %s (%s)", info.Version, info.Path, info.OS)

	compat, msg := info.CompatCheck()
	t.Logf("compatibility: %s", msg)
	_ = compat
}

func TestSearchDescription(t *testing.T) {
	desc := searchDescription("")
	if desc == "" {
		t.Error("search description should not be empty")
	}
}
