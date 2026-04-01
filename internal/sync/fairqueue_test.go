package sync

import (
	"context"
	gosync "sync"
	"testing"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
)

func testTask(project, path string) Task {
	return Task{
		Project: config.Project{Name: project},
		RelPath: path,
	}
}

func testDeleteTask(project, path string) Task {
	return Task{
		Project: config.Project{Name: project},
		RelPath: path,
		Type:    TaskDelete,
	}
}

// --- queueKey tests ---

func TestQueueKey_PerFile(t *testing.T) {
	task := testTask("proj", "src/main.go")
	key := queueKey(task)
	if key != "proj:src/main.go" {
		t.Errorf("queueKey = %q, want %q", key, "proj:src/main.go")
	}
}

func TestQueueKey_FullProjectSync_ReturnsEmpty(t *testing.T) {
	task := testTask("proj", "")
	key := queueKey(task)
	if key != "" {
		t.Errorf("queueKey for full sync = %q, want empty", key)
	}
}

// --- Basic FIFO ---

func TestFairQueue_EnqueueDequeue_FIFO(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "file1.txt"))
	q.Enqueue(testTask("B", "file2.txt"))
	q.Enqueue(testTask("A", "file3.txt"))

	ctx := context.Background()
	t1, ok := q.Dequeue(ctx)
	if !ok || t1.RelPath != "file1.txt" {
		t.Errorf("first dequeue = %q, want file1.txt", t1.RelPath)
	}
	t2, ok := q.Dequeue(ctx)
	if !ok || t2.RelPath != "file2.txt" {
		t.Errorf("second dequeue = %q, want file2.txt", t2.RelPath)
	}
	t3, ok := q.Dequeue(ctx)
	if !ok || t3.RelPath != "file3.txt" {
		t.Errorf("third dequeue = %q, want file3.txt", t3.RelPath)
	}
}

// --- Deduplication ---

func TestFairQueue_Dedup_MovesToBack(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "hot.txt"))
	q.Enqueue(testTask("A", "cold.txt"))
	// Re-enqueue hot.txt — should move to back
	q.Enqueue(testTask("A", "hot.txt"))

	if q.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (dedup should remove old entry)", q.Len())
	}

	ctx := context.Background()
	t1, _ := q.Dequeue(ctx)
	t2, _ := q.Dequeue(ctx)

	if t1.RelPath != "cold.txt" {
		t.Errorf("first dequeue = %q, want cold.txt (should advance)", t1.RelPath)
	}
	if t2.RelPath != "hot.txt" {
		t.Errorf("second dequeue = %q, want hot.txt (moved to back)", t2.RelPath)
	}
}

func TestFairQueue_Dedup_RepeatedHotFile(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "cold1.txt"))
	q.Enqueue(testTask("A", "hot.txt"))
	q.Enqueue(testTask("A", "cold2.txt"))
	// hot.txt changes again — moves to back
	q.Enqueue(testTask("A", "hot.txt"))

	if q.Len() != 3 {
		t.Fatalf("Len = %d, want 3", q.Len())
	}

	ctx := context.Background()
	t1, _ := q.Dequeue(ctx)
	t2, _ := q.Dequeue(ctx)
	t3, _ := q.Dequeue(ctx)

	if t1.RelPath != "cold1.txt" {
		t.Errorf("1st = %q, want cold1.txt", t1.RelPath)
	}
	if t2.RelPath != "cold2.txt" {
		t.Errorf("2nd = %q, want cold2.txt", t2.RelPath)
	}
	if t3.RelPath != "hot.txt" {
		t.Errorf("3rd = %q, want hot.txt (moved to back twice)", t3.RelPath)
	}
}

func TestFairQueue_DifferentProjects_NoCrossDedup(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "file.txt"))
	q.Enqueue(testTask("B", "file.txt"))

	if q.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (different projects, no dedup)", q.Len())
	}
}

// --- Full-project syncs never deduplicated ---

func TestFairQueue_FullProjectSync_NotDeduplicated(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", ""))
	q.Enqueue(testTask("A", ""))
	q.Enqueue(testTask("A", ""))

	if q.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (full syncs are never deduplicated)", q.Len())
	}
}

// --- Priority (delete events) ---

func TestFairQueue_Priority_GoesToFront(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "file1.txt"))
	q.Enqueue(testTask("A", "file2.txt"))
	// Delete goes to front
	q.EnqueuePriority(testDeleteTask("A", "urgent.txt"))

	ctx := context.Background()
	t1, _ := q.Dequeue(ctx)
	if t1.RelPath != "urgent.txt" || t1.Type != TaskDelete {
		t.Errorf("first dequeue = %q type=%d, want urgent.txt delete", t1.RelPath, t1.Type)
	}
}

func TestFairQueue_DeleteNotCoalesced(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.EnqueuePriority(testDeleteTask("A", "file.txt"))
	q.EnqueuePriority(testDeleteTask("A", "file.txt"))

	// Both deletes should be present (not deduplicated)
	if q.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (deletes are not deduplicated)", q.Len())
	}
}

// --- Blocking and close ---

func TestFairQueue_Dequeue_BlocksUntilEnqueue(t *testing.T) {
	q := NewFairQueue(0, 0)
	done := make(chan Task)

	go func() {
		task, ok := q.Dequeue(context.Background())
		if ok {
			done <- task
		}
	}()

	// Should be blocked
	select {
	case <-done:
		t.Fatal("Dequeue returned before Enqueue")
	case <-time.After(100 * time.Millisecond):
		// Good — still blocked
	}

	q.Enqueue(testTask("A", "file.txt"))

	select {
	case task := <-done:
		if task.RelPath != "file.txt" {
			t.Errorf("got %q, want file.txt", task.RelPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dequeue never unblocked after Enqueue")
	}
}

func TestFairQueue_Close_UnblocksDequeue(t *testing.T) {
	q := NewFairQueue(0, 0)
	done := make(chan bool)

	go func() {
		_, ok := q.Dequeue(context.Background())
		done <- ok
	}()

	time.Sleep(50 * time.Millisecond)
	q.Close()

	select {
	case ok := <-done:
		if ok {
			t.Error("Dequeue returned ok=true after Close on empty queue")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dequeue never unblocked after Close")
	}
}

func TestFairQueue_Close_DrainRemaining(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Enqueue(testTask("A", "file.txt"))
	q.Close()

	// Should still be able to dequeue remaining items
	task, ok := q.Dequeue(context.Background())
	if !ok {
		t.Fatal("should be able to dequeue remaining items after Close")
	}
	if task.RelPath != "file.txt" {
		t.Errorf("got %q, want file.txt", task.RelPath)
	}

	// Now queue is empty and closed
	_, ok = q.Dequeue(context.Background())
	if ok {
		t.Error("should return false when empty and closed")
	}
}

func TestFairQueue_ContextCancel_UnblocksDequeue(t *testing.T) {
	q := NewFairQueue(0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)

	go func() {
		_, ok := q.Dequeue(ctx)
		done <- ok
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case ok := <-done:
		if ok {
			t.Error("Dequeue returned ok=true after context cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dequeue never unblocked after context cancel")
	}
}

// --- Concurrent safety ---

func TestFairQueue_ConcurrentEnqueueDequeue(t *testing.T) {
	q := NewFairQueue(0, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const producers = 10
	const itemsPerProducer = 100
	totalExpected := producers * itemsPerProducer

	// Note: with deduplication, if producers use the same file names,
	// fewer items will be in the queue. Use unique names.
	var wg gosync.WaitGroup

	// Producers
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			for i := 0; i < itemsPerProducer; i++ {
				q.Enqueue(Task{
					Project: config.Project{Name: "proj"},
					RelPath: string(rune('A'+pid)) + string(rune('0'+i%10)) + ".txt",
				})
			}
		}(p)
	}

	// Consumer
	consumed := 0
	consumerDone := make(chan int)
	go func() {
		for {
			_, ok := q.Dequeue(ctx)
			if !ok {
				break
			}
			consumed++
		}
		consumerDone <- consumed
	}()

	wg.Wait()
	q.Close()

	count := <-consumerDone
	// With dedup, count may be less than totalExpected (some names collide)
	if count == 0 {
		t.Fatal("consumed 0 items")
	}
	if count > totalExpected {
		t.Errorf("consumed %d > expected %d", count, totalExpected)
	}
	t.Logf("consumed %d of %d potential items (dedup reduced)", count, totalExpected)
}

// --- Starvation test ---

func TestFairQueue_Starvation_HotFileCyclesBack(t *testing.T) {
	q := NewFairQueue(0, 0)

	// Enqueue: hot, cold1, cold2
	q.Enqueue(testTask("A", "hot.txt"))
	q.Enqueue(testTask("A", "cold1.txt"))
	q.Enqueue(testTask("A", "cold2.txt"))

	ctx := context.Background()

	// Dequeue hot.txt (front)
	t1, _ := q.Dequeue(ctx)
	if t1.RelPath != "hot.txt" {
		t.Fatalf("1st = %q, want hot.txt", t1.RelPath)
	}

	// hot.txt changes again — re-enqueue (goes to back)
	q.Enqueue(testTask("A", "hot.txt"))

	// Cold files should dequeue before hot
	t2, _ := q.Dequeue(ctx)
	if t2.RelPath != "cold1.txt" {
		t.Errorf("2nd = %q, want cold1.txt", t2.RelPath)
	}
	t3, _ := q.Dequeue(ctx)
	if t3.RelPath != "cold2.txt" {
		t.Errorf("3rd = %q, want cold2.txt", t3.RelPath)
	}
	t4, _ := q.Dequeue(ctx)
	if t4.RelPath != "hot.txt" {
		t.Errorf("4th = %q, want hot.txt (cycled to back)", t4.RelPath)
	}
}

// --- Len ---

func TestFairQueue_Len(t *testing.T) {
	q := NewFairQueue(0, 0)
	if q.Len() != 0 {
		t.Errorf("empty Len = %d", q.Len())
	}

	q.Enqueue(testTask("A", "a.txt"))
	q.Enqueue(testTask("A", "b.txt"))
	if q.Len() != 2 {
		t.Errorf("Len = %d, want 2", q.Len())
	}

	// Dedup: re-enqueue a.txt
	q.Enqueue(testTask("A", "a.txt"))
	if q.Len() != 2 {
		t.Errorf("after dedup Len = %d, want 2", q.Len())
	}
}

// --- Enqueue after Close ---

func TestFairQueue_EnqueueAfterClose_Ignored(t *testing.T) {
	q := NewFairQueue(0, 0)
	q.Close()
	q.Enqueue(testTask("A", "file.txt"))
	if q.Len() != 0 {
		t.Errorf("Len after enqueue on closed = %d, want 0", q.Len())
	}
}

// --- Cooldown tests ---

// TestFairQueue_Cooldown_BlocksRepeatedDequeue verifies that a file in cooldown
// is not dequeued until the cooldown expires.
func TestFairQueue_Cooldown_BlocksRepeatedDequeue(t *testing.T) {
	q := NewFairQueue(0, 200*time.Millisecond)

	q.Enqueue(testTask("A", "hot.txt"))
	task, ok := q.Dequeue(context.Background())
	if !ok || task.RelPath != "hot.txt" {
		t.Fatal("first dequeue failed")
	}

	// Set cooldown and re-enqueue
	q.SetCooldown("A:hot.txt")
	q.Enqueue(testTask("A", "hot.txt"))

	// Dequeue should block until cooldown expires (~200ms)
	start := time.Now()
	task, ok = q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.RelPath != "hot.txt" {
		t.Fatal("second dequeue failed")
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("dequeued too early: %v (cooldown is 200ms)", elapsed)
	}
}

// TestFairQueue_Cooldown_OtherFilesUnaffected verifies that cooldown on file A
// does not block dequeue of file B.
func TestFairQueue_Cooldown_OtherFilesUnaffected(t *testing.T) {
	q := NewFairQueue(0, 5*time.Second) // long cooldown

	// Sync A, set cooldown
	q.Enqueue(testTask("A", "hot.txt"))
	q.Dequeue(context.Background())
	q.SetCooldown("A:hot.txt")

	// Enqueue both A (cooled) and B (cold)
	q.Enqueue(testTask("A", "hot.txt"))
	q.Enqueue(testTask("A", "cold.txt"))

	// Dequeue should return cold.txt (skipping cooled hot.txt)
	task, ok := q.Dequeue(context.Background())
	if !ok {
		t.Fatal("dequeue failed")
	}
	if task.RelPath != "cold.txt" {
		t.Errorf("got %q, want cold.txt (hot.txt should be in cooldown)", task.RelPath)
	}
}

// TestFairQueue_Cooldown_FirstSyncNoCooldown verifies that a file with no
// cooldown entry dequeues immediately.
func TestFairQueue_Cooldown_FirstSyncNoCooldown(t *testing.T) {
	q := NewFairQueue(0, 5*time.Second)

	q.Enqueue(testTask("A", "new.txt"))

	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.RelPath != "new.txt" {
		t.Fatal("dequeue failed")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("first sync should be instant, took %v", elapsed)
	}
}

// TestFairQueue_Cooldown_DeletesIgnoreCooldown verifies that delete events
// (EnqueuePriority) bypass cooldown.
func TestFairQueue_Cooldown_DeletesIgnoreCooldown(t *testing.T) {
	q := NewFairQueue(0, 5*time.Second)

	// Set cooldown on a file
	q.SetCooldown("A:file.txt")

	// Enqueue a delete for the same file (priority)
	q.EnqueuePriority(testDeleteTask("A", "file.txt"))

	// Should dequeue immediately (deletes ignore cooldown)
	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.Type != TaskDelete {
		t.Fatal("expected delete task")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("delete should bypass cooldown, took %v", elapsed)
	}
}

// TestFairQueue_Cooldown_FullSyncIgnoreCooldown verifies that full-project syncs
// (RelPath="") are not subject to cooldown.
func TestFairQueue_Cooldown_FullSyncIgnoreCooldown(t *testing.T) {
	q := NewFairQueue(0, 5*time.Second)

	q.Enqueue(testTask("A", ""))

	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.RelPath != "" {
		t.Fatal("expected full-project sync")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("full sync should ignore cooldown, took %v", elapsed)
	}
}

// TestFairQueue_Cooldown_Expires verifies that a cooled file becomes available
// after the cooldown duration.
func TestFairQueue_Cooldown_Expires(t *testing.T) {
	q := NewFairQueue(0, 200*time.Millisecond)

	q.SetCooldown("A:file.txt")

	// Wait for cooldown to expire
	time.Sleep(250 * time.Millisecond)

	q.Enqueue(testTask("A", "file.txt"))

	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.RelPath != "file.txt" {
		t.Fatal("dequeue failed after cooldown expired")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("should dequeue instantly after cooldown expired, took %v", elapsed)
	}
}

// TestFairQueue_Cooldown_AllCooledDown_Waits verifies that when all items are
// in cooldown, Dequeue blocks until the earliest cooldown expires.
func TestFairQueue_Cooldown_AllCooledDown_Waits(t *testing.T) {
	q := NewFairQueue(0, 300*time.Millisecond)

	q.SetCooldown("A:a.txt")
	q.SetCooldown("A:b.txt")

	q.Enqueue(testTask("A", "a.txt"))
	q.Enqueue(testTask("A", "b.txt"))

	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("dequeue failed")
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("should wait for cooldown, only waited %v", elapsed)
	}
	// Should get one of the two files
	if task.RelPath != "a.txt" && task.RelPath != "b.txt" {
		t.Errorf("unexpected file: %q", task.RelPath)
	}
}

// TestFairQueue_Cooldown_ZeroDuration_Disabled verifies that cooldown=0
// disables the cooldown mechanism entirely (SetCooldown is a no-op).
func TestFairQueue_Cooldown_ZeroDuration_Disabled(t *testing.T) {
	q := NewFairQueue(0, 0) // no cooldown

	q.Enqueue(testTask("A", "file.txt"))
	q.Dequeue(context.Background())

	q.SetCooldown("A:file.txt") // should be no-op
	q.Enqueue(testTask("A", "file.txt"))

	start := time.Now()
	task, ok := q.Dequeue(context.Background())
	elapsed := time.Since(start)

	if !ok || task.RelPath != "file.txt" {
		t.Fatal("dequeue failed")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("should be instant with cooldown=0, took %v", elapsed)
	}
}
