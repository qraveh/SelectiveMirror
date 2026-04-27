//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// IsAdminOwnedPath must reject files owned by a regular user. Regression
// for the SEC-C5 gate — service mode loads hook-bearing configs only from
// admin-owned, admin-only-writable locations.
func TestIsAdminOwnedPath_RegularUserFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "user-owned.txt")
	if err := os.WriteFile(p, []byte("# user file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ok, err := IsAdminOwnedPath(p)
	if err != nil {
		t.Fatalf("IsAdminOwnedPath: %v", err)
	}
	if ok {
		t.Errorf("user-owned file classified as admin-owned (would let a non-admin escalate via hooks)")
	}
}

// IsAdminOwnedPath must surface an error for a path that doesn't exist —
// callers must not silently treat a missing config as admin-owned (would be
// a privilege-escalation footgun if combined with a later "create the
// missing config" path).
func TestIsAdminOwnedPath_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist.yaml")

	if _, err := IsAdminOwnedPath(missing); err == nil {
		t.Error("expected error for missing path; got nil")
	}
}
