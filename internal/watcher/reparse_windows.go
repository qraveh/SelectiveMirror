//go:build windows

package watcher

import "golang.org/x/sys/windows"

// IsReparsePoint reports whether the given path is a Windows reparse point —
// a junction, symlink, or other reparse-point type. SEC-H4: Go's os.ModeSymlink
// detects symlinks but NOT directory junctions, which are a different Windows
// reparse-point class created by `mklink /J`. Junctions in a watched mirror
// could otherwise smuggle paths outside the project root past the
// symlink-rejection logic in sync.go and watcher.go.
//
// Returns false on any error (treat unknown as "not reparse" so we don't
// reject ordinary files when GetFileAttributes fails for unrelated reasons).
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
