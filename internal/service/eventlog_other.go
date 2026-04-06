//go:build !windows

package service

// EventID is the Windows Event Log event identifier (unused on non-Windows).
const EventID = 1

// EventLog is a no-op stub on non-Windows platforms.
type EventLog struct{}

// OpenEventLog returns nil on non-Windows platforms.
func OpenEventLog() *EventLog { return nil }

// InstallEventSource is a no-op on non-Windows platforms.
func InstallEventSource() error { return nil }

// RemoveEventSource is a no-op on non-Windows platforms.
func RemoveEventSource() error { return nil }

func (e *EventLog) Info(_ uint32, _ string)    {}
func (e *EventLog) Warning(_ uint32, _ string) {}
func (e *EventLog) Error(_ uint32, _ string)   {}
func (e *EventLog) Close()                     {}
