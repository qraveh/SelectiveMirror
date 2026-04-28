//go:build windows

package watcher

import "github.com/qraveh/SelectiveMirror/internal/fsutil"

// IsReparsePoint forwards to fsutil.IsReparsePoint. Kept as a package-
// local symbol so existing watcher code doesn't have to change its
// import set; the implementation moved to internal/fsutil so internal/
// sync and cmd/smirror can use the same check (SEC-M13: WalkDir
// callbacks need to refuse recursing into junctions to prevent loops).
func IsReparsePoint(path string) bool {
	return fsutil.IsReparsePoint(path)
}
