// Package watcher provides filesystem monitoring with FairQueue-based task dispatch.
package watcher

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	gosync "sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// Manager manages filesystem watchers for all projects.
type Manager struct {
	projects     []*projectWatcher
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
	Source  string // "eventLoop", "timerCleanup", "syncEngine", etc.
	Message string
}

type projectWatcher struct {
	project  config.Project
	filter   *filter.Engine
	queue   *msync.FairQueue
	pending map[string]*time.Timer // per-file debounce timers (static mode only)
	mu      gosync.Mutex

	// Burst-delete detection: count deletes within a rolling window.
	// When threshold is reached, schedule an accelerated reconciliation
	// to catch any fsnotify events dropped under burst load (SM-050).
	deleteCount     int
	deleteWindowEnd time.Time
}

// NewManager creates a watcher manager for all configured projects.
func NewManager(projects []config.Project, filters map[string]*filter.Engine, queue *msync.FairQueue, deletePolicy config.DeletePolicy) (*Manager, error) {
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
		pw := &projectWatcher{
			project: proj,
			filter:  fe,
			queue:   queue,
			pending: make(map[string]*time.Timer),
		}
		m.projects = append(m.projects, pw)
	}

	return m, nil
}

// Start begins watching all project directories.
func (m *Manager) Start(ctx context.Context) error {
	// Add all project directories
	for i := range m.projects {
		pw := m.projects[i]
		if supportsRecursiveWatch {
			// On Windows: single recursive watch per project root.
			// ReadDirectoryChangesW with bWatchSubtree=TRUE monitors the entire
			// subtree through ONE handle on the project root. Subdirectories have
			// no open handles and can be freely renamed/moved/deleted in Explorer.
			recursivePath := pw.project.LocalPath + string(os.PathSeparator) + "..."
			if err := m.fsw.Add(recursivePath); err != nil {
				m.log.Error("failed to watch directory (recursive)", "project", pw.project.Name, "path", pw.project.LocalPath, "error", err)
				continue
			}
			m.log.Info("watching (recursive)", "project", pw.project.Name, "path", pw.project.LocalPath)
		} else {
			// On Linux/macOS: manually walk and add each subdirectory (inotify/kqueue
			// don't support recursive watching).
			count, err := m.addRecursive(pw.project.LocalPath, pw.filter)
			if err != nil {
				m.log.Error("failed to watch directory", "project", pw.project.Name, "path", pw.project.LocalPath, "error", err)
				continue
			}
			m.log.Info("watching", "project", pw.project.Name, "path", pw.project.LocalPath, "dirs", count)
		}
	}

	// Start event processing goroutine (with panic recovery)
	go m.safeGo("eventLoop", func() { m.eventLoop(ctx) })

	// Start timer-cleanup goroutines for each project (with panic recovery)
	for i := range m.projects {
		pw := m.projects[i]
		name := fmt.Sprintf("timerCleanup[%s]", pw.project.Name)
		go m.safeGo(name, func() { m.timerCleanupLoop(ctx, pw) })
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
	start := time.Now()
	defer func() {
		m.log.Debug("addRecursive complete", "path", walkRoot, "ms", time.Since(start).Milliseconds())
	}()

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
		pw := m.projects[i]
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

	// If a new directory was created, handle watcher setup and trigger reconciliation.
	if event.Has(fsnotify.Create) && lerr == nil {
		// Symlink-to-directory: do NOT follow (would watch arbitrary paths outside project).
		// Symlink-to-file: allow — will be synced as a regular file (target content copied).
		if linfo.Mode()&os.ModeSymlink != 0 {
			target, serr := os.Stat(event.Name) // follow the symlink
			if serr != nil || target.IsDir() {
				m.log.Debug("ignoring symlink to directory or broken symlink",
					"path", event.Name)
				return
			}
			// Symlink to file — fall through to enqueue/sync as regular file
		} else if linfo.IsDir() {
			if !supportsRecursiveWatch {
				// On Linux/macOS: manually add new subdirectories to the watcher.
				// This handles mkdir -p creating deep trees in one operation.
				count, _ := m.addRecursive(event.Name, pw.filter, pw.project.LocalPath)
				if count > 0 {
					m.log.Debug("watching new dir tree", "path", event.Name, "dirs", count)
				}
			}
			// Walk the new/renamed/moved-in directory and queue individual file
			// syncs. This is much faster and more deterministic than a full
			// project sync (rclone copy of entire project tree).
			m.queueFilesInDir(pw, event.Name)
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
		// Symlinks to files: follow the target to get real size for limit check.
		// Symlinks to directories were already rejected above.
		checkInfo := linfo
		if linfo.Mode()&os.ModeSymlink != 0 {
			target, serr := os.Stat(event.Name) // follow symlink
			if serr != nil || !target.Mode().IsRegular() {
				m.log.Debug("skipping broken or non-file symlink", "path", event.Name)
				return
			}
			checkInfo = target
		}
		// Skip non-regular files (named pipes, sockets, device nodes)
		if !checkInfo.Mode().IsRegular() {
			m.log.Debug("skipping non-regular file", "path", event.Name, "mode", checkInfo.Mode().String())
			return
		}
		// Skip files over size limit
		if checkInfo.Size() > pw.project.MaxFileSize() {
			return
		}
	}

	// Two dispatch modes based on project configuration.
	//
	// Static (debounce_sec > 0): quiet-window timer before enqueuing.
	// Good for Office-style saves and chatty build tools.
	//
	// Queue-based (debounce_sec = 0, default): enqueue immediately.
	// FairQueue handles dedup (move-to-back) and fairness.
	debounceDur := pw.project.DebounceDuration()

	if debounceDur > 0 {
		// --- Static debounce (for Office-style saves) ---
		// Keeps quiet-window timer: waits until file stops changing before enqueuing.
		pw.mu.Lock()
		if t, ok := pw.pending[relPath]; ok {
			t.Reset(debounceDur)
		} else {
			rp := relPath
			pw.pending[relPath] = time.AfterFunc(debounceDur, func() {
				pw.mu.Lock()
				delete(pw.pending, rp)
				pw.mu.Unlock()
				pw.queue.Enqueue(msync.Task{Project: pw.project, RelPath: rp})
			})
		}
		pw.mu.Unlock()
	} else {
		// --- Queue-based fairness (default) ---
		// Enqueue immediately. FairQueue handles dedup (move-to-back) and fairness.
		// No timers, no lastSynced tracking — queue position IS the cooldown.
		pw.queue.Enqueue(msync.Task{Project: pw.project, RelPath: relPath})
	}
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
	// Only needed on non-Windows — recursive watching handles this automatically.
	if !supportsRecursiveWatch {
		removed := m.removeRecursive(event.Name)
		if removed > 0 {
			m.log.Debug("cleaned stale watchers", "path", event.Name, "removed", removed)
		}
	}

	// Cancel and clear any pending static-mode timers for paths under removed directory
	pw.mu.Lock()
	for pendingPath, timer := range pw.pending {
		if pendingPath == relPath || isRelSubPath(pendingPath, relPath) {
			timer.Stop()
			delete(pw.pending, pendingPath)
		}
	}
	pw.mu.Unlock()

	// Queue delete task if policy allows
	if m.deletePolicy != config.DeleteIgnore {
		if pw.filter != nil && pw.filter.IsExcluded(relPath) {
			return
		}
		pw.queue.EnqueuePriority(msync.Task{
			Project: pw.project,
			RelPath: relPath,
			Type:    msync.TaskDelete,
		})
	}

	// Burst-delete detection (SM-050): when many deletes arrive within a short
	// window, fsnotify may silently drop some events (finite buffer). Schedule
	// an accelerated full-project reconciliation to catch dropped deletes.
	m.trackDeleteBurst(pw)
}

// handleRename processes Rename events (the old path is gone).
const (
	burstDeleteThreshold = 10              // deletes within window to trigger reconciliation
	burstDeleteWindow    = 5 * time.Second // rolling window for burst detection
	burstReconcileDelay  = 30 * time.Second // delay before accelerated reconciliation (SM-057: should be quiescence-based)
)

// trackDeleteBurst detects burst deletes and schedules accelerated reconciliation.
// fsnotify can silently drop events when its buffer overflows under rapid deletions.
// When we see N+ deletes within a short window, we schedule a full project sync
// shortly after to catch any dropped events (SM-050).
func (m *Manager) trackDeleteBurst(pw *projectWatcher) {
	pw.mu.Lock()
	now := time.Now()
	if now.After(pw.deleteWindowEnd) {
		// Start a new window
		pw.deleteCount = 1
		pw.deleteWindowEnd = now.Add(burstDeleteWindow)
	} else {
		pw.deleteCount++
	}
	count := pw.deleteCount
	pw.mu.Unlock()

	if count == burstDeleteThreshold {
		m.log.Info("burst delete detected, scheduling accelerated reconciliation",
			"project", pw.project.Name, "deletes", count,
			"delay", burstReconcileDelay)
		go func() {
			time.Sleep(burstReconcileDelay)
			pw.queue.Enqueue(msync.Task{Project: pw.project, RelPath: ""})
			m.log.Info("accelerated reconciliation triggered",
				"project", pw.project.Name)
		}()
	}
}

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

	// Clean stale watchers (renamed directory no longer exists at old path).
	// Only needed on non-Windows — recursive watching handles this automatically.
	if !supportsRecursiveWatch {
		removed := m.removeRecursive(event.Name)
		if removed > 0 {
			m.log.Debug("cleaned stale watchers after rename", "path", event.Name, "removed", removed)
		}
	}

	// Cancel and clear pending timers for old path
	pw.mu.Lock()
	if t, ok := pw.pending[relPath]; ok {
		t.Stop()
		delete(pw.pending, relPath)
	}
	for pendingPath, timer := range pw.pending {
		if isRelSubPath(pendingPath, relPath) {
			timer.Stop()
			delete(pw.pending, pendingPath)
		}
	}
	pw.mu.Unlock()

	// Always clean up old remote path on rename, regardless of delete policy.
	// Rename is not a user-initiated deletion — it's a lifecycle transition.
	// The old name is an orphan on the remote that must be removed.
	// (Delete policy only governs Remove events — actual file deletions.)
	if pw.filter != nil && pw.filter.IsExcluded(relPath) {
		return
	}
	pw.queue.EnqueuePriority(msync.Task{
		Project:     pw.project,
		RelPath:     relPath,
		Type:        msync.TaskDelete,
		ForceDelete: true, // bypass delete_policy — rename cleanup is mandatory
	})
	m.log.Debug("queued cleanup for renamed-away path", "project", pw.project.Name, "path", relPath)
}

// queueFilesInDir walks a directory and queues individual file sync tasks for
// every regular file inside it. Used when a directory is created, renamed, or
// moved into the project — the files inside don't get individual Create events.
func (m *Manager) queueFilesInDir(pw *projectWatcher, dirPath string) {
	start := time.Now()
	queued := 0
	filepath.WalkDir(dirPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			relPath, _ := filepath.Rel(pw.project.LocalPath, path)
			if relPath != "." && pw.filter != nil && pw.filter.IsExcluded(relPath+"/") {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip symlinks and non-regular files
		if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}

		relPath, _ := filepath.Rel(pw.project.LocalPath, path)
		relPath = filepath.ToSlash(relPath)

		if pw.filter != nil && pw.filter.IsExcluded(relPath) {
			return nil
		}

		// Check file size
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > pw.project.MaxFileSize() {
			return nil
		}

		// Queue directly — the files already exist and are stable
		pw.queue.Enqueue(msync.Task{
			Project: pw.project,
			RelPath: relPath,
		})
		queued++
		return nil
	})
	elapsed := time.Since(start)
	if queued > 0 {
		m.log.Debug("queued files from new/renamed dir", "path", dirPath, "files", queued, "ms", elapsed.Milliseconds())
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
	start := time.Now()
	changed, err := pw.filter.Reload()
	if err != nil {
		m.log.Error("failed to reload .syncignore", "project", pw.project.Name, "error", err)
		return
	}
	if !changed {
		m.log.Debug(".syncignore unchanged after write", "project", pw.project.Name)
		return
	}

	m.log.Info(".syncignore changed, filters reloaded", "project", pw.project.Name, "ms", time.Since(start).Milliseconds())

	// Trigger full project reconciliation so that:
	// - Newly included files get synced
	// - Rclone filter file reflects updated rules
	// Enqueue full-project reconciliation to apply updated filter rules.
	pw.queue.Enqueue(msync.Task{
		Project: pw.project,
		RelPath: "", // empty = full project sync
	})
}

// timerCleanupLoop waits for context cancellation and cleans up pending static-mode timers.
func (m *Manager) timerCleanupLoop(ctx context.Context, pw *projectWatcher) {
	<-ctx.Done()
	// Stop all pending timers on shutdown
	pw.mu.Lock()
	for path, timer := range pw.pending {
		timer.Stop()
		delete(pw.pending, path)
	}
	pw.mu.Unlock()
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
// On Windows, comparison is case-insensitive (NTFS/FAT are case-insensitive).
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

	// On Windows, paths are case-insensitive (NTFS, FAT32, exFAT).
	if runtime.GOOS == "windows" {
		childAbs = strings.ToLower(childAbs)
		parentAbs = strings.ToLower(parentAbs)
	}

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
