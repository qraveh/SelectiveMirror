// Package sync provides the FairQueue, a thread-safe task queue with
// deduplication and priority support for fair scheduling across mirrors.
package sync

import (
	"context"
	"log/slog"
	gosync "sync"
	"time"
)

// circuitState tracks per-mirror failure state for the circuit breaker.
type circuitState struct {
	consecutiveFailures int
	backoffUntil        time.Time
}

// FairQueue is a thread-safe task queue that provides:
//   - Deduplication: if a file is already queued, re-enqueue moves it to the back
//   - Priority: delete events go to the front of the queue
//   - Fairness: hot files naturally cycle to the back while cold files advance
//   - Cooldown: after a file is synced, it cannot be dequeued again for a configurable duration
//   - Circuit breaker: after N consecutive failures for a mirror, pause that mirror with exponential backoff
//   - Blocking dequeue with context cancellation
//
// It replaces the plain chan Task used previously, which could not inspect
// or reorder queued items.
type FairQueue struct {
	mu             gosync.Mutex
	cond           *gosync.Cond
	items          []Task
	pending        map[string]bool          // tracks which keys are in items (for O(1) membership check)
	cooldowns      map[string]time.Time     // key → earliest allowed dequeue time
	circuits       map[string]*circuitState // mirror name → failure state
	cooldownDur    time.Duration            // base cooldown (used by SetCooldown; legacy)
	closed         bool
	maxSize        int // 0 = unlimited (dedup is the natural bound)
	eventHistory   map[string][]time.Time   // key → recent event timestamps (for adaptive cooldown)
	onOverflow     func()                   // called when queue exceeds warning threshold
	overflowFired  bool                     // debounce: only fire once until queue drains
}

// NewFairQueue creates a FairQueue with an optional max size and cooldown duration.
// If maxSize <= 0, the queue is unbounded. If cooldownDur <= 0, no cooldown is applied.
// Adaptive cooldown constants.
const (
	adaptiveBaseCooldown = 5 * time.Second   // minimum cooldown
	adaptiveMaxCooldown  = 120 * time.Second // maximum cooldown
	adaptiveFreqWindow   = 60 * time.Second  // window for event frequency measurement
	adaptiveMaxFreqFactor = 10               // cap on frequency multiplier
	adaptiveSyncFactor   = 1.5               // multiplier on last sync duration
)

func NewFairQueue(maxSize int, cooldownDur time.Duration) *FairQueue {
	q := &FairQueue{
		pending:      make(map[string]bool),
		cooldowns:    make(map[string]time.Time),
		circuits:     make(map[string]*circuitState),
		cooldownDur:  cooldownDur,
		maxSize:      maxSize,
		eventHistory: make(map[string][]time.Time),
	}
	q.cond = gosync.NewCond(&q.mu)
	return q
}

// SetOnOverflow registers a callback invoked when queue depth exceeds the
// warning threshold (50K items). The callback fires once and resets when the
// queue drains below 25K. Used to trigger accelerated reconciliation.
func (q *FairQueue) SetOnOverflow(fn func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onOverflow = fn
}

// queueKey returns the deduplication key for a task.
// Full-project syncs (RelPath="") return "" so they are never deduplicated.
// Per-file tasks return "project:relPath".
func queueKey(t Task) string {
	if t.RelPath == "" {
		return "" // full-project syncs are never deduplicated
	}
	return t.Project.Name + ":" + t.RelPath
}

// Enqueue adds a task to the back of the queue.
// If a task for the same file is already queued, the old entry is removed
// and the new one is appended at the tail (move-to-back).
// Full-project syncs (RelPath="") are never deduplicated.
//
// SEC-M12: when the queue depth reaches the hard cap, sync tasks are
// REJECTED at the gate (Enqueue returns silently). Delete tasks always
// go through (they're already deduped via priority insertion in
// EnqueuePriority). Without this, a runaway event source or a stalled
// worker could grow the queue arbitrarily — the previous "natural
// dedup bound" worked for steady-state but not for any pathological
// burst (renamed top-level dir → 100k events).
func (q *FairQueue) Enqueue(task Task) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return
	}

	key := queueKey(task)

	// Dedup: if same file already in queue, remove old entry
	if key != "" && q.pending[key] {
		q.removeByKey(key)
	}

	// SEC-M12: hard cap. If we'd exceed the limit AND the existing
	// entry didn't dedup with us, refuse the enqueue. Reconciliation
	// will cover the missed file on the next pass.
	const hardCap = 100000 // 2× overflow warn threshold
	if len(q.items) >= hardCap {
		slog.Warn("queue depth at hard cap; dropping task (will be picked up by next reconciliation)",
			"depth", len(q.items), "project", task.Project.Name, "path", task.RelPath)
		return
	}

	q.items = append(q.items, task)
	if key != "" {
		q.pending[key] = true
	}

	// FR-QUEUE-08/10: log warning and fire overflow callback at 50K depth.
	const overflowThreshold = 50000
	const drainThreshold = 25000
	depth := len(q.items)
	if depth >= overflowThreshold && !q.overflowFired {
		slog.Warn("queue depth exceeds warning threshold", "depth", depth)
		q.overflowFired = true
		if q.onOverflow != nil {
			fn := q.onOverflow
			go fn() // fire outside lock to prevent deadlock
		}
	} else if depth < drainThreshold && q.overflowFired {
		q.overflowFired = false // reset when queue drains
	}

	q.cond.Signal()
}

// EnqueuePriority adds a task to the front of the queue.
// Used for delete events which need prompt execution.
// Priority tasks are NOT deduplicated — each delete must execute.
func (q *FairQueue) EnqueuePriority(task Task) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return
	}

	q.items = append([]Task{task}, q.items...)
	// Shift all pending indices (not tracked by index, just membership)
	// No index update needed since pending is a bool map, not index map
	q.cond.Signal()
}

// SetCooldown marks a file as recently synced. The file cannot be dequeued
// again until the cooldown expires. Called by the sync engine after a
// successful per-file sync. Delete events and full-project syncs are not
// subject to cooldown.
func (q *FairQueue) SetCooldown(key string) {
	if key == "" || q.cooldownDur <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cooldowns[key] = time.Now().Add(q.cooldownDur)
}

// SetAdaptiveCooldown sets a signal-based cooldown for a file after successful sync.
// The cooldown is: max(baseCooldown * eventFrequency, syncDuration * 1.5)
// This ensures hot files (frequent events) get longer cooldowns, and large files
// (long sync times) aren't re-synced before their upload even completes.
func (q *FairQueue) SetAdaptiveCooldown(key string, syncDuration time.Duration) {
	if key == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()

	// Trim event history to window, then append current event
	cutoff := now.Add(-adaptiveFreqWindow)
	history := q.eventHistory[key]
	trimmed := history[:0]
	for _, t := range history {
		if t.After(cutoff) {
			trimmed = append(trimmed, t)
		}
	}
	trimmed = append(trimmed, now)
	q.eventHistory[key] = trimmed

	// Frequency-based component: base * min(eventCount, maxFactor)
	freqFactor := len(trimmed)
	if freqFactor > adaptiveMaxFreqFactor {
		freqFactor = adaptiveMaxFreqFactor
	}
	freqCooldown := adaptiveBaseCooldown * time.Duration(freqFactor)

	// Duration-based component: don't re-sync faster than the sync took
	durationCooldown := time.Duration(float64(syncDuration) * adaptiveSyncFactor)

	// Take the larger of the two
	cooldown := freqCooldown
	if durationCooldown > cooldown {
		cooldown = durationCooldown
	}

	// Cap at maximum
	if cooldown > adaptiveMaxCooldown {
		cooldown = adaptiveMaxCooldown
	}

	q.cooldowns[key] = now.Add(cooldown)

	slog.Debug("adaptive cooldown set",
		"key", key,
		"freq", len(trimmed),
		"syncMs", syncDuration.Milliseconds(),
		"cooldownMs", cooldown.Milliseconds())
}

// Circuit breaker constants.
const (
	circuitBreakerThreshold = 3                // consecutive failures before backoff
	circuitBreakerMaxBackoff = 5 * time.Minute // maximum backoff duration
	circuitBreakerBaseBackoff = 10 * time.Second // initial backoff after threshold
)

// RecordFailure records a sync failure for a mirror. After circuitBreakerThreshold
// consecutive failures, the mirror enters backoff — all its tasks are skipped
// during dequeue until the backoff expires. Backoff doubles each time, capped
// at circuitBreakerMaxBackoff.
// RecordFailure increments the failure counter for a mirror.
// Returns true if this failure caused the circuit breaker to trip (threshold crossed).
//
// Design note (PF-E4): breaker state is keyed on the mirror NAME, not a
// stable ID. There is no `addmirror --rename` flag today — the only way to
// rename is to edit config.yaml manually, which from the engine's
// perspective is a delete + add. Breaker state under the old name then
// becomes inaccessible (it lives in `q.circuits` for the process lifetime
// and is GC'd when the daemon restarts). For a renamed mirror this is
// arguably the correct behavior: a renamed mirror is conceptually a new
// mirror and its failure history shouldn't carry forward. If a stable
// mirror UUID is ever introduced (state-DB schema change), this map's
// key should migrate to that UUID at the same time.
func (q *FairQueue) RecordFailure(mirrorName string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	cs, ok := q.circuits[mirrorName]
	if !ok {
		cs = &circuitState{}
		q.circuits[mirrorName] = cs
	}
	cs.consecutiveFailures++

	if cs.consecutiveFailures >= circuitBreakerThreshold {
		// Exponential backoff: base * 2^(failures - threshold)
		multiplier := 1 << (cs.consecutiveFailures - circuitBreakerThreshold)
		backoff := circuitBreakerBaseBackoff * time.Duration(multiplier)
		if backoff > circuitBreakerMaxBackoff {
			backoff = circuitBreakerMaxBackoff
		}
		cs.backoffUntil = time.Now().Add(backoff)
		return cs.consecutiveFailures == circuitBreakerThreshold // true only on first trip
	}
	return false
}

// RecordSuccess resets the failure counter for a mirror, clearing any backoff.
func (q *FairQueue) RecordSuccess(mirrorName string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if cs, ok := q.circuits[mirrorName]; ok {
		cs.consecutiveFailures = 0
		cs.backoffUntil = time.Time{}
	}
}

// Dequeue removes and returns the first non-cooled task from the queue.
// Tasks in cooldown are skipped (left in queue for later). If all tasks are
// in cooldown, blocks until the earliest cooldown expires, the queue is closed,
// or the context is cancelled.
// Returns (task, true) on success, or (Task{}, false) if closed or cancelled.
func (q *FairQueue) Dequeue(ctx context.Context) (Task, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// PF-D1 design note: this Dequeue spawns a "cancel helper" goroutine
	// that waits on either ctx.Done() or the local `done` channel and
	// broadcasts q.cond on cancel. Defer LIFO ordering guarantees the
	// helper exits cleanly:
	//   - defer close(done) runs FIRST (registered later)
	//   - defer q.mu.Unlock() runs SECOND
	// The helper sees <-done before any caller observes the unlocked
	// mutex, so it cannot outlive Dequeue. We explicitly q.mu.Unlock()
	// during cooldown waits (line ~380) and re-Lock before any return,
	// so the deferred Unlock always sees a locked mutex on the way out.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.cond.Broadcast()
		case <-done:
		}
	}()
	defer close(done)

	for {
		// Wait until there are items
		for len(q.items) == 0 && !q.closed && ctx.Err() == nil {
			q.cond.Wait()
		}

		if ctx.Err() != nil {
			return Task{}, false
		}

		// Scan for first eligible item (not cooled, mirror not in backoff)
		now := time.Now()
		earliestExpiry := time.Time{}
		for i, item := range q.items {
			key := queueKey(item)
			mirror := item.Project.Name

			// Priority items (deletes) skip cooldown and circuit breaker
			if item.Type == TaskDelete {
				q.items = append(q.items[:i], q.items[i+1:]...)
				if key != "" {
					delete(q.pending, key)
				}
				return item, true
			}

			// Full-project syncs skip cooldown but respect circuit breaker
			if key == "" {
				if cs, ok := q.circuits[mirror]; ok && cs.backoffUntil.After(now) {
					if earliestExpiry.IsZero() || cs.backoffUntil.Before(earliestExpiry) {
						earliestExpiry = cs.backoffUntil
					}
					continue
				}
				q.items = append(q.items[:i], q.items[i+1:]...)
				return item, true
			}

			// Check circuit breaker (mirror-level backoff)
			if cs, ok := q.circuits[mirror]; ok && cs.backoffUntil.After(now) {
				if earliestExpiry.IsZero() || cs.backoffUntil.Before(earliestExpiry) {
					earliestExpiry = cs.backoffUntil
				}
				continue
			}

			// Check per-file cooldown
			if expiry, ok := q.cooldowns[key]; ok && expiry.After(now) {
				if earliestExpiry.IsZero() || expiry.Before(earliestExpiry) {
					earliestExpiry = expiry
				}
				continue
			}

			// Eligible — take it
			q.items = append(q.items[:i], q.items[i+1:]...)
			delete(q.pending, key)
			delete(q.cooldowns, key)
			return item, true
		}

		// All items are in cooldown (or queue became empty via close)
		if q.closed && len(q.items) == 0 {
			return Task{}, false
		}

		if !earliestExpiry.IsZero() {
			// Wait until earliest cooldown expires, then re-scan
			waitDur := time.Until(earliestExpiry)
			if waitDur > 0 {
				q.mu.Unlock()
				timer := time.NewTimer(waitDur)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					q.mu.Lock()
					return Task{}, false
				case <-done:
					timer.Stop()
					q.mu.Lock()
					return Task{}, false
				}
				q.mu.Lock()
				continue // re-scan with lock held
			}
			continue // expiry passed during scan, re-scan immediately
		}

		// No items and not closed — wait for new items
		if len(q.items) == 0 {
			q.cond.Wait()
			if q.closed && len(q.items) == 0 {
				return Task{}, false
			}
		}
	}
}

// Close signals that no more tasks will be enqueued.
// All blocked Dequeue calls will return (Task{}, false).
// Remaining items can still be dequeued.
func (q *FairQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

// Len returns the current number of items in the queue.
func (q *FairQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// removeByKey removes the first item matching the given key.
// Caller must hold q.mu.
//
// Adversarial review #18 / PF-D1: previously the older task's Done
// callback was silently dropped on the assumption that "the replacement
// task will call it when processed." That's only true when the replacing
// caller carries forward the same Done — which today's only Done-using
// caller (reconcileAll with RelPath="") doesn't dedup against. A future
// caller that sets Done on a per-file task would deadlock its WaitGroup.
// Fix: invoke the displaced task's Done callback before discarding it.
// The replacement task may still install its own Done; both are honored.
func (q *FairQueue) removeByKey(key string) {
	for i, t := range q.items {
		if queueKey(t) == key {
			displaced := t
			q.items = append(q.items[:i], q.items[i+1:]...)
			delete(q.pending, key)
			// Fire the displaced Done OUTSIDE the mutex if non-nil. We can't
			// release q.mu here (caller holds it), so we capture and defer
			// via a goroutine — Done is a one-shot signal; ordering relative
			// to the replacement task doesn't matter.
			if displaced.Done != nil {
				go displaced.Done()
			}
			return
		}
	}
}
