//go:build windows

package lock

import (
	"log/slog"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// lockFile acquires an exclusive lock on the file using Windows LockFileEx.
func lockFile(f *os.File) error {
	var ol syscall.Overlapped
	handle := syscall.Handle(f.Fd())
	r1, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

// unlockFile releases the exclusive lock on the file.
func unlockFile(f *os.File) {
	var ol syscall.Overlapped
	handle := syscall.Handle(f.Fd())
	r1, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		slog.Warn("UnlockFileEx failed", "error", err)
	}
}

