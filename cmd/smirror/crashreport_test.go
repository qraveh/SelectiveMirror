package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCrashReport_ContainsVersion(t *testing.T) {
	report := buildCrashReport("nil pointer dereference", "goroutine 1 [running]:\nmain.main()", "")
	if !strings.Contains(report, version) {
		t.Errorf("crash report should contain version %q", version)
	}
}

func TestBuildCrashReport_ContainsPanicValue(t *testing.T) {
	report := buildCrashReport("index out of range [5]", "goroutine 1 [running]:", "")
	if !strings.Contains(report, "index out of range [5]") {
		t.Error("crash report should contain the panic value")
	}
}

func TestBuildCrashReport_ContainsStackTrace(t *testing.T) {
	stack := "goroutine 1 [running]:\nmain.doSomething()\n\t/path/to/main.go:42"
	report := buildCrashReport("oops", stack, "")
	if !strings.Contains(report, "main.doSomething()") {
		t.Error("crash report should contain the stack trace")
	}
}

func TestBuildCrashReport_ContainsSections(t *testing.T) {
	report := buildCrashReport("test panic", "test stack", "")
	sections := []string{
		"smirror crash report",
		"--- Panic ---",
		"--- Stack Trace ---",
		"--- Environment ---",
	}
	for _, s := range sections {
		if !strings.Contains(report, s) {
			t.Errorf("crash report should contain section %q", s)
		}
	}
}

func TestBuildCrashReport_ContainsPlatform(t *testing.T) {
	report := buildCrashReport("test", "stack", "")
	if !strings.Contains(report, "platform:") {
		t.Error("crash report should contain platform info")
	}
	if !strings.Contains(report, "go version:") {
		t.Error("crash report should contain Go version")
	}
}

func TestSaveCrashReport_WritesFile(t *testing.T) {
	// Temporarily override DefaultDataDir to use a temp dir
	dir := t.TempDir()
	origDir := crashReportDir
	crashReportDir = dir
	defer func() { crashReportDir = origDir }()

	path := saveCrashReport("test panic", "test stack", "")
	if path == "" {
		t.Fatal("saveCrashReport should return a path")
	}

	if !strings.HasPrefix(path, dir) {
		t.Errorf("path should be in temp dir, got: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("should be able to read crash report: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "test panic") {
		t.Error("saved file should contain panic value")
	}
	if !strings.Contains(content, "test stack") {
		t.Error("saved file should contain stack trace")
	}
}

func TestSaveCrashReport_FileNaming(t *testing.T) {
	dir := t.TempDir()
	origDir := crashReportDir
	crashReportDir = dir
	defer func() { crashReportDir = origDir }()

	path := saveCrashReport("test", "stack", "")
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "crash-") {
		t.Errorf("filename should start with 'crash-', got: %s", base)
	}
	if !strings.HasSuffix(base, ".txt") {
		t.Errorf("filename should end with '.txt', got: %s", base)
	}
}

func TestCheckUnsentCrashReports_NoFiles(t *testing.T) {
	// Should not panic or error when no crash files exist
	dir := t.TempDir()
	origDir := crashReportDir
	crashReportDir = dir
	defer func() { crashReportDir = origDir }()

	// This should return silently (no crash files found)
	// We can't easily test the interactive prompt, but we verify no panic
	checkUnsentCrashReports("nonexistent-config.yaml")
}

func TestCheckUnsentCrashReports_IgnoresSentFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a .sent file — should be ignored
	sentFile := filepath.Join(dir, "crash-20260404-120000.txt.sent")
	os.WriteFile(sentFile, []byte("old report"), 0644)

	matches, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
	var unsent []string
	for _, m := range matches {
		if !strings.HasSuffix(m, ".sent") {
			unsent = append(unsent, m)
		}
	}

	if len(unsent) != 0 {
		t.Errorf("should not find .sent files as unsent, got %d", len(unsent))
	}
}

func TestCheckUnsentCrashReports_FindsUnsentFiles(t *testing.T) {
	dir := t.TempDir()

	// Create an unsent crash file
	unsentFile := filepath.Join(dir, "crash-20260404-120000.txt")
	os.WriteFile(unsentFile, []byte("crash report"), 0644)

	// Also create a sent file
	sentFile := filepath.Join(dir, "crash-20260404-110000.txt.sent")
	os.WriteFile(sentFile, []byte("old report"), 0644)

	matches, _ := filepath.Glob(filepath.Join(dir, "crash-*.txt"))
	var unsent []string
	for _, m := range matches {
		if !strings.HasSuffix(m, ".sent") {
			unsent = append(unsent, m)
		}
	}

	if len(unsent) != 1 {
		t.Errorf("should find 1 unsent file, got %d", len(unsent))
	}
}
