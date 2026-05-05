//go:build !windows

package main

import "path/filepath"

// resolveRealPath on non-Windows platforms uses filepath.EvalSymlinks
// (POSIX symlink resolution) → filepath.Abs as fallback. NTFS junctions
// don't exist outside Windows, so the Windows-only
// GetFinalPathNameByHandleW path isn't needed. SelectiveMirror is
// Windows-first; this stub exists so the cmd/smirror package compiles
// on Linux (as system-validation's go-build step does for the
// telemetry-emulation workflow). Functional non-Windows behavior is
// not exercised by the test suite.
func resolveRealPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}
