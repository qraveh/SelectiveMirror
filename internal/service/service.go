// Package service provides Windows Service (SCM) integration stubs.
//
// Phase 2 will implement the full Windows service lifecycle using
// golang.org/x/sys/windows/svc. For now, all operations return
// ErrNotImplemented with guidance to use foreground mode.
package service

import (
	"errors"
	"fmt"
)

// ErrNotImplemented is returned by all service operations until Phase 2.
var ErrNotImplemented = errors.New("Windows service support is not yet implemented (Phase 2)")

// IsWindowsService reports whether the current process was started by the
// Windows Service Control Manager. Always returns false until Phase 2.
func IsWindowsService() bool {
	return false
}

// Run executes the service's main loop under SCM control.
// start is called when the service receives a Start command;
// stop is called on Stop/Shutdown.
func Run(start, stop func()) error {
	fmt.Println("Windows service mode is not yet implemented (Phase 2).")
	fmt.Println("Use 'smirror start' to run in foreground mode.")
	return ErrNotImplemented
}

// Install registers smirror as a Windows service with the given config path.
func Install(configPath string) error {
	fmt.Println("Windows service installation is not yet implemented (Phase 2).")
	fmt.Println("Use 'smirror start' to run in foreground mode.")
	return ErrNotImplemented
}

// Uninstall removes the smirror Windows service registration.
func Uninstall() error {
	fmt.Println("Windows service removal is not yet implemented (Phase 2).")
	return ErrNotImplemented
}

// Start sends a start signal to the smirror Windows service.
func Start() error {
	fmt.Println("Windows service start is not yet implemented (Phase 2).")
	fmt.Println("Use 'smirror start' to run in foreground mode.")
	return ErrNotImplemented
}

// Stop sends a stop signal to the smirror Windows service.
func Stop() error {
	fmt.Println("Windows service stop is not yet implemented (Phase 2).")
	return ErrNotImplemented
}
