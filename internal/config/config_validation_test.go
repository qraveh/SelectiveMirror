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

// =========================================================================
// Panel-review 2026-04-28 — config validation hardening
// =========================================================================

// GAP-1: rclone_extra_flags denylist. Network-listener flags, log-file
// redirection, and config-swap flags must be rejected.
func TestValidate_RcloneExtraFlags_DenylistGlobal(t *testing.T) {
	cases := []struct {
		name  string
		flags []string
		want  string
	}{
		{"--rc switch", []string{"--rc"}, "--rc"},
		{"--rc-addr", []string{"--rc-addr", "127.0.0.1:5572"}, "--rc-addr"},
		{"--rc-no-auth", []string{"--rc-no-auth"}, "--rc-no-auth"},
		{"--rcfile", []string{"--rcfile", "x.json"}, "--rcfile"},
		{"--log-file separate", []string{"--log-file", "x.log"}, "--log-file"},
		{"--log-file equals", []string{"--log-file=x.log"}, "--log-file"},
		{"--config", []string{"--config", "x.conf"}, "--config"},
		{"--password-command", []string{"--password-command", "echo hi"}, "--password-command"},
		{"--ask-password", []string{"--ask-password"}, "--ask-password"},
		{"--log-format", []string{"--log-format", "json"}, "--log-format"},
		// SM-183: symlink-following flags must be denied. Both long
		// and short forms; service-mode RejectSymlinkedFiles is
		// undermined if rclone follows symlinks via these flags.
		{"--copy-links", []string{"--copy-links"}, "--copy-links"},
		{"--copy-links separate", []string{"--copy-links", "true"}, "--copy-links"},
		{"--links", []string{"--links"}, "--links"},
		{"-L short", []string{"-L"}, "-L"},
		{"-L=value", []string{"-L=true"}, "-L"},
		{"-l short", []string{"-l"}, "-l"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRcloneExtraFlags("global", tc.flags)
			if err == nil {
				t.Fatalf("expected denylist rejection for %v", tc.flags)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v does not name the rejected flag %q", err, tc.want)
			}
		})
	}
}

func TestValidate_RcloneExtraFlags_AllowsBenign(t *testing.T) {
	for _, flag := range [][]string{
		{"--bwlimit", "10M"},
		{"--transfers", "4"},
		{"--checkers", "8"},
		{"--max-age", "30d"},
		{"--user-agent", "smirror/0.9"},
		{},
		nil,
	} {
		if err := validateRcloneExtraFlags("global", flag); err != nil {
			t.Errorf("benign flag set %v rejected: %v", flag, err)
		}
	}
}

// GAP-1: per-mirror flag list is also validated.
func TestLoad_RcloneExtraFlags_PerMirrorRejected(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: a
    local_path: LDIR_0
    remote: "x:y"
    rclone_extra_flags:
      - --rc
      - --rc-no-auth
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected per-mirror rclone_extra_flags rejection")
	}
	if !strings.Contains(err.Error(), "--rc") {
		t.Errorf("error did not mention the rejected flag: %v", err)
	}
}

// GAP-2: rclone_config must point to a regular file. Bogus paths are
// rejected at config load (not deferred to first sync).
func TestLoad_RcloneConfig_MissingPathRejected(t *testing.T) {
	p := writeConfig(t, `mirrors:
  - name: a
    local_path: LDIR_0
    remote: "x:y"
rclone_config: /does/not/exist/rclone.conf
`)
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected rclone_config missing-path rejection")
	}
	if !strings.Contains(err.Error(), "rclone_config") {
		t.Errorf("error did not mention rclone_config: %v", err)
	}
}

func TestLoad_RcloneConfig_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, fmt.Sprintf(`mirrors:
  - name: a
    local_path: LDIR_0
    remote: "x:y"
rclone_config: %q
`, dir))
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected directory rclone_config rejection")
	}
	if !strings.Contains(err.Error(), "regular file") {
		t.Errorf("error did not mention regular file: %v", err)
	}
}

// GAP-3: overlapping local_paths (parent / child) rejected.
func TestLoad_OverlappingLocalPaths_Rejected(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	p := writeConfig(t, fmt.Sprintf(`mirrors:
  - name: outer
    local_path: %q
    remote: "x:y"
  - name: inner
    local_path: %q
    remote: "x:z"
`, parent, child))
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected overlap rejection")
	}
	if !strings.Contains(err.Error(), "parent") && !strings.Contains(err.Error(), "overlap") {
		t.Errorf("error did not mention overlap: %v", err)
	}
}

// Two mirrors with the same path resolved differently still gets caught.
func TestLoad_SameLocalPath_DifferentNames_Rejected(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, fmt.Sprintf(`mirrors:
  - name: alpha
    local_path: %q
    remote: "x:y"
  - name: beta
    local_path: %q
    remote: "x:z"
`, dir, dir))
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected same-path rejection")
	}
}

// GAP-4: drive-root local_path rejected.
func TestValidate_LocalPath_DriveRootRejected(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("drive-root semantics are Windows-specific")
	}
	if reason := isUnsafeLocalPath(`C:\`); reason == "" {
		t.Error("expected `C:\\` to be rejected as drive root")
	}
	if reason := isUnsafeLocalPath(`D:\`); reason == "" {
		t.Error("expected `D:\\` to be rejected as drive root")
	}
}

func TestValidate_LocalPath_SystemDirsRejected(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("system-dir env vars are Windows-specific")
	}
	if sr := os.Getenv("SystemRoot"); sr != "" {
		if reason := isUnsafeLocalPath(sr); reason == "" {
			t.Errorf("expected %%SystemRoot%% (%s) to be rejected", sr)
		}
	}
}

func TestValidate_LocalPath_NormalDirAllowed(t *testing.T) {
	dir := t.TempDir()
	if reason := isUnsafeLocalPath(dir); reason != "" {
		t.Errorf("normal temp dir rejected unexpectedly: %s", reason)
	}
}

// SM-206: subdirectories of system dirs must be rejected, not just
// the exact path. Pre-fix, `C:\Windows\Logs` (a real Windows path
// with 600+ files) sailed through the exact-match check.
func TestValidate_LocalPath_SystemDirSubdirRejected(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("system-dir env vars are Windows-specific")
	}
	sr := os.Getenv("SystemRoot")
	if sr == "" {
		t.Skip("SystemRoot env var unset")
	}
	candidates := []string{
		filepath.Join(sr, "Logs"),
		filepath.Join(sr, "System32"),
		filepath.Join(sr, "Temp"),
	}
	for _, p := range candidates {
		if reason := isUnsafeLocalPath(p); reason == "" {
			t.Errorf("expected subdirectory of %%SystemRoot%% (%s) to be rejected", p)
		}
	}
}

// SM-207: \\?\ extended-length prefix must be stripped before the
// system-dir check. Pre-fix, `\\?\C:\Windows\Logs` sailed through
// because the literal-string compare missed the prefix.
func TestValidate_LocalPath_ExtendedLengthPrefixStripped(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("Windows-specific extended-length form")
	}
	sr := os.Getenv("SystemRoot")
	if sr == "" {
		t.Skip("SystemRoot env var unset")
	}
	cases := []string{
		`\\?\` + sr,
		`\\?\` + filepath.Join(sr, "Logs"),
		`\\?\` + filepath.Join(sr, "System32"),
	}
	for _, p := range cases {
		if reason := isUnsafeLocalPath(p); reason == "" {
			t.Errorf("expected extended-length form of system path (%s) to be rejected", p)
		}
	}
}

// SM-208: UNC paths (\\server\share\...) must be rejected outright.
// Pre-fix, `\\COMPUTERNAME\C$\Windows\Logs` sailed through because
// the env-var check uses drive-letter form (no UNC equivalent).
func TestValidate_LocalPath_UNCRejected(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("UNC form is Windows-specific")
	}
	cases := []string{
		`\\COMPUTERNAME\C$\Users\Public\Desktop`,
		`\\server\share\path`,
		`\\server\share`,
	}
	for _, p := range cases {
		reason := isUnsafeLocalPath(p)
		if reason == "" {
			t.Errorf("expected UNC path %q to be rejected", p)
		}
		if reason != "" && !strings.Contains(reason, "UNC") {
			t.Errorf("UNC rejection reason for %q should mention UNC, got %q", p, reason)
		}
	}
}

// GAP-5: traversal-shaped remote rejected.
func TestValidate_Remote_TraversalRejected(t *testing.T) {
	cases := []string{
		"local:../../etc",
		"gdrive:foo/../bar",
		`local:..\\..\\windows`,
	}
	for _, r := range cases {
		t.Run(r, func(t *testing.T) {
			if reason := isUnsafeRemote(r); reason == "" {
				t.Errorf("expected traversal rejection for %q", r)
			}
		})
	}
}

func TestValidate_Remote_NormalAllowed(t *testing.T) {
	cases := []string{
		"gdrive:smirror/foo",
		"s3:bucket/path",
		"local:foo/bar",
		"plain-no-colon-path", // unprefixed (treated as local-fs path)
		"",
	}
	for _, r := range cases {
		if reason := isUnsafeRemote(r); reason != "" {
			t.Errorf("normal remote %q rejected: %s", r, reason)
		}
	}
}

// =========================================================================
// PR-S6 (panel review pre-release 2026-04-28) — Unicode-confusable bypass
// of GAP-1 denylist + alert_min_severity enum validation
// =========================================================================

// PR-S6: a flag whose name carries any non-ASCII glyph is rejected
// before denylist matching. This blocks `--rс` (Cyrillic 'с' U+0441) and
// `--rс-addr`-style lookalikes that would otherwise slip past the
// `--rc` ASCII prefix check.
func TestValidate_RcloneExtraFlags_NonASCIIRejected(t *testing.T) {
	cases := []struct {
		name  string
		flags []string
	}{
		{"cyrillic c in --rc", []string{"--rс"}},                  // --rс
		{"cyrillic c in --rc-addr", []string{"--rс-addr", "x"}},   // --rс-addr
		{"cyrillic a in --bwlimit", []string{"--bwlimitа"}},       // --bwlimitа
		{"greek omicron in --config", []string{"--cοnfig", "x"}},  // --cοnfig
		// NB: an entry that does NOT start with ASCII '--' is not a flag from
		// our parser's standpoint (e.g. "－－rc" with fullwidth hyphens) — it
		// will be passed verbatim to rclone, which itself rejects it because
		// rclone's flag namespace requires ASCII '-'. We don't need to
		// reject it here; covered by rclone's own argument parsing.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// First flag is the one with non-ASCII name; the rest can be values.
			err := validateRcloneExtraFlags("global", tc.flags[:1])
			if err == nil {
				t.Fatalf("expected non-ASCII rejection for %q", tc.flags[0])
			}
			if !strings.Contains(err.Error(), "non-ASCII") {
				t.Errorf("error should explain non-ASCII rejection: %v", err)
			}
		})
	}
}

// PR-S6: alert_min_severity must be one of the canonical severity strings.
// A typo like `erro` previously passed Validate() and silently demoted
// filtering (severityAtLeast returns 0 for unknown thresholds).
func TestValidate_AlertMinSeverity_RejectsTypo(t *testing.T) {
	cases := []string{
		"erro",            // missing 'r'
		"warn",            // shortened
		"WARNING",         // wrong case
		"high",            // wrong vocabulary
		" error ",         // whitespace
		"error,critical",  // list (not a single severity)
	}
	for _, sev := range cases {
		t.Run(sev, func(t *testing.T) {
			cfg := &Global{
				Projects:         []Project{{Name: "a", LocalPath: t.TempDir(), Remote: "local:x"}},
				AlertMinSeverity: sev,
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected rejection of alert_min_severity %q", sev)
			}
			if !strings.Contains(err.Error(), "alert_min_severity") {
				t.Errorf("error did not name alert_min_severity: %v", err)
			}
		})
	}
}

func TestValidate_AlertMinSeverity_AcceptsValid(t *testing.T) {
	for _, sev := range []string{"", "info", "warning", "error", "critical"} {
		t.Run(sev, func(t *testing.T) {
			cfg := &Global{
				Projects:         []Project{{Name: "a", LocalPath: t.TempDir(), Remote: "local:x"}},
				AlertMinSeverity: sev,
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("valid severity %q rejected: %v", sev, err)
			}
		})
	}
}
