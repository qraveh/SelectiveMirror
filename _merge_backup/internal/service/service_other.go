//go:build !windows

// Package service provides Windows Service support for smirror.
// On non-Windows platforms, all operations return errors.
package service

import "fmt"

// IsWindowsService returns false on non-Windows platforms.
func IsWindowsService() bool { return false }

// Run is not supported on non-Windows platforms.
func Run(startFunc func(), stopFunc func()) error {
	return fmt.Errorf("Windows Service mode is only supported on Windows")
}

// Install is not supported on non-Windows platforms.
func Install(configPath string) error {
	return fmt.Errorf("Windows Service mode is only supported on Windows")
}

// Uninstall is not supported on non-Windows platforms.
func Uninstall() error {
	return fmt.Errorf("Windows Service mode is only supported on Windows")
}

// Start is not supported on non-Windows platforms.
func Start() error {
	return fmt.Errorf("Windows Service mode is only supported on Windows")
}

// Stop is not supported on non-Windows platforms.
func Stop() error {
	return fmt.Errorf("Windows Service mode is only supported on Windows")
}
