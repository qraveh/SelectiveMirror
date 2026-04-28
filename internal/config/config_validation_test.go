package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes content to a temp config file and returns its path.
// Supports placeholders LDIR_0 through LDIR_4 — each is replaced with a real
// temp directory path. Use LDIR_0 as the default local directory.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Create numbered local directories and replace placeholders
	for i := 0; i < 5; i++ {
		placeholder := fmt.Sprintf("LDIR_%d", i)
		localDir := filepath.Join(dir, fmt.Sprintf("proj%d", i))
		os.MkdirAll(localDir, 0755)
		content = strings.ReplaceAll(content, placeholder, localDir)
	}

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// --- Malformed YAML ---

func TestLoad_EmptyFile(t *testing.T) {
	p := writeConfig(t, "")
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for empty config")
	}
	// Empty YAML may produce "EOF" or "no mirrors" — both are acceptable
	errStr := err.Error()
	if !strings.Contains(errStr, "no mirrors") && !strings.Contains(errStr, "EOF") {
		t.Errorf("expected 'no mirrors' or 'EOF' error, got: %v", err)
	}
}

func TestLoad_InvalidYAMLSyntax(t *testing.T) {
	p := writeConfig(t, "mirrors:\n  - name: test\n    local_path: 'unclosed\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestLoad_TabIndentation(t *testing.T) {
	p := writeConfig(t, "mirrors:\n\t- name: test\n\t  local_path: LDIR_0\n\t  remote: \"x:y\"\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for tab indentation in YAML")
	}
}

func TestLoad_WrongTypeForMirrors(t *testing.T) {
	p := writeConfig(t, "mirrors: not_a_list\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error when mirrors is not a list")
	}
}

// --- Missing required fields ---

func TestLoad_MirrorWithoutRemote(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: test
    local_path: LDIR_0
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for missing remote")
	}
	if !strings.Contains(err.Error(), "remote is required") {
		t.Errorf("expected 'remote is required', got: %v", err)
	}
}

func TestLoad_MirrorWithoutName(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - local_path: LDIR_0
    remote: "x:y"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected 'name is required', got: %v", err)
	}
}

func TestLoad_MirrorWithoutLocalPath(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: test
    remote: "x:y"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for missing local_path")
	}
	if !strings.Contains(err.Error(), "local_path is required") {
		t.Errorf("expected 'local_path is required', got: %v", err)
	}
}

// --- Bad values ---

func TestLoad_DuplicateNames(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: dup
    local_path: LDIR_0
    remote: "x:y"
  - name: dup
    local_path: LDIR_1
    remote: "x:z"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for duplicate names")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected 'duplicate' error, got: %v", err)
	}
}

// BUG-1 (panel review 2026-04-28): case-only collisions like "WorkProject"
// vs "workproject" must also be rejected. On case-insensitive Windows NTFS
// they resolve to the same on-disk path and the same state-DB key, so two
// watchers would race on the same files.
func TestLoad_CaseOnlyDuplicateNames(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: WorkProject
    local_path: LDIR_0
    remote: "x:y"
  - name: workproject
    local_path: LDIR_1
    remote: "x:z"
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for case-only duplicate names")
	}
	if !strings.Contains(err.Error(), "case-only difference") {
		t.Errorf("expected 'case-only difference' diagnostic, got: %v", err)
	}
}

func TestLoad_LocalPathDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := `mirrors:
  - name: test
    local_path: C:\nonexistent\path\that\does\not\exist
    remote: "x:y"
`
	os.WriteFile(configPath, []byte(content), 0644)
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error for nonexistent local_path")
	}
}

func TestLoad_LocalPathIsFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "afile.txt")
	os.WriteFile(filePath, []byte("content"), 0644)
	configPath := filepath.Join(dir, "config.yaml")
	content := `mirrors:
  - name: test
    local_path: ` + filePath + `
    remote: "x:y"
`
	os.WriteFile(configPath, []byte(content), 0644)
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected error when local_path is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' error, got: %v", err)
	}
}

func TestLoad_InvalidDeletePolicy(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: test
    local_path: LDIR_0
    remote: "x:y"
    delete_policy: yeet
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for invalid delete_policy")
	}
	if !strings.Contains(err.Error(), "invalid delete_policy") {
		t.Errorf("expected 'invalid delete_policy' error, got: %v", err)
	}
}

func TestLoad_ValidDeletePolicies(t *testing.T) {
	for _, policy := range []string{"ignore", "delete", "mirror", "quarantine"} {
		p := writeConfig(t, `mirrors:
  - name: test
    local_path: LDIR_0
    remote: "x:y"
    delete_policy: `+policy+`
`)
		_, err := Load(p)
		if err != nil {
			t.Errorf("delete_policy %q should be valid, got error: %v", policy, err)
		}
	}
}

// --- Path edge cases ---

func TestLoad_PathWithSpaces(t *testing.T) {
	dir := t.TempDir()
	spacedDir := filepath.Join(dir, "My Project")
	os.MkdirAll(spacedDir, 0755)
	configPath := filepath.Join(dir, "config.yaml")
	// Use forward slashes in YAML to avoid backslash escaping issues
	yamlPath := strings.ReplaceAll(spacedDir, `\`, `/`)
	content := "mirrors:\n  - name: spaced\n    local_path: " + yamlPath + "\n    remote: \"x:y\"\n"
	os.WriteFile(configPath, []byte(content), 0644)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("expected valid config with spaces in path, got: %v", err)
	}
	// filepath.Clean normalizes separators
	if filepath.Clean(cfg.Projects[0].LocalPath) != filepath.Clean(spacedDir) {
		t.Errorf("LocalPath = %q, want %q", cfg.Projects[0].LocalPath, spacedDir)
	}
}

func TestLoad_PathWithTrailingSlash(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: test
    local_path: LDIR_0/
    remote: "x:y"
`)
	// Should load fine — trailing slash is valid for a directory
	_, err := Load(p)
	if err != nil {
		t.Fatalf("expected valid config with trailing slash, got: %v", err)
	}
}

// --- DefaultRemote field ---

func TestLoad_DefaultRemote(t *testing.T) {
	p := writeConfig(t, `default_remote: "gdrive:smirror"
mirrors:
  - name: test
    local_path: LDIR_0
    remote: "x:y"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultRemote != "gdrive:smirror" {
		t.Errorf("DefaultRemote = %q, want gdrive:smirror", cfg.DefaultRemote)
	}
}

func TestLoad_DefaultRemoteEmpty(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: test
    local_path: LDIR_0
    remote: "x:y"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultRemote != "" {
		t.Errorf("DefaultRemote = %q, want empty", cfg.DefaultRemote)
	}
}

// --- Multiple mirrors ---

func TestLoad_MultipleMirrors(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: first
    local_path: LDIR_0
    remote: "gdrive:a"
  - name: second
    local_path: LDIR_1
    remote: "s3:b"
  - name: third
    local_path: LDIR_2
    remote: "dropbox:c"
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 3 {
		t.Errorf("expected 3 mirrors, got %d", len(cfg.Projects))
	}
}

// --- Config file not found ---

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
