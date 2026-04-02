package watcher

import "time"

// Clock abstracts time operations for testability.
// Production uses realClock; tests use fakeClock for deterministic timing.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer abstracts a resettable timer.
type Timer interface {
	Reset(d time.Duration) bool
	Stop() bool
}

// realClock delegates to the standard time package.
type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) Timer {
	return realTimer{time.AfterFunc(d, f)}
}

type realTimer struct{ *time.Timer }

func (t realTimer) Reset(d time.Duration) bool { return t.Timer.Reset(d) }
func (t realTimer) Stop() bool                 { return t.Timer.Stop() }
