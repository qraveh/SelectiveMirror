//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32Test    = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileTest = modkernel32Test.NewProc("CreateFileW")
)

// openExclusiveForTest opens a file with NO sharing mode on Windows,
// which prevents any other handle (including in the same process)
// from opening it for read or write. This simulates a locked file.
func openExclusiveForTest(path string) (*os.File, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	// dwShareMode = 0 means no sharing at all.
	handle, _, callErr := procCreateFileTest.Call(
		uintptr(unsafe.Pointer(pathp)),
		uintptr(syscall.GENERIC_READ|syscall.GENERIC_WRITE),
		0, // dwShareMode = 0: exclusive
		0, // lpSecurityAttributes
		uintptr(syscall.OPEN_EXISTING),
		uintptr(syscall.FILE_ATTRIBUTE_NORMAL),
		0,
	)
	if handle == uintptr(syscall.InvalidHandle) {
		return nil, fmt.Errorf("CreateFileW: %v", callErr)
	}

	return os.NewFile(handle, path), nil
}
