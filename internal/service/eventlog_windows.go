package service

import (
	"golang.org/x/sys/windows/svc/eventlog"
)

// Event ID for all smirror events. Using ID 1 because InstallAsEventCreate
// registers %1 as the format string for the default message DLL, which
// correctly displays the full message text for event ID 1.
// Higher IDs (1000+) show "description cannot be found" warnings because
// the generic message DLL doesn't have format strings for those IDs.
const EventID = 1

// EventLog wraps the Windows Event Log for smirror service events.
// Nil-safe: methods are no-ops when receiver is nil.
type EventLog struct {
	log *eventlog.Log
}

// OpenEventLog opens the Windows Event Log for writing.
// The event source must be registered first via InstallEventSource.
// Returns nil (not an error) if opening fails — event logging is best-effort.
func OpenEventLog() *EventLog {
	l, err := eventlog.Open(serviceName)
	if err != nil {
		return nil
	}
	return &EventLog{log: l}
}

// InstallEventSource registers the smirror event source in the Windows registry.
// Called during `smirror service install`. Requires Administrator.
func InstallEventSource() error {
	return eventlog.InstallAsEventCreate(serviceName, eventlog.Info|eventlog.Warning|eventlog.Error)
}

// RemoveEventSource removes the smirror event source from the registry.
// Called during `smirror service uninstall`.
func RemoveEventSource() error {
	return eventlog.Remove(serviceName)
}

// Info writes an informational event.
func (e *EventLog) Info(eid uint32, msg string) {
	if e == nil || e.log == nil {
		return
	}
	_ = e.log.Info(eid, msg)
}

// Warning writes a warning event.
func (e *EventLog) Warning(eid uint32, msg string) {
	if e == nil || e.log == nil {
		return
	}
	_ = e.log.Warning(eid, msg)
}

// Error writes an error event.
func (e *EventLog) Error(eid uint32, msg string) {
	if e == nil || e.log == nil {
		return
	}
	_ = e.log.Error(eid, msg)
}

// Close closes the event log handle.
func (e *EventLog) Close() {
	if e == nil || e.log == nil {
		return
	}
	_ = e.log.Close()
}
