//go:build !windows

package systemval

import "os/exec"

// setNewProcessGroup is a no-op on non-Windows platforms. The Windows
// implementation (helpers_pg_windows.go) sets CREATE_NEW_PROCESS_GROUP
// so the test harness can deliver Ctrl+C to the child without
// affecting the parent. On Unix, sending SIGINT to the child PID does
// the same thing without process-group ceremony.
func setNewProcessGroup(_ *exec.Cmd) {}
