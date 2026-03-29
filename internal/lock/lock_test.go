package lock

import (
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()

	lk, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// The lock file should exist
	if lk.file == nil {
		t.Error("expected non-nil lock file")
	}

	// Release
	if err := lk.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// After release, should be able to re-acquire
	lk2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("re-Acquire after release failed: %v", err)
	}
	lk2.Release()
}

func TestDoubleAcquireFails(t *testing.T) {
	dir := t.TempDir()

	lk1, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}
	defer lk1.Release()

	// Second acquire should fail
	_, err = Acquire(dir)
	if err != ErrAlreadyRunning {
		t.Errorf("expected ErrAlreadyRunning, got: %v", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	lk1, err := Acquire(dir)
	if err != nil {
		t.Fatalf("first Acquire failed: %v", err)
	}
	lk1.Release()

	// Should succeed after release
	lk2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("second Acquire after release failed: %v", err)
	}
	lk2.Release()
}

func TestIsLockedWhenNotLocked(t *testing.T) {
	dir := t.TempDir()

	locked, _ := IsLocked(dir)
	if locked {
		t.Error("expected IsLocked to return false for unlocked dir")
	}
}
