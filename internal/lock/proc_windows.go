//go:build windows

package lock

import "golang.org/x/sys/windows"

// isProcessAlive reports whether a process with the given PID is currently
// running. Used by GAP-9 to detect stale lock files.
//
// We use OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION (the lowest
// access level that still lets us call GetExitCodeProcess) so the check
// works for processes owned by other users when smirror is running as
// admin. Closed handles do not count as "alive".
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	// gosec G115 nolint: pid is gated > 0 above; Windows PIDs are DWORD-bounded.
	h, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec
	if err != nil {
		// ERROR_INVALID_PARAMETER == process never existed or recently exited.
		// ERROR_ACCESS_DENIED == process exists but our token can't open it
		// (still alive, just unreachable). Treat AccessDenied as alive.
		if errno, ok := err.(windows.Errno); ok {
			if errno == windows.ERROR_ACCESS_DENIED {
				return true
			}
		}
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }() // best-effort; nothing we can do if it fails
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil {
		return false
	}
	// STILL_ACTIVE is the canonical "process still running" exit code (259).
	const STILL_ACTIVE = 259
	return exitCode == STILL_ACTIVE
}
