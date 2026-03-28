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
	mergedIgnore  *ignore.GitIgnore // global + project rules merged for correct negation
	globalRules   []string
	projectRules  []string

	// Stored for Reload()
	globalExcludes []string
	syncIgnorePath string

	// Generation counter: incremented on each successful Reload that changes rules.
	// Used by sync engine to detect stale filter files (SM-044).
	generation uint64
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

	e.rebuildMerged()
	return e, nil
}

// rebuildMerged creates a single GitIgnore from global rules first, then project rules.
// This preserves gitignore last-match-wins semantics so that project negation patterns
// (e.g., !important.log) correctly override global exclusions (e.g., *.log).
// Caller must hold e.mu for write (or be in constructor before sharing).
func (e *Engine) rebuildMerged() {
	var all []string
	all = append(all, e.globalRules...)
	all = append(all, e.projectRules...)
	if len(all) > 0 {
		e.mergedIgnore = ignore.CompileIgnoreLines(all...)
	} else {
		e.mergedIgnore = nil
	}
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
	e.rebuildMerged()

	// Detect if rules actually changed
	if len(oldRules) != len(e.projectRules) {
		e.generation++
		return true, nil
	}
	for i := range oldRules {
		if oldRules[i] != e.projectRules[i] {
			e.generation++
			return true, nil
		}
	}
	return false, nil
}

// SyncIgnorePath returns the path to the .syncignore file being watched.
func (e *Engine) SyncIgnorePath() string {
	return e.syncIgnorePath
}

// Generation returns the filter's generation counter (incremented on each rule change).
// Used by the sync engine to detect if a filter was hot-reloaded between task
// enqueue and execution (SM-044).
func (e *Engine) Generation() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.generation
}

// IsExcluded returns true if the relative path should be excluded from sync.
// relPath should use forward slashes. Safe for concurrent use.
// Uses a merged ignore instance (global rules first, project rules after) so
// that project negation patterns (e.g., !important.log) correctly override
// global exclusions (e.g., *.log) via gitignore last-match-wins semantics.
func (e *Engine) IsExcluded(relPath string) bool {
	// Normalize to forward slashes for consistent matching
	relPath = filepath.ToSlash(relPath)

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.mergedIgnore != nil {
		return e.mergedIgnore.MatchesPath(relPath)
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
//
// Gitignore uses last-match-wins; rclone uses first-match-wins. The merged
// gitignore order is [global..., project...] so the last project rule has
// highest priority. Reversing this combined order gives correct rclone
// semantics: the highest-priority rule (last in gitignore) becomes the
// first match in rclone (SM-037).
func (e *Engine) GenerateRcloneFilterFile() (string, error) {
	e.mu.RLock()

	// Build combined rules in gitignore order (global first, project second)
	var combined []string
	combined = append(combined, e.globalRules...)
	combined = append(combined, e.projectRules...)

	// Reverse so last-match-wins becomes first-match-wins
	var lines []string
	for i := len(combined) - 1; i >= 0; i-- {
		lines = append(lines, toRcloneFilter(combined[i]))
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
