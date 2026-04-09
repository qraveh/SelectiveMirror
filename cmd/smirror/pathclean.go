package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// installationInfo holds the result of scanning PATH for smirror installations.
type installationInfo struct {
	CurrentExe    string   // resolved path of the running binary
	AllFound      []string // all smirror(.exe) paths found on PATH
	HasDuplicates bool     // len(AllFound) > 1
}

// warnMultipleInstallations prints a formatted warning about duplicate
// smirror installations. The entry matching currentExe is marked "(current)".
func warnMultipleInstallations(info installationInfo) {
	fmt.Fprintln(os.Stderr, "WARNING: Multiple smirror installations found on PATH:")
	for i, p := range info.AllFound {
		tag := ""
		if pathsEqual(p, info.CurrentExe) {
			tag = "  (current)"
		}
		fmt.Fprintf(os.Stderr, "  %d. %s%s\n", i+1, p, tag)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "This can cause version confusion. Best practice:")
	fmt.Fprintln(os.Stderr, "  - Keep only ONE installation (prefer the MSI install)")
	fmt.Fprintln(os.Stderr, "  - Remove stale copies to avoid running an outdated version")
}

// findPATHEntriesToRemove splits pathValue on the OS path-list separator and
// partitions entries into those whose cleaned form matches any of smirrorDirs
// (case-insensitive on Windows) and those that don't.
func findPATHEntriesToRemove(pathValue string, smirrorDirs []string) (toRemove, remaining []string) {
	if pathValue == "" {
		return nil, nil
	}
	sep := string(os.PathListSeparator)
	entries := strings.Split(pathValue, sep)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		cleaned := filepath.Clean(entry)
		matched := false
		for _, dir := range smirrorDirs {
			if pathsEqual(cleaned, dir) {
				matched = true
				break
			}
		}
		if matched {
			toRemove = append(toRemove, entry)
		} else {
			remaining = append(remaining, entry)
		}
	}
	return toRemove, remaining
}

// smirrorDirsFromInfo returns deduplicated directories containing smirror
// binaries (current exe + all found on PATH).
func smirrorDirsFromInfo(info installationInfo) []string {
	seen := make(map[string]bool)
	var dirs []string
	add := func(p string) {
		if p == "" {
			return
		}
		d := filepath.Clean(filepath.Dir(p))
		key := d
		if runtime.GOOS == "windows" {
			key = strings.ToLower(d)
		}
		if !seen[key] {
			seen[key] = true
			dirs = append(dirs, d)
		}
	}
	add(info.CurrentExe)
	for _, f := range info.AllFound {
		add(f)
	}
	return dirs
}

// pathsEqual compares two file paths. On Windows, comparison is
// case-insensitive; on other platforms it is exact.
func pathsEqual(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
