//go:build windows

package fsutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// R-15: direct unit tests for internal/fsutil. Pre-v1.0.1 the package
// was tested only transitively (via internal/sync's WalkDir paths) which
// produced 0.0% direct line coverage and tripped the per-package floor
// in ci.yml. The waiver line in ci.yml was:
//
//   'internal/fsutil' = 'Trivial path helpers tested transitively via
//                       internal/sync; no direct caller path;
//                       v1.0.x no-op fix.'
//
// This file closes R-15 by exercising IsReparsePoint on five
// representative inputs. All Windows-only — the non-Windows stub
// (reparse_other.go) is trivial (always false) and tested in a sibling
// reparse_other_test.go.

// TestIsReparsePoint_RegularFile asserts that a normal NTFS file is NOT
// reported as a reparse point. The most basic negative case; would
// catch a regression where GetFileAttributes mask checks the wrong bit.
func TestIsReparsePoint_RegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "regular.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatalf("create test file: %v", err)
	}
	if IsReparsePoint(path) {
		t.Errorf("regular file %q reported as reparse point — false positive", path)
	}
}

// TestIsReparsePoint_Directory asserts that a normal directory is NOT
// reported as a reparse point. The directory case is a different code
// path because FILE_ATTRIBUTE_DIRECTORY is also set on directories;
// the IsReparsePoint check must specifically test the
// FILE_ATTRIBUTE_REPARSE_POINT bit and not accidentally trigger on
// FILE_ATTRIBUTE_DIRECTORY.
func TestIsReparsePoint_Directory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0700); err != nil {
		t.Fatalf("create test dir: %v", err)
	}
	if IsReparsePoint(subdir) {
		t.Errorf("regular directory %q reported as reparse point — false positive", subdir)
	}
}

// TestIsReparsePoint_Junction creates an NTFS directory junction via
// cmd.exe `mklink /J` (no admin required, unlike symlinks) and asserts
// the function returns true.
//
// Junctions are the primary reparse-point class the WalkDir guards in
// internal/sync need to detect — they can point back to ancestor
// directories and cause unbounded recursion if treated like real
// subdirectories.
func TestIsReparsePoint_Junction(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "junction")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatalf("create junction target: %v", err)
	}

	// `mklink /J <link> <target>` — directory junction, no admin needed.
	cmd := exec.Command("cmd", "/C", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not create junction via mklink /J (likely sandboxed env): %v\noutput: %s", err, out)
	}

	if !IsReparsePoint(link) {
		t.Errorf("junction %q reported as NOT a reparse point — false negative; "+
			"the WalkDir guards in internal/sync rely on detecting junctions to prevent unbounded recursion", link)
	}
}

// TestIsReparsePoint_NonexistentPath asserts the function returns false
// (not an error or panic) on a path that doesn't exist. The contract:
// "returns false on any error so a transient GetFileAttributes failure
// doesn't reject ordinary files." This locks that contract.
func TestIsReparsePoint_NonexistentPath(t *testing.T) {
	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "this-file-does-not-exist.txt")
	if IsReparsePoint(nonexistent) {
		t.Errorf("nonexistent path %q returned true — the contract says return false on error", nonexistent)
	}
}

// TestIsReparsePoint_NullByteInPath asserts the UTF16PtrFromString
// error branch returns false. Path containing a null byte is invalid
// for Windows API calls; the function MUST NOT panic or propagate the
// error — it must return false (the safe default).
func TestIsReparsePoint_NullByteInPath(t *testing.T) {
	// A path containing a null byte makes UTF16PtrFromString return an
	// error. The function should swallow that and return false.
	if IsReparsePoint("C:\x00\\invalid") {
		t.Errorf("path with null byte returned true — error-path contract violated")
	}
}
