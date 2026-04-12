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

// SetField updates or adds a top-level scalar field in the config YAML file.
// Preserves comments and formatting by operating on raw text lines.
// If the key already exists, its value is replaced in-place.
// If not, the field is appended before the first blank line or at EOF.
func SetField(configPath, key, value string) error {
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
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			// Preserve inline comment if any
			lines[i] = key + ": " + value
			found = true
			break
		}
	}
	if !found {
		// Insert after last non-empty top-level line, or append
		lines = append(lines, key+": "+value)
	}

	return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}

// AddMirror appends a new mirror entry to the mirrors list in the config YAML.
// The entry is inserted at the end of the mirrors section.
// Returns an error if a mirror with the same name already exists.
func AddMirror(configPath string, p Project) error {
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

	return os.WriteFile(configPath, []byte(strings.Join(result, "\n")), 0644)
}

// RemoveMirror removes a mirror entry by name from the config YAML.
// Returns an error if the mirror is not found.
func RemoveMirror(configPath, name string) error {
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

	return os.WriteFile(configPath, []byte(strings.Join(result, "\n")), 0644)
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
