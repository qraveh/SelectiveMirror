//go:build windows

package hooks

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// SEC-M14 / PF-A5: hook child processes are wrapped in a Windows Job
// Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE so that when smirror
// closes the job handle (on hook timeout, or normally on hook return),
// the kernel terminates the child AND every descendant. Without this,
// `pre_sync_hook: cmd /C start /B malware.exe` orphans `malware.exe`
// past the hook timeout — and in service mode, those orphans run as
// LocalSystem.
//
// Implementation:
//  1. CreateJobObject (anonymous, default DACL).
//  2. SetInformationJobObject(ExtendedLimitInformation) with the
//     KILL_ON_JOB_CLOSE flag.
//  3. After cmd.Start(), OpenProcess(SET_QUOTA|TERMINATE) by PID and
//     AssignProcessToJobObject.
//  4. cmd.Wait() as normal.
//  5. CloseHandle(job) (deferred) → kernel kills the entire tree.
//
// There IS a small race window between cmd.Start() and the assignment
// where the child could spawn grandchildren that escape the job. The
// alternative (CREATE_SUSPENDED + ResumeThread) requires the primary
// thread handle, which Go's os/exec does not expose. The race is
// microseconds and acceptable for hook execution.

// JobObject limit flag (not exported by golang.org/x/sys/windows).
const jobObjectLimitKillOnJobClose = 0x00002000

// jobObjectExtendedLimitInformation info-class — Windows SDK value 9.
const jobObjectExtendedLimitInformationClass = 9

type jobBasicLimitInfo struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type jobIoCountersInfo struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimitInfo struct {
	BasicLimitInformation jobBasicLimitInfo
	IoInfo                jobIoCountersInfo
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// kernel32 procs that golang.org/x/sys/windows@v0.42 does not export.
var (
	modKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procSetInformationJobObject  = modKernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = modKernel32.NewProc("AssignProcessToJobObject")
)

// newJobObject creates an anonymous Job Object configured to kill all
// member processes when the handle is closed. Returned as uintptr so
// the cross-platform hooks.Run code stays platform-neutral; the
// non-Windows stub returns 0.
func newJobObject() (uintptr, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := jobExtendedLimitInfo{
		BasicLimitInformation: jobBasicLimitInfo{
			LimitFlags: jobObjectLimitKillOnJobClose,
		},
	}
	r1, _, e1 := procSetInformationJobObject.Call(
		uintptr(h),
		uintptr(jobObjectExtendedLimitInformationClass),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info), // already uintptr; no conversion needed
	)
	if r1 == 0 {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("SetInformationJobObject: %w", e1)
	}
	return uintptr(h), nil
}

// closeJobObject closes the job handle. When the last handle is
// closed AND KILL_ON_JOB_CLOSE is set, every process in the job dies.
func closeJobObject(job uintptr) {
	if job == 0 {
		return
	}
	_ = windows.CloseHandle(windows.Handle(job))
}

// assignPIDToJob opens the process by PID with the access rights
// required for AssignProcessToJobObject and assigns it.
func assignPIDToJob(job uintptr, pid int) error {
	if job == 0 {
		return nil
	}
	const desiredAccess = windows.PROCESS_SET_QUOTA | windows.PROCESS_TERMINATE
	// gosec G115 nolint: pid is os/exec's cmd.Process.Pid (positive on Windows, DWORD-bounded).
	procHandle, err := windows.OpenProcess(desiredAccess, false, uint32(pid)) //nolint:gosec
	if err != nil {
		return fmt.Errorf("OpenProcess(pid=%d): %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(procHandle) }() // best-effort; matches the pattern at newJobObject's error path
	r1, _, e1 := procAssignProcessToJobObject.Call(job, uintptr(procHandle))
	if r1 == 0 {
		return fmt.Errorf("AssignProcessToJobObject: %w", e1)
	}
	return nil
}
