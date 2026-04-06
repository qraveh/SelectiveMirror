//go:build !windows

package logging

import "os"

// openShared opens a file for append. On Unix, file sharing is the default.
func openShared(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}
