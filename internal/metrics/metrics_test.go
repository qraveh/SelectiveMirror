package metrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordSync(t *testing.T) {
	c := New()

	c.RecordSync("Proj1", 1024, 150)
	c.RecordSync("Proj1", 2048, 200)
	c.RecordSync("Proj2", 512, 100)

	s := c.Snapshot("test")
	if s.FilesSynced != 3 {
		t.Errorf("expected 3 files synced, got %d", s.FilesSynced)
	}
	if s.BytesUploaded != 3584 {
		t.Errorf("expected 3584 bytes uploaded, got %d", s.BytesUploaded)
	}
	if s.AvgLatencyMs != 150 { // (150+200+100)/3 = 150
		t.Errorf("expected avg latency 150, got %d", s.AvgLatencyMs)
	}
}

func TestRecordError(t *testing.T) {
	c := New()

	c.RecordError("Proj1", "rclone exit 2")
	c.RecordError("Proj1", "rclone exit 7")

	s := c.Snapshot("test")
	if s.SyncErrors != 2 {
		t.Errorf("expected 2 errors, got %d", s.SyncErrors)
	}

	ps, ok := s.Projects["Proj1"]
	if !ok {
		t.Fatal("expected Proj1 in projects")
	}
	if ps.LastError != "rclone exit 7" {
		t.Errorf("expected last error 'rclone exit 7', got %q", ps.LastError)
	}
}

func TestQueueDepth(t *testing.T) {
	c := New()

	c.SetQueueDepth(42)
	s := c.Snapshot("test")
	if s.QueueDepth != 42 {
		t.Errorf("expected queue depth 42, got %d", s.QueueDepth)
	}
}

func TestRecordScanComplete(t *testing.T) {
	c := New()
	c.RecordScanComplete()

	s := c.Snapshot("test")
	if s.LastScanTime == "" {
		t.Error("expected LastScanTime to be set")
	}
}

func TestSnapshotVersion(t *testing.T) {
	c := New()
	s := c.Snapshot("1.2.3")
	if s.Version != "1.2.3" {
		t.Errorf("expected version 1.2.3, got %s", s.Version)
	}
}

func TestWriteStatusFile(t *testing.T) {
	dir := t.TempDir()
	c := New()
	c.RecordSync("P", 100, 50)

	err := c.WriteStatusFile(dir, "test")
	if err != nil {
		t.Fatalf("WriteStatusFile failed: %v", err)
	}

	path := filepath.Join(dir, "status.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading status.json: %v", err)
	}

	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if s.FilesSynced != 1 {
		t.Errorf("expected 1 file synced in status.json, got %d", s.FilesSynced)
	}
}

func TestFormatHuman(t *testing.T) {
	c := New()
	c.RecordSync("P", 1024*1024, 100) // 1 MB

	s := c.FormatHuman()
	if s == "" {
		t.Error("expected non-empty human format")
	}
}

func TestEmptySnapshot(t *testing.T) {
	c := New()
	s := c.Snapshot("dev")

	if s.FilesSynced != 0 {
		t.Errorf("expected 0 files synced, got %d", s.FilesSynced)
	}
	if s.AvgLatencyMs != 0 {
		t.Errorf("expected 0 avg latency, got %d", s.AvgLatencyMs)
	}
	if len(s.Projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(s.Projects))
	}
}

func TestLatencyPercentiles(t *testing.T) {
	c := New()

	// Empty: all zeros
	if got := c.LatencyP95(); got != 0 {
		t.Errorf("empty p95: got %d, want 0", got)
	}
	if got := c.LatencyP99(); got != 0 {
		t.Errorf("empty p99: got %d, want 0", got)
	}

	// Record 100 syncs: latencies 1, 2, 3, ..., 100
	for i := int64(1); i <= 100; i++ {
		c.RecordSync("test", 0, i)
	}

	p95 := c.LatencyP95()
	if p95 < 95 || p95 > 96 {
		t.Errorf("p95 of 1..100: got %d, want ~95", p95)
	}
	p99 := c.LatencyP99()
	if p99 < 99 || p99 > 100 {
		t.Errorf("p99 of 1..100: got %d, want ~99", p99)
	}

	// Snapshot includes percentiles
	s := c.Snapshot("test")
	if s.P95LatencyMs != p95 {
		t.Errorf("snapshot p95: got %d, want %d", s.P95LatencyMs, p95)
	}
	if s.P99LatencyMs != p99 {
		t.Errorf("snapshot p99: got %d, want %d", s.P99LatencyMs, p99)
	}
}

func TestLatencyRingOverflow(t *testing.T) {
	c := New()

	// Fill beyond ring size — old values should be evicted
	for i := int64(0); i < latencyRingSize+500; i++ {
		c.RecordSync("test", 0, i)
	}

	if c.latencyLen != latencyRingSize {
		t.Errorf("ring len: got %d, want %d", c.latencyLen, latencyRingSize)
	}

	// p95 should reflect only the latest 1000 values (500..1499)
	p95 := c.LatencyP95()
	if p95 < 1400 || p95 > 1500 {
		t.Errorf("overflow p95: got %d, want ~1450", p95)
	}
}
