package anomaly

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotate_RemovesOldFiles(t *testing.T) {
	dir := t.TempDir()

	// Create files with various dates
	os.WriteFile(filepath.Join(dir, "anomalies-2026-01-01.jsonl"), []byte("old\n"), 0644)
	os.WriteFile(filepath.Join(dir, "anomalies-2026-02-15.jsonl"), []byte("old\n"), 0644)
	os.WriteFile(filepath.Join(dir, "anomalies-2026-04-01.jsonl"), []byte("recent\n"), 0644)
	os.WriteFile(filepath.Join(dir, "anomalies-2026-04-02.jsonl"), []byte("today\n"), 0644)
	os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep\n"), 0644) // not an anomaly file

	removed, err := Rotate(dir, RotationConfig{MaxAgeDays: 30, MaxSizeMB: 100})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (Jan + Feb)", removed)
	}

	// Recent files should remain
	if _, err := os.Stat(filepath.Join(dir, "anomalies-2026-04-01.jsonl")); err != nil {
		t.Error("recent file should remain")
	}
	if _, err := os.Stat(filepath.Join(dir, "anomalies-2026-04-02.jsonl")); err != nil {
		t.Error("today's file should remain")
	}
	// Unrelated file should remain
	if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); err != nil {
		t.Error("unrelated file should remain")
	}
}

func TestRotate_RemovesBySize(t *testing.T) {
	dir := t.TempDir()

	// Create files that together exceed 1MB limit
	bigData := make([]byte, 600*1024) // 600KB each
	os.WriteFile(filepath.Join(dir, "anomalies-2026-04-01.jsonl"), bigData, 0644)
	os.WriteFile(filepath.Join(dir, "anomalies-2026-04-02.jsonl"), bigData, 0644)

	// 1.2MB total, 1MB limit — should remove oldest
	removed, err := Rotate(dir, RotationConfig{MaxAgeDays: 365, MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	// Newest should remain
	if _, err := os.Stat(filepath.Join(dir, "anomalies-2026-04-02.jsonl")); err != nil {
		t.Error("newest file should remain")
	}
}

func TestRotate_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	removed, err := Rotate(dir, DefaultRotation())
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}

func TestRotate_NonexistentDir(t *testing.T) {
	removed, err := Rotate("/nonexistent/path", DefaultRotation())
	if err != nil {
		t.Fatalf("Rotate should not error on nonexistent dir: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
}
