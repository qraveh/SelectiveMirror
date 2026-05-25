//go:build !windows

// Non-Windows fallback for install_method + background_mode detection.
// SelectiveMirror is Windows-first (CLAUDE.md); non-Windows builds
// exist only to make `go test ./...` work on dev laptops. The values
// returned here are ENUM-valid and never trip server-side schema
// validation; they just don't carry rich population data on platforms
// nobody runs the daemon on. See SM-220.

package main

func detectInstallMethod() string {
	return "unknown"
}

func detectBackgroundMode() string {
	return "unknown"
}
