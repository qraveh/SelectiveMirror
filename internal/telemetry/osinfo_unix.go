//go:build !windows

package telemetry

import "runtime"

// OSDetail returns the OS name. On non-Windows, returns runtime.GOOS.
func OSDetail() string {
	return runtime.GOOS
}
