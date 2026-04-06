package anomaly

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileWriter appends JSON-lines to date-stamped files: anomalies-YYYY-MM-DD.jsonl
type FileWriter struct {
	dataDir string
	mu      sync.Mutex
	current *os.File
	curDate string
}

// NewFileWriter creates a file writer that stores anomalies in the given directory.
func NewFileWriter(dataDir string) *FileWriter {
	return &FileWriter{dataDir: dataDir}
}

// Write appends an anomaly as a JSON line. Thread-safe.
// Sanitizes the anomaly before writing.
func (w *FileWriter) Write(a *Anomaly) error {
	if a == nil {
		return nil
	}

	// Sanitize before persisting
	SanitizeAnomaly(a)

	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if err := w.ensureFile(today); err != nil {
		return err
	}

	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("marshal anomaly: %w", err)
	}
	data = append(data, '\n')

	_, err = w.current.Write(data)
	return err
}

// Close closes the current file.
func (w *FileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.current != nil {
		err := w.current.Close()
		w.current = nil
		return err
	}
	return nil
}

// ensureFile opens the file for today's date, creating it if needed.
// Caller must hold w.mu.
func (w *FileWriter) ensureFile(date string) error {
	if w.curDate == date && w.current != nil {
		return nil
	}

	// Close previous day's file
	if w.current != nil {
		w.current.Close()
		w.current = nil
	}

	if err := os.MkdirAll(w.dataDir, 0755); err != nil {
		return fmt.Errorf("creating anomaly dir: %w", err)
	}

	filename := filepath.Join(w.dataDir, "anomalies-"+date+".jsonl")
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening anomaly file: %w", err)
	}

	w.current = f
	w.curDate = date
	return nil
}

// FilePath returns the path to today's anomaly file (for testing).
func (w *FileWriter) FilePath() string {
	today := time.Now().Format("2006-01-02")
	return filepath.Join(w.dataDir, "anomalies-"+today+".jsonl")
}
