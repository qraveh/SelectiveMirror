package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// --- SM-038: rotate() ignores openFile() error → nil pointer panic ---

func TestRotate_OpenFileFailure_NoPanic(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	w, err := newRotatingWriter(logPath, 100, 3)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	// Write some data so rotation triggers
	w.Write([]byte("initial data that fills the buffer"))

	// Make the log directory read-only so openFile fails after rotation
	os.Chmod(dir, 0444)
	defer os.Chmod(dir, 0755) // restore for cleanup

	// This write should trigger rotate(). With the bug, openFile() failure
	// leaves w.file nil, and the subsequent Write panics on nil dereference.
	// After fix, rotate returns an error and Write returns it gracefully.
	_, err = w.Write([]byte("this triggers rotation and openFile will fail"))
	if err == nil {
		// On some platforms (Windows), chmod doesn't prevent writes.
		// If we got here without panic, the nil-deref bug is at least not triggered.
		t.Log("Write succeeded (platform may not enforce read-only dirs); no panic = pass")
	} else {
		t.Logf("Write correctly returned error: %v", err)
	}
}

func TestRotate_RenameFailure_Continues(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	w, err := newRotatingWriter(logPath, 50, 3)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	// Fill up enough to trigger rotation
	w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))

	// Write again to trigger rotation — backup files don't exist, rename may "fail"
	// but should not panic or leave writer in broken state
	n, err := w.Write([]byte("after rotation"))
	if err != nil {
		t.Fatalf("Write after rotation failed: %v", err)
	}
	if n != len("after rotation") {
		t.Errorf("wrote %d bytes, expected %d", n, len("after rotation"))
	}
}

// --- SM-043: backup naming uses rune('0'+i) → wrong chars for i >= 10 ---

func TestBackupNaming_DoubleDigit(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Use maxBackups=12 to exercise double-digit naming
	w, err := newRotatingWriter(logPath, 20, 12)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	// Trigger 12 rotations
	for i := 0; i < 13; i++ {
		w.Write([]byte("abcdefghijklmnopqrstuvwxyz")) // 26 bytes > 20 maxBytes
	}

	// Verify backup files use proper numeric naming (e.g., test.log.10, not test.log.:)
	for i := 1; i <= 12; i++ {
		expected := logPath + "." + fmt.Sprintf("%d", i)
		if _, err := os.Stat(expected); err != nil {
			t.Errorf("expected backup file %s to exist", filepath.Base(expected))
		}
	}

	// Negative check: no filenames with non-numeric suffix chars
	// With the old rune('0'+10) bug, file .10 would be named ".:" (colon = ASCII 58)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		for _, c := range name {
			if c == ':' || c == ';' || c == '<' || c == '=' {
				t.Errorf("backup file has invalid char in name: %s (rune %d)", name, c)
			}
		}
	}
}

// --- General logging tests (coverage for previously untested package) ---

func TestNewRotatingWriter_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "subdir", "deep", "test.log")

	w, err := newRotatingWriter(logPath, 1024, 3)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("expected directory to be created: %v", err)
	}
}

func TestRotatingWriter_BasicWrite(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	w, err := newRotatingWriter(logPath, 1024, 3)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}

	n, err := w.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 12 {
		t.Errorf("wrote %d bytes, expected 12", n)
	}

	w.Close()

	data, _ := os.ReadFile(logPath)
	if string(data) != "hello world\n" {
		t.Errorf("file content = %q, want %q", data, "hello world\n")
	}
}

func TestRotatingWriter_RotatesAtSize(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	w, err := newRotatingWriter(logPath, 30, 3)
	if err != nil {
		t.Fatalf("newRotatingWriter: %v", err)
	}

	// Write 35 bytes — triggers rotation on second write
	w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaa")) // 23 bytes
	w.Write([]byte("bbbbbbbbbbbbb"))              // 13 bytes, total 36 > 30

	w.Close()

	// Original file should exist (fresh after rotation)
	if _, err := os.Stat(logPath); err != nil {
		t.Error("expected log file to exist after rotation")
	}
	// Backup .1 should exist
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Error("expected backup .1 to exist after rotation")
	}
}

func TestSetup_StderrOnly(t *testing.T) {
	rw, err := Setup("info", "", true)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if rw != nil {
		t.Error("expected nil rotatingWriter when no log file specified")
	}
}

func TestSetup_WithFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")

	rw, err := Setup("debug", logPath, false)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if rw == nil {
		t.Fatal("expected non-nil rotatingWriter")
	}
	defer rw.Close()

	if _, err := os.Stat(logPath); err != nil {
		t.Error("expected log file to be created")
	}
}

func TestSetup_LogLevels(t *testing.T) {
	levels := []string{"debug", "info", "warn", "warning", "error", "unknown"}
	for _, level := range levels {
		rw, err := Setup(level, "", true)
		if err != nil {
			t.Errorf("Setup(%q) failed: %v", level, err)
		}
		if rw != nil {
			rw.Close()
		}
	}
}

func TestClose_NilFile(t *testing.T) {
	w := &rotatingWriter{}
	if err := w.Close(); err != nil {
		t.Errorf("Close on nil file: %v", err)
	}
}
