//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// findInstallations returns the current binary path. Full PATH scanning
// is only implemented on Windows (where MSI installs modify PATH).
func findInstallations() installationInfo {
	exePath, err := os.Executable()
	if err != nil {
		exePath = os.Args[0]
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	exePath = filepath.Clean(exePath)
	return installationInfo{CurrentExe: exePath}
}

// cleanPATHEntries is a no-op on non-Windows platforms.
func cleanPATHEntries(_ installationInfo, _ bool) bool { return false }
