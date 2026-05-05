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
	KindSyncTimeout       Kind = "Sync:Timeout"  // legacy: 5-min wall-clock; emitted only via SMIRROR_DISABLE_LIVENESS=1
	KindSyncStalled       Kind = "Sync:Stalled"  // multi-signal flatline: rclone wedged below its own retry layer
	KindSyncLsJsonSlow    Kind = "Sync:LsJsonSlow" // info: lsjson elapsed past warn threshold, but still alive
	KindSyncFailure       Kind = "Sync:Failure"
	KindStateError        Kind = "State:Error" // state-DB write failure (audit log row, sync_state row, meta key) — defense-in-depth
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
	case KindWatcherError, KindQueueDepthWarning, KindGhostLeak, KindGhostOrphan, KindGhostStale, KindPathGone, KindSyncTimeout, KindSyncStalled, KindStateError:
		return SeverityWarning
	case KindSyncLsJsonSlow:
		return SeverityInfo
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
	//
	// PF-A8: the callback runs in a dedicated
	// goroutine, NOT in Record's calling goroutine, so a slow webhook
	// never blocks the sync engine. Anomalies overflow a bounded channel
	// rather than back-pressuring; overflow is counted in droppedCallbacks
	// and a Queue:DepthWarning is recorded once per overflow.
	OnRecord func(a *Anomaly)

	// callbackQueue is a bounded channel feeding the OnRecord goroutine.
	// Buffer is sized for short bursts (~30s of typical anomaly rate at
	// load). Overflow drops the anomaly's callback (the writer.Write
	// path still ran, so the on-disk record is preserved — only the
	// alerting hook is dropped).
	callbackQueue       chan *Anomaly
	droppedCallbacks    atomic.Int64
	overflowAnnounced   atomic.Bool
	callbackGoroutineWg sync.WaitGroup

	// sendMu serializes channel send (Record path) against channel
	// close (Close path). SM-186: the prior implementation used an
	// `atomic.Bool` checked-then-sent pattern which is not atomic as a
	// pair — a goroutine could read closed=false, then a concurrent
	// Close could set closed=true and close the channel, then the
	// first goroutine's send would panic on a closed channel. The
	// RWMutex makes the check-and-send a single critical section
	// against close.
	//
	//   Record: RLock → check closed → send (non-blocking) → RUnlock
	//   Close:  Lock  → set closed   → close(channel)       → Unlock
	//
	// Multiple Records can run concurrently (RLock); Close blocks
	// until in-flight Records finish, then performs the close exactly
	// once.
	sendMu sync.RWMutex
	closed bool
}

// Writer is the interface for anomaly persistence.
type Writer interface {
	Write(a *Anomaly) error
	Close() error
}

// NewRecorder creates an anomaly recorder with the given writer.
//
// PF-A8: starts a background goroutine that consumes the OnRecord
// callback queue. The queue is bounded so a slow webhook can't accumulate
// unbounded memory; overflow drops the callback for that anomaly and is
// counted (see droppedCallbacks).
func NewRecorder(w Writer) *Recorder {
	r := &Recorder{
		writer:        w,
		counts:        make(map[Kind]int64),
		callbackQueue: make(chan *Anomaly, 64),
	}
	r.callbackGoroutineWg.Add(1)
	go r.runCallbackLoop()
	return r
}

// runCallbackLoop drains callbackQueue and invokes OnRecord. Single-
// goroutine consumer guarantees ordered callback delivery; the channel
// is closed by Close which then drains pending entries before exit.
func (r *Recorder) runCallbackLoop() {
	defer r.callbackGoroutineWg.Done()
	for a := range r.callbackQueue {
		// Snapshot OnRecord under no lock — the field is documented as
		// "set before use" so a torn read is not a concern in practice.
		fn := r.OnRecord
		if fn != nil {
			func() {
				defer func() {
					// Don't let a panicking callback crash the recorder.
					_ = recover()
				}()
				fn(a)
			}()
		}
	}
}

// SetOnRecord installs a callback fired after each Record. No-op on nil receiver.
// Use this instead of writing to the OnRecord field directly: when anomaly
// detection is disabled, the recorder is nil and a direct field assignment
// would panic at startup.
func (r *Recorder) SetOnRecord(fn func(a *Anomaly)) {
	if r == nil {
		return
	}
	r.OnRecord = fn
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

	// PF-A8: hand off to the callback goroutine via a bounded channel.
	// Non-blocking send: if the consumer is slow (e.g., webhook is wedged
	// against a 5-second HTTP timeout), drop the callback and increment
	// the counter. The on-disk record from writer.Write above is intact —
	// only the alerting hook is dropped.
	//
	// SM-186: send is performed under sendMu's RLock. Concurrent Close
	// takes sendMu's write-lock, so this check-and-send cannot race
	// against `close(r.callbackQueue)`. If Close has already run when
	// we acquire RLock, `r.closed` is true and we skip the send entirely.
	if r.OnRecord != nil {
		r.sendMu.RLock()
		if !r.closed {
			select {
			case r.callbackQueue <- a:
			default:
				r.droppedCallbacks.Add(1)
				// Announce the overflow once so an operator knows the alerting
				// stream is degraded. Subsequent drops are silent (counter only).
				if r.overflowAnnounced.CompareAndSwap(false, true) {
					select {
					case r.callbackQueue <- &Anomaly{
						ID:       fmt.Sprintf("A-overflow-%d", time.Now().UnixNano()),
						Time:     time.Now().UTC().Format(time.RFC3339),
						Kind:     KindQueueDepthWarning,
						Severity: SeverityWarning,
						Message:  "anomaly callback queue overflow — webhook downstream is slow",
						Detail:   "subsequent dropped callbacks counted in droppedCallbacks (see Recorder)",
					}:
					default:
					}
				}
			}
		}
		r.sendMu.RUnlock()
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

// Close closes the underlying writer and stops the callback goroutine.
// Subsequent Record calls do not block; pending callbacks already in
// the queue at Close time are drained before the goroutine exits.
//
// SM-186: takes sendMu's write-lock to serialize against any in-flight
// Record's send. The lock-based mark-and-close is the critical section
// the prior atomic.Bool / non-atomic-check-and-send pair lacked.
func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.sendMu.Lock()
	alreadyClosed := r.closed
	if !alreadyClosed {
		r.closed = true
		close(r.callbackQueue)
	}
	r.sendMu.Unlock()
	if !alreadyClosed {
		r.callbackGoroutineWg.Wait()
	}
	if r.writer == nil {
		return nil
	}
	return r.writer.Close()
}

// DroppedCallbacks returns the number of OnRecord callbacks dropped due
// to queue overflow since process start. Useful for status output:
// non-zero indicates the webhook downstream is slower than the sync
// engine's anomaly rate.
func (r *Recorder) DroppedCallbacks() int64 {
	if r == nil {
		return 0
	}
	return r.droppedCallbacks.Load()
}
