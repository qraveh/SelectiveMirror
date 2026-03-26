package filter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalExcludes(t *testing.T) {
	fe, err := New([]string{".git/", "*.pyc", "*.log", "*.tmp", "__pycache__/"}, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	tests := []struct {
		path     string
		excluded bool
	}{
		{".git/config", true},
		{".git/HEAD", true},
		{"src/main.py", false},
		{"src/__pycache__/mod.pyc", true},
		{"test.pyc", true},
		{"output.log", true},
		{"temp.tmp", true},
		{"README.md", false},
		{"src/app.go", false},
	}

	for _, tt := range tests {
		got := fe.IsExcluded(tt.path)
		if got != tt.excluded {
			t.Errorf("IsExcluded(%q) = %v, want %v", tt.path, got, tt.excluded)
		}
	}
}

func TestProjectSyncIgnore(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("build/\nsecrets.json\n"), 0644)

	fe, err := New([]string{"*.tmp"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	tests := []struct {
		path     string
		excluded bool
	}{
		{"build/output.exe", true},
		{"secrets.json", true},
		{"temp.tmp", true},   // global
		{"src/main.go", false},
	}

	for _, tt := range tests {
		got := fe.IsExcluded(tt.path)
		if got != tt.excluded {
			t.Errorf("IsExcluded(%q) = %v, want %v", tt.path, got, tt.excluded)
		}
	}
}

func TestNoFilters(t *testing.T) {
	fe, err := New(nil, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if fe.IsExcluded("anything.go") {
		t.Error("expected nothing excluded with empty filters")
	}
}

func TestNonexistentSyncIgnore(t *testing.T) {
	fe, err := New([]string{"*.tmp"}, "/nonexistent/.syncignore")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	// Should still work with just global excludes
	if !fe.IsExcluded("test.tmp") {
		t.Error("expected .tmp excluded by global rule")
	}
	if fe.IsExcluded("test.go") {
		t.Error("expected .go not excluded")
	}
}

func TestEffectiveRules(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("local_only/\n"), 0644)

	fe, err := New([]string{"*.tmp", ".git/"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rules := fe.EffectiveRules()
	if len(rules) == 0 {
		t.Fatal("expected non-empty rules")
	}

	// Should contain headers and rules
	hasGlobal := false
	hasProject := false
	for _, r := range rules {
		if strings.Contains(r, "Global") {
			hasGlobal = true
		}
		if strings.Contains(r, "Project") {
			hasProject = true
		}
	}
	if !hasGlobal {
		t.Error("expected global rules header")
	}
	if !hasProject {
		t.Error("expected project rules header")
	}
}

func TestGenerateRcloneFilterFile(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("!important.log\nbuild/\n"), 0644)

	fe, err := New([]string{"*.log", ".git/"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		t.Fatalf("GenerateRcloneFilterFile failed: %v", err)
	}
	defer os.Remove(filterFile)

	data, err := os.ReadFile(filterFile)
	if err != nil {
		t.Fatalf("reading filter file: %v", err)
	}
	content := string(data)

	// Check negation pattern
	if !strings.Contains(content, "+ important.log") {
		t.Errorf("expected negation '+ important.log' in filter file, got:\n%s", content)
	}
	// Check directory exclusion
	if !strings.Contains(content, "- build/**") {
		t.Errorf("expected '- build/**' in filter file, got:\n%s", content)
	}
	// Check global exclusion
	if !strings.Contains(content, "- *.log") {
		t.Errorf("expected '- *.log' in filter file, got:\n%s", content)
	}
	// Check include-all
	if !strings.Contains(content, "+ **") {
		t.Errorf("expected '+ **' at end of filter file, got:\n%s", content)
	}
}

func TestUnicodePaths(t *testing.T) {
	fe, err := New([]string{"*.tmp"}, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Unicode filenames should work
	if fe.IsExcluded("テスト/file.go") {
		t.Error("Unicode path should not be excluded")
	}
	if !fe.IsExcluded("テスト/file.tmp") {
		t.Error("Unicode path with .tmp should be excluded")
	}
}

func TestReloadSyncIgnore(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")

	// Start with one rule
	os.WriteFile(ignorePath, []byte("build/\n"), 0644)
	fe, err := New([]string{"*.tmp"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// build/ excluded, dist/ not
	if !fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ excluded initially")
	}
	if fe.IsExcluded("dist/app.js") {
		t.Error("expected dist/ included initially")
	}

	// Update .syncignore to exclude dist/ instead
	os.WriteFile(ignorePath, []byte("dist/\n"), 0644)

	changed, err := fe.Reload()
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if !changed {
		t.Error("expected Reload to report changed=true")
	}

	// Now dist/ should be excluded and build/ should NOT be excluded
	if fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ included after reload")
	}
	if !fe.IsExcluded("dist/app.js") {
		t.Error("expected dist/ excluded after reload")
	}

	// Global rules still work
	if !fe.IsExcluded("test.tmp") {
		t.Error("expected *.tmp still excluded after reload")
	}
}

func TestReloadNoChange(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("build/\n"), 0644)

	fe, err := New([]string{}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Reload with same content
	changed, err := fe.Reload()
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if changed {
		t.Error("expected Reload to report changed=false when content unchanged")
	}
}

func TestReloadDeletedSyncIgnore(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("build/\n"), 0644)

	fe, err := New([]string{}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if !fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ excluded initially")
	}

	// Delete the file
	os.Remove(ignorePath)

	changed, err := fe.Reload()
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if !changed {
		t.Error("expected Reload to report changed=true when file deleted")
	}

	// build/ should no longer be excluded
	if fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ included after .syncignore deleted")
	}
}

func TestReloadNewSyncIgnore(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")

	// Start without .syncignore
	fe, err := New([]string{"*.tmp"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ included when no .syncignore")
	}

	// Create .syncignore
	os.WriteFile(ignorePath, []byte("build/\n"), 0644)

	changed, err := fe.Reload()
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if !changed {
		t.Error("expected Reload to report changed=true when file created")
	}

	if !fe.IsExcluded("build/output.exe") {
		t.Error("expected build/ excluded after .syncignore created")
	}
}

func TestSyncIgnorePath(t *testing.T) {
	fe, _ := New(nil, "/some/path/.syncignore")
	if fe.SyncIgnorePath() != "/some/path/.syncignore" {
		t.Errorf("expected SyncIgnorePath to return the path, got %q", fe.SyncIgnorePath())
	}

	fe2, _ := New(nil, "")
	if fe2.SyncIgnorePath() != "" {
		t.Errorf("expected empty SyncIgnorePath, got %q", fe2.SyncIgnorePath())
	}
}

func TestConcurrentReloadAndRead(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("v1/\n"), 0644)

	fe, err := New([]string{"*.tmp"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Hammer concurrent reads and reloads — should not panic or race
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			content := "v" + string(rune('A'+i%26)) + "/\n"
			os.WriteFile(ignorePath, []byte(content), 0644)
			fe.Reload()
		}
		close(done)
	}()

	// Concurrent reads
	for i := 0; i < 1000; i++ {
		fe.IsExcluded("some/path.go")
		fe.EffectiveRules()
	}

	<-done
}
