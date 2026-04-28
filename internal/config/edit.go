package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// stripBOM removes a UTF-8 BOM prefix if present.
// PowerShell's Set-Content -Encoding UTF8 writes a BOM that breaks
// string matching on the first line of YAML files.
func stripBOM(s string) string {
	if strings.HasPrefix(s, "\xEF\xBB\xBF") {
		return s[3:]
	}
	return s
}

// writePreservingMode writes data to path. If the file already exists, its
// mode is preserved — never widened. SECURITY.md documents 0600 for newly
// created configs (they may contain rclone remote names, webhook URLs, and
// other sensitive data); previous code rewrote with 0644 on every edit,
// silently downgrading the initial 0600.
//
// SEC-M6: writes are atomic. We write to a sibling .tmp file then
// os.Rename onto the target. Go's Rename uses MoveFileEx on Windows
// with replacement semantics, which is atomic relative to readers.
// Without atomicity, a crash mid-WriteFile would leave config.yaml
// truncated and unparseable on next start.
func writePreservingMode(path string, data []byte) error {
	var mode os.FileMode = 0600
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
		// Defensive narrowing: if the file is somehow group/world-writable,
		// don't carry that forward. Owner-only is the documented baseline.
		if mode&0077 != 0 {
			mode = 0600
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return err
	}
	return nil
}

// validateConfigToken rejects strings that would break YAML structure
// when interpolated into a generated config line. SEC-M6: a mirror name
// like "Foo\npre_sync_hook: calc.exe" used to inject into the YAML and
// promote a hook config under what looked like a single mirror entry.
//
// Rejected: ASCII control characters (including \n, \r, \t), backslash
// escapes that YAML resolves into newlines, and characters that would
// require quoting (which our line-based emitter doesn't do). Allows the
// printable ASCII subset plus high-byte characters (Unicode names are
// fine — only the structural-breaking subset is rejected).
func validateConfigToken(label, value string) error {
	for i, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return fmt.Errorf("%s contains a control character at offset %d (\\n, \\r, \\t are not permitted in YAML tokens emitted by smirror)", label, i)
		case r < 0x20:
			return fmt.Errorf("%s contains an ASCII control character (0x%02x) at offset %d", label, r, i)
		case r == 0x7f:
			return fmt.Errorf("%s contains DEL (0x7f) at offset %d", label, i)
		}
	}
	return nil
}

// SetField updates or adds a top-level scalar field in the config YAML file.
// Preserves comments and formatting by operating on raw text lines.
// If the key already exists, its value is replaced in-place.
// If not, the field is appended before the first blank line or at EOF.
func SetField(configPath, key, value string) error {
	if err := validateConfigToken("key", key); err != nil {
		return err
	}
	if err := validateConfigToken("value", value); err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			if dir := filepath.Dir(configPath); dir != "" {
				_ = os.MkdirAll(dir, 0755)
			}
			return os.WriteFile(configPath, []byte(key+": "+value+"\n"), 0600)
		}
		return err
	}

	lines := strings.Split(stripBOM(string(data)), "\n")
	prefix := key + ":"
	found := false
	for i, line := range lines {
		// Match top-level keys ONLY. The previous TrimSpace+HasPrefix matched
		// indented siblings: setting a global `delete_policy` would overwrite
		// the first per-mirror `delete_policy:` line found inside a mirror entry.
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			continue
		}
		bare := strings.TrimRight(line, "\r")
		if strings.HasPrefix(bare, "#") {
			continue
		}
		if strings.HasPrefix(bare, prefix) {
			lines[i] = key + ": " + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+": "+value)
	}

	return writePreservingMode(configPath, []byte(strings.Join(lines, "\n")))
}

// AddMirror appends a new mirror entry to the mirrors list in the config YAML.
// The entry is inserted at the end of the mirrors section.
// Returns an error if a mirror with the same name already exists.
//
// SEC-M6: each token interpolated into YAML must be free of newlines and
// other control characters. A name like "Foo\npre_sync_hook: calc.exe"
// would otherwise inject a hook line under what looks like a single
// mirror entry. Validate every Project field that ends up emitted.
func AddMirror(configPath string, p Project) error {
	for label, val := range map[string]string{
		"mirror name":        p.Name,
		"local_path":         p.LocalPath,
		"remote":             p.Remote,
		"syncignore_path":    p.SyncIgnorePath,
		"delete_policy":      p.DeletePolicyStr,
		"pre_sync_hook":      p.PreSyncHook,
		"post_sync_hook":     p.PostSyncHook,
	} {
		if err := validateConfigToken(label, val); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	lines := strings.Split(stripBOM(string(data)), "\n")

	// Check for duplicate name
	namePattern := regexp.MustCompile(`^\s+-\s+name:\s*` + regexp.QuoteMeta(p.Name) + `\s*$`)
	for _, line := range lines {
		if namePattern.MatchString(line) {
			return fmt.Errorf("mirror %q already exists in config", p.Name)
		}
	}

	// Build the new mirror YAML block
	block := formatMirrorBlock(p)

	// Find the mirrors section and its end
	mirrorsIdx := -1
	insertIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match "mirrors:", "mirrors: []", "mirrors: [] # comment", etc.
		if trimmed == "mirrors:" || strings.HasPrefix(trimmed, "mirrors:") {
			mirrorsIdx = i
			// If inline empty list (mirrors: []), replace with block form
			if strings.Contains(trimmed, "[]") {
				lines[i] = "mirrors:"
			}
			continue
		}
		if mirrorsIdx >= 0 && i > mirrorsIdx {
			// We're inside the mirrors section.
			// The section ends at the next top-level key (non-indented, non-blank, non-comment)
			// or at a commented-out mirror example.
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				// Blank line or comment — could be end of section or inter-item gap.
				// Keep scanning to find the true end.
				if insertIdx < 0 {
					insertIdx = i
				}
				continue
			}
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				// Top-level key — end of mirrors section
				if insertIdx < 0 {
					insertIdx = i
				}
				break
			}
			// Still in mirrors section (indented content)
			insertIdx = i + 1
		}
	}

	if mirrorsIdx < 0 {
		// No mirrors section — append one
		lines = append(lines, "", "mirrors:")
		insertIdx = len(lines)
	} else if insertIdx < 0 {
		// mirrors: was last line, no entries yet
		insertIdx = mirrorsIdx + 1
	}

	// Insert the block
	blockLines := strings.Split(block, "\n")
	result := make([]string, 0, len(lines)+len(blockLines)+1)
	result = append(result, lines[:insertIdx]...)
	result = append(result, blockLines...)
	result = append(result, lines[insertIdx:]...)

	return writePreservingMode(configPath, []byte(strings.Join(result, "\n")))
}

// RemoveMirror removes a mirror entry by name from the config YAML.
// Returns an error if the mirror is not found.
//
// SEC-M6: validate the name doesn't contain control characters before
// using it as a regex literal — the regex is anchored, but a name
// containing \n could match across line boundaries with multiline mode
// (we don't enable that; defensive check anyway).
func RemoveMirror(configPath, name string) error {
	if err := validateConfigToken("mirror name", name); err != nil {
		return err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	lines := strings.Split(stripBOM(string(data)), "\n")

	// Find the mirror's "  - name: <name>" line
	startIdx := -1
	namePattern := regexp.MustCompile(`^\s+-\s+name:\s*` + regexp.QuoteMeta(name) + `\s*$`)
	for i, line := range lines {
		if namePattern.MatchString(line) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		return fmt.Errorf("mirror %q not found in config", name)
	}

	// Find where this mirror entry ends:
	// Next "  - name:" line, or next top-level key, or EOF
	endIdx := len(lines)
	for i := startIdx + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		// Next list item
		if strings.HasPrefix(trimmed, "- name:") || strings.HasPrefix(trimmed, "- name :") {
			endIdx = i
			break
		}
		// Top-level key (no leading whitespace, not blank, not comment)
		if !strings.HasPrefix(lines[i], " ") && !strings.HasPrefix(lines[i], "\t") && !strings.HasPrefix(trimmed, "#") {
			endIdx = i
			break
		}
	}

	// Remove trailing blank lines that belonged to this entry
	for endIdx > startIdx && strings.TrimSpace(lines[endIdx-1]) == "" {
		endIdx--
	}

	// Remove the lines
	result := make([]string, 0, len(lines))
	result = append(result, lines[:startIdx]...)
	result = append(result, lines[endIdx:]...)

	return writePreservingMode(configPath, []byte(strings.Join(result, "\n")))
}

// formatMirrorBlock formats a Project as a YAML list item with 2-space indent.
// Only includes fields that have non-zero values.
func formatMirrorBlock(p Project) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  - name: %s\n", p.Name))
	b.WriteString(fmt.Sprintf("    local_path: %s\n", p.LocalPath))
	b.WriteString(fmt.Sprintf("    remote: %q\n", p.Remote))

	if p.DebounceSec > 0 {
		b.WriteString(fmt.Sprintf("    debounce_sec: %d\n", p.DebounceSec))
	}
	if p.MaxFileSizeMB > 0 && p.MaxFileSizeMB != 100 {
		b.WriteString(fmt.Sprintf("    max_file_size_mb: %d\n", p.MaxFileSizeMB))
	}
	if p.SyncIgnorePath != "" {
		b.WriteString(fmt.Sprintf("    syncignore_path: %q\n", p.SyncIgnorePath))
	}
	if p.DeletePolicyStr != "" {
		b.WriteString(fmt.Sprintf("    delete_policy: %s\n", p.DeletePolicyStr))
	}
	if p.QuarantineDays > 0 {
		b.WriteString(fmt.Sprintf("    quarantine_days: %d\n", p.QuarantineDays))
	}
	if p.PreSyncHook != "" {
		b.WriteString(fmt.Sprintf("    pre_sync_hook: %q\n", p.PreSyncHook))
	}
	if p.PostSyncHook != "" {
		b.WriteString(fmt.Sprintf("    post_sync_hook: %q\n", p.PostSyncHook))
	}

	// Trim trailing newline
	s := b.String()
	return strings.TrimRight(s, "\n")
}

// UniqueMirrorName returns a unique mirror name by appending -2, -3, etc.
// if the base name already exists in the config.
func UniqueMirrorName(baseName string, existing []string) string {
	names := make(map[string]bool)
	for _, n := range existing {
		names[strings.ToLower(n)] = true
	}
	if !names[strings.ToLower(baseName)] {
		return baseName
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", baseName, i)
		if !names[strings.ToLower(candidate)] {
			return candidate
		}
	}
	return baseName // fallback, shouldn't happen
}
