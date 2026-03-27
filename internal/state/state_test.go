package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenAndClose(t *testing.T) {
	st := tempStore(t)

	// Verify schema version was set
	v, err := st.GetMeta("schema_version")
	if err != nil {
		t.Fatalf("GetMeta failed: %v", err)
	}
	if v != "2" {
		t.Errorf("expected schema version 2, got %s", v)
	}
}

func TestUpdateAndGetFileState(t *testing.T) {
	st := tempStore(t)

	err := st.UpdateFileState("TestProj", "src/main.go", "abc123", 1024, time.Now().UnixNano(), 0)
	if err != nil {
		t.Fatalf("UpdateFileState failed: %v", err)
	}

	fs, err := st.GetFileState("TestProj", "src/main.go")
	if err != nil {
		t.Fatalf("GetFileState failed: %v", err)
	}
	if fs == nil {
		t.Fatal("expected non-nil FileState")
	}
	if fs.LocalHash != "abc123" {
		t.Errorf("expected hash abc123, got %s", fs.LocalHash)
	}
	if fs.FileSize != 1024 {
		t.Errorf("expected size 1024, got %d", fs.FileSize)
	}
	if fs.RcloneExit != 0 {
		t.Errorf("expected exit 0, got %d", fs.RcloneExit)
	}
}

func TestUpsertFileState(t *testing.T) {
	st := tempStore(t)

	// Insert
	st.UpdateFileState("P", "f.txt", "hash1", 100, time.Now().UnixNano(), 0)
	// Update
	st.UpdateFileState("P", "f.txt", "hash2", 200, time.Now().UnixNano(), 0)

	fs, _ := st.GetFileState("P", "f.txt")
	if fs.LocalHash != "hash2" {
		t.Errorf("expected updated hash hash2, got %s", fs.LocalHash)
	}
	if fs.FileSize != 200 {
		t.Errorf("expected updated size 200, got %d", fs.FileSize)
	}
}

func TestGetFileStateNotFound(t *testing.T) {
	st := tempStore(t)

	fs, err := st.GetFileState("X", "nonexistent.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fs != nil {
		t.Error("expected nil for nonexistent file")
	}
}

func TestLogAction(t *testing.T) {
	st := tempStore(t)

	err := st.LogAction("P", "f.txt", "copy", "1024 bytes", 150)
	if err != nil {
		t.Fatalf("LogAction failed: %v", err)
	}

	// Verify by querying directly
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM sync_log WHERE project = 'P'").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 log entry, got %d", count)
	}
}

func TestGetAllSyncedPaths(t *testing.T) {
	st := tempStore(t)

	st.UpdateFileState("P", "a.txt", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "b.txt", "h2", 20, time.Now().UnixNano(), 0)
	st.UpdateFileState("Q", "c.txt", "h3", 30, time.Now().UnixNano(), 0)

	paths, err := st.GetAllSyncedPaths("P")
	if err != nil {
		t.Fatalf("GetAllSyncedPaths failed: %v", err)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 paths for P, got %d", len(paths))
	}
}

func TestGetPendingFiles(t *testing.T) {
	st := tempStore(t)

	st.UpdateFileState("P", "ok.txt", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "fail.txt", "h2", 20, time.Now().UnixNano(), 2)
	st.UpdateFileState("P", "fail2.txt", "h3", 30, time.Now().UnixNano(), 7)

	pending, err := st.GetPendingFiles("P")
	if err != nil {
		t.Fatalf("GetPendingFiles failed: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending, got %d", len(pending))
	}
}

func TestGetLastSyncTime(t *testing.T) {
	st := tempStore(t)

	// No syncs yet
	ts, _ := st.GetLastSyncTime("P")
	if !ts.IsZero() {
		t.Error("expected zero time for no syncs")
	}

	st.UpdateFileState("P", "a.txt", "h1", 10, time.Now().UnixNano(), 0)
	time.Sleep(10 * time.Millisecond)
	st.UpdateFileState("P", "b.txt", "h2", 20, time.Now().UnixNano(), 0)

	ts, err := st.GetLastSyncTime("P")
	if err != nil {
		t.Fatalf("GetLastSyncTime failed: %v", err)
	}
	if ts.IsZero() {
		t.Error("expected non-zero time after syncs")
	}
}

func TestMetaSetGet(t *testing.T) {
	st := tempStore(t)

	st.SetMeta("key1", "value1")
	v, err := st.GetMeta("key1")
	if err != nil {
		t.Fatalf("GetMeta failed: %v", err)
	}
	if v != "value1" {
		t.Errorf("expected value1, got %s", v)
	}

	// Update
	st.SetMeta("key1", "value2")
	v, _ = st.GetMeta("key1")
	if v != "value2" {
		t.Errorf("expected value2, got %s", v)
	}

	// Non-existent
	v, _ = st.GetMeta("nonexistent")
	if v != "" {
		t.Errorf("expected empty for nonexistent key, got %s", v)
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("hello world\n"), 0644)

	hash, size, err := HashFile(path)
	if err != nil {
		t.Fatalf("HashFile failed: %v", err)
	}
	if size != 12 {
		t.Errorf("expected size 12, got %d", size)
	}
	// MD5 of "hello world\n" = 6f5902ac237024bdd0c176cb93063dc4
	if hash != "6f5902ac237024bdd0c176cb93063dc4" {
		t.Errorf("unexpected hash: %s", hash)
	}
}

func TestHashFileNotFound(t *testing.T) {
	_, _, err := HashFile("/nonexistent/file.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}
