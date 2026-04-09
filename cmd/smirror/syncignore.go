package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qraveh/SelectiveMirror/internal/config"
)

// defaultSyncIgnorePatterns returns the standard patterns for a new .syncignore.
var defaultSyncIgnorePatterns = []string{
	"# SelectiveMirror .syncignore",
	"# Add patterns to exclude files from syncing (uses .gitignore syntax)",
	"",
	"# Version control",
	".git/",
	".svn/",
	"",
	"# Build artifacts",
	"*.exe",
	"*.dll",
	"*.obj",
	"*.o",
	"*.a",
	"",
	"# IDE / Editor",
	".idea/",
	".vscode/",
	"*.swp",
	"*~",
	"",
	"# OS generated",
	"Thumbs.db",
	"desktop.ini",
	".DS_Store",
	"",
	"# Dependencies",
	"node_modules/",
	"__pycache__/",
	"venv/",
	".venv/",
	"",
	"# Databases / state",
	"*.db",
	"*.db-wal",
	"*.db-shm",
	"*.sqlite",
}

// createDefaultSyncIgnore creates a .syncignore file in dir with standard patterns.
// If the config has global_excludes, those are appended as well.
func createDefaultSyncIgnore(dir string, cfg *config.Global) error {
	path := filepath.Join(dir, ".syncignore")

	var lines []string
	lines = append(lines, defaultSyncIgnorePatterns...)

	// Append global excludes that aren't already in the defaults
	if cfg != nil && len(cfg.GlobalExcludes) > 0 {
		defaults := make(map[string]bool)
		for _, p := range defaultSyncIgnorePatterns {
			defaults[strings.TrimSpace(p)] = true
		}
		var extras []string
		for _, g := range cfg.GlobalExcludes {
			if !defaults[g] {
				extras = append(extras, g)
			}
		}
		if len(extras) > 0 {
			lines = append(lines, "", "# From global_excludes")
			lines = append(lines, extras...)
		}
	}

	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("creating .syncignore: %w", err)
	}
	return nil
}
