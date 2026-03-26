package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "myproject")
	os.MkdirAll(projDir, 0755)

	configPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(configPath, []byte(`
projects:
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
	if cfg.Projects[0].DebounceSec != 5 {
		t.Errorf("expected default debounce 5, got %d", cfg.Projects[0].DebounceSec)
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
projects:
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
projects:
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
projects:
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
	if p.DebounceDuration().Seconds() != 5 {
		t.Errorf("expected 5s default debounce, got %v", p.DebounceDuration())
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
	p := Project{LocalPath: `C:\MyProject`}
	expected := filepath.Join(`C:\MyProject`, ".syncignore")
	if p.SyncIgnoreFile() != expected {
		t.Errorf("expected %s, got %s", expected, p.SyncIgnoreFile())
	}

	p.SyncIgnorePath = `C:\custom\.syncignore`
	if p.SyncIgnoreFile() != `C:\custom\.syncignore` {
		t.Errorf("expected custom path, got %s", p.SyncIgnoreFile())
	}
}

func TestDeletePolicy(t *testing.T) {
	tests := []struct {
		input    string
		expected DeletePolicy
	}{
		{"", DeleteIgnore},
		{"ignore", DeleteIgnore},
		{"mirror", DeleteMirror},
		{"quarantine", DeleteQuarantine},
		{"invalid", DeleteIgnore},
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
