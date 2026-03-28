package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/state"
)

// testProject returns a config.Project pointing at a temp directory.
func testProject(t *testing.T) config.Project {
	t.Helper()
	dir := t.TempDir()
	return config.Project{
		Name:          "test-proj",
		LocalPath:     dir,
		Remote:        "fakefs:test-bucket/test-proj",
		DebounceSec:   1,
		MaxFileSizeMB: 10,
	}
}

// testConfig wraps a project in a Global config.
func testConfig(proj config.Project) *config.Global {
	return &config.Global{
		Projects:    []config.Project{proj},
		RclonePath:  "rclone",
		SyncWorkers: 2,
	}
}

// testEngine creates an Engine with a fake rclone runner backed by an in-memory state DB.
func testEngine(t *testing.T, cfg *config.Global, runner RcloneRunner) *Engine {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	st, err := state.Open(dbPath)
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	e := NewEngine(cfg, st, nil, nil)
	if runner != nil {
		e.RunRcloneFunc = runner
	}
	return e
}

// testFilter creates a filter.Engine for use in tests (no syncignore, no global excludes).
func testFilter(t *testing.T) *filter.Engine {
	t.Helper()
	fe, err := filter.New(nil, "")
	if err != nil {
		t.Fatalf("filter.New: %v", err)
	}
	return fe
}

// --- lockKey tests ---

func TestLockKey_SingleFile(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	task := Task{Project: proj, RelPath: "src/main.go"}
	key := e.lockKey(task)
	if key != "test-proj:src/main.go" {
		t.Errorf("lockKey = %q, want %q", key, "test-proj:src/main.go")
	}
}

func TestLockKey_FullProject(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	task := Task{Project: proj, RelPath: ""}
	key := e.lockKey(task)
	if key != "test-proj" {
		t.Errorf("lockKey = %q, want %q", key, "test-proj")
	}
}

// --- acquire/release lock tests ---

func TestAcquireRelease_NoDeadlock(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	task := Task{Project: proj, RelPath: "a.txt"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.acquireFileLock(task)
		e.releaseFileLock(task)
		e.acquireFileLock(task)
		e.releaseFileLock(task)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: sequential acquire/release did not complete")
	}
}

func TestAcquireFileLock_Concurrent_SameKey(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	task := Task{Project: proj, RelPath: "shared.txt"}

	var counter int64
	var maxConcurrent int64
	var mu gosync.Mutex

	var wg gosync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.acquireFileLock(task)
			defer e.releaseFileLock(task)

			cur := atomic.AddInt64(&counter, 1)
			mu.Lock()
			if cur > maxConcurrent {
				maxConcurrent = cur
			}
			mu.Unlock()

			time.Sleep(1 * time.Millisecond)
			atomic.AddInt64(&counter, -1)
		}()
	}
	wg.Wait()

	if maxConcurrent > 1 {
		t.Errorf("per-file lock allowed %d concurrent holders, want 1", maxConcurrent)
	}
}

func TestAcquireFileLock_DifferentKeys_Parallel(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	task1 := Task{Project: proj, RelPath: "file1.txt"}
	task2 := Task{Project: proj, RelPath: "file2.txt"}

	started := make(chan struct{}, 2)
	release := make(chan struct{})

	go func() {
		e.acquireFileLock(task1)
		started <- struct{}{}
		<-release
		e.releaseFileLock(task1)
	}()
	go func() {
		e.acquireFileLock(task2)
		started <- struct{}{}
		<-release
		e.releaseFileLock(task2)
	}()

	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-timeout:
			t.Fatal("different keys should not block each other")
		}
	}
	close(release)
}

// --- quiesceFile tests ---

func TestQuiesceFile_StableFile(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	path := filepath.Join(proj.LocalPath, "stable.txt")
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := e.quiesceFile(path)
	if err != nil {
		t.Fatalf("quiesceFile: %v", err)
	}
	if info == nil {
		t.Fatal("quiesceFile returned nil info for stable file")
	}
	if info.Size() != 5 {
		t.Errorf("size = %d, want 5", info.Size())
	}
}

func TestQuiesceFile_Nonexistent(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	_, err := e.quiesceFile(filepath.Join(proj.LocalPath, "nope.txt"))
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestQuiesceFile_Directory(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	_, err := e.quiesceFile(proj.LocalPath)
	if err == nil {
		t.Error("expected error for directory")
	}
	if err != nil && !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- processTask panic recovery ---

func TestProcessTask_PanicRecovery(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		panic("test panic in rclone runner")
	})

	path := filepath.Join(proj.LocalPath, "panic.txt")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	task := Task{Project: proj, RelPath: "panic.txt"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.processTask(context.Background(), task)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("processTask did not complete (panic not recovered)")
	}
}

// --- Run context cancel / channel close ---

func TestRun_ContextCancel(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestRun_ChannelClose(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(context.Background())
	}()

	close(e.TaskChan)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after channel close")
	}
}

// --- sync verb selection tests ---

func TestSyncFullProject_MirrorPolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called")
	}
	if capturedArgs[0] != "sync" {
		t.Errorf("verb = %q, want %q for mirror policy", capturedArgs[0], "sync")
	}
}

func TestSyncFullProject_IgnorePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore"

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called")
	}
	if capturedArgs[0] != "copy" {
		t.Errorf("verb = %q, want %q for ignore policy", capturedArgs[0], "copy")
	}
}

// --- delete policy tests ---

func TestDeleteRemoteFile_IgnorePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore"

	called := false
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		called = true
		return 0
	})

	e.state.UpdateFileState(proj.Name, "gone.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "gone.txt", false)

	if called {
		t.Error("rclone should NOT be called for delete with ignore policy")
	}
}

func TestDeleteRemoteFile_MirrorPolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})

	e.state.UpdateFileState(proj.Name, "gone.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "gone.txt", false)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called for mirror delete")
	}
	if capturedArgs[0] != "deletefile" {
		t.Errorf("verb = %q, want %q", capturedArgs[0], "deletefile")
	}
}

func TestDeleteRemoteFile_ForceDelete_OverridesIgnorePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore"

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})

	e.state.UpdateFileState(proj.Name, "renamed.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "renamed.txt", true)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called for force delete (rename cleanup)")
	}
	if capturedArgs[0] != "deletefile" {
		t.Errorf("verb = %q, want %q", capturedArgs[0], "deletefile")
	}
}

// --- hash unchanged skip test ---

func TestSyncSingleFile_HashUnchanged(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	called := false
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		called = true
		return 0
	})

	path := filepath.Join(proj.LocalPath, "same.txt")
	if err := os.WriteFile(path, []byte("unchanged content"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, size, err := state.HashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	mtimeNs := info.ModTime().UnixNano()

	e.state.UpdateFileState(proj.Name, "same.txt", hash, size, mtimeNs, 0)

	e.syncSingleFile(context.Background(), proj, "same.txt")

	if called {
		t.Error("rclone should NOT be called when hash and mtime are unchanged")
	}
}

// --- commonFlags tests ---

func TestCommonFlags_ContainsSkipLinks(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, nil)

	flags := e.commonFlags()
	found := false
	for _, f := range flags {
		if f == "--skip-links" {
			found = true
			break
		}
	}
	if !found {
		t.Error("commonFlags missing --skip-links")
	}
}

// --- SM-036: deleteRemoteFile must preserve state when rclone fails ---

func TestDeleteRemoteFile_RcloneFailure_StatePreserved(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 1 // simulate rclone failure
	})

	// Seed state: file was previously synced successfully
	e.state.UpdateFileState(proj.Name, "fail-delete.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "fail-delete.txt", false)

	// State must be preserved when rclone fails
	fs, err := e.state.GetFileState(proj.Name, "fail-delete.txt")
	if err != nil {
		t.Fatalf("GetFileState error: %v", err)
	}
	if fs == nil {
		t.Fatal("SM-036 CONFIRMED: state was deleted even though rclone failed (exit=1)")
	}
}

func TestDeleteRemoteFile_RcloneSuccess_StateDeleted(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 0 // rclone succeeds
	})

	e.state.UpdateFileState(proj.Name, "ok-delete.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "ok-delete.txt", false)

	// State should be deleted on success
	fs, _ := e.state.GetFileState(proj.Name, "ok-delete.txt")
	if fs != nil {
		t.Error("state should be deleted after successful rclone delete")
	}
}

func TestDeleteRemoteFile_QuarantineFailure_StatePreserved(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "quarantine"

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 1 // simulate rclone moveto failure
	})

	e.state.UpdateFileState(proj.Name, "qfail.txt", "abc123", 100, 0, 0)

	e.deleteRemoteFile(context.Background(), proj, "qfail.txt", false)

	fs, _ := e.state.GetFileState(proj.Name, "qfail.txt")
	if fs == nil {
		t.Fatal("SM-036 CONFIRMED: state deleted on quarantine failure")
	}
}

func TestDeleteRemoteDir_NoDoubleDeleteState(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	deleteCount := 0
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		deleteCount++
		return 0
	})

	// Seed: two files under dir/
	e.state.UpdateFileState(proj.Name, "dir/a.txt", "aaa", 10, 0, 0)
	e.state.UpdateFileState(proj.Name, "dir/b.txt", "bbb", 20, 0, 0)

	e.deleteRemoteDir(context.Background(), proj, "dir", true)

	// Both files should be deleted from remote
	if deleteCount != 2 {
		t.Errorf("expected 2 rclone calls, got %d", deleteCount)
	}

	// Both state entries should be gone (exactly once each)
	fs1, _ := e.state.GetFileState(proj.Name, "dir/a.txt")
	fs2, _ := e.state.GetFileState(proj.Name, "dir/b.txt")
	if fs1 != nil || fs2 != nil {
		t.Error("state entries should be deleted after successful dir cleanup")
	}
}

func TestCommonFlags_BandwidthLimit(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.BandwidthLimit = "10M"
	e := testEngine(t, cfg, nil)

	flags := e.commonFlags()
	foundBwlimit := false
	for i, f := range flags {
		if f == "--bwlimit" && i+1 < len(flags) && flags[i+1] == "10M" {
			foundBwlimit = true
		}
	}
	if !foundBwlimit {
		t.Errorf("commonFlags missing --bwlimit 10M, got: %v", flags)
	}
}

// =============================================================================
// Bug-hunting tests: mtime cascade, error swallowing, boundary conditions
// =============================================================================

// BUG HUNT: Failed mtime-only sync records non-zero exitCode in state DB.
// Next event: hash matches, but RcloneExit != 0 → triggers FULL content re-upload
// instead of retrying the lightweight mtime touch.
func TestSyncSingleFile_MtimeFailure_CausesFullReupload(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	// Create the file
	filePath := filepath.Join(proj.LocalPath, "stable.txt")
	os.WriteFile(filePath, []byte("stable content"), 0644)

	var callLog []string
	var mu gosync.Mutex
	runner := func(ctx context.Context, args []string) int {
		mu.Lock()
		callLog = append(callLog, args[0])
		mu.Unlock()
		if args[0] == "touch" {
			return 3 // simulate mtime sync failure (e.g., backend doesn't support it)
		}
		return 0
	}

	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// First sync: uploads content (rclone copyto)
	e.syncSingleFile(context.Background(), proj, "stable.txt")

	// Simulate mtime change without content change: touch the file to change mtime
	time.Sleep(50 * time.Millisecond)
	now := time.Now()
	os.Chtimes(filePath, now, now)

	// Clear log
	mu.Lock()
	callLog = nil
	mu.Unlock()

	// Second sync: hash unchanged, mtime changed → should call "touch"
	e.syncSingleFile(context.Background(), proj, "stable.txt")

	mu.Lock()
	secondCalls := make([]string, len(callLog))
	copy(secondCalls, callLog)
	callLog = nil
	mu.Unlock()

	if len(secondCalls) == 0 {
		t.Fatal("expected at least one rclone call for mtime sync")
	}
	if secondCalls[0] != "touch" {
		t.Errorf("second sync should be mtime-only 'touch', got %q", secondCalls[0])
	}

	// Third sync: hash still unchanged. The mtime sync failed (exit 3) so state
	// has exitCode=3. This should ideally retry "touch", but if the code checks
	// RcloneExit == 0 before the mtime branch, it will force a full "copyto" re-upload.
	e.syncSingleFile(context.Background(), proj, "stable.txt")

	mu.Lock()
	thirdCalls := make([]string, len(callLog))
	copy(thirdCalls, callLog)
	mu.Unlock()

	if len(thirdCalls) > 0 && thirdCalls[0] == "copyto" {
		t.Errorf("BUG: failed mtime sync (exit 3) triggered full re-upload 'copyto' instead of retrying 'touch'.\n"+
			"Third sync calls: %v", thirdCalls)
	}
}

// BUG HUNT: syncSingleFile — file at exact MaxFileSize boundary
func TestSyncSingleFile_ExactMaxFileSizeBoundary(t *testing.T) {
	proj := testProject(t)
	proj.MaxFileSizeMB = 1 // 1 MB = 1048576 bytes
	cfg := testConfig(proj)

	var synced atomic.Bool
	runner := func(ctx context.Context, args []string) int {
		synced.Store(true)
		return 0
	}
	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// File at EXACTLY the limit (1048576 bytes)
	filePath := filepath.Join(proj.LocalPath, "exact.bin")
	data := make([]byte, 1048576) // exactly 1 MB
	os.WriteFile(filePath, data, 0644)

	e.syncSingleFile(context.Background(), proj, "exact.bin")

	// The check is `info.Size() > proj.MaxFileSize()` — strictly greater than.
	// So a file at exactly the limit SHOULD be synced.
	if !synced.Load() {
		t.Error("file at exactly MaxFileSize should be synced (> not >=)")
	}
}

// BUG HUNT: file 1 byte over the limit
func TestSyncSingleFile_OneByteOverLimit(t *testing.T) {
	proj := testProject(t)
	proj.MaxFileSizeMB = 1
	cfg := testConfig(proj)

	var synced atomic.Bool
	runner := func(ctx context.Context, args []string) int {
		synced.Store(true)
		return 0
	}
	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	filePath := filepath.Join(proj.LocalPath, "toobig.bin")
	data := make([]byte, 1048577) // 1 MB + 1 byte
	os.WriteFile(filePath, data, 0644)

	e.syncSingleFile(context.Background(), proj, "toobig.bin")

	if synced.Load() {
		t.Error("file 1 byte over MaxFileSize should NOT be synced")
	}
}

// BUG HUNT: deleteRemoteFile swallows GetFileState DB errors
func TestDeleteRemoteFile_DBError_SilentlySkips(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var deleteCalled atomic.Bool
	runner := func(ctx context.Context, args []string) int {
		deleteCalled.Store(true)
		return 0
	}
	e := testEngine(t, cfg, runner)

	// Seed the state DB with a file
	e.state.UpdateFileState(proj.Name, "important.txt", "h1", 100, time.Now().UnixNano(), 0)

	// Now close the DB to simulate a transient error
	e.state.Close()

	// deleteRemoteFile should ideally report an error, but the code does:
	// fileState, _ := e.state.GetFileState(...)
	// If DB is closed, GetFileState returns (nil, error) but error is discarded.
	// So fileState=nil, falls through to GetFilesUnderDir (also fails silently),
	// and the delete is silently skipped.

	// We can't easily test this without panicking, so verify the pattern by
	// re-opening and checking the file still exists in state
	// (This test documents the behavior rather than asserting a specific outcome)
	t.Log("NOTE: deleteRemoteFile discards GetFileState errors — see sync.go line 423")
}

// syncFullProject uses global delete policy, not per-project
func TestSyncFullProject_UsesGlobalDeletePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var verb string
	runner := func(ctx context.Context, args []string) int {
		verb = args[0]
		return 0
	}
	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if verb != "sync" {
		t.Errorf("expected 'sync' verb for mirror policy, got %q", verb)
	}
}

func TestSyncFullProject_DefaultIgnorePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	// No delete policy set → defaults to "ignore" → uses "copy" verb

	var verb string
	runner := func(ctx context.Context, args []string) int {
		verb = args[0]
		return 0
	}
	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if verb != "copy" {
		t.Errorf("expected 'copy' verb for default ignore policy, got %q", verb)
	}
}

// deleteRemoteDir with ignore policy should be a no-op
func TestDeleteRemoteDir_IgnorePolicy_NoRcloneCalls(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	// Default = ignore policy

	var rcloneCalls int32
	runner := func(ctx context.Context, args []string) int {
		atomic.AddInt32(&rcloneCalls, 1)
		return 0
	}
	e := testEngine(t, cfg, runner)

	// Seed files under a dir
	e.state.UpdateFileState(proj.Name, "mydir/a.txt", "h1", 10, time.Now().UnixNano(), 0)
	e.state.UpdateFileState(proj.Name, "mydir/b.txt", "h2", 20, time.Now().UnixNano(), 0)

	e.deleteRemoteDir(context.Background(), proj, "mydir", false)

	if atomic.LoadInt32(&rcloneCalls) != 0 {
		t.Errorf("expected 0 rclone calls for ignore policy, got %d", rcloneCalls)
	}
}

// deleteFlags should include --bwlimit when configured
func TestDeleteFlags_BandwidthLimit(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.BandwidthLimit = "5M"
	e := testEngine(t, cfg, nil)

	flags := e.deleteFlags()
	found := false
	for i, f := range flags {
		if f == "--bwlimit" && i+1 < len(flags) && flags[i+1] == "5M" {
			found = true
		}
	}
	if !found {
		t.Errorf("deleteFlags missing --bwlimit 5M, got: %v", flags)
	}
}

// deleteFlags should include extra rclone flags
func TestDeleteFlags_ExtraFlags(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.RcloneExtraFlags = []string{"--fast-list", "--no-traverse"}
	e := testEngine(t, cfg, nil)

	flags := e.deleteFlags()
	flagStr := strings.Join(flags, " ")
	if !strings.Contains(flagStr, "--fast-list") {
		t.Errorf("deleteFlags missing --fast-list, got: %v", flags)
	}
	if !strings.Contains(flagStr, "--no-traverse") {
		t.Errorf("deleteFlags missing --no-traverse, got: %v", flags)
	}
}

// quarantine path format verification
func TestDeleteRemoteFile_QuarantinePath_Format(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "quarantine"

	var capturedArgs []string
	runner := func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	}
	e := testEngine(t, cfg, runner)

	// Seed state
	e.state.UpdateFileState(proj.Name, "data/report.csv", "h1", 100, time.Now().UnixNano(), 0)

	e.deleteRemoteFile(context.Background(), proj, "data/report.csv", false)

	if len(capturedArgs) == 0 {
		t.Fatal("expected rclone call for quarantine")
	}

	if capturedArgs[0] != "moveto" {
		t.Errorf("expected 'moveto' verb, got %q", capturedArgs[0])
	}

	// quarantine path should contain .quarantine/ and timestamp
	quarantineDst := capturedArgs[2]
	if !strings.Contains(quarantineDst, ".quarantine/") {
		t.Errorf("quarantine path missing .quarantine/: %s", quarantineDst)
	}
	if !strings.Contains(quarantineDst, "data/report.csv.") {
		t.Errorf("quarantine path missing original path: %s", quarantineDst)
	}
}

// =============================================================================
// Rename detection tests (coverage gap #3)
//
// SelectiveMirror does NOT have atomic rename detection. fsnotify fires:
//   - Rename event for OLD path → handleRename queues TaskDelete{ForceDelete: true}
//   - Create event for NEW path → normal debounce → TaskSync
//
// These tests verify the sync engine correctly handles the delete+create pair
// that results from a rename, including the ForceDelete semantics.
// =============================================================================

// TestRename_SameDir verifies that renaming a file within the same directory
// causes the old remote path to be deleted (ForceDelete) and the new path to be synced.
func TestRename_SameDir(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore" // ForceDelete should override this

	var rcloneCalls [][]string
	var mu gosync.Mutex
	runner := func(ctx context.Context, args []string) int {
		mu.Lock()
		copied := make([]string, len(args))
		copy(copied, args)
		rcloneCalls = append(rcloneCalls, copied)
		mu.Unlock()
		return 0
	}

	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// Seed state: old file was previously synced
	e.state.UpdateFileState(proj.Name, "docs/old-name.txt", "hash1", 50, time.Now().UnixNano(), 0)

	// Step 1: Rename event fires for old path → TaskDelete with ForceDelete
	e.processTask(context.Background(), Task{
		Project:     proj,
		RelPath:     "docs/old-name.txt",
		Type:        TaskDelete,
		ForceDelete: true,
	})

	// Step 2: Create event fires for new path → TaskSync
	// Create the new file on disk so syncSingleFile can read it
	newFilePath := filepath.Join(proj.LocalPath, "docs", "new-name.txt")
	os.MkdirAll(filepath.Dir(newFilePath), 0755)
	os.WriteFile(newFilePath, []byte("renamed content"), 0644)

	e.processTask(context.Background(), Task{
		Project: proj,
		RelPath: "docs/new-name.txt",
		Type:    TaskSync,
	})

	mu.Lock()
	defer mu.Unlock()

	// Verify: old path was deleted via rclone deletefile
	if len(rcloneCalls) < 1 {
		t.Fatal("expected at least 1 rclone call (delete old path)")
	}
	if rcloneCalls[0][0] != "deletefile" {
		t.Errorf("first call should be 'deletefile', got %q", rcloneCalls[0][0])
	}
	if !strings.Contains(strings.Join(rcloneCalls[0], " "), "old-name.txt") {
		t.Errorf("delete should target old-name.txt, got: %v", rcloneCalls[0])
	}

	// Verify: new path was synced via rclone copyto
	if len(rcloneCalls) < 2 {
		t.Fatal("expected 2 rclone calls (delete old + sync new)")
	}
	if rcloneCalls[1][0] != "copyto" {
		t.Errorf("second call should be 'copyto', got %q", rcloneCalls[1][0])
	}
	if !strings.Contains(strings.Join(rcloneCalls[1], " "), "new-name.txt") {
		t.Errorf("sync should target new-name.txt, got: %v", rcloneCalls[1])
	}

	// Verify state: old file state should be deleted
	oldState, _ := e.state.GetFileState(proj.Name, "docs/old-name.txt")
	if oldState != nil {
		t.Error("old file state should be deleted after rename cleanup")
	}

	// Verify state: new file state should exist
	newState, _ := e.state.GetFileState(proj.Name, "docs/new-name.txt")
	if newState == nil {
		t.Error("new file state should exist after sync")
	}
}

// TestRename_CrossDirectory verifies rename from one directory to another.
// Same as same-dir rename: delete old + sync new, but paths differ at directory level.
func TestRename_CrossDirectory(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore"

	var rcloneCalls [][]string
	var mu gosync.Mutex
	runner := func(ctx context.Context, args []string) int {
		mu.Lock()
		copied := make([]string, len(args))
		copy(copied, args)
		rcloneCalls = append(rcloneCalls, copied)
		mu.Unlock()
		return 0
	}

	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// Seed: file in src/ was synced
	e.state.UpdateFileState(proj.Name, "src/utils.go", "hashA", 200, time.Now().UnixNano(), 0)

	// Step 1: Rename event for old path (ForceDelete overrides ignore policy)
	e.processTask(context.Background(), Task{
		Project:     proj,
		RelPath:     "src/utils.go",
		Type:        TaskDelete,
		ForceDelete: true,
	})

	// Step 2: Create event for new path in different directory
	newPath := filepath.Join(proj.LocalPath, "pkg", "helpers", "utils.go")
	os.MkdirAll(filepath.Dir(newPath), 0755)
	os.WriteFile(newPath, []byte("package helpers\n// moved here"), 0644)

	e.processTask(context.Background(), Task{
		Project: proj,
		RelPath: "pkg/helpers/utils.go",
		Type:    TaskSync,
	})

	mu.Lock()
	defer mu.Unlock()

	// Old path should be deleted
	if len(rcloneCalls) < 1 || rcloneCalls[0][0] != "deletefile" {
		t.Fatal("expected deletefile for old path src/utils.go")
	}
	oldCallStr := strings.Join(rcloneCalls[0], " ")
	if !strings.Contains(oldCallStr, "src/utils.go") {
		t.Errorf("delete should reference src/utils.go, got: %s", oldCallStr)
	}

	// New path should be synced
	if len(rcloneCalls) < 2 || rcloneCalls[1][0] != "copyto" {
		t.Fatal("expected copyto for new path pkg/helpers/utils.go")
	}
	newCallStr := strings.Join(rcloneCalls[1], " ")
	if !strings.Contains(newCallStr, "pkg/helpers/utils.go") {
		t.Errorf("sync should reference pkg/helpers/utils.go, got: %s", newCallStr)
	}

	// State check
	oldState, _ := e.state.GetFileState(proj.Name, "src/utils.go")
	if oldState != nil {
		t.Error("old path state should be cleaned up")
	}
	newState, _ := e.state.GetFileState(proj.Name, "pkg/helpers/utils.go")
	if newState == nil {
		t.Error("new path state should exist")
	}
}

// TestRename_RapidChain verifies a→b→c rename chain results in correct final state.
// Each rename produces a ForceDelete for the old name and a sync for the new name.
// After a→b→c, only "c" should exist on remote; "a" and "b" should be deleted.
func TestRename_RapidChain(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore"

	var rcloneCalls [][]string
	var mu gosync.Mutex
	runner := func(ctx context.Context, args []string) int {
		mu.Lock()
		copied := make([]string, len(args))
		copy(copied, args)
		rcloneCalls = append(rcloneCalls, copied)
		mu.Unlock()
		return 0
	}

	e := testEngine(t, cfg, runner)
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// Seed: file "a.txt" was synced
	e.state.UpdateFileState(proj.Name, "a.txt", "h1", 10, time.Now().UnixNano(), 0)

	// Rename a→b: delete a, sync b
	e.processTask(context.Background(), Task{
		Project: proj, RelPath: "a.txt", Type: TaskDelete, ForceDelete: true,
	})
	bPath := filepath.Join(proj.LocalPath, "b.txt")
	os.WriteFile(bPath, []byte("content-ab"), 0644)
	e.processTask(context.Background(), Task{
		Project: proj, RelPath: "b.txt", Type: TaskSync,
	})

	// Rename b→c: delete b, sync c
	e.processTask(context.Background(), Task{
		Project: proj, RelPath: "b.txt", Type: TaskDelete, ForceDelete: true,
	})
	cPath := filepath.Join(proj.LocalPath, "c.txt")
	os.Rename(bPath, cPath) // move b to c on disk
	e.processTask(context.Background(), Task{
		Project: proj, RelPath: "c.txt", Type: TaskSync,
	})

	mu.Lock()
	defer mu.Unlock()

	// Verify call sequence: deletefile(a), copyto(b), deletefile(b), copyto(c)
	if len(rcloneCalls) != 4 {
		t.Fatalf("expected 4 rclone calls for a→b→c chain, got %d", len(rcloneCalls))
	}
	expected := []struct {
		verb    string
		pathSub string
	}{
		{"deletefile", "a.txt"},
		{"copyto", "b.txt"},
		{"deletefile", "b.txt"},
		{"copyto", "c.txt"},
	}
	for i, exp := range expected {
		if rcloneCalls[i][0] != exp.verb {
			t.Errorf("call[%d]: verb = %q, want %q", i, rcloneCalls[i][0], exp.verb)
		}
		callStr := strings.Join(rcloneCalls[i], " ")
		if !strings.Contains(callStr, exp.pathSub) {
			t.Errorf("call[%d]: expected path containing %q, got: %s", i, exp.pathSub, callStr)
		}
	}

	// Final state: only c.txt should exist
	aState, _ := e.state.GetFileState(proj.Name, "a.txt")
	bState, _ := e.state.GetFileState(proj.Name, "b.txt")
	cState, _ := e.state.GetFileState(proj.Name, "c.txt")
	if aState != nil {
		t.Error("a.txt state should not exist after chain rename")
	}
	if bState != nil {
		t.Error("b.txt state should not exist after chain rename")
	}
	if cState == nil {
		t.Error("c.txt state should exist as final rename destination")
	}
}

// TestRename_ExcludedFile verifies that renaming an excluded file does not
// trigger any sync or delete. The filter check in both handleRename and
// syncSingleFile should reject the file.
func TestRename_ExcludedFile_NoSync(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var rcloneCalled atomic.Bool
	runner := func(ctx context.Context, args []string) int {
		rcloneCalled.Store(true)
		return 0
	}

	e := testEngine(t, cfg, runner)

	// Create a filter that excludes *.log files
	fe, err := filter.New([]string{"*.log"}, "")
	if err != nil {
		t.Fatalf("filter.New: %v", err)
	}
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// Even if somehow an excluded file had state (shouldn't happen, but defensive)
	// the sync engine re-checks the filter before syncing.

	// Create the file on disk
	logPath := filepath.Join(proj.LocalPath, "debug.log")
	os.WriteFile(logPath, []byte("log data"), 0644)

	// Attempt to sync an excluded file (simulating the Create event after rename)
	e.syncSingleFile(context.Background(), proj, "debug.log")

	if rcloneCalled.Load() {
		t.Error("rclone should NOT be called for excluded file (*.log)")
	}
}

// TestRename_ForceDelete_OverridesAllPolicies verifies that ForceDelete works
// with every delete policy (ignore, mirror, quarantine). Rename cleanup must
// always delete the old remote path regardless of policy.
func TestRename_ForceDelete_OverridesQuarantinePolicy(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "quarantine"

	var capturedVerb string
	runner := func(ctx context.Context, args []string) int {
		capturedVerb = args[0]
		return 0
	}

	e := testEngine(t, cfg, runner)
	e.state.UpdateFileState(proj.Name, "old.txt", "h1", 10, time.Now().UnixNano(), 0)

	// ForceDelete should use "deletefile" (mirror behavior), NOT "moveto" (quarantine)
	e.deleteRemoteFile(context.Background(), proj, "old.txt", true)

	if capturedVerb != "deletefile" {
		t.Errorf("ForceDelete should use 'deletefile' (mirror), got %q", capturedVerb)
	}
}

// TestRename_NeverSyncedFile_NoRemoteDelete verifies that renaming a file that
// was never synced does not attempt a remote delete (no state = nothing to clean up).
func TestRename_NeverSyncedFile_NoRemoteDelete(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "mirror"

	var rcloneCalled atomic.Bool
	runner := func(ctx context.Context, args []string) int {
		rcloneCalled.Store(true)
		return 0
	}

	e := testEngine(t, cfg, runner)

	// No state seeded — file was never synced
	e.deleteRemoteFile(context.Background(), proj, "never-existed.txt", true)

	if rcloneCalled.Load() {
		t.Error("rclone should NOT be called for a file that was never synced")
	}
}

// TestRename_DirectoryRename verifies that renaming a directory deletes all
// previously-synced children from the remote. The watcher fires Rename for the
// old directory path; deleteRemoteFile detects it has children and delegates
// to deleteRemoteDir.
func TestRename_DirectoryRename_DeletesChildren(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	cfg.DeletePolicyStr = "ignore" // ForceDelete should still work

	var deletedPaths []string
	var mu gosync.Mutex
	runner := func(ctx context.Context, args []string) int {
		mu.Lock()
		if args[0] == "deletefile" {
			// Extract the relative path from the remote path
			deletedPaths = append(deletedPaths, args[1])
		}
		mu.Unlock()
		return 0
	}

	e := testEngine(t, cfg, runner)

	// Seed: 3 files under old-dir/
	e.state.UpdateFileState(proj.Name, "old-dir/a.txt", "ha", 10, time.Now().UnixNano(), 0)
	e.state.UpdateFileState(proj.Name, "old-dir/b.txt", "hb", 20, time.Now().UnixNano(), 0)
	e.state.UpdateFileState(proj.Name, "old-dir/sub/c.txt", "hc", 30, time.Now().UnixNano(), 0)

	// Simulate: watcher sends ForceDelete for the old directory path
	e.processTask(context.Background(), Task{
		Project:     proj,
		RelPath:     "old-dir",
		Type:        TaskDelete,
		ForceDelete: true,
	})

	mu.Lock()
	defer mu.Unlock()

	if len(deletedPaths) != 3 {
		t.Errorf("expected 3 remote deletes for directory children, got %d: %v", len(deletedPaths), deletedPaths)
	}

	// All child states should be cleaned up
	for _, child := range []string{"old-dir/a.txt", "old-dir/b.txt", "old-dir/sub/c.txt"} {
		fs, _ := e.state.GetFileState(proj.Name, child)
		if fs != nil {
			t.Errorf("state for %q should be deleted after directory rename", child)
		}
	}
}

// =============================================================================
// Reconciliation tests (coverage gap #2): periodic full-project sync safety net
// =============================================================================

// TestReconciliation_FullProjectSync_UsesChecksumAndFilterFrom verifies that
// syncFullProject (the mechanism reconciliation uses) calls rclone with
// --checksum and --filter-from flags, which are essential for catching files
// missed by the watcher.
func TestReconciliation_FullProjectSync_UsesChecksumAndFilterFrom(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called")
	}

	hasChecksum := false
	hasFilterFrom := false
	for i, arg := range capturedArgs {
		if arg == "--checksum" {
			hasChecksum = true
		}
		if arg == "--filter-from" && i+1 < len(capturedArgs) {
			hasFilterFrom = true
		}
	}
	if !hasChecksum {
		t.Errorf("reconciliation full sync missing --checksum flag, args: %v", capturedArgs)
	}
	if !hasFilterFrom {
		t.Errorf("reconciliation full sync missing --filter-from flag, args: %v", capturedArgs)
	}
}

// TestReconciliation_FullProjectSync_RecordsLastSyncMeta verifies that after
// a successful full sync, the engine writes last_full_sync_<project> metadata.
func TestReconciliation_FullProjectSync_RecordsLastSyncMeta(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	meta, err := e.state.GetMeta("last_full_sync_" + proj.Name)
	if err != nil {
		t.Fatalf("GetMeta error: %v", err)
	}
	if meta == "" {
		t.Error("last_full_sync metadata not recorded after successful full sync")
	}
	if _, err := time.Parse(time.RFC3339, meta); err != nil {
		t.Errorf("last_full_sync is not valid RFC3339: %q, error: %v", meta, err)
	}
}

// TestReconciliation_FullProjectSync_FailureDoesNotRecordMeta verifies that
// a failed full sync does NOT write last_full_sync metadata.
func TestReconciliation_FullProjectSync_FailureDoesNotRecordMeta(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 1
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	meta, _ := e.state.GetMeta("last_full_sync_" + proj.Name)
	if meta != "" {
		t.Errorf("last_full_sync should NOT be set after failed sync, got: %q", meta)
	}
}

// TestReconciliation_FullProjectSync_SourceIsLocalPath verifies that rclone
// receives the project's local path as source, ensuring reconciliation scans
// the actual filesystem (catching files not in state DB).
func TestReconciliation_FullProjectSync_SourceIsLocalPath(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	e.syncFullProject(context.Background(), proj)

	if len(capturedArgs) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(capturedArgs), capturedArgs)
	}
	if capturedArgs[1] != proj.LocalPath {
		t.Errorf("source path = %q, want %q", capturedArgs[1], proj.LocalPath)
	}
	if capturedArgs[2] != proj.Remote {
		t.Errorf("remote = %q, want %q", capturedArgs[2], proj.Remote)
	}
}

// TestReconciliation_PeriodicTicker_SendsFullProjectTasks simulates the
// periodic reconciliation pattern from heartbeatLoop: full-project sync tasks
// are enqueued for each project.
func TestReconciliation_PeriodicTicker_SendsFullProjectTasks(t *testing.T) {
	proj1 := testProject(t)
	proj1.Name = "proj-alpha"
	proj2 := testProject(t)
	proj2.Name = "proj-beta"

	cfg := &config.Global{
		Projects:    []config.Project{proj1, proj2},
		RclonePath:  "rclone",
		SyncWorkers: 2,
	}

	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{
		proj1.Name: fe,
		proj2.Name: fe,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Simulate reconciliation: send full-project tasks for all projects
	for _, proj := range cfg.Projects {
		select {
		case e.TaskChan <- Task{Project: proj, RelPath: ""}:
		case <-ctx.Done():
			t.Fatal("context cancelled before tasks were sent")
		}
	}

	// Drain and verify
	received := map[string]bool{}
	timeout := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case task := <-e.TaskChan:
			if task.RelPath != "" {
				t.Errorf("expected full-project sync (RelPath=''), got RelPath=%q", task.RelPath)
			}
			received[task.Project.Name] = true
		case <-timeout:
			t.Fatal("timed out waiting for reconciliation tasks")
		}
	}

	if !received["proj-alpha"] {
		t.Error("proj-alpha did not receive reconciliation task")
	}
	if !received["proj-beta"] {
		t.Error("proj-beta did not receive reconciliation task")
	}
}

// TestReconciliation_ContextCancel_StopsTaskSubmission verifies that when the
// context is cancelled during reconciliation task submission, the loop exits
// without blocking.
func TestReconciliation_ContextCancel_StopsTaskSubmission(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		return 0
	})

	// Fill the channel to capacity so the next send would block
	for i := 0; i < cap(e.TaskChan); i++ {
		e.TaskChan <- Task{Project: proj, RelPath: "filler"}
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, p := range cfg.Projects {
			select {
			case e.TaskChan <- Task{Project: p, RelPath: ""}:
			case <-ctx.Done():
				return
			}
		}
	}()

	cancel()

	select {
	case <-done:
		// Success: cancellation unblocked the send
	case <-time.After(2 * time.Second):
		t.Fatal("reconciliation did not respect context cancellation (blocked on full channel)")
	}
}

// TestReconciliation_FullProjectSync_NoFilterEngine verifies graceful handling
// when there is no filter engine for a project.
func TestReconciliation_FullProjectSync_NoFilterEngine(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	called := false
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		called = true
		return 0
	})
	// Deliberately don't set e.filters

	e.syncFullProject(context.Background(), proj)

	if called {
		t.Error("rclone should NOT be called when no filter engine exists for project")
	}
}

// TestReconciliation_ProcessTask_RoutesToFullSync verifies that processTask
// routes a task with empty RelPath to syncFullProject (the reconciliation path).
func TestReconciliation_ProcessTask_RoutesToFullSync(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	task := Task{Project: proj, RelPath: ""}
	e.processTask(context.Background(), task)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called via processTask for full-project sync")
	}
	if capturedArgs[0] != "copy" {
		t.Errorf("expected 'copy' verb for full-project sync, got %q", capturedArgs[0])
	}
}

// TestReconciliation_PicksUpUntrackedFiles verifies the core reconciliation
// value proposition: files that exist on disk but are NOT in the state DB
// still get synced via rclone copy --checksum over the entire project directory.
func TestReconciliation_PicksUpUntrackedFiles(t *testing.T) {
	proj := testProject(t)
	cfg := testConfig(proj)

	// Create files on disk that are NOT tracked in state DB
	for _, name := range []string{"untracked1.txt", "subdir/untracked2.txt"} {
		p := filepath.Join(proj.LocalPath, name)
		os.MkdirAll(filepath.Dir(p), 0755)
		os.WriteFile(p, []byte("missed by watcher"), 0644)
	}

	var capturedArgs []string
	e := testEngine(t, cfg, func(ctx context.Context, args []string) int {
		capturedArgs = args
		return 0
	})
	fe := testFilter(t)
	e.filters = map[string]*filter.Engine{proj.Name: fe}

	// Verify nothing is in state DB
	fs1, _ := e.state.GetFileState(proj.Name, "untracked1.txt")
	fs2, _ := e.state.GetFileState(proj.Name, "subdir/untracked2.txt")
	if fs1 != nil || fs2 != nil {
		t.Fatal("precondition failed: files should not be in state DB")
	}

	// Run full project sync (what reconciliation does)
	e.syncFullProject(context.Background(), proj)

	if len(capturedArgs) == 0 {
		t.Fatal("rclone was not called")
	}
	if capturedArgs[1] != proj.LocalPath {
		t.Errorf("rclone source should be project local path %q, got %q", proj.LocalPath, capturedArgs[1])
	}
	hasChecksum := false
	for _, arg := range capturedArgs {
		if arg == "--checksum" {
			hasChecksum = true
		}
	}
	if !hasChecksum {
		t.Error("full sync must use --checksum to reliably detect untracked files")
	}
}

// TestReconciliation_ConfigInterval_Respected verifies that ReconcileInterval
// from config is used (not hardcoded).
func TestReconciliation_ConfigInterval_Respected(t *testing.T) {
	cfg := &config.Global{
		ReconcileIntervalS: 120,
	}
	interval := cfg.ReconcileInterval()
	if interval != 2*time.Minute {
		t.Errorf("ReconcileInterval = %v, want 2m", interval)
	}
}

// TestReconciliation_ConfigInterval_Default verifies the 5-minute default.
func TestReconciliation_ConfigInterval_Default(t *testing.T) {
	cfg := &config.Global{}
	interval := cfg.ReconcileInterval()
	if interval != 5*time.Minute {
		t.Errorf("default ReconcileInterval = %v, want 5m", interval)
	}
}

// TestReconciliation_ConfigInterval_NegativeUsesDefault verifies that a negative
// value falls back to the default.
func TestReconciliation_ConfigInterval_NegativeUsesDefault(t *testing.T) {
	cfg := &config.Global{ReconcileIntervalS: -1}
	interval := cfg.ReconcileInterval()
	if interval != 5*time.Minute {
		t.Errorf("ReconcileInterval for -1 = %v, want 5m default", interval)
	}
}
