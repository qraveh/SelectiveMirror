package service

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// syncNowEventName is a global named event for cross-process IPC.
// The "Global\" prefix makes it visible across all Windows sessions
// (service in session 0, user in session 1+).
const syncNowEventName = "Global\\SmirrorSyncNow"

// CreateSyncNowEvent creates the named event that the service waits on.
// Returns the event handle. The caller should close it on shutdown.
// The event is auto-reset (resets after one waiter is released).
func CreateSyncNowEvent() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(syncNowEventName)
	if err != nil {
		return 0, fmt.Errorf("invalid event name: %w", err)
	}

	// CreateEvent: auto-reset (0), initially non-signaled (0)
	h, err := windows.CreateEvent(nil, 0, 0, name)
	if err != nil {
		return 0, fmt.Errorf("CreateEvent: %w", err)
	}
	return h, nil
}

// SignalSyncNow opens the named event and signals it.
// This is called by sync-now to tell the running service to sync immediately.
// No admin privileges required.
func SignalSyncNow() error {
	name, err := windows.UTF16PtrFromString(syncNowEventName)
	if err != nil {
		return fmt.Errorf("invalid event name: %w", err)
	}

	// EVENT_MODIFY_STATE (0x0002) is sufficient to signal
	h, err := openEvent(0x0002, false, name)
	if err != nil {
		return fmt.Errorf("cannot open sync-now event (is the service running?): %w", err)
	}
	defer windows.CloseHandle(h)

	if err := windows.SetEvent(h); err != nil {
		return fmt.Errorf("SetEvent: %w", err)
	}
	return nil
}

// WaitForSyncNowSignal blocks until the named event is signaled or ctx expires.
// Returns true if signaled, false if the wait was interrupted.
func WaitForSyncNowSignal(h windows.Handle, timeoutMs uint32) bool {
	r, _ := windows.WaitForSingleObject(h, timeoutMs)
	return r == windows.WAIT_OBJECT_0
}

// openEvent wraps the OpenEventW syscall (not directly exposed by x/sys/windows).
var (
	modkernel32  = windows.NewLazySystemDLL("kernel32.dll")
	procOpenEvent = modkernel32.NewProc("OpenEventW")
)

func openEvent(desiredAccess uint32, inheritHandle bool, name *uint16) (windows.Handle, error) {
	var inherit uintptr
	if inheritHandle {
		inherit = 1
	}
	r, _, err := procOpenEvent.Call(
		uintptr(desiredAccess),
		inherit,
		uintptr(unsafe.Pointer(name)),
	)
	if r == 0 {
		return 0, err
	}
	return windows.Handle(r), nil
}
