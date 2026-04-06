package anomaly

import (
	"sync"
	"testing"
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

	var received []*Anomaly
	r.OnRecord = func(a *Anomaly) {
		received = append(received, a)
	}

	r.Record(KindSyncFailure, "proj", "file.go", "rclone exit 1", "elapsed: 500ms")
	r.Record(KindCircuitBreaker, "proj", "", "tripped", "")

	if len(received) != 2 {
		t.Fatalf("OnRecord called %d times, want 2", len(received))
	}
	if received[0].Kind != KindSyncFailure {
		t.Errorf("first callback kind: %s, want SyncFailure", received[0].Kind)
	}
	if received[1].Kind != KindCircuitBreaker {
		t.Errorf("second callback kind: %s, want CircuitBreaker", received[1].Kind)
	}

	// Writer also got both
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
