//go:build !windows

package watcher

import "github.com/qraveh/SelectiveMirror/internal/fsutil"

func IsReparsePoint(path string) bool {
	return fsutil.IsReparsePoint(path)
}
