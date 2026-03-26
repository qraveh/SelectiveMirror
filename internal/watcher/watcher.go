// Package watcher provides filesystem monitoring with per-project debounce.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	gosync "sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// Manager manages filesystem watchers for all projects.
type Manager struct {
	projects     []projectWatcher
	fsw          *fsnotify.Watcher
	deletePolicy config.DeletePolicy
	log          *slog.Logger

	// Health monitoring
	lastEventTime   time.Time
	lastEventMu     gosync.Mutex
	healthErrors    []HealthError
	healthErrorsMu  gosync.Mutex
}

// HealthError records a runtime error for self-health reporting.
type HealthError struct {
	Time    time.Time
	Source  string // "eventLoop", "debounceLoop", "syncEngine", etc.
	Message string
}

type projectWatcher struct {
	project  config.Project
	filter   *filter.Engine
	syncChan chan<- msync.Task
	pending  map[string]time.Time
	mu       gosync.Mutex
}

// NewManager creates a watcher manager for all configured projects.
func NewManager(projects []config.Project, filters map[string]*filter.Engine, syncChan chan<- msync.Task, deletePolicy config.DeletePolicy) (*Manager, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		fsw:          fsw,
		deletePolicy: deletePolicy,
		log:          slog.Default().With("component", "watcher"),
	}

	for _, proj := range projects {
		fe := filters[proj.Name]
		pw := projectWatcher{
			project:  proj,
			filter:   fe,
			syncChan: syncChan,
			pending:  make(map[string]time.Time),
		}
		m.projects = append(m.projects, pw)
	}

	return m, nil
}

// Start begins watching all project directories.
func (m *Manager) Start(ctx context.Context) error {
	// Add all project directories and subdirectories
	for i := range m.projects {
		pw := &m.projects[i]
		count, err := m.addRecursive(pw.project.LocalPath, pw.filter)
		if err != nil {
			m.log.Error("failed to watch directory", "project", pw.project.Name, "path", pw.project.LocalPath, "error", err)
			continue
		}
		m.log.Info("watching", "project", pw.project.Name, "path", pw.project.LocalPath, "dirs", count)
	}

	// Start event processing goroutine (with panic recovery)
	go m.safeGo("eventLoop", func() { m.eventLoop(ctx) })

	// Start debounce goroutines for each project (with panic recovery)
	for i := range m.projects {
		pw := &m.projects[i]
		name := fmt.Sprintf("debounceLoop[%s]", pw.project.Name)
		go m.safeGo(name, func() { m.debounceLoop(ctx, pw) })
	}

	// Start health monitor goroutine
	go m.safeGo("healthMonitor", func() { m.healthMonitor(ctx) })

	return nil
}

// Stop closes the filesystem watcher.
func (m *Manager) Stop() {
	m.fsw.Close()
}

// safeGo runs fn with panic recovery. If fn panics, the error is logged and
// recorded in healthErrors so the daemon doesn't crash silently.
func (m *Manager) safeGo(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			msg := fmt.Sprintf("panic in %s: %v\n%s", name, r, stack)
			m.log.Error("PANIC recovered", "goroutine", name, "panic", r, "stack", stack)

			m.healthErrorsMu.Lock()
			m.healthErrors = append(m.healthErrors, HealthError{
				Time:    time.Now(),
				Source:  name,
				Message: msg,
			})
			// Keep at most 100 errors
			if len(m.healthErrors) > 100 {
				m.healthErrors = m.healthErrors[len(m.healthErrors)-100:]
			}
			m.healthErrorsMu.Unlock()
		}
	}()
	fn()
}

// HealthErrors returns a copy of recent runtime errors.
func (m *Manager) HealthErrors() []HealthError {
	m.healthErrorsMu.Lock()
	defer m.healthErrorsMu.Unlock()
	cp := make([]HealthError, len(m.healthErrors))
	copy(cp, m.healthErrors)
	return cp
}

// LastEventAge returns the time since the last fsnotify event was received.
// Returns zero if no events have been received yet.
func (m *Manager) LastEventAge() time.Duration {
	m.lastEventMu.Lock()
	defer m.lastEventMu.Unlock()
	if m.lastEventTime.IsZero() {
		return 0
	}
	return time.Since(m.lastEventTime)
}

// WatchCount returns the number of directories being watched.
func (m *Manager) WatchCount() int {
	return len(m.fsw.WatchList())
}

// addRecursive adds a directory and all subdirectories to the watcher.
// projectRoot is the top-level project path used for computing relative paths for filter checks.
// Symlink directories are NEVER followed — they could point anywhere (/, /etc, loops).
func (m *Manager) addRecursive(walkRoot string, fe *filter.Engine, projectRoots ...string) (int, error) {
	// Use projectRoot for filter rel-path computation if provided, otherwise use walkRoot
	filterRoot := walkRoot
	if len(projectRoots) > 0 && projectRoots[0] != "" {
		filterRoot = projectRoots[0]
	}

	count := 0
	err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible directories
		}

		// Skip symlinks entirely — WalkDir doesn't follow them by default,
		// but we make the intent explicit and log a warning for symlink dirs.
		if d.Type()&os.ModeSymlink != 0 {
			if d.IsDir() || isLinkToDir(path) {
				m.log.Warn("skipping symlink directory (not followed)",
					"path", path, "reason", "symlinks could escape project boundary")
			}
			return nil // skip symlinks (both file and dir)
		}

		if !d.IsDir() {
			return nil
		}

		// Check if directory is excluded (relative to project root for correct filter matching)
		relPath, _ := filepath.Rel(filterRoot, path)
		if relPath != "." && fe != nil && fe.IsExcluded(relPath+"/") {
			return filepath.SkipDir
		}

		if err := m.fsw.Add(path); err != nil {
			m.log.Debug("cannot watch dir", "path", path, "error", err)
			return nil // don't fail the whole walk
		}
		count++
		return nil
	})
	return count, err
}

// isLinkToDir resolves a symlink and checks if target is a directory.
// Returns false on any error (broken link, permission denied, etc).
func isLinkToDir(path string) bool {
	info, err := os.Stat(path) // Stat follows symlinks
	if err != nil {
		return false
	}
	return info.IsDir()
}

// removeRecursive removes a directory and all its tracked subdirectories from the watcher.
func (m *Manager) removeRecursive(dirPath string) int {
	dirAbs, err := filepath.Abs(dirPath)
	if err != nil {
		return 0
	}
	dirAbs = filepath.Clean(dirAbs)

	count := 0
	for _, watched := range m.fsw.WatchList() {
		watchedAbs, err := filepath.Abs(watched)
		if err != nil {
			continue
		}
		watchedAbs = filepath.Clean(watchedAbs)

		if watchedAbs == dirAbs || isSubPath(watchedAbs, dirAbs) {
			if err := m.fsw.Remove(watched); err == nil {
				count++
			}
		}
	}
	return count
}

// findProject returns the project watcher for a given file path.
func (m *Manager) findProject(path string) *projectWatcher {
	for i := range m.projects {
		pw := &m.projects[i]
		if isSubPath(path, pw.project.LocalPath) {
			return pw
		}
	}
	return nil
}

// eventLoop processes fsnotify events.
func (m *Manager) eventLoop(ctx context.Context) {
	for {
		select {
		case event, ok := <-m.fsw.Events:
			if !ok {
				return
			}
			// Record last event time for health monitoring
			m.lastEventMu.Lock()
			m.lastEventTime = time.Now()
			m.lastEventMu.Unlock()

			m.handleEvent(event)
		case err, ok := <-m.fsw.Errors:
			if !ok {
				return
			}
			m.log.Error("watcher error", "error", err)
			m.healthErrorsMu.Lock()
			m.healthErrors = append(m.healthErrors, HealthError{
				Time:    time.Now(),
				Source:  "fsnotify",
				Message: fmt.Sprintf("watcher error: %v", err),
			})
			m.healthErrorsMu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) handleEvent(event fsnotify.Event) {
	// Handle remove events: file/directory deletion
	if event.Has(fsnotify.Remove) {
		m.handleRemove(event)
		return
	}

	// Handle rename events: old path is gone, treat as remove + expect Create for new path
	if event.Has(fsnotify.Rename) {
		m.handleRename(event)
		return
	}

	// Only care about create, write
	if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
		return
	}

	pw := m.findProject(event.Name)
	if pw == nil {
		return
	}

	// Check if .syncignore was modified — hot-reload filter rules
	if m.isSyncIgnoreFile(pw, event.Name) {
		m.reloadFilter(pw)
		return
	}

	// Use Lstat (NOT Stat) to see the filesystem object itself, not its target.
	// This is critical: os.Stat follows symlinks transparently, which would cause
	// symlink-to-dir to be treated as a real directory (watching arbitrary paths)
	// and symlink-to-file to sync the target's content (data leak).
	linfo, lerr := os.Lstat(event.Name)

	// If a new directory was created, recursively add it and all subdirs to the watcher.
	// This handles mkdir -p creating deep trees in one operation.
	// Also handles directory rename (Create event for new name).
	if event.Has(fsnotify.Create) && lerr == nil {
		// Reject symlinks — they could point outside the project boundary.
		if linfo.Mode()&os.ModeSymlink != 0 {
			m.log.Warn("ignoring symlink in project dir",
				"path", event.Name,
				"reason", "symlinks not mirrored (could escape project boundary)")
			return
		}
		if linfo.IsDir() {
			count, _ := m.addRecursive(event.Name, pw.filter, pw.project.LocalPath)
			if count > 0 {
				m.log.Debug("watching new dir tree", "path", event.Name, "dirs", count)
			}
			// After watching a renamed/moved-in directory, trigger full project sync
			// to pick up all files inside it (they don't get individual Create events).
			pw.syncChan <- msync.Task{
				Project: pw.project,
				RelPath: "", // full project sync
			}
			return
		}
	}

	// Compute relative path
	relPath, err := filepath.Rel(pw.project.LocalPath, event.Name)
	if err != nil {
		return
	}

	// Normalize separators
	relPath = filepath.ToSlash(relPath)

	// Check filter
	if pw.filter != nil && pw.filter.IsExcluded(relPath) {
		return
	}

	// Use Lstat info to check the object itself (not symlink target)
	if lerr == nil {
		// Skip symlinks — never mirror symlinks to remote
		if linfo.Mode()&os.ModeSymlink != 0 {
			m.log.Debug("skipping symlink file", "path", event.Name)
			return
		}
		// Skip non-regular files (named pipes, sockets, device nodes)
		if !linfo.Mode().IsRegular() {
			m.log.Debug("skipping non-regular file", "path", event.Name, "mode", linfo.Mode().String())
			return
		}
		// Skip files over size limit
		if linfo.Size() > pw.project.MaxFileSize() {
			return
		}
	}

	// Add to debounce pending map
	pw.mu.Lock()
	pw.pending[relPath] = time.Now()
	pw.mu.Unlock()
}

// handleRemove processes Remove events for files and directories.
func (m *Manager) handleRemove(event fsnotify.Event) {
	pw := m.findProject(event.Name)
	if pw == nil {
		return
	}

	relPath, err := filepath.Rel(pw.project.LocalPath, event.Name)
	if err != nil {
		return
	}
	relPath = filepath.ToSlash(relPath)

	// Clean up stale watchers if a directory was removed.
	// We can't os.Stat to check IsDir (path is gone), so try removing from watcher
	// unconditionally — fsw.Remove on a non-watched path is a no-op.
	removed := m.removeRecursive(event.Name)
	if removed > 0 {
		m.log.Debug("cleaned stale watchers", "path", event.Name, "removed", removed)
	}

	// Clear any pending debounce entries for paths under removed directory
	pw.mu.Lock()
	for pendingPath := range pw.pending {
		if pendingPath == relPath || isRelSubPath(pendingPath, relPath) {
			delete(pw.pending, pendingPath)
		}
	}
	pw.mu.Unlock()

	// Queue delete task if policy allows
	if m.deletePolicy != config.DeleteIgnore {
		if pw.filter != nil && pw.filter.IsExcluded(relPath) {
			return
		}
		pw.syncChan <- msync.Task{
			Project: pw.project,
			RelPath: relPath,
			Type:    msync.TaskDelete,
		}
	}
}

// handleRename processes Rename events (the old path is gone).
// On Windows, Rename fires for the old name; Create fires separately for the new name.
// We treat Rename as a removal of the old path.
func (m *Manager) handleRename(event fsnotify.Event) {
	pw := m.findProject(event.Name)
	if pw == nil {
		return
	}

	relPath, err := filepath.Rel(pw.project.LocalPath, event.Name)
	if err != nil {
		return
	}
	relPath = filepath.ToSlash(relPath)

	// Clean stale watchers (renamed directory no longer exists at old path)
	removed := m.removeRecursive(event.Name)
	if removed > 0 {
		m.log.Debug("cleaned stale watchers after rename", "path", event.Name, "removed", removed)
	}

	// Clear pending entries for old path
	pw.mu.Lock()
	delete(pw.pending, relPath)
	for pendingPath := range pw.pending {
		if isRelSubPath(pendingPath, relPath) {
			delete(pw.pending, pendingPath)
		}
	}
	pw.mu.Unlock()

	// Queue delete for old remote path if policy allows (file was renamed away)
	if m.deletePolicy != config.DeleteIgnore {
		if pw.filter != nil && pw.filter.IsExcluded(relPath) {
			return
		}
		pw.syncChan <- msync.Task{
			Project: pw.project,
			RelPath: relPath,
			Type:    msync.TaskDelete,
		}
		m.log.Debug("queued delete for renamed-away path", "project", pw.project.Name, "path", relPath)
	}
}

// isSyncIgnoreFile checks if the changed file is this project's .syncignore.
func (m *Manager) isSyncIgnoreFile(pw *projectWatcher, absPath string) bool {
	if pw.filter == nil {
		return false
	}
	syncPath := pw.filter.SyncIgnorePath()
	if syncPath == "" {
		return false
	}
	// Compare cleaned absolute paths
	a, err1 := filepath.Abs(absPath)
	b, err2 := filepath.Abs(syncPath)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// reloadFilter hot-reloads a project's .syncignore and triggers reconciliation if rules changed.
func (m *Manager) reloadFilter(pw *projectWatcher) {
	changed, err := pw.filter.Reload()
	if err != nil {
		m.log.Error("failed to reload .syncignore", "project", pw.project.Name, "error", err)
		return
	}
	if !changed {
		m.log.Debug(".syncignore unchanged after write", "project", pw.project.Name)
		return
	}

	m.log.Info(".syncignore changed, filters reloaded", "project", pw.project.Name)

	// Trigger full project reconciliation so that:
	// - Newly included files get synced
	// - Rclone filter file reflects updated rules
	pw.syncChan <- msync.Task{
		Project: pw.project,
		RelPath: "", // empty = full project sync
	}
}

// debounceLoop periodically emits matured pending files to the sync channel.
func (m *Manager) debounceLoop(ctx context.Context, pw *projectWatcher) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pw.mu.Lock()
			now := time.Now()
			for relPath, lastEvent := range pw.pending {
				if now.Sub(lastEvent) >= pw.project.DebounceDuration() {
					pw.syncChan <- msync.Task{
						Project: pw.project,
						RelPath: relPath,
					}
					delete(pw.pending, relPath)
				}
			}
			pw.mu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// healthMonitor runs periodic self-checks on the watcher subsystem.
func (m *Manager) healthMonitor(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			watchCount := m.WatchCount()
			m.log.Debug("health check", "watches", watchCount)

			// Check for watcher handle leaks (arbitrary but sane upper bound)
			if watchCount > 50000 {
				m.log.Warn("high watch count — possible handle leak", "watches", watchCount)
				m.healthErrorsMu.Lock()
				m.healthErrors = append(m.healthErrors, HealthError{
					Time:    time.Now(),
					Source:  "healthMonitor",
					Message: fmt.Sprintf("high watch count: %d (possible handle leak)", watchCount),
				})
				m.healthErrorsMu.Unlock()
			}

		case <-ctx.Done():
			return
		}
	}
}

// isSubPath checks if child is under parent directory.
func isSubPath(child, parent string) bool {
	childAbs, err := filepath.Abs(child)
	if err != nil {
		return false
	}
	parentAbs, err := filepath.Abs(parent)
	if err != nil {
		return false
	}
	// Normalize separators
	childAbs = filepath.Clean(childAbs)
	parentAbs = filepath.Clean(parentAbs)

	return childAbs == parentAbs || len(childAbs) > len(parentAbs) &&
		childAbs[:len(parentAbs)] == parentAbs &&
		(childAbs[len(parentAbs)] == filepath.Separator)
}

// isRelSubPath checks if child relative path is under parent relative path.
// Both should use forward slashes.
func isRelSubPath(child, parent string) bool {
	if parent == "." || parent == "" {
		return true
	}
	return len(child) > len(parent) &&
		child[:len(parent)] == parent &&
		child[len(parent)] == '/'
}
