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
	mu           gosync.Mutex
	cond         *gosync.Cond
	items        []Task
	pending      map[string]bool          // tracks which keys are in items (for O(1) membership check)
	cooldowns    map[string]time.Time     // key → earliest allowed dequeue time
	circuits     map[string]*circuitState // mirror name → failure state
	cooldownDur  time.Duration            // base cooldown (used by SetCooldown; legacy)
	closed       bool
	maxSize      int // 0 = unlimited
	eventHistory map[string][]time.Time   // key → recent event timestamps (for adaptive cooldown)
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

	// Max size enforcement: drop with warning if at capacity
	if q.maxSize > 0 && len(q.items) >= q.maxSize {
		// Already at capacity and couldn't dedup — drop the event
		q.mu.Unlock()
		q.mu.Lock()
		return
	}

	q.items = append(q.items, task)
	if key != "" {
		q.pending[key] = true
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
func (q *FairQueue) RecordFailure(mirrorName string) {
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
	}
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

	// Start a goroutine to signal cond when context is cancelled
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
func (q *FairQueue) removeByKey(key string) {
	for i, t := range q.items {
		if queueKey(t) == key {
			// Call Done callback if present (task is being replaced, not executed)
			// Don't call Done — the replacement task will call it when processed.
			q.items = append(q.items[:i], q.items[i+1:]...)
			delete(q.pending, key)
			return
		}
	}
}
