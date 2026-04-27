//go:build !windows

package sync

import "errors"

// Non-Windows stub. SelectiveMirror is Windows-only at runtime; this file
// exists so `go test ./internal/...` runs on Linux/macOS dev boxes during
// development. Tests that need a non-zero probe override Engine.LivenessProbe
// directly, so the stubs below are reached only by code that genuinely
// expected to be on Windows.

var errProbeNotSupported = errors.New("liveness probe not supported on this platform; tests must override Engine.LivenessProbe")

func openProcessHandle(pid int) (uintptr, error) {
	return 0, errProbeNotSupported
}

func closeProcessHandle(h uintptr) {}

func realLivenessProbe(h uintptr) (Signals, error) {
	return Signals{}, errProbeNotSupported
}
