package service

import (
	"golang.org/x/sys/windows/svc/eventlog"
)

// Event IDs for Windows Event Log entries.
const (
	EventServiceStarted  = 1000
	EventServiceStopped  = 1001
	EventServiceError    = 1002
	EventSyncNowReceived = 1003
	EventPanicRecovered  = 1004
)

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
