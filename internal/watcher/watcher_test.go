package watcher

import (
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
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

// --- helpers ---

func makeProject(localPath, name string) config.Project {
	return config.Project{
		Name:      name,
		LocalPath: localPath,
	}
}
