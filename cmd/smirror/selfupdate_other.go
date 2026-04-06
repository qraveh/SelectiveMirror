//go:build !windows

package main

import "os"

// isAdmin reports whether the current process is running as root on Unix.
func isAdmin() bool {
	return os.Getuid() == 0
}
