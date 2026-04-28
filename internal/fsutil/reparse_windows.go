//go:build windows

// Package fsutil provides shared filesystem helpers used across the
// watcher, sync, and CLI packages. Functions here are pure and have no
// runtime state — they're safe to call from any goroutine.
package fsutil

import "golang.org/x/sys/windows"

// IsReparsePoint reports whether the given path is a Windows reparse
// point (junction, symlink, mount point, or other reparse type).
// SEC-H4 / SEC-M13: Go's os.ModeSymlink only detects symlinks, not
// junctions. WalkDir-style traversals must not recurse into junctions
// because they could point back into an ancestor and produce an
// unbounded loop, and symlinks-to-arbitrary-targets are a security
// concern in service mode (LocalSystem).
//
// Returns false on any error so a transient GetFileAttributes failure
// doesn't reject ordinary files.
func IsReparsePoint(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return false
	}
	if attrs == windows.INVALID_FILE_ATTRIBUTES {
		return false
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
