//go:build windows

// Runtime detection of install_method and background_mode dimensions for
// the telemetry SystemView. SM-220 (2026-05-22) replaced the prior
// "unknown" placeholders in cmd_telemetry.go with real Windows-side
// detection.
//
// install_method = "msi"     if HKLM\Software\SelectiveMirror\ExePath
//                             matches the running binary's path
//                  "manual"  if the binary runs from elsewhere with
//                             no matching MSI registration
//                  "unknown" on registry read failure (the field is
//                             ENUM-constrained server-side; "unknown"
//                             is the safe fallback that never trips
//                             schema validation)
//
// background_mode = "service" if started by Windows Service Control
//                              Manager (detected via
//                              service.IsWindowsService())
//                   "task"    DEFERRED — see SM-220 §"Deferred to
//                              v1.0.x" — would need parent-process
//                              inspection or an env-marker contract
//                              with internal/task; not worth the
//                              complexity for v1.0.1
//                   "foreground" otherwise

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"

	"github.com/qraveh/SelectiveMirror/internal/service"
)

func detectInstallMethod() string {
	// The MSI installer writes HKLM\Software\SelectiveMirror\ExePath
	// pointing at the per-machine install location (see Variables.wxi
	// + the InstallExecuteSequence in Package.wxs). If the path
	// stored there equals the running binary's path, this binary
	// came from the MSI install. Otherwise it was placed somewhere
	// else manually (zip, drag-drop, dev build).
	exePath, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		// Symlink-eval failure is non-fatal for the comparison; fall
		// back to the un-evaluated path. The registry value isn't
		// likely to be a symlink either (MSI writes the resolved
		// target), so the un-evaluated comparison is usually fine.
		exePath, _ = os.Executable()
	}

	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`Software\SelectiveMirror`, registry.QUERY_VALUE)
	if err != nil {
		// No HKLM key → no MSI registration → manual install
		// (zip drop, dev build, etc.). This is the typical case for
		// `go build` followed by direct invocation.
		return "manual"
	}
	defer k.Close()
	regExe, _, err := k.GetStringValue("ExePath")
	if err != nil {
		return "manual"
	}
	// Use the project's existing pathsEqual (pathclean.go) which
	// handles case-insensitive comparison on Windows after
	// filepath.Clean.
	if pathsEqual(regExe, exePath) {
		return "msi"
	}
	// HKLM exists but points at a different binary than the one
	// currently running. Two co-installed copies, or a side-by-side
	// dev build under a non-Program-Files path. The other one is
	// "msi", this one is not.
	return "manual"
}

func detectBackgroundMode() string {
	if service.IsWindowsService() {
		return "service"
	}
	// "task" detection (Scheduled Task host process) is deferred —
	// would need parent-process inspection or an explicit env-marker
	// contract with internal/task. SM-220 §"Deferred to v1.0.x".
	// For now, anything not running under SCM is reported as
	// foreground. This means a Scheduled-Task-launched daemon
	// currently reports as foreground; correct as a population-
	// behavior signal at the current audience size but worth
	// tightening when audience grows.
	return "foreground"
}
