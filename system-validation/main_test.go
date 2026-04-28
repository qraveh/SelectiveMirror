// Package systemval is a stand-alone black-box system validation suite for
// SelectiveMirror.  It builds smirror.exe from source, invokes it via
// os/exec, and verifies behaviour through exit codes, stdout/stderr, and
// filesystem side-effects.  Zero code is imported from SelectiveMirror.
package systemval

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// globals set by TestMain
var (
	smirrorBin string // absolute path to built smirror.exe
	rcloneBin  string // absolute path to rclone (empty if not present)
	repoRoot   string // C:\SelectiveMirror
)

// requireRclone skips the test if rclone wasn't detected on PATH at
// startup. Call this from any test that performs an actual rclone
// invocation (mirror / backend / scenario tests). Static source /
// documentation / RLS / SQL tests should NOT call this — they run
// without rclone. SM-177.
func requireRclone(t *testing.T) {
	t.Helper()
	if rcloneBin == "" {
		t.Skip("rclone not available on PATH; skipping rclone-backed test")
	}
}

func TestMain(m *testing.M) {
	// 1. Locate repo root (parent of system-validation/).
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: cannot get working directory: %v\n", err)
		os.Exit(1)
	}
	repoRoot = filepath.Dir(wd)

	// Sanity: go.mod must exist in the repo root.
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: repo root %s has no go.mod\n", repoRoot)
		os.Exit(1)
	}

	// 2. Build smirror.exe into a temp directory.
	tmpBuild, err := os.MkdirTemp("", "systemval-build-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: cannot create temp build dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpBuild)

	binName := "smirror"
	if runtime.GOOS == "windows" {
		binName = "smirror.exe"
	}
	smirrorBin = filepath.Join(tmpBuild, binName)

	fmt.Printf("Building %s from %s ...\n", binName, repoRoot)
	build := exec.Command("go", "build", "-o", smirrorBin, "./cmd/smirror/")
	build.Dir = repoRoot
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: go build failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Built: %s\n", smirrorBin)

	// 3. SM-177: rclone detection is now LAZY. Only tests that
	//    actually invoke rclone-backed behavior should call
	//    requireRclone(t); static tests for sources / docs / RLS /
	//    SQL contracts can run without rclone on PATH. This means a
	//    developer or CI environment with Go but no rclone can still
	//    iterate on the privacy/security side of the suite.
	if bin, err := exec.LookPath("rclone"); err == nil {
		rcloneBin = bin
		out, verr := exec.Command(rcloneBin, "version").CombinedOutput()
		if verr == nil {
			firstLine := strings.SplitN(string(out), "\n", 2)[0]
			fmt.Printf("rclone: %s (%s)\n", firstLine, rcloneBin)
		} else {
			fmt.Printf("rclone: present at %s but `rclone version` failed (%v) — backend tests will skip\n",
				rcloneBin, verr)
			rcloneBin = ""
		}
	} else {
		fmt.Println("rclone: not found on PATH — backend / mirror integration tests will skip; static tests run.")
	}

	// 4. Run tests.
	code := m.Run()

	// 5. Print coverage report after all tests complete.
	runPattern := flag.Lookup("test.run").Value.String()
	focusedRun := runPattern != ""
	report, allMet := coverage.Report(focusedRun)
	fmt.Println(report)
	if focusedRun {
		fmt.Printf("Focused run (-run %q): full-suite coverage gate skipped.\n", runPattern)
	}
	if !focusedRun && !allMet && code == 0 {
		code = 1
	}
	os.Exit(code)
}
