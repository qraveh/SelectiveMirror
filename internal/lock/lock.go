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
	"strings"
	"time"
)

// ErrAlreadyRunning is returned when another instance holds the lock.
var ErrAlreadyRunning = errors.New("another smirror instance is already running")

// ErrStaleLockHeld is returned when the lock file's recorded PID is not
// alive — typically a crashed previous instance whose handle the OS
// has not yet released. Callers can wrap this and surface a clearer
// remedy (delete the file, retry, etc.). GAP-9.
var ErrStaleLockHeld = errors.New("lock file held but recorded PID is dead")

// readLockPID parses the `pid=N` line from a lock file written by
// Acquire. Returns the PID and ok=true on success.
func readLockPID(lockPath string) (int, bool) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pid=") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(line, "pid="))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// Lock represents a single-instance file lock.
type Lock struct {
	path string
	file *os.File
}

// Acquire creates or opens the lock file with exclusive access.
// Returns ErrAlreadyRunning if another instance holds the lock.
//
// GAP-9: if Acquire fails because another process appears to hold the
// file lock, the recorded PID is checked against the OS process list.
// If the PID is no longer alive (the previous instance crashed without
// cleaning up), we surface ErrStaleLockHeld with the dead PID — the
// caller can decide whether to force-clean. The most common case
// (normal another-instance) still returns ErrAlreadyRunning.
func Acquire(dataDir string) (*Lock, error) {
	lockPath := filepath.Join(dataDir, "smirror.lock")

	// Ensure directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating lock dir: %w", err)
	}

	// Try to open the file with exclusive access.
	// On Windows, os.OpenFile with O_CREATE|O_WRONLY will fail if another
	// process has the file open. We use platform-specific locking below.
	//
	// SEC-L2: 0600 owner-only. The lock file content is "pid=N\ntime=..."
	// — the PID isn't a secret, but the SECURITY.md baseline is 0600
	// for files under the user data dir; consistency matters.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	// Try to acquire an exclusive lock (platform-specific)
	if err := lockFile(f); err != nil {
		f.Close()
		// GAP-9: classify before returning. Read PID from the locked
		// file (we can't read while it's locked by us, but we can read
		// the file we failed to lock — opening a separate handle for
		// reading is allowed; lockFile is advisory).
		if pid, ok := readLockPID(lockPath); ok {
			if !isProcessAlive(pid) {
				return nil, fmt.Errorf("%w (recorded PID %d is no longer alive — likely a crashed previous instance; manual remedy: delete %s and retry)", ErrStaleLockHeld, pid, lockPath)
			}
		}
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
	// SEC-L2: 0600 to match Acquire().
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
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
