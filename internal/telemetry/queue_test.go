package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueue_NewQueueCreatesDirs(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewQueue(dir); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"sending", "dead-letter"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Errorf("subdir %q not created: %v", sub, err)
		}
	}
}

func TestQueue_EnqueueAndClaim(t *testing.T) {
	dir := t.TempDir()
	q, err := NewQueue(dir)
	if err != nil {
		t.Fatal(err)
	}

	payload := []byte(`{"hello":"world"}`)
	path, err := q.Enqueue(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Fatal("enqueued file not on disk")
	}

	n, err := q.PendingCount()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("PendingCount = %d, want 1", n)
	}

	claimed, got, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("claimed payload = %q, want %q", got, payload)
	}
	if !fileExists(claimed) {
		t.Fatal("claimed file not in sending")
	}
	if fileExists(path) {
		t.Error("original path still exists after claim")
	}

	// PendingCount should now be 0 (file moved to sending)
	n, _ = q.PendingCount()
	if n != 0 {
		t.Errorf("PendingCount after claim = %d, want 0", n)
	}
}

func TestQueue_Complete(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))

	claimed, _, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(claimed); err != nil {
		t.Fatal(err)
	}
	if fileExists(claimed) {
		t.Error("file still exists after Complete")
	}

	// Complete is idempotent — second call on missing file is fine.
	if err := q.Complete(claimed); err != nil {
		t.Errorf("second Complete returned error: %v", err)
	}
}

func TestQueue_Release(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))

	claimed, _, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Release(claimed); err != nil {
		t.Fatal(err)
	}
	n, _ := q.PendingCount()
	if n != 1 {
		t.Errorf("PendingCount after Release = %d, want 1", n)
	}
	if fileExists(claimed) {
		t.Error("claimed path still exists after Release (should have moved)")
	}
}

func TestQueue_DeadLetter(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))

	claimed, _, _ := q.Claim()
	if err := q.DeadLetter(claimed); err != nil {
		t.Fatal(err)
	}

	dlc, err := q.DeadLetterCount()
	if err != nil {
		t.Fatal(err)
	}
	if dlc != 1 {
		t.Errorf("DeadLetterCount = %d, want 1", dlc)
	}
}

func TestQueue_ClaimEmpty(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)

	path, payload, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || payload != nil {
		t.Error("Claim on empty queue should return empty values")
	}
}

func TestQueue_OldestFirst(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)

	_, _ = q.Enqueue([]byte(`"first"`))
	time.Sleep(2 * time.Millisecond) // unix-nano differs
	_, _ = q.Enqueue([]byte(`"second"`))

	_, got, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `"first"` {
		t.Errorf("got %q, want first", got)
	}
}

func TestQueue_SweepStaleClaims(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))

	claimed, _, _ := q.Claim()

	// Backdate the claimed file to look stale
	staleTime := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(claimed, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	swept, err := q.SweepStaleClaims(30 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if swept != 1 {
		t.Errorf("swept = %d, want 1", swept)
	}
	n, _ := q.PendingCount()
	if n != 1 {
		t.Errorf("PendingCount after sweep = %d, want 1", n)
	}
}

func TestQueue_PurgeAll(t *testing.T) {
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))
	_, _ = q.Enqueue([]byte(`{}`))
	c1, _, _ := q.Claim() // one in sending
	_ = q.DeadLetter(c1)  // one in dead-letter

	purged, err := q.PurgeAll()
	if err != nil {
		t.Fatal(err)
	}
	if purged < 2 {
		t.Errorf("purged = %d, want >= 2", purged)
	}

	n, _ := q.PendingCount()
	if n != 0 {
		t.Errorf("PendingCount after PurgeAll = %d, want 0", n)
	}
	dlc, _ := q.DeadLetterCount()
	if dlc != 0 {
		t.Errorf("DeadLetterCount after PurgeAll = %d, want 0", dlc)
	}
}

func TestQueue_AtomicWrite(t *testing.T) {
	// Verify Enqueue uses temp+rename — at no point should a partial
	// .json file be visible to a concurrent Claim. We can't easily
	// test the race directly, but we can verify the .tmp file is gone
	// after Enqueue completes.
	dir := t.TempDir()
	q, _ := NewQueue(dir)
	_, _ = q.Enqueue([]byte(`{}`))

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file %s left behind after Enqueue", e.Name())
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
