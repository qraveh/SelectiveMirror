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

	// SM-107: Patterns that failed compilation. Skipped in GenerateRcloneFilterFile().
	badPatterns []string
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

	// SM-110: On initial load, strip bad patterns and rebuild so the filter
	// engine starts with only valid rules. (On Reload, we roll back instead.)
	if len(e.badPatterns) > 0 {
		badSet := make(map[string]bool)
		for _, bp := range e.badPatterns {
			badSet[bp] = true
		}
		var clean []string
		for _, r := range e.projectRules {
			if !badSet[r] {
				clean = append(clean, r)
			}
		}
		e.projectRules = clean
		e.rebuildMerged()
	}
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
		// SM-080/SM-107: Check for pattern parse errors. Record bad patterns
		// so they can be skipped in GenerateRcloneFilterFile().
		e.badPatterns = nil
		if errs := m.Errors(); len(errs) > 0 {
			for _, pe := range errs {
				slog.Warn("pattern parse error in filter rules", "error", pe)
				e.badPatterns = append(e.badPatterns, pe.Pattern)
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

	// Strip UTF-8 BOM if present (PowerShell Set-Content -Encoding UTF8 adds one)
	content := string(data)
	if strings.HasPrefix(content, "\xEF\xBB\xBF") {
		content = content[3:]
	}

	for _, line := range strings.Split(content, "\n") {
		// Strip CR (Windows CRLF)
		line = strings.TrimRight(line, "\r")
		// Strip unescaped trailing whitespace (gitignore spec).
		// "foo\ " keeps the space, "foo " strips it, "foo\t" strips the tab.
		line = trimTrailingUnescaped(line)
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e.projectRules = append(e.projectRules, line)
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

	// SM-110: If any patterns failed to compile, the merged matcher may be
	// incomplete (e.g., [abc is invalid → old exclusion rules lost → fail-open).
	// Roll back to last-known-good state to prevent excluded files from syncing.
	if len(e.badPatterns) > 0 {
		slog.Warn("malformed patterns in .syncignore, keeping previous valid rules",
			"path", e.syncIgnorePath, "bad_patterns", e.badPatterns)
		e.projectRules = prevProjectRules
		e.merged = prevMerged
		e.badPatterns = nil
		return false, nil
	}

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

// trimTrailingUnescaped strips trailing spaces and tabs unless the last space
// is escaped with a backslash (gitignore spec: "foo\ " keeps the trailing space).
func trimTrailingUnescaped(s string) string {
	i := len(s)
	for i > 0 && (s[i-1] == ' ' || s[i-1] == '\t') {
		if i >= 2 && s[i-2] == '\\' && s[i-1] == ' ' {
			// Escaped space — stop stripping
			break
		}
		i--
	}
	return s[:i]
}

// HasBadPatterns reports whether the filter engine has malformed patterns
// that were skipped during compilation. When true, batch sync should refuse
// to run to avoid fail-open behavior.
func (e *Engine) HasBadPatterns() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.badPatterns) > 0
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

	// SM-107: Build set of bad patterns to skip in rclone filter output.
	badSet := make(map[string]bool, len(e.badPatterns))
	for _, bp := range e.badPatterns {
		badSet[bp] = true
	}

	var combined []string
	combined = append(combined, otherGlobalRules...)
	combined = append(combined, e.projectRules...)

	var lines []string
	for _, d := range globalDirExcludes {
		if !badSet[d] {
			lines = append(lines, toRcloneFilter(d))
		}
	}
	for i := len(combined) - 1; i >= 0; i-- {
		if !badSet[combined[i]] {
			lines = append(lines, toRcloneFilter(combined[i]))
		}
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
