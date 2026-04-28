//go:build !windows

package watcher

// Non-Windows stub for IsReparsePoint. Reparse points are a Windows-NTFS
// concept; on POSIX, symlinks are the only "reparse-like" mechanism and
// they're already covered by os.ModeSymlink checks. SEC-H4 is Windows-
// specific.
func IsReparsePoint(path string) bool {
	return false
}
