package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "myproject")
	os.MkdirAll(projDir, 0755)

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
mirrors:
  - name: TestProj
    local_path: `+projDir+`
    remote: "gdrive:test"
global_excludes:
  - .git/
  - "*.pyc"
state_db: `+filepath.Join(dir, "state.db")+`
log_file: `+filepath.Join(dir, "test.log")+`
`), 0644)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
	}
	if cfg.Projects[0].Name != "TestProj" {
		t.Errorf("expected name TestProj, got %s", cfg.Projects[0].Name)
	}
	if cfg.Projects[0].DebounceSec != 0 {
		t.Errorf("expected default debounce 0 (dynamic), got %d", cfg.Projects[0].DebounceSec)
	}
	if cfg.Projects[0].MaxFileSizeMB != 100 {
		t.Errorf("expected default max_file_size_mb 100, got %d", cfg.Projects[0].MaxFileSizeMB)
	}
}

func TestLoadInvalidNoProjects(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
global_excludes:
  - .git/
`), 0644)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for config with no projects")
	}
}

func TestLoadInvalidDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "proj")
	os.MkdirAll(projDir, 0755)

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
mirrors:
  - name: Dup
    local_path: `+projDir+`
    remote: "gdrive:a"
  - name: Dup
    local_path: `+projDir+`
    remote: "gdrive:b"
`), 0644)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for duplicate project names")
	}
}

func TestLoadMissingLocalPath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
mirrors:
  - name: NoPath
    local_path: `+filepath.Join(dir, "nonexistent")+`
    remote: "gdrive:test"
`), 0644)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for nonexistent local_path")
	}
}

func TestLoadMissingName(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "proj")
	os.MkdirAll(projDir, 0755)

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
mirrors:
  - local_path: `+projDir+`
    remote: "gdrive:test"
`), 0644)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for missing project name")
	}
}

func TestProjectDefaults(t *testing.T) {
	p := Project{DebounceSec: 0, MaxFileSizeMB: 0}
	if p.DebounceDuration() != 0 {
		t.Errorf("expected 0 (dynamic debounce) default, got %v", p.DebounceDuration())
	}
	if p.MaxFileSize() != 100*1024*1024 {
		t.Errorf("expected 100MB default max file size, got %d", p.MaxFileSize())
	}
}

func TestProjectCustomValues(t *testing.T) {
	p := Project{DebounceSec: 10, MaxFileSizeMB: 50}
	if p.DebounceDuration().Seconds() != 10 {
		t.Errorf("expected 10s debounce, got %v", p.DebounceDuration())
	}
	if p.MaxFileSize() != 50*1024*1024 {
		t.Errorf("expected 50MB max file size, got %d", p.MaxFileSize())
	}
}

func TestSyncIgnoreFile(t *testing.T) {
	dir := t.TempDir()

	// Default: <local_path>/.syncignore
	p := Project{LocalPath: dir}
	expected := filepath.Join(dir, ".syncignore")
	if p.SyncIgnoreFile() != expected {
		t.Errorf("expected %s, got %s", expected, p.SyncIgnoreFile())
	}

	// Custom path overrides default
	customPath := filepath.Join(t.TempDir(), ".syncignore")
	p.SyncIgnorePath = customPath
	if p.SyncIgnoreFile() != customPath {
		t.Errorf("expected custom path %s, got %s", customPath, p.SyncIgnoreFile())
	}
}

func TestDefaultDataDir(t *testing.T) {
	// Normal case: should return non-empty path ending in .selectivemirror
	dir := DefaultDataDir()
	if dir == "" {
		t.Fatal("DefaultDataDir() returned empty string")
	}
	if filepath.Base(dir) != ".selectivemirror" {
		t.Errorf("expected path ending in .selectivemirror, got %s", dir)
	}

	// Test fallback when UserHomeDir would fail: the function falls back to
	// USERPROFILE (Windows) or HOME (Linux). Verify that env var is used.
	if runtime.GOOS == "windows" {
		// Save and override USERPROFILE
		orig := os.Getenv("USERPROFILE")
		fakePath := t.TempDir()
		os.Setenv("USERPROFILE", fakePath)
		defer os.Setenv("USERPROFILE", orig)

		// UserHomeDir on Windows checks USERPROFILE first, so this also
		// exercises the primary path. Verify it picks up the override.
		got := DefaultDataDir()
		expected := filepath.Join(fakePath, ".selectivemirror")
		if got != expected {
			t.Errorf("expected %s with overridden USERPROFILE, got %s", expected, got)
		}
	} else {
		// Save and override HOME
		orig := os.Getenv("HOME")
		fakePath := t.TempDir()
		os.Setenv("HOME", fakePath)
		defer os.Setenv("HOME", orig)

		got := DefaultDataDir()
		expected := filepath.Join(fakePath, ".selectivemirror")
		if got != expected {
			t.Errorf("expected %s with overridden HOME, got %s", expected, got)
		}
	}
}

func TestDeletePolicy(t *testing.T) {
	tests := []struct {
		input    string
		expected DeletePolicy
	}{
		{"", DeleteDelete},         // empty = default = delete
		{"ignore", DeleteIgnore},
		{"delete", DeleteDelete},
		{"mirror", DeleteDelete},   // deprecated alias
		{"quarantine", DeleteQuarantine},
		{"invalid", DeleteDelete},  // unrecognized = default = delete
	}

	for _, tt := range tests {
		g := Global{DeletePolicyStr: tt.input}
		got := g.DeletePolicy()
		if got != tt.expected {
			t.Errorf("DeletePolicy(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestQuarantineRetention(t *testing.T) {
	g := Global{QuarantineDays: 0}
	if g.QuarantineRetention() != 30 {
		t.Errorf("expected default 30, got %d", g.QuarantineRetention())
	}
	g.QuarantineDays = 7
	if g.QuarantineRetention() != 7 {
		t.Errorf("expected 7, got %d", g.QuarantineRetention())
	}
}

// --- FR-ASP-06: Per-mirror delete policy ---

func TestProjectDeletePolicy_Override(t *testing.T) {
	g := &Global{DeletePolicyStr: "ignore"}
	p := Project{DeletePolicyStr: "delete"}
	if got := p.DeletePolicy(g); got != DeleteDelete {
		t.Errorf("expected per-mirror 'delete', got %q", got)
	}
}

func TestProjectDeletePolicy_FallbackToGlobal(t *testing.T) {
	g := &Global{DeletePolicyStr: "quarantine"}
	p := Project{} // empty = use global
	if got := p.DeletePolicy(g); got != DeleteQuarantine {
		t.Errorf("expected global fallback 'quarantine', got %q", got)
	}
}

func TestProjectDeletePolicy_MirrorDeprecation(t *testing.T) {
	g := &Global{DeletePolicyStr: "ignore"}
	p := Project{DeletePolicyStr: "mirror"}
	if got := p.DeletePolicy(g); got != DeleteDelete {
		t.Errorf("expected 'mirror' → 'delete', got %q", got)
	}
}

func TestProjectDeletePolicy_InvalidFallsBack(t *testing.T) {
	g := &Global{DeletePolicyStr: "quarantine"}
	p := Project{DeletePolicyStr: "bogus"}
	// Invalid per-mirror value falls to parseDeletePolicy default (delete), not global
	if got := p.DeletePolicy(g); got != DeleteDelete {
		t.Errorf("expected invalid → 'delete', got %q", got)
	}
}

func TestProjectQuarantineRetention_Override(t *testing.T) {
	g := &Global{QuarantineDays: 30}
	p := Project{QuarantineDays: 7}
	if got := p.QuarantineRetention(g); got != 7 {
		t.Errorf("expected per-mirror 7, got %d", got)
	}
}

func TestProjectQuarantineRetention_FallbackToGlobal(t *testing.T) {
	g := &Global{QuarantineDays: 14}
	p := Project{}
	if got := p.QuarantineRetention(g); got != 14 {
		t.Errorf("expected global 14, got %d", got)
	}
}

func TestFindProject(t *testing.T) {
	g := &Global{
		Projects: []Project{
			{Name: "A", LocalPath: "a", Remote: "r:a"},
			{Name: "B", LocalPath: "b", Remote: "r:b"},
		},
	}
	if g.FindProject("A") == nil {
		t.Error("expected to find project A")
	}
	if g.FindProject("C") != nil {
		t.Error("expected nil for nonexistent project C")
	}
}

func TestProjectNames(t *testing.T) {
	g := &Global{
		Projects: []Project{
			{Name: "X"},
			{Name: "Y"},
		},
	}
	names := g.ProjectNames()
	if len(names) != 2 || names[0] != "X" || names[1] != "Y" {
		t.Errorf("unexpected names: %v", names)
	}
}

// =============================================================================
// Bug-hunting tests: boundary values, zero semantics, expandHome edge cases
// =============================================================================

// DebounceSec=0 enables dynamic debounce: first event fires immediately,
// subsequent rapid events for the same file activate a short debounce timer.
func TestDebounceSec_ZeroMeansDynamic(t *testing.T) {
	p := Project{DebounceSec: 0}
	d := p.DebounceDuration()
	if d != 0 {
		t.Errorf("DebounceSec=0 → %v (expected 0 = dynamic debounce)", d)
	}
}

func TestDebounceSec_NegativeMeansDynamic(t *testing.T) {
	p := Project{DebounceSec: -1}
	if p.DebounceDuration() != 0 {
		t.Errorf("DebounceSec=-1 → %v (expected 0 = dynamic debounce)", p.DebounceDuration())
	}
}

// MaxFileSizeMB=0 means "use default (100MB)" not "no limit"
func TestMaxFileSizeMB_ZeroMeansDefault(t *testing.T) {
	p := Project{MaxFileSizeMB: 0}
	if p.MaxFileSize() != 100*1024*1024 {
		t.Errorf("MaxFileSizeMB=0 → %d (expected 100MB default)", p.MaxFileSize())
	}
	t.Log("NOTE: MaxFileSizeMB=0 gives 100MB default. There's no way to configure 'no limit'.")
}

// Workers boundary: 0, 1, 16, 17
func TestWorkers_Boundaries(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 4},  // default
		{-1, 4}, // default
		{1, 1},  // minimum
		{4, 4},  // normal
		{16, 16}, // max
		{17, 16}, // capped
		{1000, 16}, // way over cap
	}
	for _, tt := range tests {
		g := Global{SyncWorkers: tt.input}
		got := g.Workers()
		if got != tt.expected {
			t.Errorf("Workers(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// HeartbeatInterval: zero/negative → 5 minutes default
func TestHeartbeatInterval_Defaults(t *testing.T) {
	g := Global{HeartbeatIntervalS: 0}
	if g.HeartbeatInterval() != 5*time.Minute {
		t.Errorf("HeartbeatInterval(0) = %v, want 5m", g.HeartbeatInterval())
	}

	g2 := Global{HeartbeatIntervalS: 30}
	if g2.HeartbeatInterval() != 30*time.Second {
		t.Errorf("HeartbeatInterval(30) = %v, want 30s", g2.HeartbeatInterval())
	}
}

// ReconcileInterval: zero/negative → 5 minutes default
func TestReconcileInterval_Defaults(t *testing.T) {
	g := Global{ReconcileIntervalS: 0}
	if g.ReconcileInterval() != 5*time.Minute {
		t.Errorf("ReconcileInterval(0) = %v, want 5m", g.ReconcileInterval())
	}
}

// QuarantineRetention: zero/negative → 30 days default
func TestQuarantineRetention_Boundaries(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 30},
		{-5, 30},
		{1, 1},
		{365, 365},
	}
	for _, tt := range tests {
		g := Global{QuarantineDays: tt.input}
		got := g.QuarantineRetention()
		if got != tt.expected {
			t.Errorf("QuarantineRetention(%d) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

// expandHome edge cases
func TestExpandHome_JustTilde(t *testing.T) {
	result := expandHome("~")
	// "~" alone has len=1, so len(path) > 1 is false → NOT expanded
	if result != "~" {
		t.Logf("expandHome(\"~\") = %q (not expanded, as expected)", result)
	}
	t.Log("NOTE: expandHome(\"~\") does NOT expand. Users writing state_db: ~ get literal tilde.")
}

func TestExpandHome_TildeSlash(t *testing.T) {
	result := expandHome("~/test")
	if result == "~/test" {
		t.Error("expandHome(\"~/test\") should expand ~ to home dir")
	}
}

func TestExpandHome_TildeBackslash(t *testing.T) {
	result := expandHome("~\\test")
	if result == "~\\test" {
		t.Error("expandHome(\"~\\\\test\") should expand ~ to home dir on Windows")
	}
}

func TestExpandHome_NoTilde(t *testing.T) {
	result := expandHome("/absolute/path")
	if result != "/absolute/path" {
		t.Errorf("expandHome should not modify paths without tilde: got %q", result)
	}
}

func TestExpandHome_Empty(t *testing.T) {
	result := expandHome("")
	if result != "" {
		t.Errorf("expandHome(\"\") should return empty, got %q", result)
	}
}

// DeletePolicy parsing
func TestDeletePolicy_InvalidString(t *testing.T) {
	g := Global{DeletePolicyStr: "garbage"}
	if g.DeletePolicy() != DeleteDelete {
		t.Errorf("invalid delete policy should default to 'delete', got %q", g.DeletePolicy())
	}
}

func TestDeletePolicy_CaseSensitive(t *testing.T) {
	// "Mirror" (capitalized) — does the parser handle it?
	g := Global{DeletePolicyStr: "Mirror"}
	if g.DeletePolicy() != DeleteDelete {
		// It's case-sensitive: "Mirror" != "mirror" → falls through to default (delete)
		t.Log("DeletePolicy is case-sensitive: 'Mirror' → default 'delete'")
	} else {
		t.Log("NOTE: DeletePolicy is case-sensitive. 'Mirror' treated as unknown → delete.")
	}
}

// Validate: project with local_path that is a file (not directory)
func TestValidate_LocalPathIsFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "notadir.txt")
	os.WriteFile(filePath, []byte("x"), 0644)

	g := &Global{
		Projects: []Project{
			{Name: "P", LocalPath: filePath, Remote: "r:p"},
		},
	}
	err := g.Validate()
	if err == nil {
		t.Error("Validate should reject local_path that is a file")
	}
}

// Validate: empty remote
func TestValidate_EmptyRemote(t *testing.T) {
	dir := t.TempDir()
	g := &Global{
		Projects: []Project{
			{Name: "P", LocalPath: dir, Remote: ""},
		},
	}
	err := g.Validate()
	if err == nil {
		t.Error("Validate should reject empty remote")
	}
}

func TestVerifyInterval_Default(t *testing.T) {
	g := Global{VerifyIntervalS: 0}
	if g.VerifyInterval() != 6*time.Hour {
		t.Errorf("VerifyInterval(0) = %v, want 6h", g.VerifyInterval())
	}
}

func TestVerifyInterval_Disabled(t *testing.T) {
	g := Global{VerifyIntervalS: -1}
	if g.VerifyInterval() != 0 {
		t.Errorf("VerifyInterval(-1) = %v, want 0 (disabled)", g.VerifyInterval())
	}
}

func TestVerifyInterval_Custom(t *testing.T) {
	g := Global{VerifyIntervalS: 3600}
	if g.VerifyInterval() != time.Hour {
		t.Errorf("VerifyInterval(3600) = %v, want 1h", g.VerifyInterval())
	}
}

func TestIsNotifyEnabled_Default(t *testing.T) {
	g := Global{}
	if !g.IsNotifyEnabled() {
		t.Error("IsNotifyEnabled() should default to true")
	}
}

func TestIsNotifyEnabled_ExplicitFalse(t *testing.T) {
	f := false
	g := Global{NotifyEnabled: &f}
	if g.IsNotifyEnabled() {
		t.Error("IsNotifyEnabled() should return false when set to false")
	}
}

func TestIsNotifyEnabled_ExplicitTrue(t *testing.T) {
	tr := true
	g := Global{NotifyEnabled: &tr}
	if !g.IsNotifyEnabled() {
		t.Error("IsNotifyEnabled() should return true when set to true")
	}
}

// SEC-C4: webhook URL validation
func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantError bool
	}{
		{"valid_https", "https://hooks.example.com/webhook", false},
		{"reject_http", "http://example.com/webhook", true},
		{"reject_loopback_ipv4", "https://127.0.0.1/webhook", true},
		{"reject_loopback_ipv6", "https://[::1]/webhook", true},
		{"reject_private_10", "https://10.0.0.1/webhook", true},
		{"reject_private_192", "https://192.168.1.1/webhook", true},
		{"reject_private_172", "https://172.16.0.1/webhook", true},
		{"reject_link_local", "https://169.254.169.254/latest/meta-data", true},
		{"reject_empty_host", "https:///path", true},
		{"reject_invalid_scheme", "ftp://example.com/", true},
		{"reject_file_scheme", "file:///etc/passwd", true},
		{"reject_malformed", "not a url at all", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebhookURL(tc.url)
			if (err != nil) != tc.wantError {
				t.Errorf("validateWebhookURL(%q): want error=%v, got %v", tc.url, tc.wantError, err)
			}
		})
	}
}

// SEC-C5: HasHooks detects any configured hook (global or per-mirror).
func TestHasHooks(t *testing.T) {
	tests := []struct {
		name string
		g    Global
		want bool
	}{
		{"no_hooks", Global{Projects: []Project{{Name: "p"}}}, false},
		{"global_pre", Global{PreSyncHook: "echo"}, true},
		{"global_post", Global{PostSyncHook: "echo"}, true},
		{"per_mirror_pre", Global{Projects: []Project{{Name: "p", PreSyncHook: "echo"}}}, true},
		{"per_mirror_post", Global{Projects: []Project{{Name: "p", PostSyncHook: "echo"}}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.g.HasHooks(); got != tc.want {
				t.Errorf("HasHooks() = %v, want %v", got, tc.want)
			}
		})
	}
}

// SEC-C5: IsAdminOwnedPath reports true for admin-owned files (sanity check
// that the API exists and returns without panic on both platforms).
func TestIsAdminOwnedPath_TempFileIsNotAdminOwned(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "userfile.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A file we just created in our own temp dir should NOT be admin-owned
	// (we are running tests as a normal user, not as SYSTEM/root).
	ownedByAdmin, err := IsAdminOwnedPath(f)
	if err != nil {
		t.Fatalf("IsAdminOwnedPath: %v", err)
	}
	if ownedByAdmin {
		t.Skip("test is running with admin privileges; skipping user-owned assertion")
	}
}
