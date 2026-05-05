//go:build windows

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// resolveRealPath on Windows uses GetFinalPathNameByHandleW to resolve
// NTFS junctions that filepath.EvalSymlinks does not handle (SM-138).
// Falls back to filepath.EvalSymlinks → filepath.Abs on any failure.
func resolveRealPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return abs
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0)
	if err != nil {
		// Fall back to EvalSymlinks
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			return r
		}
		return abs
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getFinalPath := kernel32.NewProc("GetFinalPathNameByHandleW")

	buf := make([]uint16, 512)
	n, _, _ := getFinalPath.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	if n == 0 || n >= uintptr(len(buf)) {
		return abs
	}
	result := syscall.UTF16ToString(buf[:n])
	result = strings.TrimPrefix(result, `\\?\`)
	return result
}
