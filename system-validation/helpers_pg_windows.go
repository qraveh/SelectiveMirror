//go:build windows

package systemval

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup configures cmd to launch in a new process group on
// Windows. This is required so the test harness can deliver Ctrl+C to
// the child without affecting the parent (Go test process). Lives in a
// build-tagged file because syscall.CreationFlags and the
// CREATE_NEW_PROCESS_GROUP constant are Windows-only — a runtime
// `if runtime.GOOS == "windows"` guard would still trigger a Linux-side
// compile error on the unknown-identifier reference.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
