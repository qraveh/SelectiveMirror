package anomaly

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestFileWriter_WritesJSONLines(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	defer w.Close()

	a := &Anomaly{
		ID:       "A-000001",
		Time:     "2026-04-02T15:00:00Z",
		Kind:     KindPanic,
		Severity: SeverityCritical,
		Project:  "test",
		Message:  "test panic",
	}

	if err := w.Write(a); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Read back and parse
	f, err := os.Open(w.FilePath())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("no lines in file")
	}

	var parsed Anomaly
	if err := json.Unmarshal(scanner.Bytes(), &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if parsed.Kind != KindPanic {
		t.Errorf("kind = %q, want Panic", parsed.Kind)
	}
	if parsed.Project != "test" {
		t.Errorf("project = %q, want test", parsed.Project)
	}
}

func TestFileWriter_MultipleWrites(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	defer w.Close()

	for i := 0; i < 5; i++ {
		if err := w.Write(&Anomaly{Kind: KindWatcherError, Message: "err"}); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	f, _ := os.Open(w.FilePath())
	defer f.Close()
	count := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		count++
	}
	if count != 5 {
		t.Errorf("got %d lines, want 5", count)
	}
}

func TestFileWriter_NilAnomaly(t *testing.T) {
	dir := t.TempDir()
	w := NewFileWriter(dir)
	defer w.Close()

	if err := w.Write(nil); err != nil {
		t.Fatalf("Write(nil) should succeed, got: %v", err)
	}
}
