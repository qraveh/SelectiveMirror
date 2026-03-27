//go:build windows

// On Windows, fsnotify opens a directory handle for every watched directory.
// Windows Explorer detects these open handles via RestartManager and blocks
// rename/move/delete operations on watched directories with:
//   "The action can't be completed because the file or folder is opened in another program"
//
// Fix: enable fsnotify's built-in recursive watching (ReadDirectoryChangesW with
// bWatchSubtree=TRUE). This watches the entire project subtree through a SINGLE
// handle on the project root. Subdirectories have no open handles and can be
// freely manipulated in Explorer.
//
// fsnotify v1.9.0 has recursive watching implemented but gated behind an
// unexported variable (enableRecurse = false, "Only enabled in tests for now").
// We enable it via go:linkname. If a future fsnotify version removes this
// variable, the build will fail with a clear linker error — not silently.

package watcher

import (
	_ "unsafe" // required for go:linkname
)

//go:linkname fsnotifyEnableRecurse github.com/fsnotify/fsnotify.enableRecurse
var fsnotifyEnableRecurse bool

func init() {
	fsnotifyEnableRecurse = true
}

const supportsRecursiveWatch = true
