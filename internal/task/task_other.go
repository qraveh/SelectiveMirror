//go:build !windows

package task

// Install, Uninstall, Start, Stop, IsInstalled, and Query all return
// ErrUnsupported on non-Windows platforms. smirror's scheduled-task mode
// is a Windows-only feature because it wraps the Windows Task Scheduler.

// Install returns ErrUnsupported.
func Install(configPath string) error { return ErrUnsupported }

// Uninstall returns ErrUnsupported.
func Uninstall() error { return ErrUnsupported }

// Start returns ErrUnsupported.
func Start() error { return ErrUnsupported }

// Stop returns ErrUnsupported.
func Stop() error { return ErrUnsupported }

// IsInstalled returns false, ErrUnsupported.
func IsInstalled() (bool, error) { return false, ErrUnsupported }

// Query returns an empty Status and ErrUnsupported.
func Query() (Status, error) { return Status{}, ErrUnsupported }
