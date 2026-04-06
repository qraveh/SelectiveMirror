//go:build !windows

package service

// CreateSyncNowEvent returns 0 and nil on non-Windows platforms.
// The zero handle is safely ignored by WaitForSyncNowSignal.
func CreateSyncNowEvent() (uintptr, error) { return 0, nil }

// SignalSyncNow is a no-op on non-Windows platforms.
func SignalSyncNow() error { return nil }

// WaitForSyncNowSignal always returns false on non-Windows platforms.
func WaitForSyncNowSignal(_ uintptr, _ uint32) bool { return false }
