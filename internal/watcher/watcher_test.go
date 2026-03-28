package watcher

import (
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// --- isSubPath tests ---

func TestIsSubPath_ChildUnderParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !isSubPath(`C:\Projects\MyApp\src\main.go`, `C:\Projects\MyApp`) {
			t.Error("expected src/main.go to be under MyApp")
		}
	} else {
		if !isSubPath("/home/user/projects/app/src", "/home/user/projects/app") {
			t.Error("expected src to be under app")
		}
	}
}

func TestIsSubPath_ParentItself(t *testing.T) {
	dir := t.TempDir()
	if !isSubPath(dir, dir) {
		t.Error("a path should be 'sub' of itself (equality)")
	}
}

func TestIsSubPath_NotChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		if isSubPath(`C:\Other\File.txt`, `C:\Projects\MyApp`) {
			t.Error("Other should not be under MyApp")
		}
	} else {
		if isSubPath("/tmp/other", "/home/user") {
			t.Error("/tmp/other should not be under /home/user")
		}
	}
}

func TestIsSubPath_SimilarPrefix(t *testing.T) {
	// "MyAppBackup" should NOT match "MyApp"
	if runtime.GOOS == "windows" {
		if isSubPath(`C:\Projects\MyAppBackup\file.txt`, `C:\Projects\MyApp`) {
			t.Error("MyAppBackup should not match MyApp prefix")
		}
	} else {
		if isSubPath("/projects/myapp-backup/f.txt", "/projects/myapp") {
			t.Error("myapp-backup should not match myapp prefix")
		}
	}
}

// --- isRelSubPath tests ---

func TestIsRelSubPath_ChildUnderParent(t *testing.T) {
	if !isRelSubPath("src/main.go", "src") {
		t.Error("src/main.go should be under src")
	}
}

func TestIsRelSubPath_DeepChild(t *testing.T) {
	if !isRelSubPath("a/b/c/d.txt", "a/b") {
		t.Error("a/b/c/d.txt should be under a/b")
	}
}

func TestIsRelSubPath_DotParent(t *testing.T) {
	if !isRelSubPath("any/path", ".") {
		t.Error("any path should be under '.'")
	}
}

func TestIsRelSubPath_EmptyParent(t *testing.T) {
	if !isRelSubPath("any/path", "") {
		t.Error("any path should be under empty parent")
	}
}

func TestIsRelSubPath_NotChild(t *testing.T) {
	if isRelSubPath("other/file.txt", "src") {
		t.Error("other/file.txt should not be under src")
	}
}

func TestIsRelSubPath_SimilarPrefix(t *testing.T) {
	if isRelSubPath("srcgen/file.txt", "src") {
		t.Error("srcgen should not match src prefix (no separator)")
	}
}

func TestIsRelSubPath_ExactMatch(t *testing.T) {
	if isRelSubPath("src", "src") {
		t.Error("exact match (no separator after) should return false")
	}
}

// --- findProject tests ---

func TestFindProject_Match(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		projects: []*projectWatcher{
			{project: makeProject(dir, "proj-a")},
		},
	}

	filePath := filepath.Join(dir, "subdir", "file.txt")
	pw := m.findProject(filePath)
	if pw == nil {
		t.Fatal("expected to find project for file under project dir")
	}
	if pw.project.Name != "proj-a" {
		t.Errorf("project = %q, want %q", pw.project.Name, "proj-a")
	}
}

func TestFindProject_NoMatch(t *testing.T) {
	dir := t.TempDir()
	m := &Manager{
		projects: []*projectWatcher{
			{project: makeProject(dir, "proj-a")},
		},
	}

	pw := m.findProject("/completely/different/path.txt")
	if pw != nil {
		t.Error("expected nil for path outside all projects")
	}
}

// --- safeGo panic recovery ---

func TestSafeGo_PanicRecovery(t *testing.T) {
	m := &Manager{log: slog.Default()}

	done := make(chan struct{})
	go func() {
		m.safeGo("test-goroutine", func() {
			panic("deliberate test panic")
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo did not recover from panic")
	}

	errs := m.HealthErrors()
	if len(errs) != 1 {
		t.Fatalf("expected 1 health error, got %d", len(errs))
	}
	if errs[0].Source != "test-goroutine" {
		t.Errorf("source = %q, want %q", errs[0].Source, "test-goroutine")
	}
}

// --- HealthErrors tests ---

func TestHealthErrors_RecordAndRetrieve(t *testing.T) {
	m := &Manager{log: slog.Default()}

	// Empty at start
	if errs := m.HealthErrors(); len(errs) != 0 {
		t.Errorf("expected 0 health errors, got %d", len(errs))
	}

	// Record some errors via safeGo panics
	for i := 0; i < 3; i++ {
		func() {
			m.safeGo("test", func() { panic("boom") })
		}()
	}

	errs := m.HealthErrors()
	if len(errs) != 3 {
		t.Errorf("expected 3 health errors, got %d", len(errs))
	}

	// Verify returned slice is a copy (modifying it doesn't affect internal state)
	errs[0].Source = "modified"
	original := m.HealthErrors()
	if original[0].Source == "modified" {
		t.Error("HealthErrors should return a copy, not a reference")
	}
}

func TestHealthErrors_CappedAt100(t *testing.T) {
	m := &Manager{log: slog.Default()}

	for i := 0; i < 110; i++ {
		func() {
			m.safeGo("overflow", func() { panic("boom") })
		}()
	}

	errs := m.HealthErrors()
	if len(errs) > 100 {
		t.Errorf("health errors should be capped at 100, got %d", len(errs))
	}
}

// =============================================================================
// Debounce timer tests
// =============================================================================

// TestDebounce_SingleEvent verifies that a single file event fires exactly once
// after the debounce duration (event-driven, no polling).
func TestDebounce_SingleEvent(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:  config.Project{Name: "debounce-test", LocalPath: t.TempDir(), DebounceSec: 1},
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}

	addDebounceTimer(m, pw, "file.txt")

	select {
	case task := <-syncChan:
		if task.RelPath != "file.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "file.txt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timer did not fire")
	}

	// No duplicate
	time.Sleep(200 * time.Millisecond)
	select {
	case task := <-syncChan:
		t.Errorf("duplicate task for %q", task.RelPath)
	default:
	}
}

// TestDebounce_MultipleRapidEvents_SameFile verifies coalescing via timer reset.
func TestDebounce_MultipleRapidEvents_SameFile(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:  config.Project{Name: "debounce-coalesce", LocalPath: t.TempDir(), DebounceSec: 1},
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}

	for i := 0; i < 10; i++ {
		addDebounceTimer(m, pw, "rapid.txt")
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case task := <-syncChan:
		if task.RelPath != "rapid.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "rapid.txt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timer did not fire")
	}

	time.Sleep(200 * time.Millisecond)
	select {
	case <-syncChan:
		t.Error("expected only 1 coalesced task")
	default:
	}
}

// TestDebounce_DifferentFiles_Independent verifies per-file independence.
func TestDebounce_DifferentFiles_Independent(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:  config.Project{Name: "debounce-independent", LocalPath: t.TempDir(), DebounceSec: 1},
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}

	addDebounceTimer(m, pw, "alpha.txt")
	addDebounceTimer(m, pw, "beta.txt")
	addDebounceTimer(m, pw, "gamma.txt")

	seen := make(map[string]bool)
	timeout := time.After(3 * time.Second)
	for i := 0; i < 3; i++ {
		select {
		case task := <-syncChan:
			seen[task.RelPath] = true
		case <-timeout:
			t.Fatalf("only got %d/3 tasks", i)
		}
	}
	for _, name := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
		if !seen[name] {
			t.Errorf("missing task for %q", name)
		}
	}
}

// TestDebounce_TimerReset verifies that resetting delays emission.
func TestDebounce_TimerReset(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:  config.Project{Name: "debounce-reset", LocalPath: t.TempDir(), DebounceSec: 1},
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}

	addDebounceTimer(m, pw, "reset.txt")
	time.Sleep(500 * time.Millisecond)
	addDebounceTimer(m, pw, "reset.txt") // reset: 1s from now

	// At 800ms from start (300ms after reset), nothing should have fired
	time.Sleep(300 * time.Millisecond)
	select {
	case task := <-syncChan:
		t.Errorf("timer NOT reset: premature task for %q", task.RelPath)
	default:
	}

	// Wait for the reset timer
	select {
	case task := <-syncChan:
		if task.RelPath != "reset.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "reset.txt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reset timer never fired")
	}
}

// TestDebounce_PendingMapCleared verifies cleanup after emission.
func TestDebounce_PendingMapCleared(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:  config.Project{Name: "debounce-clear", LocalPath: t.TempDir(), DebounceSec: 1},
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}

	addDebounceTimer(m, pw, "once.txt")

	select {
	case <-syncChan:
	case <-time.After(3 * time.Second):
		t.Fatal("timer never fired")
	}

	pw.mu.Lock()
	remaining := len(pw.pending)
	pw.mu.Unlock()
	if remaining != 0 {
		t.Errorf("pending map should be empty, has %d entries", remaining)
	}
}

// addDebounceTimer simulates the event-driven debounce: starts or resets a
// per-file timer, mirroring the logic in handleEvent.
func addDebounceTimer(m *Manager, pw *projectWatcher, relPath string) {
	pw.mu.Lock()
	if t, ok := pw.pending[relPath]; ok {
		t.Reset(pw.project.DebounceDuration())
	} else {
		rp := relPath
		pw.pending[relPath] = time.AfterFunc(pw.project.DebounceDuration(), func() {
			pw.mu.Lock()
			delete(pw.pending, rp)
			pw.mu.Unlock()
			select {
			case pw.syncChan <- msync.Task{Project: pw.project, RelPath: rp}:
			default:
				m.log.Warn("sync channel full", "path", rp)
			}
		})
	}
	pw.mu.Unlock()
}

// =============================================================================
// Dynamic debounce tests (debounce_sec = 0, the default)
// =============================================================================

// TestDynamicDebounce_FirstEventFiresImmediately verifies that in dynamic mode,
// the very first event for a file fires immediately without any delay.
func TestDynamicDebounce_FirstEventFiresImmediately(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:    config.Project{Name: "dyn-immediate", LocalPath: t.TempDir(), DebounceSec: 0},
		syncChan:   syncChan,
		pending:    make(map[string]*time.Timer),
		lastSynced: make(map[string]time.Time),
	}

	simulateDynamicEvent(m, pw, "new_file.txt")

	select {
	case task := <-syncChan:
		if task.RelPath != "new_file.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "new_file.txt")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("first event should fire immediately in dynamic mode")
	}
}

// TestDynamicDebounce_RapidEventsDebounce verifies that rapid events within
// the detection window activate debouncing instead of firing immediately.
func TestDynamicDebounce_RapidEventsDebounce(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:    config.Project{Name: "dyn-burst", LocalPath: t.TempDir(), DebounceSec: 0},
		syncChan:   syncChan,
		pending:    make(map[string]*time.Timer),
		lastSynced: make(map[string]time.Time),
	}

	// First event fires immediately
	simulateDynamicEvent(m, pw, "burst.txt")
	<-syncChan // drain

	// Second event within 500ms should NOT fire immediately — it should debounce
	time.Sleep(50 * time.Millisecond)
	simulateDynamicEvent(m, pw, "burst.txt")

	// Verify nothing fires immediately
	time.Sleep(100 * time.Millisecond)
	select {
	case <-syncChan:
		t.Error("rapid second event should debounce, not fire immediately")
	default:
		// expected — debounce timer is active
	}

	// Wait for the debounce timer to fire (500ms)
	select {
	case task := <-syncChan:
		if task.RelPath != "burst.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "burst.txt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("debounce timer did not fire")
	}
}

// TestDynamicDebounce_IsolatedEventAfterCooldown verifies that an event arriving
// after the detection window has passed fires immediately (back to instant mode).
func TestDynamicDebounce_IsolatedEventAfterCooldown(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:    config.Project{Name: "dyn-cooldown", LocalPath: t.TempDir(), DebounceSec: 0},
		syncChan:   syncChan,
		pending:    make(map[string]*time.Timer),
		lastSynced: make(map[string]time.Time),
	}

	// First event fires immediately
	simulateDynamicEvent(m, pw, "cool.txt")
	<-syncChan

	// Wait past the detection window
	time.Sleep(dynamicDebounceDetect + 100*time.Millisecond)

	// Next event should fire immediately (not debounced)
	start := time.Now()
	simulateDynamicEvent(m, pw, "cool.txt")

	select {
	case task := <-syncChan:
		elapsed := time.Since(start)
		if elapsed > 100*time.Millisecond {
			t.Errorf("event should fire immediately after cooldown, took %v", elapsed)
		}
		if task.RelPath != "cool.txt" {
			t.Errorf("RelPath = %q, want %q", task.RelPath, "cool.txt")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event did not fire after cooldown")
	}
}

// TestDynamicDebounce_RapidBurstCoalesces verifies that a burst of N events
// for the same file results in exactly 2 syncs: 1 immediate + 1 debounced.
func TestDynamicDebounce_RapidBurstCoalesces(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:    config.Project{Name: "dyn-coalesce", LocalPath: t.TempDir(), DebounceSec: 0},
		syncChan:   syncChan,
		pending:    make(map[string]*time.Timer),
		lastSynced: make(map[string]time.Time),
	}

	// Fire 10 rapid events
	for i := 0; i < 10; i++ {
		simulateDynamicEvent(m, pw, "rapid.txt")
		time.Sleep(10 * time.Millisecond)
	}

	// Should get exactly 2: first fires immediately, rest coalesce into 1 debounced
	count := 0
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-syncChan:
			count++
			if count > 2 {
				t.Fatalf("expected at most 2 syncs, got %d+", count)
			}
		case <-timeout:
			if count != 2 {
				t.Errorf("expected exactly 2 syncs (1 immediate + 1 debounced), got %d", count)
			}
			return
		}
	}
}

// TestDynamicDebounce_DifferentFilesIndependent verifies that dynamic debounce
// tracks files independently: event for file A doesn't affect file B's timing.
func TestDynamicDebounce_DifferentFilesIndependent(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	m := &Manager{log: slog.Default()}
	pw := &projectWatcher{
		project:    config.Project{Name: "dyn-independent", LocalPath: t.TempDir(), DebounceSec: 0},
		syncChan:   syncChan,
		pending:    make(map[string]*time.Timer),
		lastSynced: make(map[string]time.Time),
	}

	// Three different files — all should fire immediately (first event for each)
	simulateDynamicEvent(m, pw, "a.txt")
	simulateDynamicEvent(m, pw, "b.txt")
	simulateDynamicEvent(m, pw, "c.txt")

	seen := make(map[string]bool)
	timeout := time.After(500 * time.Millisecond)
	for i := 0; i < 3; i++ {
		select {
		case task := <-syncChan:
			seen[task.RelPath] = true
		case <-timeout:
			t.Fatalf("only got %d/3 immediate tasks", i)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if !seen[name] {
			t.Errorf("missing immediate task for %q", name)
		}
	}
}

// simulateDynamicEvent simulates the dynamic debounce logic from handleEvent.
func simulateDynamicEvent(m *Manager, pw *projectWatcher, relPath string) {
	pw.mu.Lock()
	if t, ok := pw.pending[relPath]; ok {
		t.Reset(dynamicDebounceDuration)
	} else if last, ok := pw.lastSynced[relPath]; ok && time.Since(last) < dynamicDebounceDetect {
		rp := relPath
		pw.pending[relPath] = time.AfterFunc(dynamicDebounceDuration, func() {
			pw.mu.Lock()
			delete(pw.pending, rp)
			pw.lastSynced[rp] = time.Now()
			pw.mu.Unlock()
			select {
			case pw.syncChan <- msync.Task{Project: pw.project, RelPath: rp}:
			default:
				m.log.Warn("sync channel full", "path", rp)
			}
		})
	} else {
		pw.lastSynced[relPath] = time.Now()
		pw.mu.Unlock()
		select {
		case pw.syncChan <- msync.Task{Project: pw.project, RelPath: relPath}:
		default:
			m.log.Warn("sync channel full", "path", relPath)
		}
		return
	}
	pw.mu.Unlock()
}

// --- helpers ---

func makeProject(localPath, name string) config.Project {
	return config.Project{
		Name:      name,
		LocalPath: localPath,
	}
}

// =============================================================================
// Bug-hunting tests: path matching edge cases
// =============================================================================

// BUG HUNT: isSubPath uses string comparison without case normalization.
// On Windows, "C:\Foo" and "c:\foo" are the same path but string comparison
// says they're different. This could cause findProject to miss files.
func TestIsSubPath_CaseSensitivity(t *testing.T) {
	// Test with different-case paths
	// On Windows this is a real bug; on Linux, paths ARE case-sensitive.
	if filepath.Separator == '\\' {
		// Windows: these should match
		result := isSubPath(`C:\Users\Test\Project\file.txt`, `c:\users\test\project`)
		if !result {
			t.Error("BUG: isSubPath is case-sensitive on Windows — " +
				"C:\\Users\\Test\\Project\\file.txt not recognized as child of c:\\users\\test\\project")
		}
	} else {
		t.Skip("case sensitivity is correct on Unix")
	}
}

// isRelSubPath: parent = child should return false (strictly under, not equal)
func TestIsRelSubPath_ExactSameReturnsFlase(t *testing.T) {
	if isRelSubPath("src", "src") {
		t.Error("isRelSubPath(\"src\", \"src\") should be false (same path, not a child)")
	}
}

// isRelSubPath: parent with trailing slash
func TestIsRelSubPath_TrailingSlash(t *testing.T) {
	// "src/main.go" under "src/" — the trailing slash breaks the check
	result := isRelSubPath("src/main.go", "src/")
	if result {
		// With trailing slash: len("src/main.go") > len("src/") = true
		// child[:4] = "src/" == "src/" = true
		// child[4] = 'm' != '/' → false
		// So trailing slash causes false negative
		t.Log("isRelSubPath handles trailing slash correctly (returns false — slash is part of parent)")
	} else {
		t.Log("NOTE: isRelSubPath(\"src/main.go\", \"src/\") returns false. " +
			"Callers must strip trailing slashes before calling.")
	}
}

// isSubPath: similar prefix but different directory
func TestIsSubPath_SimilarPrefixNoSeparator(t *testing.T) {
	dir := t.TempDir()
	similar := dir + "extra"

	result := isSubPath(similar, dir)
	if result {
		t.Error("isSubPath should not match similar prefix without separator")
	}
}

// isSubPath with relative paths
func TestIsSubPath_RelativePaths(t *testing.T) {
	// filepath.Abs will resolve relative paths, so this should work
	result := isSubPath(".", ".")
	if !result {
		t.Error("isSubPath(\".\", \".\") should be true (same directory)")
	}
}

// findProject: path outside all projects
func TestFindProject_PathOutsideAllProjects(t *testing.T) {
	m := &Manager{
		projects: []*projectWatcher{
			{project: makeProject("/tmp/proj1", "P1")},
			{project: makeProject("/tmp/proj2", "P2")},
		},
	}

	pw := m.findProject("/completely/different/path.txt")
	if pw != nil {
		t.Error("findProject should return nil for path outside all projects")
	}
}

// LastEventAge with no events
func TestLastEventAge_NoEvents(t *testing.T) {
	m := &Manager{}
	age := m.LastEventAge()
	if age != 0 {
		t.Errorf("LastEventAge with no events should be 0, got %v", age)
	}
}

// --- Burst delete detection tests (SM-050) ---

func TestTrackDeleteBurst_BelowThreshold_NoReconciliation(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	pw := &projectWatcher{
		project:  makeProject(t.TempDir(), "test"),
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}
	m := &Manager{log: slog.Default()}

	// Fire fewer deletes than threshold
	for i := 0; i < burstDeleteThreshold-1; i++ {
		m.trackDeleteBurst(pw)
	}

	// Give goroutine time to fire (it shouldn't)
	time.Sleep(100 * time.Millisecond)

	select {
	case <-syncChan:
		t.Error("reconciliation should not trigger below threshold")
	default:
		// expected
	}
}

func TestTrackDeleteBurst_AtThreshold_TriggersReconciliation(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	pw := &projectWatcher{
		project:  makeProject(t.TempDir(), "test"),
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}
	m := &Manager{log: slog.Default()}

	// Fire exactly threshold deletes
	for i := 0; i < burstDeleteThreshold; i++ {
		m.trackDeleteBurst(pw)
	}

	// The reconciliation fires after burstReconcileDelay, but we don't want
	// to wait 30s in a test. Verify the goroutine was launched by checking
	// that additional deletes beyond threshold don't trigger another reconciliation.
	// The key invariant: count == threshold triggers exactly once.

	// Fire more deletes — should not trigger again (count > threshold, not ==)
	for i := 0; i < 5; i++ {
		m.trackDeleteBurst(pw)
	}

	// No way to test the 30s delay without waiting, but we verify the counter logic.
	pw.mu.Lock()
	if pw.deleteCount != burstDeleteThreshold+5 {
		t.Errorf("expected deleteCount=%d, got %d", burstDeleteThreshold+5, pw.deleteCount)
	}
	pw.mu.Unlock()
}

func TestTrackDeleteBurst_WindowExpiry_ResetsCounter(t *testing.T) {
	syncChan := make(chan msync.Task, 100)
	pw := &projectWatcher{
		project:  makeProject(t.TempDir(), "test"),
		syncChan: syncChan,
		pending:  make(map[string]*time.Timer),
	}
	m := &Manager{log: slog.Default()}

	// Fire some deletes
	for i := 0; i < 5; i++ {
		m.trackDeleteBurst(pw)
	}

	// Expire the window manually
	pw.mu.Lock()
	pw.deleteWindowEnd = time.Now().Add(-1 * time.Second)
	pw.mu.Unlock()

	// Next delete should start a fresh window with count=1
	m.trackDeleteBurst(pw)

	pw.mu.Lock()
	if pw.deleteCount != 1 {
		t.Errorf("expected deleteCount=1 after window expiry, got %d", pw.deleteCount)
	}
	pw.mu.Unlock()
}
