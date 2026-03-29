//go:build windows

package lock

import (
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
	procUnlockFileEx.Call(
		uintptr(handle),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&ol)),
	)
}

// readFileShared opens a file with FILE_SHARE_READ|FILE_SHARE_WRITE so it can
// be read even when another process holds an exclusive lock via LockFileEx.
func readFileShared(path string) ([]byte, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(h)

	var buf [256]byte
	var read uint32
	err = syscall.ReadFile(h, buf[:], &read, nil)
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

// signalProcess checks if a process is running on Windows.
// On Windows, os.FindProcess always succeeds, so we try OpenProcess.
func signalProcess(proc *os.Process) error {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(proc.Pid))
	if err != nil {
		return err
	}
	syscall.CloseHandle(h)
	return nil
}
