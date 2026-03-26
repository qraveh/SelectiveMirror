//go:build !windows

package lock

import (
	"os"
	"syscall"
)

// lockFile acquires an exclusive lock on the file using flock.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the exclusive lock on the file.
func unlockFile(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// signalProcess checks if a process is running by sending signal 0.
func signalProcess(proc *os.Process) error {
	return proc.Signal(syscall.Signal(0))
}
