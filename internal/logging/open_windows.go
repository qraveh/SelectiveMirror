//go:build windows

package logging

import (
	"os"
	"syscall"
)

// openShared opens a file for append with FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE,
// so other processes (PowerShell Get-Content -Wait, tail, etc.) can read it concurrently.
func openShared(path string) (*os.File, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	// Seek to end for append behavior
	if _, err := f.Seek(0, 2); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
