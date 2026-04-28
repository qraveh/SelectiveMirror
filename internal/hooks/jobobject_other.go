//go:build !windows

package hooks

// Stub for POSIX. The Windows Job Object pattern doesn't have a direct
// POSIX equivalent; on Linux/macOS, hook orphan-grandchildren can be
// killed by setting the child to its own process group (Setpgid) and
// signaling that group on timeout. Out of scope for the Windows-first
// runtime; this stub keeps the package portable for tests on dev boxes.

func newJobObject() (uintptr, error)  { return 0, nil }
func assignPIDToJob(job uintptr, pid int) error { _ = job; _ = pid; return nil }
func closeJobObject(job uintptr)      { _ = job }
