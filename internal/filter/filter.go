// Package filter provides .syncignore parsing and rclone filter generation.
package filter

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/git-pkgs/gitignore"
)

// Engine evaluates file paths against global and per-project ignore patterns.
// It is safe for concurrent use and supports hot-reloading of .syncignore files.
type Engine struct {
	mu sync.RWMutex

	merged      *gitignore.Matcher // global + project rules merged
	globalRules []string
	projectRules []string

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

	if err := e.loadProjectIgnore(); err != nil {
		return nil, err
	}

	e.rebuildMerged()
	return e, nil
}

// rebuildMerged creates a single Matcher from global rules first, then project rules.
// This preserves gitignore last-match-wins semantics so that project negation patterns
// (e.g., !important.log) correctly override global exclusions (e.g., *.log).
// Caller must hold e.mu for write (or be in constructor before sharing).
func (e *Engine) rebuildMerged() {
	var all []string
	all = append(all, e.globalRules...)
	all = append(all, e.projectRules...)
	if len(all) > 0 {
		m := &gitignore.Matcher{} // bare matcher — do NOT use New() which auto-loads .gitignore
		m.AddPatterns([]byte(strings.Join(all, "\n")), "")
		// SM-080: Check for pattern parse errors that would otherwise be silently lost.
		if errs := m.Errors(); len(errs) > 0 {
			for _, e := range errs {
				slog.Warn("pattern parse error in filter rules", "error", e)
			}
		}
		e.merged = m
	} else {
		e.merged = nil
	}
}

// loadProjectIgnore reads and compiles the project .syncignore file.
// Caller must hold e.mu for write (or be in constructor before sharing).
func (e *Engine) loadProjectIgnore() error {
	e.projectRules = nil

	if e.syncIgnorePath == "" {
		return nil
	}

	if _, err := os.Stat(e.syncIgnorePath); err != nil {
		return nil // file doesn't exist — not an error, just no project rules
	}

	// Read raw lines for both pattern compilation and rclone filter generation
	data, err := os.ReadFile(e.syncIgnorePath)
	if err != nil {
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			e.projectRules = append(e.projectRules, line)
		}
	}

	// Lint: warn about unanchored negation patterns that match at any depth.
	for _, rule := range e.projectRules {
		if strings.HasPrefix(rule, "!") && !strings.HasPrefix(rule, "!/") {
			pattern := rule[1:]
			if !strings.Contains(pattern, "/") || strings.HasSuffix(pattern, "/") {
				slog.Warn("unanchored negation pattern matches at any directory depth",
					"syncignore", e.syncIgnorePath,
					"pattern", rule,
					"hint", "use !/"+pattern+" to anchor to project root")
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

	// FR-FILTER-11: Save previous state before loading.
	prevProjectRules := make([]string, len(e.projectRules))
	copy(prevProjectRules, e.projectRules)
	prevMerged := e.merged

	if err := e.loadProjectIgnore(); err != nil {
		// Restore previous state — keep last-known-good filter rules
		e.projectRules = prevProjectRules
		e.merged = prevMerged
		slog.Warn("malformed .syncignore, keeping previous rules",
			"path", e.syncIgnorePath, "error", err)
		return false, nil
	}
	e.rebuildMerged()

	// Detect if rules actually changed and log the diff (SM-070)
	changed = false
	if len(oldRules) != len(e.projectRules) {
		changed = true
	} else {
		for i := range oldRules {
			if oldRules[i] != e.projectRules[i] {
				changed = true
				break
			}
		}
	}

	if changed {
		e.generation++
		logFilterDiff(e.syncIgnorePath, oldRules, e.projectRules)
	}
	return changed, nil
}

// logFilterDiff logs added and removed rules when .syncignore changes.
func logFilterDiff(path string, oldRules, newRules []string) {
	old := make(map[string]bool, len(oldRules))
	for _, r := range oldRules {
		old[r] = true
	}
	newSet := make(map[string]bool, len(newRules))
	for _, r := range newRules {
		newSet[r] = true
	}

	var added, removed []string
	for _, r := range newRules {
		if !old[r] {
			added = append(added, r)
		}
	}
	for _, r := range oldRules {
		if !newSet[r] {
			removed = append(removed, r)
		}
	}

	if len(added) > 0 {
		slog.Info(".syncignore rules added", "path", path, "added", added)
	}
	if len(removed) > 0 {
		slog.Info(".syncignore rules removed", "path", path, "removed", removed)
	}
}

// SyncIgnorePath returns the path to the .syncignore file being watched.
func (e *Engine) SyncIgnorePath() string {
	return e.syncIgnorePath
}

// Generation returns the filter's generation counter (incremented on each rule change).
func (e *Engine) Generation() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.generation
}

// IsExcluded returns true if the relative path should be excluded from sync.
// relPath should use forward slashes. Safe for concurrent use.
func (e *Engine) IsExcluded(relPath string) bool {
	relPath = filepath.ToSlash(relPath)

	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.merged != nil {
		isDir := strings.HasSuffix(relPath, "/")
		return e.merged.MatchPath(relPath, isDir)
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
//
// Global directory exclusions (e.g., .git/, node_modules/) are hoisted to the
// top of the filter file so they cannot be overridden by unanchored project
// negation patterns (e.g., !hooks/*). This enforces gitignore's excluded-parent
// constraint.
func (e *Engine) GenerateRcloneFilterFile() (string, error) {
	e.mu.RLock()

	var globalDirExcludes []string
	var otherGlobalRules []string
	for _, r := range e.globalRules {
		if strings.HasSuffix(r, "/") && !strings.HasPrefix(r, "!") {
			globalDirExcludes = append(globalDirExcludes, r)
		} else {
			otherGlobalRules = append(otherGlobalRules, r)
		}
	}

	var combined []string
	combined = append(combined, otherGlobalRules...)
	combined = append(combined, e.projectRules...)

	var lines []string
	for _, d := range globalDirExcludes {
		lines = append(lines, toRcloneFilter(d))
	}
	for i := len(combined) - 1; i >= 0; i-- {
		lines = append(lines, toRcloneFilter(combined[i]))
	}
	e.mu.RUnlock()

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

	if strings.HasPrefix(pattern, "!") {
		return "+ " + pattern[1:]
	}
	if strings.HasSuffix(pattern, "/") {
		return "- " + pattern + "**"
	}
	return "- " + pattern
}
