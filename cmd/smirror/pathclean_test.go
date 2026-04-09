package main

import (
	"os"
	"runtime"
	"testing"
)

func TestFindPATHEntriesToRemove_Basic(t *testing.T) {
	sep := string(os.PathListSeparator)
	path := "C:\\Go\\bin" + sep + "C:\\Program Files\\SelectiveMirror" + sep + "C:\\Windows"
	dirs := []string{"C:\\Program Files\\SelectiveMirror"}

	toRemove, remaining := findPATHEntriesToRemove(path, dirs)

	if len(toRemove) != 1 || toRemove[0] != "C:\\Program Files\\SelectiveMirror" {
		t.Errorf("toRemove = %v, want [C:\\Program Files\\SelectiveMirror]", toRemove)
	}
	if len(remaining) != 2 {
		t.Errorf("remaining = %v, want 2 entries", remaining)
	}
}

func TestFindPATHEntriesToRemove_CaseInsensitive(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive comparison is Windows-only")
	}
	sep := string(os.PathListSeparator)
	path := "c:\\program files\\selectivemirror" + sep + "C:\\Windows"
	dirs := []string{"C:\\Program Files\\SelectiveMirror"}

	toRemove, remaining := findPATHEntriesToRemove(path, dirs)

	if len(toRemove) != 1 {
		t.Errorf("toRemove = %v, want 1 entry (case-insensitive match)", toRemove)
	}
	if len(remaining) != 1 {
		t.Errorf("remaining = %v, want 1 entry", remaining)
	}
}

func TestFindPATHEntriesToRemove_NoMatch(t *testing.T) {
	sep := string(os.PathListSeparator)
	path := "C:\\Go\\bin" + sep + "C:\\Windows"
	dirs := []string{"C:\\Program Files\\SelectiveMirror"}

	toRemove, remaining := findPATHEntriesToRemove(path, dirs)

	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}
	if len(remaining) != 2 {
		t.Errorf("remaining = %v, want 2 entries", remaining)
	}
}

func TestFindPATHEntriesToRemove_MultipleMatches(t *testing.T) {
	sep := string(os.PathListSeparator)
	path := "C:\\tools\\smirror" + sep + "C:\\Go\\bin" + sep + "C:\\Program Files\\SelectiveMirror"
	dirs := []string{"C:\\tools\\smirror", "C:\\Program Files\\SelectiveMirror"}

	toRemove, remaining := findPATHEntriesToRemove(path, dirs)

	if len(toRemove) != 2 {
		t.Errorf("toRemove = %v, want 2 entries", toRemove)
	}
	if len(remaining) != 1 {
		t.Errorf("remaining = %v, want 1 entry", remaining)
	}
}

func TestFindPATHEntriesToRemove_Empty(t *testing.T) {
	toRemove, remaining := findPATHEntriesToRemove("", []string{"C:\\foo"})

	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %v, want empty", remaining)
	}
}

func TestFindPATHEntriesToRemove_TrailingSeparator(t *testing.T) {
	sep := string(os.PathListSeparator)
	path := "C:\\Go\\bin" + sep + "C:\\Program Files\\SelectiveMirror" + sep
	dirs := []string{"C:\\Program Files\\SelectiveMirror"}

	toRemove, remaining := findPATHEntriesToRemove(path, dirs)

	if len(toRemove) != 1 {
		t.Errorf("toRemove = %v, want 1 entry", toRemove)
	}
	if len(remaining) != 1 {
		t.Errorf("remaining = %v, want 1 entry (trailing empty skipped)", remaining)
	}
}

func TestSmirrorDirsFromInfo_Dedup(t *testing.T) {
	info := installationInfo{
		CurrentExe: "C:\\Program Files\\SelectiveMirror\\smirror.exe",
		AllFound: []string{
			"C:\\Program Files\\SelectiveMirror\\smirror.exe",
			"C:\\tools\\smirror.exe",
		},
	}
	dirs := smirrorDirsFromInfo(info)

	if len(dirs) != 2 {
		t.Errorf("dirs = %v, want 2 unique directories", dirs)
	}
}

func TestSmirrorDirsFromInfo_Empty(t *testing.T) {
	info := installationInfo{}
	dirs := smirrorDirsFromInfo(info)
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want empty", dirs)
	}
}

func TestPathsEqual(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"C:\\foo\\bar", "C:\\foo\\bar", true},
		{"C:\\foo\\bar\\", "C:\\foo\\bar", true}, // trailing slash
		{"C:\\foo\\bar", "C:\\baz", false},
	}
	if runtime.GOOS == "windows" {
		tests = append(tests, struct {
			a, b string
			want bool
		}{"c:\\foo\\bar", "C:\\Foo\\Bar", true}) // case-insensitive on Windows
	}
	for _, tt := range tests {
		got := pathsEqual(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("pathsEqual(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
