package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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
	expected := fmt.Sprintf("%d", len(migrations))
	if v != expected {
		t.Errorf("expected schema version %s, got %s", expected, v)
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

// --- SM-039: Migration error handling ---

func TestOpenIdempotentMigration(t *testing.T) {
	// Opening the same DB twice should work — the ALTER TABLE migration
	// should detect that mtime_ns already exists and skip it.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	st1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	st1.Close()

	st2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open should succeed (idempotent migration): %v", err)
	}
	st2.Close()
}

func TestOpenMigrationOnExistingDB(t *testing.T) {
	// Simulate an old DB without mtime_ns, then re-open.
	// The migration should add the column without error.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Insert a row and verify mtime_ns defaults to 0
	st.UpdateFileState("proj", "file.txt", "abc", 100, 0, 0)
	fs, err := st.GetFileState("proj", "file.txt")
	if err != nil {
		t.Fatalf("GetFileState: %v", err)
	}
	if fs.MtimeNs != 0 {
		t.Errorf("expected MtimeNs=0, got %d", fs.MtimeNs)
	}
	st.Close()
}

// =============================================================================
// Bug-hunting tests: SQL LIKE wildcards, concurrency, boundary conditions
// =============================================================================

// BUG HUNT: GetFilesUnderDir uses SQL LIKE with unescaped user input.
// Directory names containing SQL wildcard chars (_ or %) produce wrong results.
func TestGetFilesUnderDir_UnderscoreInDirName(t *testing.T) {
	st := tempStore(t)

	// Create files under "test_dir/" — note the underscore
	st.UpdateFileState("P", "test_dir/a.txt", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "test_dir/b.txt", "h2", 20, time.Now().UnixNano(), 0)

	// Create a file under "testXdir/" — should NOT match "test_dir/"
	st.UpdateFileState("P", "testXdir/c.txt", "h3", 30, time.Now().UnixNano(), 0)

	files, err := st.GetFilesUnderDir("P", "test_dir")
	if err != nil {
		t.Fatalf("GetFilesUnderDir: %v", err)
	}

	// Should find exactly 2 files (test_dir/a.txt and test_dir/b.txt)
	// BUG: SQL LIKE treats _ as single-char wildcard, so "test_dir/%" also
	// matches "testXdir/%" — we'd get 3 files instead of 2.
	if len(files) != 2 {
		t.Errorf("expected 2 files under test_dir/, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if f == "testXdir/c.txt" {
			t.Errorf("LIKE wildcard leak: testXdir/c.txt matched test_dir/ query")
		}
	}
}

func TestGetFilesUnderDir_PercentInDirName(t *testing.T) {
	st := tempStore(t)

	// Create files under "100%done/"
	st.UpdateFileState("P", "100%done/file.txt", "h1", 10, time.Now().UnixNano(), 0)

	// Create unrelated files
	st.UpdateFileState("P", "100/other.txt", "h2", 20, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "100xyz/other.txt", "h3", 30, time.Now().UnixNano(), 0)

	files, err := st.GetFilesUnderDir("P", "100%done")
	if err != nil {
		t.Fatalf("GetFilesUnderDir: %v", err)
	}

	// Should find exactly 1 file
	// BUG: SQL LIKE treats % as multi-char wildcard
	if len(files) != 1 {
		t.Errorf("expected 1 file under 100%%done/, got %d: %v", len(files), files)
	}
}

func TestGetFilesUnderDir_ExactMatch(t *testing.T) {
	st := tempStore(t)

	// A file that IS the dirPrefix (not under it)
	st.UpdateFileState("P", "mydir", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "mydir/child.txt", "h2", 20, time.Now().UnixNano(), 0)

	files, err := st.GetFilesUnderDir("P", "mydir")
	if err != nil {
		t.Fatalf("GetFilesUnderDir: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("expected 2 results (exact + child), got %d: %v", len(files), files)
	}
}

func TestGetFilesUnderDir_SimilarPrefix(t *testing.T) {
	st := tempStore(t)

	st.UpdateFileState("P", "src/main.go", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "src2/main.go", "h2", 20, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "srclib/main.go", "h3", 30, time.Now().UnixNano(), 0)

	files, err := st.GetFilesUnderDir("P", "src")
	if err != nil {
		t.Fatalf("GetFilesUnderDir: %v", err)
	}

	// Should match "src/main.go" and the exact "src" if it existed.
	// Should NOT match "src2/main.go" or "srclib/main.go"
	for _, f := range files {
		if f == "src2/main.go" || f == "srclib/main.go" {
			t.Errorf("prefix leak: %s matched 'src' query", f)
		}
	}
}

func TestGetFilesUnderDir_EmptyResult(t *testing.T) {
	st := tempStore(t)
	files, err := st.GetFilesUnderDir("P", "nonexistent")
	if err != nil {
		t.Fatalf("GetFilesUnderDir: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

// Concurrency: multiple goroutines writing to the same DB simultaneously.
func TestConcurrentWrites_NoCorruption(t *testing.T) {
	st := tempStore(t)

	var wg gosync.WaitGroup
	goroutines := 8
	filesPerGoroutine := 50

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < filesPerGoroutine; i++ {
				path := fmt.Sprintf("g%d/file%d.txt", id, i)
				hash := fmt.Sprintf("hash_%d_%d", id, i)
				err := st.UpdateFileState("P", path, hash, int64(i*100), time.Now().UnixNano(), 0)
				if err != nil {
					t.Errorf("concurrent write failed: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	// Verify all writes persisted
	paths, err := st.GetAllSyncedPaths("P")
	if err != nil {
		t.Fatalf("GetAllSyncedPaths: %v", err)
	}
	expected := goroutines * filesPerGoroutine
	if len(paths) != expected {
		t.Errorf("expected %d files, got %d (data lost under concurrency)", expected, len(paths))
	}
}

// Concurrent reads and writes: readers shouldn't see corrupt state.
func TestConcurrentReadWrite_NoCorruption(t *testing.T) {
	st := tempStore(t)

	// Seed some data
	for i := 0; i < 10; i++ {
		st.UpdateFileState("P", fmt.Sprintf("file%d.txt", i), "initial", 100, time.Now().UnixNano(), 0)
	}

	ctx := make(chan struct{})
	var wg gosync.WaitGroup

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			path := fmt.Sprintf("file%d.txt", i%10)
			st.UpdateFileState("P", path, fmt.Sprintf("v%d", i), int64(i), time.Now().UnixNano(), 0)
		}
		close(ctx)
	}()

	// Reader goroutines
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx:
					return
				default:
					st.GetAllSyncedPaths("P")
					st.GetFileState("P", "file0.txt")
					st.CountFiles("P")
				}
			}
		}()
	}

	wg.Wait()
}

// CountFiles: verify accuracy
func TestCountFiles_Correct(t *testing.T) {
	st := tempStore(t)

	if st.CountFiles("P") != 0 {
		t.Error("expected 0 for empty project")
	}

	st.UpdateFileState("P", "a.txt", "h1", 10, time.Now().UnixNano(), 0)
	st.UpdateFileState("P", "b.txt", "h2", 20, time.Now().UnixNano(), 0)
	st.UpdateFileState("Q", "c.txt", "h3", 30, time.Now().UnixNano(), 0)

	if st.CountFiles("P") != 2 {
		t.Errorf("expected 2 for P, got %d", st.CountFiles("P"))
	}
	if st.CountFiles("Q") != 1 {
		t.Errorf("expected 1 for Q, got %d", st.CountFiles("Q"))
	}
	if st.CountFiles("X") != 0 {
		t.Errorf("expected 0 for nonexistent project, got %d", st.CountFiles("X"))
	}
}

// UpdateMtimeOnly: verify it updates ONLY mtime, not hash/size/exit
func TestUpdateMtimeOnly_PreservesOtherFields(t *testing.T) {
	st := tempStore(t)

	originalMtime := int64(1000000)
	st.UpdateFileState("P", "f.txt", "originalhash", 999, originalMtime, 0)

	newMtime := int64(2000000)
	st.UpdateMtimeOnly("P", "f.txt", newMtime)

	fs, err := st.GetFileState("P", "f.txt")
	if err != nil {
		t.Fatalf("GetFileState: %v", err)
	}

	if fs.MtimeNs != newMtime {
		t.Errorf("mtime not updated: got %d, want %d", fs.MtimeNs, newMtime)
	}
	if fs.LocalHash != "originalhash" {
		t.Errorf("hash was corrupted: got %s", fs.LocalHash)
	}
	if fs.FileSize != 999 {
		t.Errorf("size was corrupted: got %d", fs.FileSize)
	}
	if fs.RcloneExit != 0 {
		t.Errorf("exit code was corrupted: got %d", fs.RcloneExit)
	}
}

// DeleteFileState followed by GetFileState: should return nil, not stale data
func TestDeleteFileState_ThenGet(t *testing.T) {
	st := tempStore(t)

	st.UpdateFileState("P", "f.txt", "h", 10, time.Now().UnixNano(), 0)
	st.DeleteFileState("P", "f.txt")

	fs, err := st.GetFileState("P", "f.txt")
	if err != nil {
		t.Fatalf("GetFileState after delete: %v", err)
	}
	if fs != nil {
		t.Error("expected nil after DeleteFileState")
	}
}

// DeleteFileState for nonexistent file: should not error
func TestDeleteFileState_Nonexistent(t *testing.T) {
	st := tempStore(t)

	err := st.DeleteFileState("P", "ghost.txt")
	if err != nil {
		t.Errorf("DeleteFileState for nonexistent should not error: %v", err)
	}
}

// GetPendingFiles: verify negative exit codes also count as pending
func TestGetPendingFiles_NegativeExitCode(t *testing.T) {
	st := tempStore(t)

	st.UpdateFileState("P", "timeout.txt", "h1", 10, time.Now().UnixNano(), -2) // timeout
	st.UpdateFileState("P", "execfail.txt", "h2", 20, time.Now().UnixNano(), -1) // exec failure
	st.UpdateFileState("P", "ok.txt", "h3", 30, time.Now().UnixNano(), 0)

	pending, err := st.GetPendingFiles("P")
	if err != nil {
		t.Fatalf("GetPendingFiles: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending (negative exit codes), got %d: %v", len(pending), pending)
	}
}

func TestPruneOldLogs(t *testing.T) {
	st := tempStore(t)

	// Insert logs with different timestamps
	old := time.Now().UTC().AddDate(0, 0, -60).Format(time.RFC3339)  // 60 days ago
	recent := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339) // 10 days ago
	now := time.Now().UTC().Format(time.RFC3339)

	st.db.Exec("INSERT INTO sync_log (timestamp, project, rel_path, action) VALUES (?, 'proj', 'old.txt', 'copy')", old)
	st.db.Exec("INSERT INTO sync_log (timestamp, project, rel_path, action) VALUES (?, 'proj', 'recent.txt', 'copy')", recent)
	st.db.Exec("INSERT INTO sync_log (timestamp, project, rel_path, action) VALUES (?, 'proj', 'now.txt', 'copy')", now)

	// Prune entries older than 30 days
	deleted, err := st.PruneOldLogs(30)
	if err != nil {
		t.Fatalf("PruneOldLogs: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 row deleted (60-day-old entry), got %d", deleted)
	}

	// Verify remaining rows
	var count int
	st.db.QueryRow("SELECT COUNT(*) FROM sync_log").Scan(&count)
	if count != 2 {
		t.Errorf("expected 2 remaining log entries, got %d", count)
	}
}

func TestAutoMigration_FreshDB(t *testing.T) {
	st := tempStore(t)

	// Verify schema version matches migration count
	v, _ := st.GetMeta("schema_version")
	expected := fmt.Sprintf("%d", len(migrations))
	if v != expected {
		t.Errorf("fresh DB schema_version = %q, want %q", v, expected)
	}

	// Verify mtime_ns column exists (added by migration 0)
	_, err := st.db.Exec("SELECT mtime_ns FROM sync_state LIMIT 1")
	if err != nil {
		t.Errorf("mtime_ns column should exist after migration: %v", err)
	}
}

func TestAutoMigration_Incremental(t *testing.T) {
	// Create a DB at "version 0" (base schema without mtime_ns column)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatal(err)
	}
	// Create base schema WITHOUT mtime_ns
	_, err = db.Exec(`
		CREATE TABLE sync_state (
			project TEXT NOT NULL, rel_path TEXT NOT NULL,
			local_hash TEXT, file_size INTEGER,
			synced_at TEXT NOT NULL, rclone_exit INTEGER,
			PRIMARY KEY (project, rel_path)
		);
		CREATE TABLE sync_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL, project TEXT NOT NULL,
			rel_path TEXT NOT NULL, action TEXT NOT NULL,
			detail TEXT, duration_ms INTEGER
		);
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
	`)
	if err != nil {
		t.Fatal(err)
	}
	// Set version to 0 (pre-migration)
	db.Exec("INSERT INTO meta (key, value) VALUES ('schema_version', '0')")
	// Insert a row to verify migration doesn't break existing data
	db.Exec("INSERT INTO sync_state (project, rel_path, local_hash, file_size, synced_at, rclone_exit) VALUES ('proj', 'file.txt', 'abc', 100, '2026-01-01', 0)")
	db.Close()

	// Now open with our Store (should run migration 0 → add mtime_ns)
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on v0 DB should succeed: %v", err)
	}
	defer st.Close()

	// Verify migration ran: schema_version should be updated
	v, _ := st.GetMeta("schema_version")
	expected := fmt.Sprintf("%d", len(migrations))
	if v != expected {
		t.Errorf("schema_version after migration = %q, want %q", v, expected)
	}

	// Verify mtime_ns column exists
	_, err = st.db.Exec("SELECT mtime_ns FROM sync_state LIMIT 1")
	if err != nil {
		t.Errorf("mtime_ns column should exist after incremental migration: %v", err)
	}

	// Verify existing data survived
	fs, err := st.GetFileState("proj", "file.txt")
	if err != nil || fs == nil {
		t.Fatal("pre-existing row should survive migration")
	}
	if fs.LocalHash != "abc" {
		t.Errorf("hash = %q, want 'abc'", fs.LocalHash)
	}

	// Verify remote verification columns exist (migration 1)
	_, err = st.db.Exec("SELECT remote_verified_at, remote_hash, remote_size FROM sync_state LIMIT 1")
	if err != nil {
		t.Errorf("remote verification columns should exist after migration 1: %v", err)
	}

	// Verify existing row has empty remote verification (backfill hasn't run)
	if fs.IsRemoteVerified() {
		t.Error("pre-existing row should not be remote-verified after migration")
	}
}

// --- SM-083: Remote verification trust model ---

func TestUpdateRemoteVerification_SetsFields(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Create a file entry first
	st.UpdateFileState("proj", "file.txt", "localhash", 100, 1234567890, 0)

	// Update remote verification
	err = st.UpdateRemoteVerification("proj", "file.txt", "remotehash", 100)
	if err != nil {
		t.Fatalf("UpdateRemoteVerification: %v", err)
	}

	fs, _ := st.GetFileState("proj", "file.txt")
	if fs == nil {
		t.Fatal("file state should exist")
	}
	if !fs.IsRemoteVerified() {
		t.Error("expected remote-verified after UpdateRemoteVerification")
	}
	if fs.RemoteHash != "remotehash" {
		t.Errorf("remote_hash = %q, want 'remotehash'", fs.RemoteHash)
	}
	if fs.RemoteSize != 100 {
		t.Errorf("remote_size = %d, want 100", fs.RemoteSize)
	}
	if fs.RemoteVerifiedAt.IsZero() {
		t.Error("remote_verified_at should be set")
	}
}

func TestUpdateRemoteVerification_DoesNotTouchSyncedAt(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.UpdateFileState("proj", "file.txt", "hash1", 50, 0, 0)
	fs1, _ := st.GetFileState("proj", "file.txt")
	syncedAt1 := fs1.SyncedAt

	// Small delay to ensure timestamps differ
	time.Sleep(10 * time.Millisecond)

	st.UpdateRemoteVerification("proj", "file.txt", "remotehash", 50)
	fs2, _ := st.GetFileState("proj", "file.txt")

	if !fs2.SyncedAt.Equal(syncedAt1) {
		t.Errorf("synced_at changed from %v to %v — should be untouched", syncedAt1, fs2.SyncedAt)
	}
	if fs2.LocalHash != "hash1" {
		t.Errorf("local_hash changed to %q — should be untouched", fs2.LocalHash)
	}
}

func TestIsRemoteVerified_FalseWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.UpdateFileState("proj", "new.txt", "hash", 10, 0, 0)
	fs, _ := st.GetFileState("proj", "new.txt")
	if fs.IsRemoteVerified() {
		t.Error("new file should not be remote-verified")
	}
}

func TestGetFileState_IncludesRemoteFields(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	st.UpdateFileState("proj", "file.txt", "abc", 200, 999, 0)
	st.UpdateRemoteVerification("proj", "file.txt", "def", 200)

	fs, err := st.GetFileState("proj", "file.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Local fields
	if fs.LocalHash != "abc" {
		t.Errorf("local_hash = %q", fs.LocalHash)
	}
	if fs.FileSize != 200 {
		t.Errorf("file_size = %d", fs.FileSize)
	}
	if fs.MtimeNs != 999 {
		t.Errorf("mtime_ns = %d", fs.MtimeNs)
	}

	// Remote fields
	if fs.RemoteHash != "def" {
		t.Errorf("remote_hash = %q", fs.RemoteHash)
	}
	if fs.RemoteSize != 200 {
		t.Errorf("remote_size = %d", fs.RemoteSize)
	}
	if fs.RemoteVerifiedAt.IsZero() {
		t.Error("remote_verified_at should be set")
	}
}
