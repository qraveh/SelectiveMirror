//go:build !windows

package service

import "errors"

var errNotWindows = errors.New("Windows Service is not supported on this platform")

// IsWindowsService always returns false on non-Windows platforms.
func IsWindowsService() bool { return false }

// Run is not supported on non-Windows platforms.
func Run(_, _, _ func()) error { return errNotWindows }

// SendSyncNow is not supported on non-Windows platforms.
func SendSyncNow() error { return errNotWindows }

// Install is not supported on non-Windows platforms.
func Install(_ string) error { return errNotWindows }

// Uninstall is not supported on non-Windows platforms.
func Uninstall() error { return errNotWindows }

// IsInstalled always returns false on non-Windows platforms.
func IsInstalled() bool { return false }

// IsRunning always returns false on non-Windows platforms.
func IsRunning() (bool, bool) { return false, false }

// Start is not supported on non-Windows platforms.
func Start() error { return errNotWindows }

// Stop is not supported on non-Windows platforms.
func Stop() error { return errNotWindows }
