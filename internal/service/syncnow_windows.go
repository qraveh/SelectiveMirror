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
// The security descriptor grants EVENT_MODIFY_STATE to Authenticated Users
// (necessary because the service runs in Session 0 and the user is in Session 1+).
func CreateSyncNowEvent() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(syncNowEventName)
	if err != nil {
		return 0, fmt.Errorf("invalid event name: %w", err)
	}

	// Build a security descriptor that grants Everyone EVENT_MODIFY_STATE.
	// SDDL: D:(A;;0x0002;;;WD) = DACL Allow EVENT_MODIFY_STATE to World (Everyone)
	sa, err := securityAttributesForAuthUsers()
	if err != nil {
		// Fall back to default security (will work for admin callers only)
		h, createErr := windows.CreateEvent(nil, 0, 0, name)
		if createErr != nil {
			return 0, fmt.Errorf("CreateEvent: %w", createErr)
		}
		return h, nil
	}

	h, err := windows.CreateEvent(sa, 0, 0, name)
	if err != nil {
		return 0, fmt.Errorf("CreateEvent: %w", err)
	}
	return h, nil
}

// securityAttributesForAuthUsers creates a SecurityAttributes with a DACL that
// grants EVENT_MODIFY_STATE (0x0002) to Everyone (WD).
func securityAttributesForAuthUsers() (*windows.SecurityAttributes, error) {
	// SM-086: Restrict to SYSTEM + Administrators only.
	// SDDL: D:(A;;0x0002;;;SY)(A;;0x0002;;;BA)
	// SY = LocalSystem (service account), BA = BUILTIN\Administrators
	// Non-admin sync-now uses state DB signaling fallback (SM-077).
	sddl := "D:(A;;0x0002;;;SY)(A;;0x0002;;;BA)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("parse SDDL: %w", err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	return sa, nil
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
	defer func() { _ = windows.CloseHandle(h) }()

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
