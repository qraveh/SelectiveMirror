// Package filter provides .syncignore parsing and rclone filter generation.
package filter

import (
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// Engine evaluates file paths against global and per-project ignore patterns.
type Engine struct {
	globalIgnore  *ignore.GitIgnore
	projectIgnore *ignore.GitIgnore
	globalRules   []string
	projectRules  []string
}

// New creates a filter engine with global excludes and an optional project .syncignore.
func New(globalExcludes []string, syncIgnorePath string) (*Engine, error) {
	e := &Engine{
		globalRules: globalExcludes,
	}

	if len(globalExcludes) > 0 {
		e.globalIgnore = ignore.CompileIgnoreLines(globalExcludes...)
	}

	if syncIgnorePath != "" {
		if _, err := os.Stat(syncIgnorePath); err == nil {
			gi, err := ignore.CompileIgnoreFile(syncIgnorePath)
			if err != nil {
				return nil, err
			}
			e.projectIgnore = gi

			// Read raw lines for rclone filter generation
			data, err := os.ReadFile(syncIgnorePath)
			if err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if line != "" && !strings.HasPrefix(line, "#") {
						e.projectRules = append(e.projectRules, line)
					}
				}
			}
		}
	}

	return e, nil
}

// IsExcluded returns true if the relative path should be excluded from sync.
// relPath should use forward slashes.
func (e *Engine) IsExcluded(relPath string) bool {
	// Normalize to forward slashes for consistent matching
	relPath = filepath.ToSlash(relPath)

	// Project-specific rules take precedence (may negate global rules)
	if e.projectIgnore != nil {
		if e.projectIgnore.MatchesPath(relPath) {
			return true
		}
	}

	// Global excludes
	if e.globalIgnore != nil {
		if e.globalIgnore.MatchesPath(relPath) {
			return true
		}
	}

	return false
}

// EffectiveRules returns all active filter rules for display.
func (e *Engine) EffectiveRules() []string {
	var rules []string
	if len(e.globalRules) > 0 {
		rules = append(rules, "# Global excludes")
		rules = append(rules, e.globalRules...)
	}
	if len(e.projectRules) > 0 {
		rules = append(rules, "# Project .syncignore")
		rules = append(rules, e.projectRules...)
	}
	return rules
}

// GenerateRcloneFilterFile creates a temporary file with rclone filter rules.
// Returns the path to the temp file. Caller must remove it after use.
func (e *Engine) GenerateRcloneFilterFile() (string, error) {
	var lines []string

	// Project-specific rules first (negations must come before exclusions in rclone)
	for _, rule := range e.projectRules {
		lines = append(lines, toRcloneFilter(rule))
	}

	// Global excludes
	for _, rule := range e.globalRules {
		lines = append(lines, toRcloneFilter(rule))
	}

	// Include everything else
	lines = append(lines, "+ **")

	f, err := os.CreateTemp("", "smirror-filter-*.txt")
	if err != nil {
		return "", err
	}

	content := strings.Join(lines, "\n") + "\n"
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}

	f.Close()
	return f.Name(), nil
}

// toRcloneFilter converts a .gitignore-style pattern to rclone filter syntax.
func toRcloneFilter(pattern string) string {
	pattern = strings.TrimSpace(pattern)

	// Negation: !pattern -> + pattern
	if strings.HasPrefix(pattern, "!") {
		return "+ " + pattern[1:]
	}

	// Directory-only pattern: dir/ -> - dir/**
	if strings.HasSuffix(pattern, "/") {
		return "- " + pattern + "**"
	}

	// Regular exclusion
	return "- " + pattern
}
