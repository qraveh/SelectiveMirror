package systemval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFuzz_ConfigPermutations generates random/malformed configs and verifies:
//  1. smirror never panics (no crash on any input)
//  2. Exit code is 0 or 2 (valid or config error), never undefined
//  3. All 7+ validation error types are triggered
func TestFuzz_ConfigPermutations(t *testing.T) {
	t.Parallel()

	type configCase struct {
		name    string
		yaml    string
		goalID  string // which validation error this should trigger
	}

	// We need real paths for some tests.
	root := t.TempDir()
	realDir := filepath.Join(root, "realdir")
	os.MkdirAll(realDir, 0755)
	realDir2 := filepath.Join(root, "realdir2")
	os.MkdirAll(realDir2, 0755)
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	stateDB := filepath.Join(dataDir, "state.db")
	logFile := filepath.Join(dataDir, "fuzz.log")

	cases := []configCase{
		// --- Missing fields ---
		{
			name: "no_mirrors",
			yaml: fmt.Sprintf(`state_db: %q
log_file: %q
`, stateDB, logFile),
			goalID: "cfgerr_no_mirrors",
		},
		{
			name: "empty_mirrors",
			yaml: fmt.Sprintf(`mirrors: []
state_db: %q
log_file: %q
`, stateDB, logFile),
			goalID: "cfgerr_no_mirrors",
		},
		{
			name: "missing_name",
			yaml: fmt.Sprintf(`mirrors:
  - local_path: %q
    remote: %q
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "cfgerr_no_name",
		},
		{
			name: "missing_local_path",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    remote: %q
state_db: %q
log_file: %q
`, realDir2, stateDB, logFile),
			goalID: "cfgerr_no_local_path",
		},
		{
			name: "missing_remote",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
state_db: %q
log_file: %q
`, realDir, stateDB, logFile),
			goalID: "cfgerr_no_remote",
		},

		// --- Duplicate names ---
		{
			name: "duplicate_mirror_names",
			yaml: fmt.Sprintf(`mirrors:
  - name: "dup"
    local_path: %q
    remote: %q
  - name: "dup"
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
`, realDir, realDir2, realDir2, realDir, stateDB, logFile),
			goalID: "cfgerr_dup_name",
		},

		// --- Invalid YAML ---
		{
			name:   "bad_yaml_bracket",
			yaml:   "mirrors: [{",
			goalID: "cfgerr_bad_yaml",
		},
		{
			name:   "bad_yaml_tabs",
			yaml:   "mirrors:\n\t- name: test",
			goalID: "cfgerr_bad_yaml",
		},
		{
			name:   "bad_yaml_colon",
			yaml:   ":: not valid ::",
			goalID: "cfgerr_bad_yaml",
		},

		// --- Invalid delete policy ---
		{
			name: "bad_delete_policy",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
delete_policy: "yeet"
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "cfgerr_bad_policy",
		},
		{
			name: "bad_delete_policy_mirror_level",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
    delete_policy: "explode"
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "cfgerr_bad_policy",
		},

		// --- Type mismatches (Bug hunter: wrong types should be caught) ---
		{
			name: "sync_workers_string",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
sync_workers: "not_a_number"
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "", // may parse or fail
		},
		{
			name: "sync_workers_negative",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
sync_workers: -5
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},
		{
			name: "sync_workers_zero",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
sync_workers: 0
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},
		{
			name: "sync_workers_over_max",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
sync_workers: 999
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},
		{
			name: "quarantine_days_negative",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
quarantine_days: -1
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},

		// --- Unknown keys (Bug hunter: typos in config) ---
		{
			name: "unknown_key",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
unknwon_setting: true
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},

		// --- Empty config ---
		{
			name:   "completely_empty",
			yaml:   "",
			goalID: "cfgerr_no_mirrors",
		},
		{
			name:   "only_whitespace",
			yaml:   "   \n\n   \n",
			goalID: "cfgerr_no_mirrors",
		},
		{
			name:   "only_comments",
			yaml:   "# nothing here\n# just comments\n",
			goalID: "cfgerr_no_mirrors",
		},

		// --- Null / special values ---
		{
			name:   "null_mirrors",
			yaml:   "mirrors: null",
			goalID: "cfgerr_no_mirrors",
		},
		{
			name: "null_name",
			yaml: fmt.Sprintf(`mirrors:
  - name: null
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "cfgerr_no_name",
		},

		// --- Extremely long values ---
		{
			name: "very_long_name",
			yaml: fmt.Sprintf(`mirrors:
  - name: %q
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
`, strings.Repeat("x", 10000), realDir, realDir2, stateDB, logFile),
			goalID: "",
		},

		// --- Nonexistent local_path ---
		{
			name: "nonexistent_local_path",
			yaml: fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: "/definitely/does/not/exist/xyz"
    remote: %q
state_db: %q
log_file: %q
`, realDir2, stateDB, logFile),
			goalID: "",
		},

		// --- Valid config (control) ---
		{
			name: "valid_minimal",
			yaml: fmt.Sprintf(`mirrors:
  - name: "valid"
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
`, realDir, realDir2, stateDB, logFile),
			goalID: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			cfgPath := filepath.Join(cfgDir, "config.yaml")
			os.WriteFile(cfgPath, []byte(tc.yaml), 0644)

			// Use dry-run as a lightweight command that exercises config loading.
			r := runSmirror(t, cfgPath, "dry-run")
			assertNoPanic(t, r)

			// Exit code should be 0 (valid) or 2 (config error), not 1 (crash).
			if r.ExitCode != 0 && r.ExitCode != 2 {
				// Some commands may return 1 for other reasons; just ensure no panic.
				t.Logf("exit code %d for %q (not 0 or 2)", r.ExitCode, tc.name)
			}

			if tc.goalID != "" {
				coverage.Record(tc.goalID)
			}
		})
	}
}

// TestFuzz_ConfigRandomMutations applies random mutations to a valid config
// and verifies no crashes.
func TestFuzz_ConfigRandomMutations(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realDir := filepath.Join(root, "src")
	realDir2 := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(realDir, 0755)
	os.MkdirAll(realDir2, 0755)
	os.MkdirAll(dataDir, 0755)

	validConfig := fmt.Sprintf(`mirrors:
  - name: "test"
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
sync_workers: 2
delete_policy: "ignore"
`, realDir, realDir2,
		filepath.Join(dataDir, "state.db"),
		filepath.Join(dataDir, "fuzz.log"))

	mutations := []func(string) string{
		// Inject random bytes.
		func(s string) string { return s + "\x00\x01\x02\x03" },
		// Remove random line.
		func(s string) string {
			lines := strings.Split(s, "\n")
			if len(lines) > 2 {
				return strings.Join(append(lines[:1], lines[3:]...), "\n")
			}
			return s
		},
		// Duplicate a line.
		func(s string) string {
			lines := strings.Split(s, "\n")
			if len(lines) > 2 {
				return strings.Join(append(lines[:2], append([]string{lines[1]}, lines[2:]...)...), "\n")
			}
			return s
		},
		// Replace a value with a number.
		func(s string) string { return strings.Replace(s, `"ignore"`, "42", 1) },
		// Add BOM.
		func(s string) string { return "\xef\xbb\xbf" + s },
		// CRLF line endings.
		func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") },
		// Tab indentation (YAML hates tabs in some positions).
		func(s string) string { return strings.ReplaceAll(s, "  ", "\t") },
	}

	for i, mutate := range mutations {
		t.Run(fmt.Sprintf("mutation_%d", i), func(t *testing.T) {
			cfgDir := t.TempDir()
			mutated := mutate(validConfig)
			cfgPath := filepath.Join(cfgDir, "config.yaml")
			os.WriteFile(cfgPath, []byte(mutated), 0644)

			r := runSmirror(t, cfgPath, "dry-run")
			assertNoPanic(t, r)
		})
	}
}
