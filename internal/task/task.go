// Package task provides per-user Windows Scheduled Task integration for smirror.
//
// Rationale: Running as a Windows Service (LocalSystem) is an overreach of
// privilege for a desktop file sync tool — files end up owned by SYSTEM, users
// need admin rights to stop sync or clean up, and the exposed attack surface
// (LocalSystem RCE via config or hooks) is substantially larger than necessary.
//
// The task mode registers a per-user Scheduled Task that runs at logon as the
// current user. No admin privileges are required to install, start, stop, or
// uninstall the task — users own their own tasks. This matches the deployment
// model of Dropbox, Google Drive Desktop, and OneDrive.
//
// Service mode remains available as an advanced option for 24/7 operation
// where sync must continue across user logoff/reboot (see internal/service).
package task

import "errors"

// ErrUnsupported indicates the task scheduler is not available on this
// platform (non-Windows builds).
var ErrUnsupported = errors.New("scheduled tasks are only supported on Windows")

// ErrNotInstalled indicates no SelectiveMirror task is registered for the
// current user.
var ErrNotInstalled = errors.New("task is not installed")

// ErrAlreadyInstalled indicates a SelectiveMirror task is already registered
// for the current user.
var ErrAlreadyInstalled = errors.New("task is already installed")

// Status reports the current state of the scheduled task.
type Status struct {
	Installed bool
	Running   bool
	// LastRunTime / LastRunResult are populated when Installed is true and
	// the platform supports querying them. Zero-valued otherwise.
	LastRunTime   string // RFC3339 timestamp from schtasks output; empty if never run
	LastRunResult string // hex status code from schtasks; empty if never run
	// NextRunTime is populated when Installed is true. Empty for logon-only
	// triggers because the next run is "at next logon" and has no wall clock.
	NextRunTime string
}

// TaskName is the Windows Task Scheduler name used for the SelectiveMirror
// per-user logon task. Scoped with a prefix (not a subfolder) to keep the
// schtasks-based implementation portable across Windows SKUs that restrict
// task folder creation.
const TaskName = "SelectiveMirror"
