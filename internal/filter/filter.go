// Package filter provides .syncignore parsing and rclone filter generation.
package filter

import (
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

	// SM-110: SHA-256 of the raw .syncignore bytes that produced the currently
	// committed projectRules. Used for content-hash idempotency (skip reload if
	// file content hasn't changed) and rule-shrink stability detection.
	committedContentHash [32]byte
}

// New creates a filter engine with global excludes and an optional project .syncignore.
func New(globalExcludes []string, syncIgnorePath string) (*Engine, error) {
	e := &Engine{
		globalExcludes: globalExcludes,
		syncIgnorePath: syncIgnorePath,
		globalRules:    globalExcludes,
	}

	rules, contentHash, err := e.readProjectRules()
	if err != nil {
		return nil, err
	}
	e.projectRules = rules
	e.committedContentHash = contentHash

	e.rebuildMerged()

	// SM-110: On initial load, strip bad patterns and rebuild so the filter
	// engine starts with only valid rules. (On Reload, we refuse instead.)
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

// readProjectRules reads the .syncignore file and returns parsed rules and a
// SHA-256 hash of the raw file bytes. The hash is used for content-hash
// idempotency and rule-shrink stability detection (SM-110).
// Returns nil rules (not error) if the file doesn't exist.
func (e *Engine) readProjectRules() ([]string, [32]byte, error) {
	if e.syncIgnorePath == "" {
		return nil, sha256.Sum256(nil), nil
	}
	if _, err := os.Stat(e.syncIgnorePath); err != nil {
		return nil, sha256.Sum256(nil), nil // file doesn't exist — not an error
	}
	data, err := os.ReadFile(e.syncIgnorePath)
	if err != nil {
		return nil, [32]byte{}, err
	}

	contentHash := sha256.Sum256(data)

	content := strings.TrimPrefix(string(data), "\xEF\xBB\xBF") // strip UTF-8 BOM

	var rules []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		line = trimTrailingUnescaped(line)
		line = strings.TrimLeft(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}

	// Lint: warn about unanchored negation patterns
	for _, rule := range rules {
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

	return rules, contentHash, nil
}

// Reload re-reads the .syncignore file and recompiles the project filter rules.
// Global excludes are not affected. Returns true if the rules actually changed.
// Safe for concurrent use — callers of IsExcluded are briefly blocked during swap.
//
// SM-110 formally verifiable reload:
//
//  1. Content-hash idempotency: if file bytes haven't changed since last
//     commit, return immediately. Eliminates duplicate reloads.
//
//  2. Validate-before-commit: trial-compile new rules into temp matcher.
//     If any pattern is malformed, refuse entirely.
//
//  3. Rule-shrink stability check: if the new rule set is a strict subset
//     of current rules (the signature of a truncated file), double-read
//     the file after 50ms. If the hash changed, the file is still being
//     written — abort. If stable, commit.
//
// Invariant: rules never shrink due to transient file states.
func (e *Engine) Reload() (changed bool, err error) {
	// 1. Read new rules and content hash from disk (no lock needed).
	newRules, newHash, readErr := e.readProjectRules()
	if readErr != nil {
		slog.Warn("malformed .syncignore, keeping previous rules",
			"path", e.syncIgnorePath, "error", readErr)
		return false, nil
	}

	// 2. Content-hash idempotency: if file content is identical to what
	//    we last committed, skip entirely.
	e.mu.RLock()
	if newHash == e.committedContentHash {
		e.mu.RUnlock()
		return false, nil
	}
	currentRules := make([]string, len(e.projectRules))
	copy(currentRules, e.projectRules)
	e.mu.RUnlock()

	// 3. Trial-compile: merge global + new project rules into a temp matcher.
	var all []string
	e.mu.RLock()
	all = append(all, e.globalRules...)
	e.mu.RUnlock()
	all = append(all, newRules...)

	var badPatterns []string
	var testMerged *gitignore.Matcher
	if len(all) > 0 {
		m := &gitignore.Matcher{}
		m.AddPatterns([]byte(strings.Join(all, "\n")), "")
		if errs := m.Errors(); len(errs) > 0 {
			for _, pe := range errs {
				slog.Warn("pattern parse error in filter rules", "error", pe)
				badPatterns = append(badPatterns, pe.Pattern)
			}
		}
		testMerged = m
	}

	// 4. If any patterns failed, refuse the change entirely.
	if len(badPatterns) > 0 {
		slog.Warn("malformed patterns in .syncignore, keeping previous valid rules",
			"path", e.syncIgnorePath, "bad_patterns", badPatterns)
		return false, nil
	}

	// 5. Rule-shrink stability check: if the new rule set is a strict subset
	//    of the current rules, the file may be transiently truncated (Windows
	//    writes fire truncate + content events). Double-read after 50ms to
	//    confirm the content is stable before committing a rule removal.
	if isStrictSubset(newRules, currentRules) {
		time.Sleep(50 * time.Millisecond)
		_, confirmHash, confirmErr := e.readProjectRules()
		if confirmErr != nil || confirmHash != newHash {
			slog.Info("syncignore content changed during stability check, deferring reload",
				"path", e.syncIgnorePath)
			return false, nil
		}
	}

	// 6. New rules are valid and stable — commit under write lock.
	e.mu.Lock()
	defer e.mu.Unlock()

	oldRules := make([]string, len(e.projectRules))
	copy(oldRules, e.projectRules)

	e.projectRules = newRules
	e.merged = testMerged
	e.badPatterns = nil
	e.committedContentHash = newHash

	// Detect if rules actually changed and log the diff (SM-070)
	changed = false
	if len(oldRules) != len(newRules) {
		changed = true
	} else {
		for i := range oldRules {
			if oldRules[i] != newRules[i] {
				changed = true
				break
			}
		}
	}

	if changed {
		e.generation++
		logFilterDiff(e.syncIgnorePath, oldRules, newRules)
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

// isStrictSubset returns true when every element of newRules exists in oldRules
// AND newRules has fewer elements. This is the signature of file truncation:
// rules are removed but none are added.
func isStrictSubset(newRules, oldRules []string) bool {
	if len(newRules) >= len(oldRules) {
		return false
	}
	old := make(map[string]int, len(oldRules))
	for _, r := range oldRules {
		old[r]++
	}
	for _, r := range newRules {
		if old[r] <= 0 {
			return false // newRules has a rule not in oldRules — not a subset
		}
		old[r]--
	}
	return true
}

// HasBadPatterns reports whether the filter engine has malformed patterns
// that were skipped during compilation. When true, batch sync should refuse
// to run to avoid fail-open behavior.
func (e *Engine) HasBadPatterns() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.badPatterns) > 0
}

// BadPatternSource returns a human-readable description of where malformed
// patterns originated: ".syncignore", "global_excludes", or both.
// Returns "" if there are no bad patterns.
func (e *Engine) BadPatternSource() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.badPatterns) == 0 {
		return ""
	}
	globalSet := make(map[string]bool, len(e.globalRules))
	for _, r := range e.globalRules {
		globalSet[r] = true
	}
	projectSet := make(map[string]bool, len(e.projectRules))
	for _, r := range e.projectRules {
		projectSet[r] = true
	}
	inGlobal, inProject := false, false
	for _, bp := range e.badPatterns {
		if globalSet[bp] {
			inGlobal = true
		}
		if projectSet[bp] {
			inProject = true
		}
	}
	switch {
	case inGlobal && inProject:
		return "global_excludes and .syncignore"
	case inGlobal:
		return "global_excludes"
	default:
		return ".syncignore"
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
//
// BUG-R3-1 (panel review 2026-04-28; tracked as deviation, not regression):
// per the gitignore specification, "It is not possible to re-include a file
// if a parent directory of that file is excluded." smirror's underlying
// matcher (github.com/git-pkgs/gitignore) applies last-pattern-wins
// independently per file, which means a `!foo/bar/baz.txt` rule will
// re-include the leaf even if a prior `foo/**` excluded the entire
// foo/ directory. This is a documented deviation from gitignore semantics
// (FR-FILTER-01); the planned fix for v1.0 walks parent directories and
// short-circuits the leaf when any ancestor matches an exclude pattern.
// Until that ships, the deviation is exercised by
// system-validation/TestPanelR3_Gitignore_ExcludedParentBlocksChildNegation
// and listed in CHANGELOG "Known issues". Practical impact is small —
// `.syncignore` authors writing this pattern still get the file synced
// (fail-open); the SRS update marks it as "deferred conformance gap".
func (e *Engine) IsExcluded(relPath string) bool {
	relPath = filepath.ToSlash(relPath)

	// SM-125: Auto-exclude .syncignore control file.
	base := filepath.Base(relPath)
	if base == ".syncignore" {
		return true
	}
	if e.syncIgnorePath != "" && base == filepath.Base(e.syncIgnorePath) && !strings.Contains(relPath, "/") {
		return true
	}

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

	// SM-125: Auto-exclude the .syncignore control file from sync.
	// If syncignore_path is set and points inside the mirror root, exclude
	// its basename too. Always exclude ".syncignore" (the default name).
	syncIgnoreBasename := ""
	if e.syncIgnorePath != "" {
		syncIgnoreBasename = filepath.Base(e.syncIgnorePath)
	}

	var lines []string
	lines = append(lines, "- .syncignore")
	if syncIgnoreBasename != "" && syncIgnoreBasename != ".syncignore" {
		lines = append(lines, "- "+syncIgnoreBasename)
	}
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
// SM-121: Use trimTrailingUnescaped instead of TrimSpace to preserve escaped
// trailing spaces (gitignore "\ " syntax), then convert the escape to a
// literal space for rclone's filter format.
func toRcloneFilter(pattern string) string {
	pattern = trimTrailingUnescaped(pattern)
	// Convert gitignore "\ " escapes to literal spaces for rclone filter syntax.
	pattern = strings.ReplaceAll(pattern, "\\ ", " ")

	if strings.HasPrefix(pattern, "!") {
		return "+ " + pattern[1:]
	}
	if strings.HasSuffix(pattern, "/") {
		return "- " + pattern + "**"
	}
	return "- " + pattern
}
