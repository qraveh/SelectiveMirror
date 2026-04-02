package watcher

import (
	"sync"
	"time"
)

// fakeClock allows tests to control time deterministically.
// Timers don't fire based on wall-clock time; they fire when
// Advance() moves the fake clock past their deadline.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now()}
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{
		deadline: c.now.Add(d),
		f:        f,
		duration: d,
		clock:    c,
	}
	c.timers = append(c.timers, ft)
	return ft
}

// Advance moves the clock forward by d and fires any timers whose deadline has passed.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now

	// Collect timers to fire (snapshot under lock)
	var toFire []*fakeTimer
	var remaining []*fakeTimer
	for _, ft := range c.timers {
		ft.mu.Lock()
		if !ft.stopped && !now.Before(ft.deadline) {
			toFire = append(toFire, ft)
		} else if !ft.stopped {
			remaining = append(remaining, ft)
		}
		ft.mu.Unlock()
	}
	c.timers = remaining
	c.mu.Unlock()

	// Fire outside lock to avoid deadlocks with timer callbacks
	for _, ft := range toFire {
		ft.f()
	}
}

type fakeTimer struct {
	mu       sync.Mutex
	deadline time.Time
	f        func()
	duration time.Duration
	stopped  bool
	clock    *fakeClock // set when Reset re-registers
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.mu.Lock()
	wasActive := !t.stopped
	t.deadline = time.Now().Add(d) // placeholder — will be overridden by Advance
	t.duration = d
	t.stopped = false
	t.mu.Unlock()

	// Re-register with the clock if we have a reference
	if t.clock != nil {
		t.clock.mu.Lock()
		// Update deadline relative to clock's now
		t.mu.Lock()
		t.deadline = t.clock.now.Add(d)
		t.mu.Unlock()
		// Add back to timers if not already there
		found := false
		for _, ft := range t.clock.timers {
			if ft == t {
				found = true
				break
			}
		}
		if !found {
			t.clock.timers = append(t.clock.timers, t)
		}
		t.clock.mu.Unlock()
	}
	return wasActive
}

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}
