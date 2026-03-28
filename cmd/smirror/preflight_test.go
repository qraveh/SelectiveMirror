package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/config"
)

func TestPreflightPath_ValidDirectory(t *testing.T) {
	dir := t.TempDir()
	proj := config.Project{Name: "test", LocalPath: dir, Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) > 0 {
		t.Errorf("expected no errors for valid dir, got: %v", errs)
	}
}

func TestPreflightPath_NonexistentPath(t *testing.T) {
	proj := config.Project{Name: "test", LocalPath: filepath.Join(t.TempDir(), "nope"), Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %s", errs[0])
	}
}

func TestPreflightPath_RegularFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	os.WriteFile(f, []byte("data"), 0644)

	proj := config.Project{Name: "test", LocalPath: f, Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "regular file") {
		t.Errorf("expected 'regular file' error, got: %s", errs[0])
	}
}

func TestPreflightPath_SymlinkToDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Symlinks on Windows require special privileges in many configurations
		t.Skip("symlink tests unreliable on Windows without developer mode")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.Mkdir(target, 0755)

	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	proj := config.Project{Name: "test", LocalPath: link, Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) > 0 {
		t.Errorf("symlink to dir should be a warning, not error, got: %v", errs)
	}
}

func TestPreflightPath_BrokenSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests unreliable on Windows without developer mode")
	}

	dir := t.TempDir()
	link := filepath.Join(dir, "broken")
	os.Symlink(filepath.Join(dir, "nonexistent"), link)

	proj := config.Project{Name: "test", LocalPath: link, Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for broken symlink, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "broken symlink") && !strings.Contains(errs[0], "non-existent target") {
		t.Errorf("expected broken symlink error, got: %s", errs[0])
	}
}

func TestPreflightPath_SymlinkToFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests unreliable on Windows without developer mode")
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "file.txt")
	os.WriteFile(target, []byte("data"), 0644)

	link := filepath.Join(dir, "link")
	os.Symlink(target, link)

	proj := config.Project{Name: "test", LocalPath: link, Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for symlink-to-file, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "regular file") {
		t.Errorf("expected 'regular file' error, got: %s", errs[0])
	}
}

func TestPreflightPath_ErrorMessageIncludesProjectName(t *testing.T) {
	proj := config.Project{Name: "my-project", LocalPath: filepath.Join(t.TempDir(), "gone"), Remote: "fake:bucket"}

	errs := preflightPath(proj)
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
	if !strings.Contains(errs[0], "my-project") {
		t.Errorf("error should mention project name, got: %s", errs[0])
	}
}

func TestPreflight_MultipleProjects_AllErrors(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Global{
		Projects: []config.Project{
			{Name: "good", LocalPath: dir, Remote: "fake:bucket"},
			{Name: "bad1", LocalPath: filepath.Join(dir, "nope1"), Remote: "fake:bucket"},
			{Name: "bad2", LocalPath: filepath.Join(dir, "nope2"), Remote: "fake:bucket"},
		},
		RclonePath: "rclone-that-does-not-exist-xyz",
	}

	errs := preflight(cfg)

	// Should have errors for bad1, bad2, and rclone
	if len(errs) < 3 {
		t.Errorf("expected at least 3 errors (2 paths + rclone), got %d: %v", len(errs), errs)
	}

	// Verify all project errors are present
	found := map[string]bool{"bad1": false, "bad2": false, "rclone": false}
	for _, e := range errs {
		if strings.Contains(e, "bad1") {
			found["bad1"] = true
		}
		if strings.Contains(e, "bad2") {
			found["bad2"] = true
		}
		if strings.Contains(e, "rclone") {
			found["rclone"] = true
		}
	}
	for key, ok := range found {
		if !ok {
			t.Errorf("missing error for %s", key)
		}
	}
}

func TestPreflight_AllGood_NoErrors(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.Global{
		Projects: []config.Project{
			{Name: "proj1", LocalPath: dir, Remote: "fake:bucket"},
		},
		// RclonePath left empty — will use system rclone. If rclone isn't installed,
		// the test will report that error but the path checks should still pass.
	}

	errs := preflight(cfg)

	// Filter out rclone errors (we can't guarantee rclone is installed in CI)
	var pathErrors []string
	for _, e := range errs {
		if !strings.Contains(e, "rclone") {
			pathErrors = append(pathErrors, e)
		}
	}

	if len(pathErrors) > 0 {
		t.Errorf("expected no path errors, got: %v", pathErrors)
	}
}
