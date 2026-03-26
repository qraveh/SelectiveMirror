// Package lock provides single-instance protection using a file-based lock.
//
// On Windows, the lock file is held open with exclusive access, preventing
// a second instance from acquiring it. On other platforms, the same mechanism
// works via OS-level file locking semantics.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// ErrAlreadyRunning is returned when another instance holds the lock.
var ErrAlreadyRunning = errors.New("another smirror instance is already running")

// Lock represents a single-instance file lock.
type Lock struct {
	path string
	file *os.File
}

// Acquire creates or opens the lock file with exclusive access.
// Returns ErrAlreadyRunning if another instance holds the lock.
func Acquire(dataDir string) (*Lock, error) {
	lockPath := filepath.Join(dataDir, "smirror.lock")

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}

	// Try to open the file with exclusive access.
	// On Windows, os.OpenFile with O_CREATE|O_WRONLY will fail if another
	// process has the file open. We use platform-specific locking below.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	// Try to acquire an exclusive lock (platform-specific)
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, ErrAlreadyRunning
	}

	// Write PID for diagnostics
	f.Truncate(0)
	f.Seek(0, 0)
	fmt.Fprintf(f, "pid=%d\ntime=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	f.Sync()

	return &Lock{path: lockPath, file: f}, nil
}

// Release releases the lock and removes the lock file.
func (l *Lock) Release() error {
	if l.file == nil {
		return nil
	}
	unlockFile(l.file)
	l.file.Close()
	l.file = nil
	os.Remove(l.path)
	return nil
}

// IsLocked checks if the lock file exists and contains a valid PID.
// This is a best-effort check for diagnostic purposes (e.g., doctor command).
func IsLocked(dataDir string) (bool, int) {
	lockPath := filepath.Join(dataDir, "smirror.lock")
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return false, 0
	}

	// Try to parse PID
	for _, line := range splitLines(string(data)) {
		if len(line) > 4 && line[:4] == "pid=" {
			if pid, err := strconv.Atoi(line[4:]); err == nil {
				// Check if process is still running
				proc, err := os.FindProcess(pid)
				if err != nil {
					return false, pid
				}
				// On Windows, FindProcess always succeeds. Try to signal 0.
				if err := signalProcess(proc); err != nil {
					return false, pid
				}
				return true, pid
			}
		}
	}
	return false, 0
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
