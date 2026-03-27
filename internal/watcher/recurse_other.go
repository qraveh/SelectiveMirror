//go:build !windows

// On non-Windows platforms (Linux inotify, macOS kqueue), recursive watching
// is not supported by the kernel API. We manually walk and add each subdirectory.

package watcher

const supportsRecursiveWatch = false
