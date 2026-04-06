// Package anomaly provides runtime anomaly classification, recording, and analysis.
// Anomalies are events that deviate from expected behavior — sync failures, panics,
// watcher errors, ghost accumulation, circuit breaker activations, etc.
package anomaly

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Kind classifies the anomaly category.
type Kind string

const (
	KindPanic             Kind = "Panic"
	KindCircuitBreaker    Kind = "CircuitBreaker"
	KindWatcherError      Kind = "Watcher:Error"
	KindQueueDepthWarning Kind = "Queue:DepthWarning"
	KindGhostLeak         Kind = "Ghost:Leak"
	KindGhostOrphan       Kind = "Ghost:Orphan"
	KindGhostStale        Kind = "Ghost:Stale"
	KindReconcileStale    Kind = "Reconciliation:Stale"
	KindPathGone          Kind = "Path:Gone"
	KindSyncTimeout       Kind = "Sync:Timeout"
	KindSyncFailure       Kind = "Sync:Failure"
)

// Severity levels for anomalies.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

// Anomaly records a single anomalous event.
type Anomaly struct {
	ID         string   `json:"id"`
	Time       string   `json:"time"` // RFC3339
	Kind       Kind     `json:"kind"`
	Severity   Severity `json:"severity"`
	Project    string   `json:"project,omitempty"`
	Path       string   `json:"path,omitempty"`
	Message    string   `json:"message"`
	Detail     string   `json:"detail,omitempty"`
	Hypothesis string   `json:"hypothesis,omitempty"`
}

// SeverityFor returns the default severity for a given anomaly kind.
func SeverityFor(k Kind) Severity {
	switch k {
	case KindPanic:
		return SeverityCritical
	case KindCircuitBreaker, KindReconcileStale, KindSyncFailure:
		return SeverityError
	case KindWatcherError, KindQueueDepthWarning, KindGhostLeak, KindGhostOrphan, KindGhostStale, KindPathGone, KindSyncTimeout:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// Recorder classifies and persists anomalies. Thread-safe. Nil-safe (methods are no-ops on nil receiver).
type Recorder struct {
	writer  Writer // interface for writing anomalies
	counter atomic.Int64
	mu      sync.Mutex
	counts  map[Kind]int64

	// OnRecord is called after each anomaly is recorded. Set before use; not thread-safe to change.
	// Intended for alerting integration (webhook, notification).
	OnRecord func(a *Anomaly)
}

// Writer is the interface for anomaly persistence.
type Writer interface {
	Write(a *Anomaly) error
	Close() error
}

// NewRecorder creates an anomaly recorder with the given writer.
func NewRecorder(w Writer) *Recorder {
	return &Recorder{
		writer: w,
		counts: make(map[Kind]int64),
	}
}

// Record creates and persists an anomaly. Thread-safe. No-op if receiver is nil.
func (r *Recorder) Record(kind Kind, project, path, message, detail string) *Anomaly {
	if r == nil {
		return nil
	}

	id := r.counter.Add(1)
	a := &Anomaly{
		ID:       fmt.Sprintf("A-%06d", id),
		Time:     time.Now().UTC().Format(time.RFC3339),
		Kind:     kind,
		Severity: SeverityFor(kind),
		Project:  project,
		Path:     path,
		Message:  message,
		Detail:   detail,
	}

	r.mu.Lock()
	r.counts[kind]++
	r.mu.Unlock()

	// Auto-populate hypothesis from templates
	if a.Hypothesis == "" {
		a.Hypothesis = HypothesisFor(kind)
	}

	if r.writer != nil {
		_ = r.writer.Write(a) // best-effort; anomaly recording should not block sync
	}

	if r.OnRecord != nil {
		r.OnRecord(a)
	}

	return a
}

// Summary returns anomaly counts by kind since process start.
func (r *Recorder) Summary() map[Kind]int64 {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[Kind]int64, len(r.counts))
	for k, v := range r.counts {
		cp[k] = v
	}
	return cp
}

// SummaryStrings returns anomaly counts keyed by string (for JSON/metrics integration).
func (r *Recorder) SummaryStrings() map[string]int64 {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[string]int64, len(r.counts))
	for k, v := range r.counts {
		cp[string(k)] = v
	}
	return cp
}

// Total returns the total number of anomalies recorded.
func (r *Recorder) Total() int64 {
	if r == nil {
		return 0
	}
	return r.counter.Load()
}

// Close closes the underlying writer.
func (r *Recorder) Close() error {
	if r == nil || r.writer == nil {
		return nil
	}
	return r.writer.Close()
}
