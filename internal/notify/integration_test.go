package notify_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/anomaly"
	"github.com/qraveh/SelectiveMirror/internal/notify"
)

// memWriter is a no-op anomaly writer for testing.
type memWriter struct{}

func (w *memWriter) Write(_ *anomaly.Anomaly) error { return nil }
func (w *memWriter) Close() error                    { return nil }

// TestFullPipeline_AnomalyToWebhookIncident verifies the complete anomaly →
// OnRecord callback → webhook incident lifecycle: open, accumulate, escalate, resolve.
func TestFullPipeline_AnomalyToWebhookIncident(t *testing.T) {
	// Collect webhook payloads
	var mu sync.Mutex
	var payloads []notify.WebhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.WebhookPayload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	// Set up webhook sender with fast timers for testing
	ws := notify.NewWebhookSender(srv.URL)
	ws.EscalateAfter = 80 * time.Millisecond
	ws.ResolveAfter = 60 * time.Millisecond

	// Set up anomaly recorder with OnRecord → webhook
	rec := anomaly.NewRecorder(&memWriter{})
	rec.OnRecord = func(a *anomaly.Anomaly) {
		ws.Record(string(a.Kind), string(a.Severity), a.Project, a.Path, a.Message, a.Detail)
	}

	// Phase 1: First anomaly → incident_opened
	rec.Record(anomaly.KindCircuitBreaker, "TestMirror", "file.go",
		"circuit breaker tripped", "5 consecutive failures")
	waitFor(t, &mu, &payloads, 1)
	assertEvent(t, payloads[0], "incident_opened", "CircuitBreaker", "TestMirror")

	// Phase 2: Repeated anomalies → accumulated silently (no new webhook)
	for i := 0; i < 10; i++ {
		rec.Record(anomaly.KindCircuitBreaker, "TestMirror", "file.go", "still failing", "")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	count := len(payloads)
	mu.Unlock()
	if count != 1 {
		t.Errorf("phase 2: expected 1 payload (accumulating), got %d", count)
	}

	// Phase 3: Wait past escalation window, send another → incident_escalated
	time.Sleep(50 * time.Millisecond) // total ~100ms > EscalateAfter (80ms)
	rec.Record(anomaly.KindCircuitBreaker, "TestMirror", "file.go", "still failing", "")
	waitFor(t, &mu, &payloads, 2)
	assertEvent(t, payloads[1], "incident_escalated", "CircuitBreaker", "TestMirror")
	if payloads[1].Count < 10 {
		t.Errorf("escalation count: got %d, want >= 10", payloads[1].Count)
	}

	// Phase 4: Silence → CheckResolved → incident_resolved
	time.Sleep(70 * time.Millisecond)
	ws.CheckResolved()
	waitFor(t, &mu, &payloads, 3)
	assertEvent(t, payloads[2], "incident_resolved", "CircuitBreaker", "TestMirror")
	if ws.OpenIncidents() != 0 {
		t.Errorf("expected 0 open incidents after resolve, got %d", ws.OpenIncidents())
	}

	// Phase 5: New anomaly after resolve → incident_opened again (reopen)
	rec.Record(anomaly.KindCircuitBreaker, "TestMirror", "file.go", "failed again", "")
	waitFor(t, &mu, &payloads, 4)
	assertEvent(t, payloads[3], "incident_opened", "CircuitBreaker", "TestMirror")
}

// TestFullPipeline_PanicBypassesIncidentGrouping verifies that Panic anomalies
// always produce immediate webhook alerts without incident grouping.
func TestFullPipeline_PanicBypassesIncidentGrouping(t *testing.T) {
	var mu sync.Mutex
	var payloads []notify.WebhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.WebhookPayload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ws := notify.NewWebhookSender(srv.URL)
	rec := anomaly.NewRecorder(&memWriter{})
	rec.OnRecord = func(a *anomaly.Anomaly) {
		ws.Record(string(a.Kind), string(a.Severity), a.Project, a.Path, a.Message, a.Detail)
	}

	// Two panics should both alert immediately
	rec.Record(anomaly.KindPanic, "P1", "a.go", "nil pointer", "goroutine 1")
	rec.Record(anomaly.KindPanic, "P1", "b.go", "index out of range", "goroutine 2")
	waitFor(t, &mu, &payloads, 2)

	for i, p := range payloads {
		if p.Event != "incident_opened" || p.Kind != "Panic" {
			t.Errorf("panic[%d]: event=%q kind=%q, want incident_opened/Panic", i, p.Event, p.Kind)
		}
	}
}

// TestFullPipeline_SeverityFilter verifies that OnRecord respects min severity.
func TestFullPipeline_SeverityFilter(t *testing.T) {
	var mu sync.Mutex
	var payloads []notify.WebhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.WebhookPayload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		payloads = append(payloads, p)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ws := notify.NewWebhookSender(srv.URL)
	rec := anomaly.NewRecorder(&memWriter{})

	// Only forward error+ to webhook (filter out warning)
	rec.OnRecord = func(a *anomaly.Anomaly) {
		order := map[string]int{"info": 0, "warning": 1, "error": 2, "critical": 3}
		if order[string(a.Severity)] >= order["error"] {
			ws.Record(string(a.Kind), string(a.Severity), a.Project, a.Path, a.Message, a.Detail)
		}
	}

	rec.Record(anomaly.KindQueueDepthWarning, "", "", "queue high", "")       // warning — filtered
	rec.Record(anomaly.KindCircuitBreaker, "P", "", "tripped", "")            // error — passes
	rec.Record(anomaly.KindPanic, "P", "", "crash", "")                       // critical — passes

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(payloads)
	mu.Unlock()

	if count != 2 {
		t.Errorf("expected 2 payloads (error + critical), got %d", count)
	}
}

func waitFor(t *testing.T, mu *sync.Mutex, payloads *[]notify.WebhookPayload, n int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		mu.Lock()
		got := len(*payloads)
		mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	t.Fatalf("timeout waiting for %d payloads, got %d", n, len(*payloads))
	mu.Unlock()
}

func assertEvent(t *testing.T, p notify.WebhookPayload, event, kind, project string) {
	t.Helper()
	if p.Event != event {
		t.Errorf("event: got %q, want %q", p.Event, event)
	}
	if p.Kind != kind {
		t.Errorf("kind: got %q, want %q", p.Kind, kind)
	}
	if p.Project != project {
		t.Errorf("project: got %q, want %q", p.Project, project)
	}
}
