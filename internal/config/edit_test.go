package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// testConfigDir creates a temp dir with a valid minimal config file.
// Returns the config path and a cleanup function.
func testConfigDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Create the local_path directory referenced in the config
	localDir := filepath.Join(dir, "TestProject")
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Replace placeholder with real path
	content = strings.ReplaceAll(content, "LOCAL_PATH", localDir)
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

const baseConfig = `mirrors:
  - name: TestProject
    local_path: LOCAL_PATH
    remote: "gdrive:backup/TestProject"

global_excludes:
  - .git/
`

func TestSetField_ExistingKey(t *testing.T) {
	configPath := testConfigDir(t, baseConfig+"\nlog_level: info\n")

	if err := SetField(configPath, "log_level", "debug"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), "log_level: debug") {
		t.Errorf("expected log_level: debug, got:\n%s", data)
	}
}

// SetField must only match top-level keys. A previous bug used TrimSpace+
// HasPrefix, which matched indented sibling keys: setting a global
// `delete_policy` would silently overwrite the first per-mirror
// `delete_policy:` line found in a mirror entry.
func TestSetField_DoesNotMatchIndentedSibling(t *testing.T) {
	cfg := `mirrors:
  - name: TestProject
    local_path: LOCAL_PATH
    remote: "gdrive:backup/TestProject"
    delete_policy: ignore

global_excludes:
  - .git/
`
	configPath := testConfigDir(t, cfg)

	if err := SetField(configPath, "delete_policy", "quarantine"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	got := string(data)

	if !strings.Contains(got, "    delete_policy: ignore") {
		t.Errorf("indented per-mirror delete_policy was overwritten:\n%s", got)
	}
	// Top-level delete_policy: quarantine should appear (no leading whitespace
	// on its line).
	lines := strings.Split(got, "\n")
	foundTopLevel := false
	for _, line := range lines {
		if line == "delete_policy: quarantine" {
			foundTopLevel = true
			break
		}
	}
	if !foundTopLevel {
		t.Errorf("expected top-level `delete_policy: quarantine` line, got:\n%s", got)
	}
}

// SetField must skip lines beginning with `#`. Otherwise a commented-out
// example like `# default_remote: gdrive:foo` would be matched and rewritten.
func TestSetField_SkipsCommentLines(t *testing.T) {
	cfg := `# default_remote: gdrive:original-comment

mirrors:
  - name: TestProject
    local_path: LOCAL_PATH
    remote: "gdrive:backup/TestProject"
`
	configPath := testConfigDir(t, cfg)

	if err := SetField(configPath, "default_remote", `"gdrive:new"`); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	got := string(data)

	if !strings.Contains(got, "# default_remote: gdrive:original-comment") {
		t.Errorf("comment line was rewritten:\n%s", got)
	}
	if !strings.Contains(got, `default_remote: "gdrive:new"`) {
		t.Errorf("expected default_remote: \"gdrive:new\", got:\n%s", got)
	}
}

// SetField (and AddMirror, RemoveMirror) must not widen the file's mode on
// edit. Previous code rewrote with 0644 unconditionally, downgrading the
// initial 0600 from new-config creation.
func TestSetField_PreservesMode(t *testing.T) {
	if os.Getenv("CI") == "" && filepath.Separator == '\\' {
		// On Windows, file modes are largely simulated; the meaningful bit
		// is the read-only flag controlled by mode&0200. We still assert the
		// stored mode value to catch regressions in the helper, but tolerate
		// platform differences.
	}
	configPath := testConfigDir(t, baseConfig)
	// testConfigDir creates with 0644; force 0600 for this test.
	if err := os.Chmod(configPath, 0600); err != nil {
		t.Fatalf("chmod 0600: %v", err)
	}

	if err := SetField(configPath, "log_level", "debug"); err != nil {
		t.Fatal(err)
	}

	st, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Mode().Perm() & 0777; got != 0600 {
		// On Windows, os.Chmod sets the read-only bit only — Stat returns 0666
		// for writable files. Tolerate non-strict equality if not on Linux.
		if filepath.Separator != '\\' {
			t.Errorf("file mode = %#o after SetField, want 0600", got)
		}
	}
}

func TestSetField_NewKey(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	if err := SetField(configPath, "default_remote", `"gdrive:smirror"`); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), `default_remote: "gdrive:smirror"`) {
		t.Errorf("expected default_remote added, got:\n%s", data)
	}

	// Verify config still loads
	if _, err := Load(configPath); err != nil {
		t.Errorf("config no longer valid after SetField: %v", err)
	}
}

func TestAddMirror_ToExistingList(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	// Create the new project directory
	newDir := filepath.Join(filepath.Dir(configPath), "NewProject")
	os.MkdirAll(newDir, 0755)

	p := Project{
		Name:      "NewProject",
		LocalPath: newDir,
		Remote:    "gdrive:backup/NewProject",
	}
	if err := AddMirror(configPath, p); err != nil {
		t.Fatal(err)
	}

	// Verify config loads with 2 mirrors
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid after AddMirror: %v", err)
	}
	if len(cfg.Projects) != 2 {
		t.Errorf("expected 2 mirrors, got %d", len(cfg.Projects))
	}
	if cfg.Projects[1].Name != "NewProject" {
		t.Errorf("expected second mirror 'NewProject', got %q", cfg.Projects[1].Name)
	}
}

func TestAddMirror_DuplicateName(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	p := Project{
		Name:      "TestProject",
		LocalPath: filepath.Join(filepath.Dir(configPath), "TestProject"),
		Remote:    "gdrive:other",
	}
	err := AddMirror(configPath, p)
	if err == nil {
		t.Fatal("expected error for duplicate name, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestAddMirror_WithOptionalFields(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	newDir := filepath.Join(filepath.Dir(configPath), "FullProject")
	os.MkdirAll(newDir, 0755)

	p := Project{
		Name:            "FullProject",
		LocalPath:       newDir,
		Remote:          "s3:bucket/full",
		DebounceSec:     5,
		DeletePolicyStr: "quarantine",
		QuarantineDays:  7,
	}
	if err := AddMirror(configPath, p); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	fp := cfg.FindProject("FullProject")
	if fp == nil {
		t.Fatal("FullProject not found")
	}
	if fp.DebounceSec != 5 {
		t.Errorf("DebounceSec = %d, want 5", fp.DebounceSec)
	}
	if fp.DeletePolicyStr != "quarantine" {
		t.Errorf("DeletePolicyStr = %q, want quarantine", fp.DeletePolicyStr)
	}
}

func TestRemoveMirror_ByName(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	// Add a second mirror first
	newDir := filepath.Join(filepath.Dir(configPath), "Second")
	os.MkdirAll(newDir, 0755)
	AddMirror(configPath, Project{Name: "Second", LocalPath: newDir, Remote: "gdrive:backup/Second"})

	// Remove the first one
	if err := RemoveMirror(configPath, "TestProject"); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid after RemoveMirror: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 mirror, got %d", len(cfg.Projects))
	}
	if cfg.Projects[0].Name != "Second" {
		t.Errorf("expected remaining mirror 'Second', got %q", cfg.Projects[0].Name)
	}
}

func TestRemoveMirror_NotFound(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	err := RemoveMirror(configPath, "NonExistent")
	if err == nil {
		t.Fatal("expected error for missing mirror, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestAddRemoveRoundTrip(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)

	originalData, _ := os.ReadFile(configPath)

	// Add a mirror
	newDir := filepath.Join(filepath.Dir(configPath), "Temp")
	os.MkdirAll(newDir, 0755)
	AddMirror(configPath, Project{Name: "Temp", LocalPath: newDir, Remote: "gdrive:backup/Temp"})

	// Verify it's there
	cfg, _ := Load(configPath)
	if len(cfg.Projects) != 2 {
		t.Fatalf("expected 2 mirrors after add, got %d", len(cfg.Projects))
	}

	// Remove it
	RemoveMirror(configPath, "Temp")

	// Verify original mirror still works
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid after round-trip: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 mirror after round-trip, got %d", len(cfg.Projects))
	}

	// Content should be similar (not necessarily identical due to formatting)
	afterData, _ := os.ReadFile(configPath)
	if !strings.Contains(string(afterData), "TestProject") {
		t.Error("TestProject missing after round-trip")
	}
	_ = originalData // original content preserved structurally
}

func TestAddMirror_InlineEmptyList(t *testing.T) {
	// Reproduces the bug: "mirrors: []" caused AddMirror to create a duplicate key
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	localDir := filepath.Join(dir, "proj")
	os.MkdirAll(localDir, 0755)

	content := "default_remote: \"gdrive:test\"\n\nmirrors: []\n"
	os.WriteFile(configPath, []byte(content), 0644)

	p := Project{Name: "proj", LocalPath: localDir, Remote: "gdrive:test/proj"}
	if err := AddMirror(configPath, p); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid after AddMirror to mirrors:[]: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 mirror, got %d", len(cfg.Projects))
	}
}

func TestAddMirror_NoMirrorsSection(t *testing.T) {
	// Config has no mirrors section at all — AddMirror should create it
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	localDir := filepath.Join(dir, "proj")
	os.MkdirAll(localDir, 0755)

	content := "default_remote: \"gdrive:test\"\n"
	os.WriteFile(configPath, []byte(content), 0644)

	p := Project{Name: "proj", LocalPath: localDir, Remote: "gdrive:test/proj"}
	if err := AddMirror(configPath, p); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("config invalid after AddMirror to empty config: %v", err)
	}
	if len(cfg.Projects) != 1 {
		t.Errorf("expected 1 mirror, got %d", len(cfg.Projects))
	}
}

func TestUniqueMirrorName(t *testing.T) {
	existing := []string{"MyProject", "Other"}

	// No collision
	if got := UniqueMirrorName("Fresh", existing); got != "Fresh" {
		t.Errorf("got %q, want Fresh", got)
	}

	// Collision
	if got := UniqueMirrorName("MyProject", existing); got != "MyProject-2" {
		t.Errorf("got %q, want MyProject-2", got)
	}

	// Multiple collisions
	existing = append(existing, "MyProject-2")
	if got := UniqueMirrorName("MyProject", existing); got != "MyProject-3" {
		t.Errorf("got %q, want MyProject-3", got)
	}
}

func TestUniqueMirrorName_CaseInsensitive(t *testing.T) {
	existing := []string{"myproject"}
	if got := UniqueMirrorName("MyProject", existing); got != "MyProject-2" {
		t.Errorf("got %q, want MyProject-2 (case-insensitive collision)", got)
	}
}

// SEC-M6: a mirror name (or any other Project field) containing newline
// or other control characters must be rejected before AddMirror writes
// it into the YAML. Otherwise "Foo\npre_sync_hook: calc.exe" would
// inject a hook line that the next config load happily applies.
func TestAddMirror_RejectsControlCharsInName(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)
	dir := t.TempDir()
	cases := []struct {
		name string
		val  string
	}{
		{"newline in name", "Inject\npre_sync_hook: calc.exe"},
		{"carriage return", "Inject\rpre_sync_hook: calc.exe"},
		{"tab in name", "tab\there"},
		{"control char", "X\x01Y"},
		{"DEL char", "X\x7fY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AddMirror(configPath, Project{
				Name:      tc.val,
				LocalPath: dir,
				Remote:    "x:y",
			})
			if err == nil {
				t.Fatalf("AddMirror accepted control-char name %q", tc.val)
			}
			lowerErr := strings.ToLower(err.Error())
			if !strings.Contains(lowerErr, "control") && !strings.Contains(lowerErr, "del") {
				t.Errorf("error %v doesn't mention control char or DEL", err)
			}
		})
	}
}

// SM-205 CRITICAL: addmirror with YAML-special characters in
// local_path or name must round-trip correctly through the YAML
// emit + reparse cycle. Pre-fix used `%s` (unquoted) for both
// name and local_path; ` #x` mid-value got parsed as a YAML
// end-of-line comment, truncating the path. The fix uses `%q`
// (Go-double-quoted form, which YAML accepts as a double-quoted
// scalar with `\\` for backslash) for both fields.
//
// The reproducer specifically uses ` #` (space-hash) because YAML
// 1.2 §6.6 says "Outside scalars, characters following the # are
// considered to be a comment, which can also be used to introduce
// the meaning of an indicator". The space-then-hash is the
// minimal trigger.
func TestAddMirror_YAMLSpecialCharsInPath_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		mirrorName string
		mirrorPath string
	}{
		{"hash-comment in path", "foo bar #x", "C:\\Temp\\foo bar #x"},
		{"hash-comment in name", "name#suffix", "C:\\Temp\\plain"},
		{"colon-space in path", "colonpath", "C:\\Temp\\foo: bar"},
		{"leading dash in name", "-leading", "C:\\Temp\\plain"},
		{"yaml folded indicator", ">folded", "C:\\Temp\\plain"},
		{"yaml literal indicator", "|literal", "C:\\Temp\\plain"},
		{"yaml anchor", "&anchor", "C:\\Temp\\plain"},
		{"yaml alias", "*alias", "C:\\Temp\\plain"},
		{"yaml tag", "!tag", "C:\\Temp\\plain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := formatMirrorBlock(Project{
				Name:      tc.mirrorName,
				LocalPath: tc.mirrorPath,
				Remote:    "x:y",
			})
			// The block must round-trip through yaml unmarshal
			// without losing any character of the name/path.
			yamlText := "mirrors:\n" + block + "\n"
			var parsed struct {
				Mirrors []Project `yaml:"mirrors"`
			}
			if err := yaml.Unmarshal([]byte(yamlText), &parsed); err != nil {
				t.Fatalf("yaml unmarshal failed: %v\nblock:\n%s", err, block)
			}
			if len(parsed.Mirrors) != 1 {
				t.Fatalf("expected 1 mirror, got %d\nblock:\n%s", len(parsed.Mirrors), block)
			}
			got := parsed.Mirrors[0]
			if got.Name != tc.mirrorName {
				t.Errorf("name round-trip lost characters: got %q, want %q\nblock:\n%s", got.Name, tc.mirrorName, block)
			}
			if got.LocalPath != tc.mirrorPath {
				t.Errorf("local_path round-trip lost characters: got %q, want %q\nblock:\n%s", got.LocalPath, tc.mirrorPath, block)
			}
		})
	}
}

func TestSetField_RejectsControlCharsInValue(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)
	if err := SetField(configPath, "default_remote", "gdrive:foo\nrclone_path: calc.exe"); err == nil {
		t.Fatal("SetField accepted newline in value (SEC-M6)")
	}
}

// SEC-M6 atomic write: writePreservingMode uses temp+rename so a partial
// write doesn't truncate config.yaml. We can't easily simulate a
// crash mid-write in a unit test, but we can verify no .tmp file is
// left behind on success.
func TestSetField_NoTempFileLeftBehind(t *testing.T) {
	configPath := testConfigDir(t, baseConfig)
	if err := SetField(configPath, "log_level", "debug"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file was not cleaned up after successful write")
	}
}
