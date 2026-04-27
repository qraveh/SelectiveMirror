//go:build windows

package sync

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processQueryLimitedInformation is the access mask sufficient for both
// GetProcessTimes and GetProcessIoCounters. We do NOT request elevated
// access (PROCESS_ALL_ACCESS, PROCESS_QUERY_INFORMATION) because the
// supervisor doesn't need to read protected information.
const processQueryLimitedInformation = 0x1000

// ioCounters mirrors Windows' IO_COUNTERS struct. Defined locally because
// golang.org/x/sys/windows v0.42 exposes the struct as windows.IO_COUNTERS
// but does NOT expose the GetProcessIoCounters function — we have to load
// the proc address from kernel32.dll ourselves.
//
// Layout per winnt.h: 6 × ULONGLONG (uint64). 48 bytes total.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// kernel32 / GetProcessIoCounters lazy-loaded once at package init. The
// LazyDLL/LazyProc indirection is documented as safe for concurrent use.
var (
	modKernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessIoCounters = modKernel32.NewProc("GetProcessIoCounters")
)

// openProcessHandle wraps OpenProcess for the supervisor. Caller must
// closeProcessHandle the returned handle when sampling is done. Lifetime
// must span all sample reads — never re-OpenProcess by PID, because the
// OS may have reused the PID between reads.
//
// Returns the handle as uintptr so the supervisor's signature is platform
// neutral (proc_other.go provides an erroring stub of the same shape).
func openProcessHandle(pid int) (uintptr, error) {
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("OpenProcess(pid=%d): %w", pid, err)
	}
	return uintptr(h), nil
}

// closeProcessHandle releases the OS handle held for sampling.
func closeProcessHandle(h uintptr) {
	_ = windows.CloseHandle(windows.Handle(h))
}

// readProcessTimes returns kernel + user CPU time as a combined nanoseconds
// value. Filetime is two uint32 (low, high) of 100-nanosecond units since
// 1601; we only care about deltas, so the absolute origin doesn't matter.
func readProcessTimes(h windows.Handle) (uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, fmt.Errorf("GetProcessTimes: %w", err)
	}
	kernNs := (uint64(kernel.HighDateTime)<<32 | uint64(kernel.LowDateTime)) * 100
	userNs := (uint64(user.HighDateTime)<<32 | uint64(user.LowDateTime)) * 100
	return kernNs + userNs, nil
}

// readProcessIoCounters returns the sum of Read+Write+Other transfer
// byte counts. We sum to a single number because the OR combinator only
// cares whether any of them moved, not which one.
//
// golang.org/x/sys/windows@v0.42 does not expose GetProcessIoCounters as
// a Go function (only the constant is there). We load it from kernel32.dll
// via LazyDLL.NewProc. The Win32 signature:
//
//	BOOL GetProcessIoCounters(HANDLE Process, PIO_COUNTERS lpIoCounters);
//
// Returns nonzero on success; 0 + GetLastError on failure.
func readProcessIoCounters(h windows.Handle) (uint64, error) {
	var counters ioCounters
	r1, _, e1 := procGetProcessIoCounters.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&counters)),
	)
	if r1 == 0 {
		return 0, fmt.Errorf("GetProcessIoCounters: %w", e1)
	}
	return counters.ReadTransferCount +
		counters.WriteTransferCount +
		counters.OtherTransferCount, nil
}

// realLivenessProbe is the production probe. Samples both CPU time and
// I/O bytes via the Windows APIs above. Returns zero values + error if any
// sample fails — the caller treats "error" as "unknown, do not count as
// flat" so a transient API failure cannot trigger a kill.
func realLivenessProbe(h uintptr) (Signals, error) {
	hh := windows.Handle(h)
	cpu, err := readProcessTimes(hh)
	if err != nil {
		return Signals{}, err
	}
	io, err := readProcessIoCounters(hh)
	if err != nil {
		return Signals{}, err
	}
	return Signals{CPUTimeNs: cpu, IOBytes: io}, nil
}
