//go:build !windows

package fsutil

// IsReparsePoint stub for POSIX. Reparse points are an NTFS concept;
// on POSIX, symlinks are the only equivalent and they're already
// covered by os.ModeSymlink checks at the WalkDir level.
func IsReparsePoint(path string) bool {
	return false
}
