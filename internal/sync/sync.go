// Package sync handles rclone invocation for file mirroring.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/metrics"
	"github.com/qraveh/SelectiveMirror/internal/state"
)

// TaskType distinguishes sync from delete operations.
type TaskType int

const (
	TaskSync   TaskType = iota // copy file to remote
	TaskDelete                 // delete or quarantine on remote
)

// Task represents a file to be synced or deleted.
type Task struct {
	Project config.Project
	RelPath string   // empty means full project sync
	Type    TaskType // TaskSync (default) or TaskDelete
}

// Engine processes sync tasks using rclone.
type Engine struct {
	cfg      *config.Global
	state    *state.Store
	filters  map[string]*filter.Engine // project name -> filter
	metrics  *metrics.Collector
	TaskChan chan Task
	log      *slog.Logger
}

// NewEngine creates a sync engine.
func NewEngine(cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, m *metrics.Collector) *Engine {
	return &Engine{
		cfg:      cfg,
		state:    st,
		filters:  filters,
		metrics:  m,
		TaskChan: make(chan Task, 1000),
		log:      slog.Default().With("component", "sync"),
	}
}

// Run processes sync tasks until context is cancelled.
// Recovers from panics in individual task processing to prevent daemon crash.
func (e *Engine) Run(ctx context.Context) {
	e.log.Info("sync engine started")
	for {
		select {
		case task := <-e.TaskChan:
			if e.metrics != nil {
				e.metrics.SetQueueDepth(int64(len(e.TaskChan)))
			}
			e.processTask(ctx, task)
		case <-ctx.Done():
			e.log.Info("sync engine stopping")
			return
		}
	}
}

// processTask handles a single task with panic recovery.
func (e *Engine) processTask(ctx context.Context, task Task) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			e.log.Error("PANIC in sync task", "project", task.Project.Name,
				"path", task.RelPath, "type", task.Type, "panic", r, "stack", stack)
			if e.metrics != nil {
				e.metrics.RecordError(task.Project.Name,
					fmt.Sprintf("panic processing %s: %v", task.RelPath, r))
			}
		}
	}()

	switch task.Type {
	case TaskDelete:
		e.deleteRemoteFile(ctx, task.Project, task.RelPath)
	default:
		if task.RelPath == "" {
			e.syncFullProject(ctx, task.Project)
		} else {
			e.syncSingleFile(ctx, task.Project, task.RelPath)
		}
	}
}

// quiesceFile confirms a file is stable before syncing.
// Returns the os.FileInfo if stable, or nil if the file is still changing or locked.
// Defense-in-depth: rejects symlinks, non-regular files, and other exotic objects
// even if they somehow bypassed the watcher's Lstat check.
func (e *Engine) quiesceFile(localPath string) (os.FileInfo, error) {
	// Lstat check: reject symlinks and non-regular files at the sync boundary.
	// This is defense-in-depth — the watcher should already filter these out,
	// but a race between file creation and event processing could let one through.
	linfo, err := os.Lstat(localPath)
	if err != nil {
		return nil, err
	}
	if linfo.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to sync symlink: %s", localPath)
	}
	if !linfo.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (mode %s): %s", linfo.Mode().String(), localPath)
	}

	info1 := linfo // first stat (already done via Lstat which returns same size/mtime for regular files)

	// Wait 200ms and re-check
	time.Sleep(200 * time.Millisecond)

	info2, err := os.Lstat(localPath)
	if err != nil {
		return nil, err
	}

	// If size or mtime changed, file is still being written
	if info1.Size() != info2.Size() || info1.ModTime() != info2.ModTime() {
		return nil, fmt.Errorf("file still changing")
	}

	// Try to open for shared read (detects locked files on Windows)
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.Open(localPath)
		if err == nil {
			f.Close()
			return info2, nil
		}
		if attempt < 2 {
			time.Sleep(1 * time.Second)
		}
	}

	return nil, fmt.Errorf("file locked after 3 attempts")
}

func (e *Engine) syncSingleFile(ctx context.Context, proj config.Project, relPath string) {
	localPath := filepath.Join(proj.LocalPath, relPath)

	// Quiescence check: confirm file is stable and not locked
	info, err := e.quiesceFile(localPath)
	if err != nil {
		if strings.Contains(err.Error(), "still changing") {
			// Re-enter debounce by not syncing; watcher will re-fire
			e.log.Debug("file still changing, deferring", "project", proj.Name, "path", relPath)
			return
		}
		e.log.Debug("file gone or locked before sync", "project", proj.Name, "path", relPath, "error", err)
		e.state.LogAction(proj.Name, relPath, "skip_gone", fmt.Sprintf("%v", err), 0)
		return
	}

	// Skip if too large
	if info.Size() > proj.MaxFileSize() {
		e.log.Debug("file too large", "project", proj.Name, "path", relPath, "size", info.Size())
		e.state.LogAction(proj.Name, relPath, "skip_size", fmt.Sprintf("%d bytes", info.Size()), 0)
		return
	}

	// Compute hash and check against state
	hash, size, err := state.HashFile(localPath)
	if err != nil {
		e.log.Warn("hash failed", "project", proj.Name, "path", relPath, "error", err)
		e.state.LogAction(proj.Name, relPath, "error", fmt.Sprintf("hash: %v", err), 0)
		if e.metrics != nil {
			e.metrics.RecordError(proj.Name, fmt.Sprintf("hash failed: %v", err))
		}
		return
	}

	// Check if unchanged since last sync
	existing, err := e.state.GetFileState(proj.Name, relPath)
	if err == nil && existing != nil && existing.LocalHash == hash && existing.RcloneExit == 0 {
		e.log.Debug("unchanged", "project", proj.Name, "path", relPath)
		return // already synced with same hash
	}

	// Build rclone command
	remotePath := proj.Remote + "/" + filepath.ToSlash(relPath)
	args := []string{"copyto", localPath, remotePath, "--checksum"}
	args = append(args, e.commonFlags()...)

	start := time.Now()
	exitCode := e.runRclone(ctx, args)
	elapsed := time.Since(start)

	e.state.UpdateFileState(proj.Name, relPath, hash, size, exitCode)

	if exitCode == 0 {
		e.log.Info("synced", "project", proj.Name, "path", relPath, "size", size, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "copy", fmt.Sprintf("%d bytes, %dms", size, elapsed.Milliseconds()), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordSync(proj.Name, size, elapsed.Milliseconds())
		}
	} else {
		e.log.Warn("sync failed", "project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordError(proj.Name, fmt.Sprintf("rclone exit %d for %s", exitCode, relPath))
		}
	}
}

func (e *Engine) syncFullProject(ctx context.Context, proj config.Project) {
	e.log.Info("full project sync", "project", proj.Name)
	start := time.Now()

	// Generate filter file
	fe, ok := e.filters[proj.Name]
	if !ok {
		e.log.Error("no filter engine for project", "project", proj.Name)
		return
	}

	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		e.log.Error("filter file generation failed", "project", proj.Name, "error", err)
		return
	}
	defer os.Remove(filterFile)

	args := []string{"copy", proj.LocalPath, proj.Remote, "--checksum", "--filter-from", filterFile}
	args = append(args, e.commonFlags()...)

	exitCode := e.runRclone(ctx, args)
	elapsed := time.Since(start)

	if exitCode == 0 {
		e.log.Info("full sync complete", "project", proj.Name, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, "", "full_sync", fmt.Sprintf("ok, %dms", elapsed.Milliseconds()), elapsed.Milliseconds())
		e.state.SetMeta("last_full_sync_"+proj.Name, time.Now().UTC().Format(time.RFC3339))
	} else {
		e.log.Warn("full sync failed", "project", proj.Name, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, "", "full_sync_error", fmt.Sprintf("rclone exit %d, %dms", exitCode, elapsed.Milliseconds()), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordError(proj.Name, fmt.Sprintf("full sync rclone exit %d", exitCode))
		}
	}
}

// deleteRemoteFile handles file deletion on remote based on delete policy.
func (e *Engine) deleteRemoteFile(ctx context.Context, proj config.Project, relPath string) {
	policy := e.cfg.DeletePolicy()
	remotePath := proj.Remote + "/" + filepath.ToSlash(relPath)

	switch policy {
	case config.DeleteMirror:
		args := []string{"deletefile", remotePath}
		args = append(args, e.commonFlags()...)

		start := time.Now()
		exitCode := e.runRclone(ctx, args)
		elapsed := time.Since(start)

		if exitCode == 0 {
			e.log.Info("remote deleted", "project", proj.Name, "path", relPath, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "delete", "mirrored delete", elapsed.Milliseconds())
		} else {
			e.log.Warn("remote delete failed", "project", proj.Name, "path", relPath, "exit", exitCode)
			e.state.LogAction(proj.Name, relPath, "delete_error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		}

	case config.DeleteQuarantine:
		ts := time.Now().UTC().Format("20060102T150405Z")
		quarantinePath := proj.Remote + "/.quarantine/" + filepath.ToSlash(relPath) + "." + ts
		args := []string{"moveto", remotePath, quarantinePath}
		args = append(args, e.commonFlags()...)

		start := time.Now()
		exitCode := e.runRclone(ctx, args)
		elapsed := time.Since(start)

		if exitCode == 0 {
			e.log.Info("remote quarantined", "project", proj.Name, "path", relPath, "quarantine", quarantinePath, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "quarantine", quarantinePath, elapsed.Milliseconds())
		} else {
			e.log.Warn("remote quarantine failed", "project", proj.Name, "path", relPath, "exit", exitCode)
			e.state.LogAction(proj.Name, relPath, "quarantine_error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		}

	default:
		// DeleteIgnore — do nothing
		e.log.Debug("local delete ignored (policy=ignore)", "project", proj.Name, "path", relPath)
		e.state.LogAction(proj.Name, relPath, "delete_ignored", "policy=ignore", 0)
	}
}

func (e *Engine) commonFlags() []string {
	flags := []string{
		"--retries", "3",
		"--retries-sleep", "10s",
		"--stats", "0",
		"--log-level", "NOTICE",
		"--skip-links", // Never follow symlinks — they could point outside the project
	}
	if e.cfg.BandwidthLimit != "" {
		flags = append(flags, "--bwlimit", e.cfg.BandwidthLimit)
	}
	flags = append(flags, e.cfg.RcloneExtraFlags...)
	return flags
}

func (e *Engine) runRclone(ctx context.Context, args []string) int {
	rclonePath := e.cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}

	e.log.Debug("rclone", "cmd", rclonePath, "args", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, rclonePath, args...)
	cmd.Stdout = os.Stdout // Let rclone output flow through in foreground mode
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		e.log.Error("rclone exec failed", "error", err)
		return -1
	}
	return 0
}

// DryRun lists what would be synced for a project without actually syncing.
func (e *Engine) DryRun(ctx context.Context, proj config.Project) error {
	fe, ok := e.filters[proj.Name]
	if !ok {
		return fmt.Errorf("no filter engine for project %q", proj.Name)
	}

	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		return fmt.Errorf("filter file: %w", err)
	}
	defer os.Remove(filterFile)

	args := []string{"copy", proj.LocalPath, proj.Remote, "--checksum", "--filter-from", filterFile, "--dry-run", "--log-level", "INFO"}
	// Don't append commonFlags for dry-run (they include --log-level which conflicts)
	if e.cfg.BandwidthLimit != "" {
		args = append(args, "--bwlimit", e.cfg.BandwidthLimit)
	}

	fmt.Printf("=== Dry run: %s ===\n", proj.Name)
	fmt.Printf("Source: %s\n", proj.LocalPath)
	fmt.Printf("Destination: %s\n", proj.Remote)
	fmt.Printf("Running: %s %s\n\n", e.cfg.RclonePath, strings.Join(args, " "))

	exitCode := e.runRclone(ctx, args)
	if exitCode != 0 {
		return fmt.Errorf("rclone exit code %d", exitCode)
	}
	return nil
}

// Validate checks rclone availability and remote connectivity.
func Validate(cfg *config.Global) error {
	rclonePath := cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}

	// Check rclone exists
	cmd := exec.Command(rclonePath, "version")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("rclone not found at %q: %w", rclonePath, err)
	}
	fmt.Printf("rclone: %s\n", strings.TrimSpace(strings.Split(string(out), "\n")[0]))

	// Check each project's remote
	for _, proj := range cfg.Projects {
		remote := proj.Remote
		// Extract remote name (everything before :)
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) < 2 {
			return fmt.Errorf("project %q: invalid remote format %q (expected remote:path)", proj.Name, remote)
		}

		fmt.Printf("Checking %s -> %s ... ", proj.Name, remote)
		cmd := exec.Command(rclonePath, "lsd", remote, "--max-depth", "0")
		if err := cmd.Run(); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("project %q: remote %q unreachable: %w", proj.Name, remote, err)
		}
		fmt.Println("OK")
	}

	return nil
}

// RemoteFile represents a file on the remote backend (from rclone lsjson).
type RemoteFile struct {
	Path    string            `json:"Path"`
	Name    string            `json:"Name"`
	Size    int64             `json:"Size"`
	IsDir   bool              `json:"IsDir"`
	Hashes  map[string]string `json:"Hashes"`
	ModTime string            `json:"ModTime"`
}

// ListRemote returns all files on the remote for a project using rclone lsjson.
func ListRemote(cfg *config.Global, proj config.Project) ([]RemoteFile, error) {
	rclonePath := cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}

	cmd := exec.Command(rclonePath, "lsjson", proj.Remote, "--recursive", "--hash", "--no-mimetype", "--no-modtime")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rclone lsjson: %w", err)
	}

	var files []RemoteFile
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, fmt.Errorf("parsing lsjson: %w", err)
	}
	return files, nil
}
