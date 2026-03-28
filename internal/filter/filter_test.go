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

// --- SM-037: project negation must override global exclusion ---

func TestNegationOverridesGlobalExclude(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("!important.log\n"), 0644)

	fe, err := New([]string{"*.log"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Project !important.log should override global *.log
	if fe.IsExcluded("important.log") {
		t.Error("SM-037 CONFIRMED: important.log excluded despite project !negation pattern")
	}

	// Other .log files should still be excluded by global rule
	if !fe.IsExcluded("debug.log") {
		t.Error("debug.log should still be excluded by global *.log")
	}

	// Non-log files unaffected
	if fe.IsExcluded("readme.md") {
		t.Error("readme.md should not be excluded")
	}
}

func TestNegationWithinProjectOnly(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("*.bak\n!keep.bak\n"), 0644)

	fe, err := New(nil, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if fe.IsExcluded("keep.bak") {
		t.Error("keep.bak should not be excluded (project negation within same layer)")
	}
	if !fe.IsExcluded("other.bak") {
		t.Error("other.bak should be excluded by project *.bak")
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

	// SM-041 fix: use a start barrier so both goroutines begin simultaneously,
	// ensuring actual concurrent overlap between reads and writes.
	start := make(chan struct{})
	done := make(chan struct{})

	go func() {
		<-start // wait for barrier
		for i := 0; i < 100; i++ {
			content := "v" + string(rune('A'+i%26)) + "/\n"
			os.WriteFile(ignorePath, []byte(content), 0644)
			fe.Reload()
		}
		close(done)
	}()

	close(start) // release both goroutines simultaneously

	// Concurrent reads — run until writer is done to guarantee overlap
	for {
		fe.IsExcluded("some/path.go")
		fe.EffectiveRules()
		select {
		case <-done:
			return
		default:
		}
	}
}

// =============================================================================
// Bug-hunting tests: rclone filter vs IsExcluded divergence, edge patterns
// =============================================================================

// BUG HUNT: rclone filter file uses first-match-wins; IsExcluded uses last-match-wins.
// When a project .syncignore has exclusion BEFORE negation (e.g., "*.bak\n!keep.bak"),
// the rclone filter file produces:
//   - *.bak
//   + keep.bak
//   + **
// Rclone first-match: "keep.bak" matches "- *.bak" → EXCLUDED (wrong!)
// IsExcluded last-match: "!keep.bak" overrides "*.bak" → NOT excluded (correct)
func TestRcloneFilterVsIsExcluded_NegationAfterExclusion(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	// Exclusion first, then negation — the gitignore-standard pattern
	os.WriteFile(ignorePath, []byte("*.bak\n!keep.bak\n"), 0644)

	fe, err := New(nil, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// IsExcluded should say keep.bak is NOT excluded (last-match-wins)
	if fe.IsExcluded("keep.bak") {
		t.Error("IsExcluded: keep.bak should not be excluded (negation after exclusion)")
	}

	// Now check the rclone filter file — does it agree?
	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		t.Fatalf("GenerateRcloneFilterFile: %v", err)
	}
	defer os.Remove(filterFile)

	data, _ := os.ReadFile(filterFile)
	content := string(data)
	lines := strings.Split(strings.TrimSpace(content), "\n")

	// In rclone filter, first-match-wins. Find which rule matches "keep.bak" first.
	// If "- *.bak" appears before "+ keep.bak", rclone will EXCLUDE keep.bak
	// even though IsExcluded says it's included. This is a divergence bug.
	bakExcludeIdx := -1
	keepIncludeIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "- *.bak" {
			bakExcludeIdx = i
		}
		if strings.TrimSpace(line) == "+ keep.bak" {
			keepIncludeIdx = i
		}
	}

	if bakExcludeIdx >= 0 && keepIncludeIdx >= 0 && bakExcludeIdx < keepIncludeIdx {
		t.Errorf("DIVERGENCE: rclone filter has '- *.bak' (line %d) before '+ keep.bak' (line %d).\n"+
			"Rclone will EXCLUDE keep.bak (first-match-wins), but IsExcluded says INCLUDE.\n"+
			"Filter file:\n%s", bakExcludeIdx, keepIncludeIdx, content)
	}
}

// Same test but with global exclusion + project negation (the cross-layer case)
func TestRcloneFilterVsIsExcluded_CrossLayer(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("!important.log\n"), 0644)

	fe, err := New([]string{"*.log"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// IsExcluded says important.log is NOT excluded (project negation overrides global)
	if fe.IsExcluded("important.log") {
		t.Error("IsExcluded: important.log should not be excluded")
	}

	// Check rclone filter file agrees
	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		t.Fatalf("GenerateRcloneFilterFile: %v", err)
	}
	defer os.Remove(filterFile)

	data, _ := os.ReadFile(filterFile)
	content := string(data)
	lines := strings.Split(strings.TrimSpace(content), "\n")

	// In GenerateRcloneFilterFile: project rules come first, then global.
	// So "+ important.log" should come before "- *.log" — correct for rclone.
	includeIdx := -1
	excludeIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "+ important.log" {
			includeIdx = i
		}
		if strings.TrimSpace(line) == "- *.log" {
			excludeIdx = i
		}
	}

	if includeIdx < 0 {
		t.Errorf("missing '+ important.log' in filter file:\n%s", content)
	}
	if excludeIdx < 0 {
		t.Errorf("missing '- *.log' in filter file:\n%s", content)
	}
	if includeIdx >= 0 && excludeIdx >= 0 && includeIdx > excludeIdx {
		t.Errorf("DIVERGENCE: '+ important.log' comes after '- *.log' in rclone filter.\n"+
			"Rclone would exclude important.log. Filter file:\n%s", content)
	}
}

// Edge: directory pattern with negation
func TestRcloneFilter_DirectoryNegation(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("logs/\n!logs/important/\n"), 0644)

	fe, err := New(nil, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		t.Fatalf("GenerateRcloneFilterFile: %v", err)
	}
	defer os.Remove(filterFile)

	data, _ := os.ReadFile(filterFile)
	content := string(data)

	// Verify the negated directory appears as include
	if !strings.Contains(content, "+ logs/important/") {
		t.Errorf("expected negated dir '+ logs/important/' in filter, got:\n%s", content)
	}
}

// Edge: empty .syncignore (just comments and blank lines)
func TestEmptySyncIgnore(t *testing.T) {
	dir := t.TempDir()
	ignorePath := filepath.Join(dir, ".syncignore")
	os.WriteFile(ignorePath, []byte("# comment\n\n  # another\n\n"), 0644)

	fe, err := New([]string{"*.tmp"}, ignorePath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Global rules should still work
	if !fe.IsExcluded("test.tmp") {
		t.Error("*.tmp should be excluded by global rule")
	}
	if fe.IsExcluded("test.go") {
		t.Error("test.go should not be excluded")
	}
}

// Edge: extremely long pattern
func TestLongPattern(t *testing.T) {
	longPath := strings.Repeat("a/", 100) + "deep.txt"
	fe, err := New(nil, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Should not panic on very deep paths
	if fe.IsExcluded(longPath) {
		t.Error("deep path should not be excluded with no rules")
	}
}

// Edge: pattern that looks like a regex but is a glob
func TestGlobNotRegex(t *testing.T) {
	fe, err := New([]string{"*.log", "test[0-9].txt"}, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Gitignore supports [0-9] character classes
	if !fe.IsExcluded("test5.txt") {
		t.Error("test5.txt should be excluded by [0-9] pattern")
	}
	if fe.IsExcluded("testA.txt") {
		t.Error("testA.txt should not be excluded by [0-9] pattern")
	}
}

// Edge: backslash in paths (Windows)
func TestBackslashPaths(t *testing.T) {
	fe, err := New([]string{"build/"}, "")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Windows-style backslash paths should be normalized
	if !fe.IsExcluded("build\\output.exe") {
		t.Error("build\\output.exe should be excluded (backslash normalized to forward slash)")
	}
}

// Edge: toRcloneFilter with double negation
func TestToRcloneFilter_DoubleNegation(t *testing.T) {
	// !! is not standard gitignore but could appear
	result := toRcloneFilter("!!keep.log")
	// First ! is negation prefix, remaining is "!keep.log"
	if result != "+ !keep.log" {
		t.Errorf("double negation: expected '+ !keep.log', got %q", result)
	}
}

// Edge: whitespace-only pattern
func TestToRcloneFilter_WhitespaceOnly(t *testing.T) {
	result := toRcloneFilter("   ")
	if result != "-" && result != "- " {
		// After TrimSpace, pattern is "", so "- " + "" = "- "
		t.Logf("whitespace pattern produces: %q", result)
	}
}
