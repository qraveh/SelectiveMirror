// Package logging provides structured logging with file rotation.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// rotatingWriter implements a simple size-based rotating file writer.
type rotatingWriter struct {
	mu          sync.Mutex
	path        string
	maxBytes    int64
	maxBackups  int
	currentSize int64
	file        *os.File
}

func newRotatingWriter(path string, maxBytes int64, maxBackups int) (*rotatingWriter, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	w := &rotatingWriter{
		path:       path,
		maxBytes:   maxBytes,
		maxBackups: maxBackups,
	}

	if err := w.openFile(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) openFile() error {
	f, err := openShared(w.path)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.currentSize = info.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentSize+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = w.file.Write(p)
	w.currentSize += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() error {
	w.file.Close()

	// Shift backups: .4 -> .5, .3 -> .4, etc.
	for i := w.maxBackups - 1; i >= 1; i-- {
		src := w.path + "." + fmt.Sprintf("%d", i)
		dst := w.path + "." + fmt.Sprintf("%d", i+1)
		os.Rename(src, dst) // ignore: source may not exist yet
	}
	os.Rename(w.path, w.path+".1") // ignore: best-effort

	// Open fresh file — must check error to avoid nil w.file
	if err := w.openFile(); err != nil {
		return fmt.Errorf("rotate: failed to open new log file: %w", err)
	}
	return nil
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}

// Setup initializes the slog default logger.
// level: "debug", "info", "warn", "error"
// logFile: path to log file (empty = stderr only)
// console: whether to also log to stderr
func Setup(level string, logFile string, console bool) (*rotatingWriter, error) {
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	var writers []io.Writer

	if console {
		writers = append(writers, os.Stderr)
	}

	var rw *rotatingWriter
	if logFile != "" {
		var err error
		rw, err = newRotatingWriter(logFile, 10*1024*1024, 5) // 10MB, 5 backups
		if err != nil {
			return nil, err
		}
		writers = append(writers, rw)
	}

	if len(writers) == 0 {
		writers = append(writers, os.Stderr)
	}

	w := io.MultiWriter(writers...)

	opts := &slog.HandlerOptions{
		Level: slogLevel,
	}

	var handler slog.Handler
	if console {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}

	slog.SetDefault(slog.New(handler))
	return rw, nil
}
