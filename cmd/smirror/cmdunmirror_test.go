package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/state"
)

// TestProbePurgeTargetLocal covers the local-path branch of probePurgeTarget:
// present dir (count matches), empty dir (exists with 0), and missing path.
func TestProbePurgeTargetLocal(t *testing.T) {
	dir := t.TempDir()

	// Populated dir: 2 entries.
	full := filepath.Join(dir, "full")
	if err := os.MkdirAll(full, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(full, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Empty dir.
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0755); err != nil {
		t.Fatal(err)
	}

	// Missing path: never created.
	missing := filepath.Join(dir, "missing")

	cases := []struct {
		name       string
		path       string
		wantExists bool
		wantCount  int
	}{
		{"populated", full, true, 2},
		{"empty", empty, true, 0},
		{"missing", missing, false, 0},
	}
	for _, tc := range cases {
		got := probePurgeTarget(tc.path, nil, "")
		if got.exists != tc.wantExists {
			t.Errorf("%s: exists want %v, got %v", tc.name, tc.wantExists, got.exists)
		}
		if got.count != tc.wantCount {
			t.Errorf("%s: count want %d, got %d", tc.name, tc.wantCount, got.count)
		}
	}
}

// TestPurgeOneLocal covers purgeOne for local paths: deletes populated dir,
// deletes empty dir, no-ops on missing path.
func TestPurgeOneLocal(t *testing.T) {
	dir := t.TempDir()

	full := filepath.Join(dir, "full")
	os.MkdirAll(filepath.Join(full, "sub"), 0755)
	os.WriteFile(filepath.Join(full, "a.txt"), []byte("x"), 0644)

	empty := filepath.Join(dir, "empty")
	os.MkdirAll(empty, 0755)

	missing := filepath.Join(dir, "missing")

	for _, p := range []string{full, empty, missing} {
		if err := purgeOne(p, nil, ""); err != nil {
			t.Errorf("purgeOne(%q) unexpected error: %v", p, err)
		}
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("purgeOne(%q): path still exists after purge", p)
		}
	}
}

// TestCleanStateForProject writes some state rows, then verifies that
// cleanStateForProject deletes only the named project's rows and leaves
// other projects intact.
func TestCleanStateForProject(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")

	st, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Seed sync_state for two projects.
	for _, tc := range []struct {
		project, path, hash string
	}{
		{"A", "f1.txt", "h1"},
		{"A", "f2.txt", "h2"},
		{"B", "f3.txt", "h3"},
	} {
		if err := st.UpdateFileState(tc.project, tc.path, tc.hash, 0, 0, 0); err != nil {
			t.Fatalf("UpdateFileState(%s, %s): %v", tc.project, tc.path, err)
		}
	}
	st.Close()

	// Clean project A; should report 2 rows.
	rows, err := cleanStateForProject(dbPath, "A")
	if err != nil {
		t.Fatalf("cleanStateForProject: %v", err)
	}
	if rows < 2 {
		t.Errorf("want >=2 rows deleted, got %d", rows)
	}

	// Verify: A is empty, B still has 1 row.
	st2, err := state.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if n := st2.CountFiles("A"); n != 0 {
		t.Errorf("A: want 0 rows after clean, got %d", n)
	}
	if n := st2.CountFiles("B"); n != 1 {
		t.Errorf("B: want 1 row preserved, got %d", n)
	}
}

// TestCleanStateForProjectMissingDB: when the DB file doesn't exist, return
// (0, nil) — a removed mirror that never synced has no state to clean.
func TestCleanStateForProjectMissingDB(t *testing.T) {
	dir := t.TempDir()
	rows, err := cleanStateForProject(filepath.Join(dir, "nonexistent.db"), "A")
	if err != nil {
		t.Fatalf("want nil error for missing DB, got %v", err)
	}
	if rows != 0 {
		t.Errorf("want 0 rows, got %d", rows)
	}
}
