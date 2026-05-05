//go:build !windows

package lock

import "syscall"

// isProcessAlive reports whether a process with the given PID is currently
// running. Used by # to detect stale lock files. POSIX path: send
// signal 0 (no-op) and inspect errno.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		// ESRCH = no such process; EPERM = process exists but we can't
		// signal it (different user). Treat EPERM as alive.
		if err == syscall.EPERM {
			return true
		}
		return false
	}
	return true
}
