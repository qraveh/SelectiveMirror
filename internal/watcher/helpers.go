// Package watcher — pure helper functions extracted for testability.
// These functions contain the core decision logic from handleEvent, handleRemove,
// and related methods, separated from side effects (fsnotify, filesystem I/O, queue dispatch).
package watcher

import (
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/qraveh/SelectiveMirror/internal/filter"
)

// EventAction describes what the watcher should do with a filesystem event.
type EventAction int

const (
	ActionIgnore       EventAction = iota // skip this event entirely
	ActionSyncFile                        // enqueue file for sync
	ActionDeleteFile                      // enqueue file for delete
	ActionReloadFilter                    // hot-reload .syncignore
	ActionNewDir                          // new directory: setup watch + queue files
)

// ClassifyEvent determines the high-level action for a filesystem event
// based only on the event operation flags. Pure function, no side effects.
func ClassifyEvent(op fsnotify.Op) EventAction {
	if op.Has(fsnotify.Remove) {
		return ActionDeleteFile
	}
	if op.Has(fsnotify.Rename) {
		return ActionDeleteFile // old path is gone; Create will fire for new path
	}
	if op.Has(fsnotify.Create) || op.Has(fsnotify.Write) {
		return ActionSyncFile
	}
	return ActionIgnore
}

// ShouldSync determines whether a file at the given relative path should be
// synced, based on filter rules and file metadata. Pure function.
//
// Returns false if:
//   - the file is excluded by filter rules
//   - the file is not a regular file (pipe, socket, device)
//   - the file exceeds the max size limit
//   - the file info is nil (gone before we could stat it)
func ShouldSync(relPath string, fe *filter.Engine, info os.FileInfo, maxFileSize int64) bool {
	if info == nil {
		return false
	}
	if fe != nil && fe.IsExcluded(relPath) {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > maxFileSize {
		return false
	}
	return true
}

// ComputeRelPath computes the slash-normalized relative path of absPath
// under projectRoot. Returns ("", false) if the path is not under the root
// or if Rel fails. Pure function.
func ComputeRelPath(absPath, projectRoot string) (string, bool) {
	rel, err := filepath.Rel(projectRoot, absPath)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// IsSymlinkToDir checks whether the given FileInfo represents a symlink
// whose target is a directory. Requires both lstat info (to detect symlink)
// and the path (to Stat-follow the target).
//
// Returns true if: it's a symlink AND the target is a directory (or broken).
// Returns false if: not a symlink, or symlink to a regular file.
func IsSymlinkToDir(path string, linfo os.FileInfo) bool {
	if linfo == nil {
		return false
	}
	if linfo.Mode()&os.ModeSymlink == 0 {
		return false // not a symlink
	}
	// Follow the symlink to check target type
	target, err := os.Stat(path)
	if err != nil {
		return true // broken symlink — treat as dir-like (reject)
	}
	return target.IsDir()
}

// IsSyncIgnoreFile checks if absPath is the .syncignore file for a project's filter.
// Pure function (only uses filepath operations).
func IsSyncIgnoreFile(absPath string, fe *filter.Engine) bool {
	if fe == nil {
		return false
	}
	syncPath := fe.SyncIgnorePath()
	if syncPath == "" {
		return false
	}
	a, err1 := filepath.Abs(absPath)
	b, err2 := filepath.Abs(syncPath)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
