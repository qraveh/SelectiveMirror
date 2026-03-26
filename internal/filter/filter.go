// Package filter provides .syncignore parsing and rclone filter generation.
package filter

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	ignore "github.com/sabhiram/go-gitignore"
)

// Engine evaluates file paths against global and per-project ignore patterns.
// It is safe for concurrent use and supports hot-reloading of .syncignore files.
type Engine struct {
	mu sync.RWMutex

	globalIgnore  *ignore.GitIgnore
	projectIgnore *ignore.GitIgnore
	globalRules   []string
	projectRules  []string

	// Stored for Reload()
	globalExcludes []string
	syncIgnorePath string
}

// New creates a filter engine with global excludes and an optional project .syncignore.
func New(globalExcludes []string, syncIgnorePath string) (*Engine, error) {
	e := &Engine{
		globalExcludes: globalExcludes,
		syncIgnorePath: syncIgnorePath,
		globalRules:    globalExcludes,
	}

	if len(globalExcludes) > 0 {
		e.globalIgnore = ignore.CompileIgnoreLines(globalExcludes...)
	}

	if err := e.loadProjectIgnore(); err != nil {
		return nil, err
	}

	return e, nil
}

// loadProjectIgnore reads and compiles the project .syncignore file.
// Caller must hold e.mu for write (or be in constructor before sharing).
func (e *Engine) loadProjectIgnore() error {
	e.projectIgnore = nil
	e.projectRules = nil

	if e.syncIgnorePath == "" {
		return nil
	}

	if _, err := os.Stat(e.syncIgnorePath); err != nil {
		return nil // file doesn't exist — not an error, just no project rules
	}

	gi, err := ignore.CompileIgnoreFile(e.syncIgnorePath)
	if err != nil {
		return err
	}
	e.projectIgnore = gi

	// Read raw lines for rclone filter generation
	data, err := os.ReadFile(e.syncIgnorePath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				e.projectRules = append(e.projectRules, line)
			}
		}
	}

	return nil
}

// Reload re-reads the .syncignore file and recompiles the project filter rules.
// Global excludes are not affected. Returns true if the rules actually changed.
// Safe for concurrent use — callers of IsExcluded are briefly blocked during swap.
func (e *Engine) Reload() (changed bool, err error) {
	// Read the new rules before acquiring write lock (minimize lock time)
	oldRules := func() []string {
		e.mu.RLock()
		defer e.mu.RUnlock()
		cp := make([]string, len(e.projectRules))
		copy(cp, e.projectRules)
		return cp
	}()

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.loadProjectIgnore(); err != nil {
		return false, err
	}

	// Detect if rules actually changed
	if len(oldRules) != len(e.projectRules) {
		return true, nil
	}
	for i := range oldRules {
		if oldRules[i] != e.projectRules[i] {
			return true, nil
		}
	}
	return false, nil
}

// SyncIgnorePath returns the path to the .syncignore file being watched.
func (e *Engine) SyncIgnorePath() string {
	return e.syncIgnorePath
}

// IsExcluded returns true if the relative path should be excluded from sync.
// relPath should use forward slashes. Safe for concurrent use.
func (e *Engine) IsExcluded(relPath string) bool {
	// Normalize to forward slashes for consistent matching
	relPath = filepath.ToSlash(relPath)

	e.mu.RLock()
	defer e.mu.RUnlock()

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

// EffectiveRules returns all active filter rules for display. Safe for concurrent use.
func (e *Engine) EffectiveRules() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

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
// Returns the path to the temp file. Caller must remove it after use. Safe for concurrent use.
func (e *Engine) GenerateRcloneFilterFile() (string, error) {
	e.mu.RLock()
	var lines []string

	// Project-specific rules first (negations must come before exclusions in rclone)
	for _, rule := range e.projectRules {
		lines = append(lines, toRcloneFilter(rule))
	}

	// Global excludes
	for _, rule := range e.globalRules {
		lines = append(lines, toRcloneFilter(rule))
	}
	e.mu.RUnlock()

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
