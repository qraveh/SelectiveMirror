package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// payloadCollector captures payloads from a test webhook server safely
// across the HTTP handler goroutine and the test goroutine.
type payloadCollector struct {
	mu       sync.Mutex
	payloads []WebhookPayload
	arrived  chan struct{}
}

func (pc *payloadCollector) add(p WebhookPayload) {
	pc.mu.Lock()
	pc.payloads = append(pc.payloads, p)
	pc.mu.Unlock()
	// Non-blocking notify — drop if nobody is listening yet.
	select {
	case pc.arrived <- struct{}{}:
	default:
	}
}

func (pc *payloadCollector) len() int {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return len(pc.payloads)
}

func (pc *payloadCollector) at(i int) WebhookPayload {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.payloads[i]
}

// waitFor blocks until at least n payloads have arrived or timeout elapses.
// Returns true if n arrived. Event-based: each arrival sends on the channel.
func (pc *payloadCollector) waitFor(n int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for pc.len() < n {
		select {
		case <-pc.arrived:
			// loop and re-check pc.len()
		case <-deadline:
			return pc.len() >= n
		}
	}
	return true
}

// collectPayloads returns a test server and a payload collector.
func collectPayloads(t *testing.T) (*httptest.Server, *payloadCollector) {
	t.Helper()
	pc := &payloadCollector{arrived: make(chan struct{}, 32)}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p WebhookPayload
		json.NewDecoder(r.Body).Decode(&p)
		pc.add(p)
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	return srv, pc
}

// Backward-compatible shim used by existing tests.
func waitForPayloads(pc *payloadCollector, n int) {
	pc.waitFor(n, 1*time.Second)
}

func TestIncident_OpenOnFirstEvent(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)

	ws.Record("CircuitBreaker", "error", "MyProject", "", "tripped after 5 failures", "")
	waitForPayloads(payloads, 1)

	if payloads.len() != 1 {
		t.Fatalf("expected 1 payload, got %d", payloads.len())
	}
	p := payloads.at(0)
	if p.Event != "incident_opened" {
		t.Errorf("event: got %q, want incident_opened", p.Event)
	}
	if p.Kind != "CircuitBreaker" {
		t.Errorf("kind: got %q, want CircuitBreaker", p.Kind)
	}
	if p.Count != 1 {
		t.Errorf("count: got %d, want 1", p.Count)
	}
	if ws.OpenIncidents() != 1 {
		t.Errorf("open incidents: got %d, want 1", ws.OpenIncidents())
	}
}

func TestIncident_AccumulateSilently(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)
	ws.EscalateAfter = time.Hour // prevent escalation during test

	// First event opens incident
	ws.Record("SyncFailure", "error", "P", "a.go", "rclone exit 1", "")
	waitForPayloads(payloads, 1)

	// Next 50 events for same kind+project accumulate silently
	for i := 0; i < 50; i++ {
		ws.Record("SyncFailure", "error", "P", "b.go", "rclone exit 1", "")
	}
	time.Sleep(100 * time.Millisecond)

	if payloads.len() != 1 {
		t.Errorf("expected 1 payload (only opener), got %d", payloads.len())
	}
}

func TestIncident_EscalateAfterDuration(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)
	ws.EscalateAfter = 50 * time.Millisecond // fast escalation for test

	ws.Record("CircuitBreaker", "error", "P", "", "tripped", "")
	waitForPayloads(payloads, 1)

	// Wait past escalation window, then send another event
	time.Sleep(60 * time.Millisecond)
	ws.Record("CircuitBreaker", "error", "P", "", "still failing", "")
	waitForPayloads(payloads, 2)

	if payloads.len() != 2 {
		t.Fatalf("expected 2 payloads (open + escalate), got %d", payloads.len())
	}
	if payloads.at(1).Event != "incident_escalated" {
		t.Errorf("second event: got %q, want incident_escalated", payloads.at(1).Event)
	}
	if payloads.at(1).Count < 2 {
		t.Errorf("escalation count: got %d, want >= 2", payloads.at(1).Count)
	}
}

func TestIncident_ResolveAfterSilence(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)
	ws.ResolveAfter = 50 * time.Millisecond // fast resolve for test

	ws.Record("WatcherError", "warning", "P", "", "access denied", "")
	waitForPayloads(payloads, 1)

	// Wait for silence window, then check
	time.Sleep(60 * time.Millisecond)
	ws.CheckResolved()
	waitForPayloads(payloads, 2)

	if payloads.len() != 2 {
		t.Fatalf("expected 2 payloads (open + resolve), got %d", payloads.len())
	}
	if payloads.at(1).Event != "incident_resolved" {
		t.Errorf("second event: got %q, want incident_resolved", payloads.at(1).Event)
	}
	if ws.OpenIncidents() != 0 {
		t.Errorf("open incidents after resolve: got %d, want 0", ws.OpenIncidents())
	}
}

func TestIncident_DifferentProjectsSeparate(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)

	ws.Record("CircuitBreaker", "error", "ProjectA", "", "tripped", "")
	ws.Record("CircuitBreaker", "error", "ProjectB", "", "tripped", "")
	waitForPayloads(payloads, 2)

	if payloads.len() != 2 {
		t.Fatalf("expected 2 payloads (separate incidents), got %d", payloads.len())
	}
	if ws.OpenIncidents() != 2 {
		t.Errorf("open incidents: got %d, want 2", ws.OpenIncidents())
	}
}

func TestIncident_PanicAlwaysAlerts(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)

	// Panics bypass incident grouping — each one alerts
	ws.Record("Panic", "critical", "P", "", "goroutine panic 1", "stack1")
	ws.Record("Panic", "critical", "P", "", "goroutine panic 2", "stack2")
	waitForPayloads(payloads, 2)

	if payloads.len() != 2 {
		t.Fatalf("expected 2 payloads (panics always alert), got %d", payloads.len())
	}
	for i := 0; i < payloads.len(); i++ {
		p := payloads.at(i)
		if p.Event != "incident_opened" {
			t.Errorf("panic event: got %q, want incident_opened", p.Event)
		}
	}
}

func TestIncident_ReopenAfterResolve(t *testing.T) {
	srv, payloads := collectPayloads(t)
	ws := NewWebhookSender(srv.URL)
	ws.ResolveAfter = 30 * time.Millisecond

	// Open
	ws.Record("SyncFailure", "error", "P", "", "fail", "")
	waitForPayloads(payloads, 1)

	// Resolve
	time.Sleep(40 * time.Millisecond)
	ws.CheckResolved()
	waitForPayloads(payloads, 2)

	// Reopen with new event
	ws.Record("SyncFailure", "error", "P", "", "fail again", "")
	waitForPayloads(payloads, 3)

	if payloads.len() != 3 {
		t.Fatalf("expected 3 payloads (open, resolve, reopen), got %d", payloads.len())
	}
	events := []string{payloads.at(0).Event, payloads.at(1).Event, payloads.at(2).Event}
	want := []string{"incident_opened", "incident_resolved", "incident_opened"}
	for i, e := range events {
		if e != want[i] {
			t.Errorf("event[%d]: got %q, want %q", i, e, want[i])
		}
	}
}

func TestWebhookNilSafe(t *testing.T) {
	var ws *WebhookSender
	ws.Record("Panic", "critical", "P", "", "msg", "")
	ws.CheckResolved()
	if ws.OpenIncidents() != 0 {
		t.Error("nil sender should return 0 incidents")
	}
}

func TestWebhookEmptyURL(t *testing.T) {
	ws := NewWebhookSender("")
	ws.Record("Panic", "critical", "P", "", "msg", "")
	// Should be no-op — no panic, no HTTP call
}
