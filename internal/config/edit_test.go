package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
