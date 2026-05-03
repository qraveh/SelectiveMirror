package anomaly

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// dateAgoName returns the anomaly filename for a date `daysAgo` days
// before now, using the YYYY-MM-DD layout that internal/anomaly/rotation.go
// parses out of the filename.
//
// Tests in this package previously hard-coded absolute dates
// (anomalies-2026-04-01.jsonl, anomalies-2026-04-02.jsonl, etc.) which
// is the time-bomb anti-pattern: TestRotate_RemovesOldFiles passed in
// April 2026, started failing in May 2026 once "today" drifted past the
// 30-day window. Using relative dates anchors the assertion to "now",
// not to a specific calendar moment.
func dateAgoName(daysAgo int) string {
	d := time.Now().AddDate(0, 0, -daysAgo)
	return fmt.Sprintf("anomalies-%s.jsonl", d.Format("2006-01-02"))
}

func TestRotate_RemovesOldFiles(t *testing.T) {
	dir := t.TempDir()

	// Create files with various ages relative to "now". The 30-day cutoff
	// is computed by Rotate from time.Now(), so:
	//   - 120 days ago: well past cutoff → must be removed
	//   - 75 days ago:  past cutoff      → must be removed
	//   - 5 days ago:   inside window    → must remain
	//   - today (0):    inside window    → must remain
	old1 := filepath.Join(dir, dateAgoName(120))
	old2 := filepath.Join(dir, dateAgoName(75))
	recent := filepath.Join(dir, dateAgoName(5))
	today := filepath.Join(dir, dateAgoName(0))
	unrelated := filepath.Join(dir, "unrelated.txt")
	for _, p := range []string{old1, old2, recent, today, unrelated} {
		if err := os.WriteFile(p, []byte("x\n"), 0644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	removed, err := Rotate(dir, RotationConfig{MaxAgeDays: 30, MaxSizeMB: 100})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2 (the two files >30 days old)", removed)
	}

	// Recent files should remain
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent file (5 days ago) should remain: %v", err)
	}
	if _, err := os.Stat(today); err != nil {
		t.Errorf("today's file should remain: %v", err)
	}
	// Unrelated file should remain (rotation filters by anomalies-*.jsonl prefix/suffix)
	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("unrelated file should remain: %v", err)
	}
	// Old files should be gone
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Errorf("old file (120 days ago) should be removed; stat err = %v", err)
	}
	if _, err := os.Stat(old2); !os.IsNotExist(err) {
		t.Errorf("old file (75 days ago) should be removed; stat err = %v", err)
	}
}

func TestRotate_RemovesBySize(t *testing.T) {
	dir := t.TempDir()

	// Create files that together exceed 1MB limit. Use relative dates
	// (5 + 4 days ago) so the test stays inside the MaxAgeDays=365 window
	// without becoming a time bomb in 2027.
	older := filepath.Join(dir, dateAgoName(5))
	newer := filepath.Join(dir, dateAgoName(4))
	bigData := make([]byte, 600*1024) // 600KB each
	if err := os.WriteFile(older, bigData, 0644); err != nil {
		t.Fatalf("write older: %v", err)
	}
	if err := os.WriteFile(newer, bigData, 0644); err != nil {
		t.Fatalf("write newer: %v", err)
	}

	// 1.2MB total, 1MB limit — should remove oldest
	removed, err := Rotate(dir, RotationConfig{MaxAgeDays: 365, MaxSizeMB: 1})
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (the older of the two)", removed)
	}

	// Newest should remain
	if _, err := os.Stat(newer); err != nil {
		t.Errorf("newest file (4 days ago) should remain: %v", err)
	}
	// Oldest should be gone
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Errorf("older file (5 days ago) should be removed; stat err = %v", err)
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
