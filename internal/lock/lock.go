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

// IsLocked checks if the lock file is held by another instance.
// It attempts to acquire the lock — if that fails, another instance is running.
func IsLocked(dataDir string) (bool, int) {
	lockPath := filepath.Join(dataDir, "smirror.lock")

	// Try to open and lock the file. If we can lock it, no one else holds it.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return false, 0
	}

	if err := lockFile(f); err != nil {
		// Lock failed — another instance holds it.
		f.Close()
		return true, 0
	}

	// We acquired the lock — no other instance is running.
	// Release it immediately so we don't interfere.
	unlockFile(f)
	f.Close()
	return false, 0
}
