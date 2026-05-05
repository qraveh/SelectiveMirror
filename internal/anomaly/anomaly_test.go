package anomaly

import (
	"sync"
	"testing"
	"time"
)

// memWriter is a test writer that stores anomalies in memory.
type memWriter struct {
	mu        sync.Mutex
	anomalies []*Anomaly
}

func (w *memWriter) Write(a *Anomaly) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.anomalies = append(w.anomalies, a)
	return nil
}

func (w *memWriter) Close() error { return nil }

func TestRecord_BasicFields(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	a := r.Record(KindPanic, "proj", "file.txt", "panic: nil pointer", "stack trace here")
	if a == nil {
		t.Fatal("expected non-nil anomaly")
	}
	if a.Kind != KindPanic {
		t.Errorf("kind = %q, want %q", a.Kind, KindPanic)
	}
	if a.Severity != SeverityCritical {
		t.Errorf("severity = %q, want %q", a.Severity, SeverityCritical)
	}
	if a.Project != "proj" {
		t.Errorf("project = %q, want %q", a.Project, "proj")
	}
	if a.Message != "panic: nil pointer" {
		t.Errorf("message = %q", a.Message)
	}
	if a.ID != "A-000001" {
		t.Errorf("id = %q, want A-000001", a.ID)
	}

	// Verify writer received it
	if len(w.anomalies) != 1 {
		t.Fatalf("writer got %d anomalies, want 1", len(w.anomalies))
	}
}

func TestRecord_MonotonicIDs(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	a1 := r.Record(KindWatcherError, "", "", "err1", "")
	a2 := r.Record(KindWatcherError, "", "", "err2", "")
	a3 := r.Record(KindWatcherError, "", "", "err3", "")

	if a1.ID >= a2.ID || a2.ID >= a3.ID {
		t.Errorf("IDs not monotonic: %s, %s, %s", a1.ID, a2.ID, a3.ID)
	}
}

func TestRecord_NilRecorder(t *testing.T) {
	var r *Recorder
	a := r.Record(KindPanic, "proj", "file.txt", "msg", "")
	if a != nil {
		t.Error("expected nil from nil recorder")
	}
	if r.Total() != 0 {
		t.Error("expected 0 total from nil recorder")
	}
	if r.Summary() != nil {
		t.Error("expected nil summary from nil recorder")
	}
}

func TestSummary_CountsByKind(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	r.Record(KindPanic, "", "", "p1", "")
	r.Record(KindPanic, "", "", "p2", "")
	r.Record(KindWatcherError, "", "", "w1", "")
	r.Record(KindCircuitBreaker, "", "", "c1", "")

	s := r.Summary()
	if s[KindPanic] != 2 {
		t.Errorf("panic count = %d, want 2", s[KindPanic])
	}
	if s[KindWatcherError] != 1 {
		t.Errorf("watcher count = %d, want 1", s[KindWatcherError])
	}
	if s[KindCircuitBreaker] != 1 {
		t.Errorf("circuit count = %d, want 1", s[KindCircuitBreaker])
	}
	if r.Total() != 4 {
		t.Errorf("total = %d, want 4", r.Total())
	}
}

func TestSeverityFor(t *testing.T) {
	tests := []struct {
		kind     Kind
		expected Severity
	}{
		{KindPanic, SeverityCritical},
		{KindCircuitBreaker, SeverityError},
		{KindWatcherError, SeverityWarning},
		{KindGhostLeak, SeverityWarning},
		{Kind("unknown"), SeverityInfo},
	}
	for _, tt := range tests {
		if got := SeverityFor(tt.kind); got != tt.expected {
			t.Errorf("SeverityFor(%q) = %q, want %q", tt.kind, got, tt.expected)
		}
	}
}

func TestRecord_ConcurrentSafety(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record(KindSyncFailure, "proj", "file.txt", "fail", "")
		}()
	}
	wg.Wait()

	if r.Total() != 100 {
		t.Errorf("total = %d, want 100", r.Total())
	}
	if len(w.anomalies) != 100 {
		t.Errorf("writer got %d, want 100", len(w.anomalies))
	}
}

// SM-186: Record's check-of-closed and send-to-channel must be atomic
// against Close. Pre-fix used `atomic.Bool` checked-then-sent which
// could race with `close(r.callbackQueue)`, producing a "send on
// closed channel" panic. The fix wraps send under sendMu's RLock
// and close under sendMu's Lock. This test exercises the race
// window with -race; without the fix, the race detector + the
// random scheduling sometimes produces the panic.
//
// Run via `go test -race -run TestRecord_CloseRace_NoPanic`.
func TestRecord_CloseRace_NoPanic(t *testing.T) {
	for trial := 0; trial < 5; trial++ {
		w := &memWriter{}
		r := NewRecorder(w)
		// OnRecord must be set so the send path is exercised.
		r.SetOnRecord(func(*Anomaly) {})

		var wg sync.WaitGroup
		// Producers: many goroutines hammering Record.
		for i := 0; i < 64; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if rv := recover(); rv != nil {
						t.Errorf("Record panicked under concurrent Close: %v", rv)
					}
				}()
				for j := 0; j < 32; j++ {
					r.Record(KindSyncFailure, "proj", "file.txt", "fail", "")
				}
			}()
		}
		// Closer: race-against-producers Close.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if rv := recover(); rv != nil {
					t.Errorf("Close panicked under concurrent Record: %v", rv)
				}
			}()
			_ = r.Close()
		}()
		wg.Wait()
	}
}

func TestSummaryStrings(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	r.Record(KindPanic, "", "", "p", "")
	r.Record(KindGhostLeak, "", "", "l", "")

	ss := r.SummaryStrings()
	if ss["Panic"] != 1 {
		t.Errorf("Panic count = %d, want 1", ss["Panic"])
	}
	if ss["Ghost:Leak"] != 1 {
		t.Errorf("Ghost:Leak count = %d, want 1", ss["Ghost:Leak"])
	}
}

func TestOnRecord_CalledAfterWrite(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)

	// PF-A8: OnRecord fires from a dedicated goroutine, not the Record
	// caller. Use a channel to coordinate test assertions instead of
	// scanning a slice that may not be populated yet.
	got := make(chan *Anomaly, 4)
	r.OnRecord = func(a *Anomaly) { got <- a }

	r.Record(KindSyncFailure, "proj", "file.go", "rclone exit 1", "elapsed: 500ms")
	r.Record(KindCircuitBreaker, "proj", "", "tripped", "")

	// Drain via Close — guarantees the callback goroutine has processed
	// every queued entry before we assert.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(got)

	var received []*Anomaly
	for a := range got {
		received = append(received, a)
	}

	if len(received) != 2 {
		t.Fatalf("OnRecord called %d times, want 2", len(received))
	}
	if received[0].Kind != KindSyncFailure {
		t.Errorf("first callback kind: %s, want SyncFailure", received[0].Kind)
	}
	if received[1].Kind != KindCircuitBreaker {
		t.Errorf("second callback kind: %s, want CircuitBreaker", received[1].Kind)
	}

	// Writer also got both (synchronous path — no goroutine needed)
	if len(w.anomalies) != 2 {
		t.Errorf("writer got %d anomalies, want 2", len(w.anomalies))
	}
}

func TestOnRecord_NilCallbackSafe(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)
	// OnRecord not set — should not panic
	r.Record(KindPanic, "", "", "msg", "")
	if len(w.anomalies) != 1 {
		t.Error("expected 1 anomaly written even without OnRecord callback")
	}
}

// SetOnRecord must be safe to call on a nil *Recorder. Regression for the
// startup-panic bug where main.go assigned anomalyRecorder.OnRecord = ...
// without checking that the recorder was non-nil first (the
// anomaly_detection: false + alert_webhook_url: ... combo).
func TestSetOnRecord_NilReceiverDoesNotPanic(t *testing.T) {
	var r *Recorder // nil
	r.SetOnRecord(func(*Anomaly) { t.Fatal("callback unexpectedly fired") })
	if got := r.Record(KindPanic, "", "", "", ""); got != nil {
		t.Errorf("nil receiver Record returned %v, want nil", got)
	}
}

// PF-A8: a slow OnRecord callback must not
// block Record(). Previously the callback ran in the calling goroutine,
// so a webhook with a 5-second HTTP timeout blocked the sync engine for
// the full timeout. Now the callback runs in a dedicated goroutine fed
// by a bounded channel; Record returns immediately.
func TestRecord_DoesNotBlockOnSlowCallback(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)
	defer r.Close()

	// Block the callback goroutine until we explicitly release it.
	release := make(chan struct{})
	r.OnRecord = func(*Anomaly) { <-release }

	// Record N anomalies. The callback queue is buffered (size 64); the
	// first will pin the consumer goroutine, subsequent ones queue. After
	// N=64 entries, the buffer is full and the next Record drops the
	// callback (counter increments) — but Record still returns promptly.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 70; i++ {
			r.Record(KindSyncFailure, "p", "f", "msg", "")
		}
		close(done)
	}()

	// If Record is blocking on the callback, this select hits the
	// default and we fail. Buffered channel of 64 + one in-flight
	// callback = ~65 quick records before any drops; the loop of 70
	// must complete in well under the timeout.
	select {
	case <-done:
		// good — record loop completed quickly
	case <-time.After(2 * time.Second):
		t.Fatal("Record blocked on slow callback (PF-A8 regression)")
	}

	// Some callbacks must have been dropped (we sent 70, queue holds 64).
	close(release)
	if r.DroppedCallbacks() == 0 {
		t.Error("expected non-zero DroppedCallbacks after overflow")
	}
}

func TestSetOnRecord_InstallsCallback(t *testing.T) {
	w := &memWriter{}
	r := NewRecorder(w)
	// PF-A8: callback is async; use a channel to confirm fire-once
	// without depending on goroutine scheduling.
	got := make(chan struct{}, 1)
	r.SetOnRecord(func(*Anomaly) { got <- struct{}{} })
	r.Record(KindPanic, "", "", "msg", "")

	// Drain via Close so we know the callback goroutine processed the entry.
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fired := len(got)
	if fired != 1 {
		t.Errorf("callback fired %d times, want 1", fired)
	}
}
