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
