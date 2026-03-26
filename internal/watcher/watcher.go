// Package watcher provides filesystem monitoring with per-project debounce.
package watcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	gosync "sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// Manager manages filesystem watchers for all projects.
type Manager struct {
	projects []projectWatcher
	fsw      *fsnotify.Watcher
	log      *slog.Logger
}

type projectWatcher struct {
	project  config.Project
	filter   *filter.Engine
	syncChan chan<- msync.Task
	pending  map[string]time.Time
	mu       gosync.Mutex
}

// NewManager creates a watcher manager for all configured projects.
func NewManager(projects []config.Project, filters map[string]*filter.Engine, syncChan chan<- msync.Task) (*Manager, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	m := &Manager{
		fsw: fsw,
		log: slog.Default().With("component", "watcher"),
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

	// Start event processing goroutine
	go m.eventLoop(ctx)

	// Start debounce goroutines for each project
	for i := range m.projects {
		pw := &m.projects[i]
		go m.debounceLoop(ctx, pw)
	}

	return nil
}

// Stop closes the filesystem watcher.
func (m *Manager) Stop() {
	m.fsw.Close()
}

// addRecursive adds a directory and all subdirectories to the watcher.
func (m *Manager) addRecursive(root string, fe *filter.Engine) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible directories
		}
		if !d.IsDir() {
			return nil
		}

		// Check if directory is excluded
		relPath, _ := filepath.Rel(root, path)
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
			m.handleEvent(event)
		case err, ok := <-m.fsw.Errors:
			if !ok {
				return
			}
			m.log.Error("watcher error", "error", err)
		case <-ctx.Done():
			return
		}
	}
}

func (m *Manager) handleEvent(event fsnotify.Event) {
	// Only care about create, write, rename
	if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) && !event.Has(fsnotify.Rename) {
		return
	}

	pw := m.findProject(event.Name)
	if pw == nil {
		return
	}

	// If a new directory was created, add it to the watcher
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			relPath, _ := filepath.Rel(pw.project.LocalPath, event.Name)
			if pw.filter == nil || !pw.filter.IsExcluded(relPath+"/") {
				m.fsw.Add(event.Name)
				m.log.Debug("watching new dir", "path", event.Name)
			}
			return // directories themselves don't need syncing
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

	// Check file size (best effort — file may still be writing)
	if info, err := os.Stat(event.Name); err == nil {
		if !info.Mode().IsRegular() {
			return // skip non-regular files (symlinks, devices, etc.)
		}
		if info.Size() > pw.project.MaxFileSize() {
			return
		}
	}

	// Add to debounce pending map
	pw.mu.Lock()
	pw.pending[relPath] = time.Now()
	pw.mu.Unlock()
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
