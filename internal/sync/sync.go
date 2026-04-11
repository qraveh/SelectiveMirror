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
	gosync "sync"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/anomaly"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/hooks"
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
	Project     config.Project
	RelPath     string   // empty means full project sync
	Type        TaskType // TaskSync (default) or TaskDelete
	ForceDelete bool     // true for rename cleanup: always delete old remote path regardless of delete policy
	Done        func()   // optional completion callback (e.g., WaitGroup.Done); called after task finishes
}

// RcloneRunner executes an rclone command and returns the exit code.
// The default implementation spawns a subprocess; tests inject a fake.
type RcloneRunner func(ctx context.Context, args []string) int

// RemoteLister returns the list of files on the remote for a project.
// The default implementation calls rclone lsjson; tests inject a fake.
type RemoteLister func(cfg *config.Global, proj config.Project) ([]RemoteFile, error)

// Engine processes sync tasks using rclone.
type Engine struct {
	cfg      *config.Global
	state    *state.Store
	filters  map[string]*filter.Engine // project name -> filter
	metrics  *metrics.Collector
	Queue    *FairQueue
	log      *slog.Logger

	// RunRcloneFunc executes rclone. If nil, uses the default subprocess runner.
	RunRcloneFunc RcloneRunner

	// ListRemoteFunc lists files on the remote. If nil, uses the default rclone lsjson implementation.
	ListRemoteFunc RemoteLister

	// Anomaly is the optional anomaly recorder. Nil-safe (no-op when nil).
	Anomaly *anomaly.Recorder

	// Hooks is the optional hook runner. Nil-safe (no-op when nil).
	Hooks *hooks.Runner

	// Per-file locks prevent two workers from syncing the same file simultaneously.
	// Key: "project:relPath". Full-project syncs (relPath="") use project name as key.
	fileLocks gosync.Map // map[string]*gosync.Mutex
}

// NewEngine creates a sync engine.
func NewEngine(cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, m *metrics.Collector) *Engine {
	e := &Engine{
		cfg:     cfg,
		state:   st,
		filters: filters,
		metrics: m,
		Queue:   NewFairQueue(0, 30*time.Second),
		log:     slog.Default().With("component", "sync"),
	}
	e.RunRcloneFunc = e.defaultRunRclone
	return e
}

// Run spawns concurrent workers to process sync tasks until context is cancelled.
func (e *Engine) Run(ctx context.Context) {
	workers := e.cfg.Workers()
	e.log.Info("sync engine started", "workers", workers)

	var wg gosync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			e.runWorker(ctx, id)
		}(i)
	}

	wg.Wait()
	e.log.Info("sync engine stopped")
}

func (e *Engine) runWorker(ctx context.Context, id int) {
	for {
		task, ok := e.Queue.Dequeue(ctx)
		if !ok {
			return // queue closed or context cancelled
		}
		if e.metrics != nil {
			e.metrics.SetQueueDepth(int64(e.Queue.Len()))
		}
		e.processTask(ctx, task)
	}
}

// lockKey returns the per-file lock key for a task.
func (e *Engine) lockKey(task Task) string {
	if task.RelPath == "" {
		return task.Project.Name // full-project sync locks by project
	}
	return task.Project.Name + ":" + task.RelPath
}

// acquireFileLock gets or creates a mutex for the given task and locks it.
// Mutexes are kept in the map permanently (not deleted after unlock) to prevent
// a race where Delete-after-Unlock causes two goroutines to hold different
// mutexes for the same key, breaking mutual exclusion.
func (e *Engine) acquireFileLock(task Task) {
	key := e.lockKey(task)
	val, _ := e.fileLocks.LoadOrStore(key, &gosync.Mutex{})
	mu := val.(*gosync.Mutex)
	mu.Lock()
}

// releaseFileLock unlocks the per-file mutex.
func (e *Engine) releaseFileLock(task Task) {
	key := e.lockKey(task)
	val, ok := e.fileLocks.Load(key)
	if ok {
		val.(*gosync.Mutex).Unlock()
	}
}

// processTask handles a single task with per-file locking and panic recovery.
func (e *Engine) processTask(ctx context.Context, task Task) {
	// Signal completion to caller (e.g., reconcileAll waiting via WaitGroup).
	if task.Done != nil {
		defer task.Done()
	}

	// Per-file lock: prevent two workers from syncing the same file simultaneously.
	e.acquireFileLock(task)
	defer e.releaseFileLock(task)

	taskStart := time.Now()
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			e.log.Error("PANIC in sync task", "project", task.Project.Name,
				"path", task.RelPath, "type", task.Type, "panic", r, "stack", stack)
			if e.metrics != nil {
				e.metrics.RecordError(task.Project.Name,
					fmt.Sprintf("panic processing %s: %v", task.RelPath, r))
			}
			e.Anomaly.Record(anomaly.KindPanic, task.Project.Name, task.RelPath,
				fmt.Sprintf("panic: %v", r), stack)
		}
		// Task duration is logged at DEBUG level for observability.
		// We do NOT emit warnings or anomalies based on duration alone — a slow
		// sync with many files is normal, not anomalous. Real timeout anomalies
		// are emitted when rclone hits the 5-minute deadline (exit -2), which
		// means the process was killed — that's a genuine problem.
		// Previous timer-based warnings (5s, then 30s/120s) produced thousands
		// of false alarms because they measured wall clock without understanding
		// the workload (file count, total bytes, filter changes, cold start).
		elapsed := time.Since(taskStart)
		e.log.Debug("task completed", "project", task.Project.Name, "path", task.RelPath,
			"elapsed", elapsed.Round(time.Millisecond))
	}()

	// Pre-sync hook (FR-ASP-17)
	if task.Type != TaskDelete && task.RelPath != "" {
		hookCmd := task.Project.EffectivePreSyncHook(e.cfg)
		_ = e.Hooks.Run(ctx, hookCmd, hooks.Env{
			Project: task.Project.Name,
			File:    task.RelPath,
			Remote:  task.Project.Remote,
			Event:   "pre_sync",
		})
	}

	switch task.Type {
	case TaskDelete:
		e.deleteRemoteFile(ctx, task.Project, task.RelPath, task.ForceDelete)
	default:
		if task.RelPath == "" {
			e.syncFullProject(ctx, task.Project)
		} else {
			e.syncSingleFile(ctx, task.Project, task.RelPath)
		}
	}

	// Post-sync hook (FR-ASP-17)
	if task.Type != TaskDelete && task.RelPath != "" {
		hookCmd := task.Project.EffectivePostSyncHook(e.cfg)
		_ = e.Hooks.Run(ctx, hookCmd, hooks.Env{
			Project: task.Project.Name,
			File:    task.RelPath,
			Remote:  task.Project.Remote,
			Event:   "post_sync",
		})
	}
}

// quiesceFile confirms a file is stable before syncing.
// Returns the os.FileInfo if stable, or nil if the file is still changing or locked.
// For symlinks to files, follows the link and checks the target.
// Rejects symlinks to directories, non-regular files, and broken symlinks.
func (e *Engine) quiesceFile(localPath string) (os.FileInfo, error) {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			e.log.Warn("slow quiescence", "path", localPath, "ms", elapsed.Milliseconds())
		}
	}()

	// SM-085: Resolve symlinks once upfront, then use the resolved path for all
	// subsequent operations. This closes the TOCTOU window where a symlink target
	// could be swapped between check and use.
	readPath := localPath // path used for Stat/Open (resolved target if symlink)

	linfo, err := os.Lstat(localPath)
	if err != nil {
		return nil, err
	}

	var info1 os.FileInfo
	if linfo.Mode()&os.ModeSymlink != 0 {
		// Resolve to absolute target path once — all further I/O uses this.
		resolved, serr := filepath.EvalSymlinks(localPath)
		if serr != nil {
			return nil, fmt.Errorf("broken symlink: %s -> %v", localPath, serr)
		}
		target, serr := os.Lstat(resolved) // Lstat the resolved path (no more following)
		if serr != nil {
			return nil, fmt.Errorf("broken symlink target: %s -> %v", resolved, serr)
		}
		if target.IsDir() {
			return nil, fmt.Errorf("symlink to directory not synced: %s", localPath)
		}
		info1 = target
		readPath = resolved
	} else {
		info1 = linfo
	}

	if !info1.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file (mode %s): %s", info1.Mode().String(), localPath)
	}

	// Wait 200ms and re-check using the resolved path (not the mutable symlink)
	time.Sleep(200 * time.Millisecond)

	info2, err := os.Lstat(readPath)
	if err != nil {
		return nil, err
	}

	// If size or mtime changed, file is still being written
	if info1.Size() != info2.Size() || info1.ModTime() != info2.ModTime() {
		return nil, fmt.Errorf("file still changing")
	}

	// Try to open the resolved path for shared read (detects locked files on Windows)
	for attempt := 0; attempt < 3; attempt++ {
		f, err := os.Open(readPath)
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
	// Re-check filter: the file may have been accepted by the watcher before
	// a .syncignore hot-reload excluded it (SM-045).
	if fe, ok := e.filters[proj.Name]; ok && fe != nil && fe.IsExcluded(relPath) {
		e.log.Debug("excluded after filter reload", "project", proj.Name, "path", relPath)
		return
	}

	localPath := filepath.Join(proj.LocalPath, relPath)

	// SM-087: Bounds check — reject paths that escape the project root.
	// Defense-in-depth: relPath currently comes from filepath.Rel on fsnotify events
	// (always under watched dir), but this guard protects against future input sources.
	if absLocal, err := filepath.Abs(localPath); err == nil {
		if absProj, err := filepath.Abs(proj.LocalPath); err == nil {
			if !strings.HasPrefix(absLocal+string(filepath.Separator), absProj+string(filepath.Separator)) && absLocal != absProj {
				e.log.Error("path escape blocked", "project", proj.Name, "path", relPath, "resolved", absLocal)
				e.Anomaly.Record(anomaly.KindSyncFailure, proj.Name, relPath,
					"path escape blocked: resolved path outside project root", absLocal)
				return
			}
		}
	}

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
		// Content is identical. Check whether mtime changed (metadata-only update).
		currentMtimeNs := info.ModTime().UnixNano()

		switch {
		case existing.MtimeNs == 0:
			// First observation after schema migration — record mtime silently, no remote call needed.
			e.state.UpdateMtimeOnly(proj.Name, relPath, currentMtimeNs)
			e.log.Debug("bootstrapped mtime tracking", "project", proj.Name, "path", relPath)

		case existing.MtimeNs == currentMtimeNs:
			// True no-op: hash and mtime both unchanged.
			e.log.Debug("unchanged", "project", proj.Name, "path", relPath)

		default:
			// Hash unchanged but mtime differs — metadata-only update.
			e.syncMtime(ctx, proj, relPath, info.ModTime(), hash, size, currentMtimeNs)
		}
		return
	}

	// Content-addressed skip: if the remote already has this exact content
	// (verified by lsjson or optimistically set after prior sync), skip the
	// rclone call entirely. This eliminates network I/O for files that changed
	// metadata (mtime, permissions) but not content, and prevents "big cold area"
	// re-uploads during batch reconciliation on backends that don't support mtime.
	if existing != nil && existing.RemoteHash != "" && existing.RemoteHash == hash {
		e.log.Debug("content-addressed skip: remote already has this hash",
			"project", proj.Name, "path", relPath, "hash", hash[:8])
		// Update local state to reflect current local file state
		e.state.UpdateFileState(proj.Name, relPath, hash, size, info.ModTime().UnixNano(), 0)
		e.state.LogAction(proj.Name, relPath, "skip_hash_match", "remote already has content", 0)
		return
	}

	// Build rclone command for content sync
	remotePath := proj.Remote + "/" + filepath.ToSlash(relPath)
	args := []string{"copyto", localPath, remotePath}
	args = append(args, e.commonFlags(proj)...)

	start := time.Now()
	exitCode := e.runRclone(ctx, args)

	// SM-100: If context was cancelled (graceful shutdown) or rclone was killed
	// by signal (exit >= 128), don't retry, don't record failure, just return.
	if exitCode == -3 || ctx.Err() != nil {
		e.log.Debug("sync cancelled (shutdown)", "project", proj.Name, "path", relPath)
		return
	}

	// FR-SYNC-16: Retry once on transient failure.
	// Exit 1 = general error (often transient network), exit 5 = temporary error (rclone docs:
	// "more retries might fix it" — API rate limits, server-side throttling).
	// Don't retry: exit 3 (dir not found), exit 9 (no transfer), negative (timeout/exec failure).
	if exitCode == 1 || exitCode == 5 {
		e.log.Info("transient failure, retrying once", "project", proj.Name, "path", relPath, "exit", exitCode)
		time.Sleep(2 * time.Second)
		exitCode = e.runRclone(ctx, args)
		// Re-check cancellation after retry
		if exitCode == -3 || ctx.Err() != nil {
			e.log.Debug("sync cancelled during retry (shutdown)", "project", proj.Name, "path", relPath)
			return
		}
	}
	elapsed := time.Since(start)

	e.state.UpdateFileState(proj.Name, relPath, hash, size, info.ModTime().UnixNano(), exitCode)

	if exitCode == 0 {
		e.log.Info("synced", "project", proj.Name, "path", relPath, "size", size, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "copy", fmt.Sprintf("%d bytes, %dms", size, elapsed.Milliseconds()), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordSync(proj.Name, size, elapsed.Milliseconds())
		}
		// Optimistically set remote_hash: rclone succeeded, so remote now has
		// this content. This enables content-addressed skip on the next sync
		// and closes the A→B→A window (Case 4 in edge case analysis).
		if err := e.state.UpdateRemoteVerification(proj.Name, relPath, hash, size); err != nil {
			e.log.Warn("state: update remote verification", "project", proj.Name, "path", relPath, "err", err)
		}
		e.Queue.SetAdaptiveCooldown(proj.Name+":"+relPath, elapsed)
		e.Queue.RecordSuccess(proj.Name)
	} else {
		e.log.Warn("sync failed", "project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordError(proj.Name, fmt.Sprintf("rclone exit %d for %s", exitCode, relPath))
		}
		if exitCode == -2 {
			e.Anomaly.Record(anomaly.KindSyncTimeout, proj.Name, relPath,
				"rclone timed out", fmt.Sprintf("exit code: %d, elapsed: %dms", exitCode, elapsed.Milliseconds()))
		} else {
			e.Anomaly.Record(anomaly.KindSyncFailure, proj.Name, relPath,
				fmt.Sprintf("rclone exit %d", exitCode), fmt.Sprintf("elapsed: %dms", elapsed.Milliseconds()))
		}
		if e.Queue.RecordFailure(proj.Name) {
			e.Anomaly.Record(anomaly.KindCircuitBreaker, proj.Name, relPath,
				fmt.Sprintf("circuit breaker tripped after %d consecutive failures", circuitBreakerThreshold),
				fmt.Sprintf("last rclone exit code: %d", exitCode))
		}
	}
}

// syncMtime updates the remote file's modification time without re-uploading content.
// Called when content hash is unchanged but local mtime has changed.
// Uses `rclone touch --timestamp` which is a lightweight metadata-only operation.
// If the backend does not support mtime updates, the error is logged and the
// mtime_ns is updated in the DB anyway to avoid continuous retries.
func (e *Engine) syncMtime(ctx context.Context, proj config.Project, relPath string, mtime time.Time, hash string, size, mtimeNs int64) {
	remotePath := proj.Remote + "/" + filepath.ToSlash(relPath)

	// rclone touch accepts ISO 8601 timestamp (UTC, second precision is sufficient for all backends)
	ts := mtime.UTC().Format("2006-01-02T15:04:05")
	args := []string{"touch", "--timestamp", ts, "--no-create", remotePath}
	args = append(args, e.commonFlags(proj)...)

	start := time.Now()
	exitCode := e.runRclone(ctx, args)
	elapsed := time.Since(start)

	// Update DB regardless of exit code: record the new mtime so we don't retry
	// on every subsequent event. The content hash is unchanged so the file is intact.
	// SM-048: Always record rclone_exit=0 here. The content was already synced
	// successfully (that's how we reached syncMtime). Recording a non-zero exit
	// from the touch command would cause the next sync to see "failed sync" and
	// trigger a full re-upload instead of just retrying the mtime touch.
	e.state.UpdateFileState(proj.Name, relPath, hash, size, mtimeNs, 0)

	if exitCode == 0 {
		e.log.Info("metadata synced", "project", proj.Name, "path", relPath,
			"mtime", mtime.UTC().Format(time.RFC3339), "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "mtime_sync",
			fmt.Sprintf("mtime=%s, %dms", mtime.UTC().Format(time.RFC3339), elapsed.Milliseconds()),
			elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordMetadataSync(proj.Name)
		}
	} else {
		// Exit code 3 = "directory not found", others may mean "not supported"
		e.log.Warn("metadata sync failed (backend may not support mtime updates)",
			"project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "mtime_sync_error",
			fmt.Sprintf("rclone exit %d (backend may not support mtime updates)", exitCode),
			elapsed.Milliseconds())
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

	// Capture filter generation before generating the rclone filter file.
	// If the filter is hot-reloaded between now and rclone execution, a new
	// full sync will be queued by reloadFilter — so we can safely skip this
	// stale one (SM-044).
	genBefore := fe.Generation()

	filterFile, err := fe.GenerateRcloneFilterFile()
	if err != nil {
		e.log.Error("filter file generation failed", "project", proj.Name, "error", err)
		return
	}
	defer os.Remove(filterFile)

	// Check if filter was reloaded between task dequeue and filter file generation.
	// If so, this full sync would use stale rules. Skip it — a fresh full sync
	// is already queued by reloadFilter with the updated rules.
	if fe.Generation() != genBefore {
		e.log.Info("filter changed during full sync setup, skipping stale sync", "project", proj.Name)
		return
	}

	// Use "sync" when delete_policy=mirror — makes remote match local exactly,
	// including deleting remote-only files (orphans from WSL renames, etc.).
	// Use "copy" otherwise — upload-only, safe default.
	verb := "copy"
	if proj.DeletePolicy(e.cfg) == config.DeleteDelete {
		verb = "sync"
	}
	args := []string{verb, proj.LocalPath, proj.Remote, "--checksum", "--filter-from", filterFile}
	args = append(args, e.commonFlags(proj)...)

	exitCode := e.runRclone(ctx, args)
	elapsed := time.Since(start)

	if exitCode == 0 {
		e.log.Info("full sync complete", "project", proj.Name, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, "", "full_sync", fmt.Sprintf("ok, %dms", elapsed.Milliseconds()), elapsed.Milliseconds())
		e.state.SetMeta("last_full_sync_"+proj.Name, time.Now().UTC().Format(time.RFC3339))
		e.Queue.RecordSuccess(proj.Name)

		// SM-083: Backfill state DB for files synced by batch reconciliation.
		// rclone copy doesn't report per-file results, so we walk the local tree
		// and record state for any included file that has no DB entry.
		e.backfillStateAfterBatchSync(ctx, proj)
	} else {
		e.log.Warn("full sync failed", "project", proj.Name, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, "", "full_sync_error", fmt.Sprintf("rclone exit %d, %dms", exitCode, elapsed.Milliseconds()), elapsed.Milliseconds())
		if e.metrics != nil {
			e.metrics.RecordError(proj.Name, fmt.Sprintf("full sync rclone exit %d", exitCode))
		}
		if e.Queue.RecordFailure(proj.Name) {
			e.Anomaly.Record(anomaly.KindCircuitBreaker, proj.Name, "",
				fmt.Sprintf("circuit breaker tripped after %d consecutive failures", circuitBreakerThreshold),
				fmt.Sprintf("last rclone exit code: %d (full sync)", exitCode))
		}
	}
}

// deleteRemoteFile handles file deletion on remote based on delete policy.
// If force is true, the delete is always executed as a mirror-delete regardless
// of the configured policy. This is used for rename cleanup: when a file is
// renamed, the old remote path is an orphan that must be removed.
func (e *Engine) deleteRemoteFile(ctx context.Context, proj config.Project, relPath string, force bool) {
	policy := proj.DeletePolicy(e.cfg)
	if force {
		policy = config.DeleteDelete
	}

	// Check if this path was ever synced as a file.
	// If not, check if it's a directory with synced children underneath.
	// SM-079: DB errors must NOT be silently ignored — proceeding with a delete
	// when the DB is unavailable could cause incorrect remote deletions.
	fileState, dbErr := e.state.GetFileState(proj.Name, relPath)
	if dbErr != nil {
		e.log.Error("state DB error in deleteRemoteFile, skipping delete", "project", proj.Name, "path", relPath, "error", dbErr)
		return
	}
	if fileState == nil {
		// Not a synced file — might be a directory that was renamed/deleted.
		files, dirErr := e.state.GetFilesUnderDir(proj.Name, relPath)
		if dirErr != nil {
			e.log.Error("state DB error in deleteRemoteFile, skipping delete", "project", proj.Name, "path", relPath, "error", dirErr)
			return
		}
		if len(files) > 0 {
			// Directory with synced children — delete them individually.
			e.deleteRemoteDir(ctx, proj, relPath, force)
			return
		}
		// Never synced as file or directory — nothing to delete on remote.
		e.log.Debug("skipping remote delete (never synced)", "project", proj.Name, "path", relPath)
		return
	}

	if policy == config.DeleteIgnore {
		e.log.Debug("local delete ignored (policy=ignore)", "project", proj.Name, "path", relPath)
		e.state.LogAction(proj.Name, relPath, "delete_ignored", "policy=ignore", 0)
		return
	}

	remotePath := proj.Remote + "/" + filepath.ToSlash(relPath)

	switch policy {
	case config.DeleteDelete:
		args := []string{"deletefile", remotePath}
		args = append(args, e.deleteFlags(proj)...)

		start := time.Now()
		exitCode := e.runRclone(ctx, args)
		elapsed := time.Since(start)

		if exitCode == 0 {
			e.log.Info("remote deleted", "project", proj.Name, "path", relPath, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "delete", "mirrored delete", elapsed.Milliseconds())
			e.state.DeleteFileState(proj.Name, relPath)
		} else {
			e.log.Warn("remote delete failed", "project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "delete_error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		}

	case config.DeleteQuarantine:
		ts := time.Now().UTC().Format("20060102T150405Z")
		quarantinePath := proj.Remote + "/.quarantine/" + filepath.ToSlash(relPath) + "." + ts
		args := []string{"moveto", remotePath, quarantinePath}
		args = append(args, e.deleteFlags(proj)...)

		start := time.Now()
		exitCode := e.runRclone(ctx, args)
		elapsed := time.Since(start)

		if exitCode == 0 {
			e.log.Info("remote quarantined", "project", proj.Name, "path", relPath, "quarantine", quarantinePath, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "quarantine", quarantinePath, elapsed.Milliseconds())
			e.state.DeleteFileState(proj.Name, relPath)
		} else {
			e.log.Warn("remote quarantine failed", "project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, relPath, "quarantine_error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
		}

	}
}

// deleteFlags returns rclone flags for delete/quarantine operations.
// Uses minimal retries (1 attempt, no retry sleep) to avoid blocking the sync
// engine for 30+ seconds on transient failures. Deletes are best-effort;
// orphaned files will be caught by the next 'verify' or reconciliation.
func (e *Engine) deleteFlags(proj config.Project) []string {
	flags := []string{
		"--retries", "1",
		"--stats", "0",
		"--log-level", "NOTICE",
		"--skip-links",
	}
	if e.cfg.BandwidthLimit != "" {
		flags = append(flags, "--bwlimit", e.cfg.BandwidthLimit)
	}
	flags = append(flags, e.cfg.RcloneExtraFlags...)
	flags = append(flags, proj.RcloneExtraFlags...)
	return flags
}

// deleteRemoteDir handles cleanup of a renamed/deleted directory on the remote.
// Queries the state DB for all synced files under the directory prefix and deletes
// each one individually. This avoids calling `rclone deletefile` on directory paths
// (which fails and retries for 30s, blocking the sync engine).
func (e *Engine) deleteRemoteDir(ctx context.Context, proj config.Project, dirPath string, force bool) {
	start := time.Now()
	policy := proj.DeletePolicy(e.cfg)
	if force {
		policy = config.DeleteDelete
	}
	if policy == config.DeleteIgnore {
		e.log.Debug("dir delete ignored (policy=ignore)", "project", proj.Name, "path", dirPath)
		return
	}

	// Find all files that were synced under this directory
	files, err := e.state.GetFilesUnderDir(proj.Name, dirPath)
	if err != nil {
		e.log.Warn("failed to query files under dir", "project", proj.Name, "path", dirPath, "error", err)
		return
	}
	if len(files) == 0 {
		e.log.Debug("no synced files under dir (nothing to delete)", "project", proj.Name, "path", dirPath)
		return
	}

	e.log.Info("cleaning remote dir", "project", proj.Name, "path", dirPath, "files", len(files))

	// FR-DEL-07: Use rclone purge for atomic directory delete when policy=delete.
	// Quarantine mode still needs per-file iteration (each file gets individual timestamp).
	if policy == config.DeleteDelete {
		remotePath := proj.Remote + "/" + filepath.ToSlash(dirPath)
		args := []string{"purge", remotePath}
		args = append(args, e.deleteFlags(proj)...)

		exitCode := e.runRclone(ctx, args)
		elapsed := time.Since(start)

		if exitCode == 0 {
			// Clean state DB for all files under the directory
			for _, relPath := range files {
				e.state.DeleteFileState(proj.Name, relPath)
			}
			e.log.Info("remote dir purged (atomic)", "project", proj.Name, "path", dirPath, "files", len(files), "ms", elapsed.Milliseconds())
			e.state.LogAction(proj.Name, dirPath, "dir_purge", fmt.Sprintf("%d files, %dms", len(files), elapsed.Milliseconds()), elapsed.Milliseconds())
		} else {
			// Purge failed — fall back to per-file deletion
			e.log.Warn("rclone purge failed, falling back to per-file delete", "project", proj.Name, "path", dirPath, "exit", exitCode)
			for _, relPath := range files {
				e.deleteRemoteFile(ctx, proj, relPath, force)
			}
			elapsed = time.Since(start)
			e.log.Info("remote dir cleaned (per-file fallback)", "project", proj.Name, "path", dirPath, "files", len(files), "ms", elapsed.Milliseconds())
		}
		return
	}

	// Quarantine mode: per-file iteration (each needs individual timestamped path)
	for _, relPath := range files {
		e.deleteRemoteFile(ctx, proj, relPath, force)
	}
	elapsed := time.Since(start)
	e.log.Info("remote dir cleaned", "project", proj.Name, "path", dirPath, "files", len(files), "ms", elapsed.Milliseconds())
}

func (e *Engine) commonFlags(proj config.Project) []string {
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
	flags = append(flags, proj.RcloneExtraFlags...)
	return flags
}

// runRclone delegates to RunRcloneFunc (injectable for testing).
func (e *Engine) runRclone(ctx context.Context, args []string) int {
	return e.RunRcloneFunc(ctx, args)
}

// defaultRunRclone is the production rclone runner that spawns a subprocess.
func (e *Engine) defaultRunRclone(ctx context.Context, args []string) int {
	rclonePath := e.cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}

	// Prepend global rclone flags (--config for service/SYSTEM account, etc.)
	args = append(e.cfg.RcloneArgs(), args...)

	e.log.Debug("rclone", "cmd", rclonePath, "args", strings.Join(args, " "))

	// Apply a 5-minute timeout to prevent a single rclone operation from
	// blocking a worker indefinitely. Full-project syncs with --checksum
	// on large directories can be slow; 5 minutes is generous.
	rcloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(rcloneCtx, rclonePath, args...)

	// Capture stderr for diagnostics. Without this, rclone error messages
	// are lost when running as a Windows service (SYSTEM has no console).
	// 311 failures went undiagnosed because stderr went to os.Stderr = nowhere.
	var stderrBuf strings.Builder
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	if err != nil {
		stderrMsg := strings.TrimSpace(stderrBuf.String())
		if rcloneCtx.Err() == context.DeadlineExceeded {
			e.log.Error("rclone timed out after 5 minutes", "args", strings.Join(args, " "), "stderr", stderrMsg)
			return -2
		}
		// SM-100: Check context cancellation (graceful shutdown).
		// rclone exits with 143 (SIGTERM) or similar when the parent is shutting down.
		// This is not a failure — it's a cancellation.
		if ctx.Err() == context.Canceled {
			e.log.Debug("rclone cancelled (shutdown)", "args", strings.Join(args[:min(3, len(args))], " "))
			return -3
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			if stderrMsg != "" {
				e.log.Warn("rclone failed", "exit", exitCode, "stderr", stderrMsg, "args", strings.Join(args[:min(3, len(args))], " "))
			}
			return exitCode
		}
		e.log.Error("rclone exec failed", "error", err, "stderr", stderrMsg)
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
	args = append(args, e.cfg.RcloneExtraFlags...)
	args = append(args, proj.RcloneExtraFlags...)

	rclonePath := e.cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}
	fullArgs := append(e.cfg.RcloneArgs(), args...)

	fmt.Printf("=== Dry run: %s ===\n", proj.Name)
	fmt.Printf("Source: %s\n", proj.LocalPath)
	fmt.Printf("Destination: %s\n", proj.Remote)
	fmt.Printf("Running: %s %s\n\n", rclonePath, strings.Join(fullArgs, " "))

	// Dry-run output (INFO-level "Skipped copy" messages) goes to rclone's
	// stderr. Pass it through to the terminal instead of capturing it.
	cmd := exec.CommandContext(ctx, rclonePath, fullArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("rclone exit code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("rclone: %w", err)
	}
	return nil
}

// MigrateRemote performs a server-side move of all files from oldRemote to newRemote.
// Used when a mirror's remote path changes in config — avoids re-uploading files.
// On Google Drive, this is a server-side rename with no data transfer.
func (e *Engine) MigrateRemote(ctx context.Context, proj config.Project, oldRemote, newRemote string) error {
	e.log.Info("migrating remote", "old", oldRemote, "new", newRemote)
	start := time.Now()

	args := []string{"moveto", oldRemote, newRemote}
	args = append(args, e.commonFlags(proj)...)

	exitCode := e.runRclone(ctx, args)
	elapsed := time.Since(start)

	if exitCode != 0 {
		e.log.Warn("remote migration failed", "old", oldRemote, "new", newRemote,
			"exit", exitCode, "ms", elapsed.Milliseconds())
		return fmt.Errorf("rclone moveto exit %d", exitCode)
	}

	e.log.Info("remote migration complete", "old", oldRemote, "new", newRemote,
		"ms", elapsed.Milliseconds())
	return nil
}

// GhostFile represents a remote file with no valid local counterpart.
// GhostKind classifies remote-only files by why they exist on remote.
type GhostKind string

const (
	// GhostLeak: file is excluded by current filter but exists on remote.
	// Cause: synced before filter rule was added. Action: auto-clean.
	GhostLeak GhostKind = "LEAK"

	// GhostRetained: file was synced, then deleted locally, but remote copy
	// preserved by delete_policy=ignore. Action: informational only.
	GhostRetained GhostKind = "RETAINED"

	// GhostStale: file was synced (has state DB entry), local file is gone,
	// not excluded by filter. Likely a rename/move residue.
	// Action: report, may need cleanup.
	GhostStale GhostKind = "STALE"

	// GhostOrphan: file on remote with no state DB entry and no local counterpart.
	// Cause: batch reconciliation, manual upload, or rclone bug.
	// Action: report, investigate.
	GhostOrphan GhostKind = "ORPHAN"
)

type GhostFile struct {
	Project string    // mirror name
	Path    string    // relative path on remote
	Size    int64
	Kind    GhostKind // classification
}

// IsLeak returns true if this ghost is a LEAK (for backward compatibility).
func (g GhostFile) IsLeak() bool { return g.Kind == GhostLeak }

// findGhosts detects remote-only files for a project by comparing the remote listing
// against the local filesystem + filter rules. Returns the list of ghost files.
func (e *Engine) findGhosts(proj config.Project) ([]GhostFile, error) {
	fe, ok := e.filters[proj.Name]
	if !ok {
		return nil, fmt.Errorf("no filter engine for project %q", proj.Name)
	}

	lister := e.ListRemoteFunc
	if lister == nil {
		lister = ListRemote
	}
	remoteFiles, err := lister(e.cfg, proj)
	if err != nil {
		return nil, fmt.Errorf("listing remote: %w", err)
	}

	// Build set of local non-excluded files
	localFiles := make(map[string]bool)
	if err := filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			relPath, _ := filepath.Rel(proj.LocalPath, path)
			if relPath != "." && fe.IsExcluded(relPath+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		relPath, _ := filepath.Rel(proj.LocalPath, path)
		relPath = filepath.ToSlash(relPath)
		if fe.IsExcluded(relPath) {
			return nil
		}
		localFiles[relPath] = true
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walking local path: %w", err)
	}

	var ghosts []GhostFile
	for _, rf := range remoteFiles {
		if rf.IsDir {
			continue
		}
		remotePath := filepath.ToSlash(rf.Path)
		if strings.HasPrefix(remotePath, ".quarantine/") {
			continue
		}
		if !localFiles[remotePath] {
			kind := ClassifyGhost(fe, e.state, proj.Name, remotePath, proj.DeletePolicy(e.cfg))
			ghosts = append(ghosts, GhostFile{
				Project: proj.Name,
				Path:    remotePath,
				Size:    rf.Size,
				Kind:    kind,
			})
		}
	}
	return ghosts, nil
}

// backfillStateAfterBatchSync walks the local tree and records state DB entries
// for any included file that has no entry. This closes the blind spot where
// rclone copy (batch sync) uploads files without recording per-file state.
// Only backfills successful syncs (assumes batch sync succeeded for all files).
func (e *Engine) backfillStateAfterBatchSync(ctx context.Context, proj config.Project) {
	fe, ok := e.filters[proj.Name]
	if !ok {
		return
	}

	backfilled := 0
	_ = filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if d.IsDir() {
			relPath, _ := filepath.Rel(proj.LocalPath, path)
			relPath = filepath.ToSlash(relPath)
			if relPath != "." && fe.IsExcluded(relPath+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}

		relPath, _ := filepath.Rel(proj.LocalPath, path)
		relPath = filepath.ToSlash(relPath)
		if fe.IsExcluded(relPath) {
			return nil
		}

		// Check if state DB already has an entry
		existing, _ := e.state.GetFileState(proj.Name, relPath)
		if existing != nil {
			return nil // already tracked
		}

		// Compute hash and record state
		info, err := d.Info()
		if err != nil {
			return nil
		}

		// Skip files exceeding max size (they weren't synced)
		if info.Size() > proj.MaxFileSize() {
			return nil
		}

		hash, _, err := state.HashFile(path)
		if err != nil {
			return nil
		}

		e.state.UpdateFileState(proj.Name, relPath, hash, info.Size(), info.ModTime().UnixNano(), 0)
		backfilled++
		return nil
	})

	if backfilled > 0 {
		e.log.Info("backfilled state DB after batch sync", "project", proj.Name, "files", backfilled)
	}
}

// ClassifyGhost determines why a remote-only file exists using filter rules and state DB.
// deletePolicy is the effective policy for this project (per-mirror or global).
func ClassifyGhost(fe *filter.Engine, st *state.Store, project, remotePath string, deletePolicy config.DeletePolicy) GhostKind {
	// LEAK: excluded by current filter — synced before exclusion was added
	if fe != nil && fe.IsExcluded(remotePath) {
		return GhostLeak
	}

	// Check state DB: was this file ever synced by smirror?
	if st != nil {
		fs, err := st.GetFileState(project, remotePath)
		if err == nil && fs != nil {
			// Has state DB entry → was synced, file is now gone locally.
			// If delete_policy=ignore, this is intentional (RETAINED).
			// Otherwise, it's stale (rename/move residue or missed deletion).
			if deletePolicy == config.DeleteIgnore {
				return GhostRetained
			}
			return GhostStale
		}
	}

	// No state DB entry, not excluded → true orphan (batch reconciliation, manual upload)
	return GhostOrphan
}

// recordGhostAnomalies records one anomaly per ghost kind found in a project.
// Groups by kind to avoid flooding the anomaly system with per-file records.
func (e *Engine) recordGhostAnomalies(proj config.Project, ghosts []GhostFile) {
	counts := make(map[GhostKind]int)
	for _, g := range ghosts {
		counts[g.Kind]++
	}
	kindMap := map[GhostKind]anomaly.Kind{
		GhostLeak:   anomaly.KindGhostLeak,
		GhostOrphan: anomaly.KindGhostOrphan,
		GhostStale:  anomaly.KindGhostStale,
	}
	for gk, count := range counts {
		if ak, ok := kindMap[gk]; ok {
			e.Anomaly.Record(ak, proj.Name, "",
				fmt.Sprintf("%d %s ghost(s) detected", count, gk),
				fmt.Sprintf("run 'smirror test-mirrors %s' for details", proj.Name))
		}
	}
}

// CleanupGhosts removes remote-only files (LEAKs and ORPHANs) for a project.
// Returns the number of files successfully deleted.
func (e *Engine) CleanupGhosts(ctx context.Context, proj config.Project) (int, error) {
	ghosts, err := e.findGhosts(proj)
	if err != nil {
		return 0, err
	}
	if len(ghosts) == 0 {
		return 0, nil
	}
	e.recordGhostAnomalies(proj, ghosts)

	deleted := 0
	for _, g := range ghosts {
		remotePath := proj.Remote + "/" + g.Path
		args := []string{"deletefile", remotePath}
		args = append(args, e.deleteFlags(proj)...)

		exitCode := e.runRclone(ctx, args)
		kind := string(g.Kind)
		if exitCode == 0 {
			e.log.Info("ghost cleaned", "project", proj.Name, "path", g.Path, "kind", kind)
			e.state.LogAction(proj.Name, g.Path, "ghost_cleanup", kind, 0)
			e.state.DeleteFileState(proj.Name, g.Path)
			deleted++
		} else if exitCode == 4 {
			// SM-075: deletefile failed with "file not found" — likely duplicate directories
			// on Google Drive. Fall back to rclone delete --rmdirs on parent directory.
			parentDir := filepath.Dir(g.Path)
			if parentDir != "." && parentDir != "" {
				parentRemote := proj.Remote + "/" + parentDir
				fallbackArgs := []string{"delete", parentRemote, "--rmdirs"}
				fallbackArgs = append(fallbackArgs, e.deleteFlags(proj)...)
				if e.runRclone(ctx, fallbackArgs) == 0 {
					e.log.Info("ghost cleaned (parent dir fallback)", "project", proj.Name, "path", g.Path, "kind", kind)
					e.state.LogAction(proj.Name, g.Path, "ghost_cleanup", kind+" (dir fallback)", 0)
					e.state.DeleteFileState(proj.Name, g.Path)
					deleted++
				} else {
					e.log.Warn("ghost cleanup failed (dir fallback also failed)", "project", proj.Name, "path", g.Path, "kind", kind)
				}
			}
		} else {
			e.log.Warn("ghost cleanup failed", "project", proj.Name, "path", g.Path, "kind", kind, "exit", exitCode)
		}
	}
	return deleted, nil
}

// CleanupLeaks removes LEAK files (excluded by filter but present on remote) for a project.
// Unlike CleanupGhosts, this only removes LEAKs — not ORPHANs. LEAKs represent files that
// the user explicitly excluded via .syncignore; they should be cleaned regardless of
// delete_policy (which controls user-deleted files, a different intent).
// Called automatically when .syncignore filter rules change.
func (e *Engine) CleanupLeaks(ctx context.Context, proj config.Project) (int, error) {
	ghosts, err := e.findGhosts(proj)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, g := range ghosts {
		if g.Kind != GhostLeak {
			continue // only clean LEAKs — other kinds respect delete_policy
		}
		remotePath := proj.Remote + "/" + g.Path
		args := []string{"deletefile", remotePath}
		args = append(args, e.deleteFlags(proj)...)

		exitCode := e.runRclone(ctx, args)
		if exitCode == 0 {
			e.log.Info("leak cleaned (filter exclusion)", "project", proj.Name, "path", g.Path)
			e.state.LogAction(proj.Name, g.Path, "leak_cleanup", "filter_change", 0)
			e.state.DeleteFileState(proj.Name, g.Path)
			deleted++
		} else {
			e.log.Warn("leak cleanup failed", "project", proj.Name, "path", g.Path, "exit", exitCode)
		}
	}
	return deleted, nil
}

// DryRunCleanup shows what ghost files would be cleaned up for a project.
// Returns the number of ghost files found.
func (e *Engine) DryRunCleanup(ctx context.Context, proj config.Project) (int, error) {
	ghosts, err := e.findGhosts(proj)
	if err != nil {
		return 0, err
	}
	if len(ghosts) == 0 {
		fmt.Printf("  No ghost files to clean up\n")
		return 0, nil
	}

	actionable := 0
	for _, g := range ghosts {
		if g.Kind == GhostRetained {
			continue // don't suggest deleting intentionally retained files
		}
		fmt.Printf("  [%s] would delete: %s (%d bytes)\n", g.Kind, g.Path, g.Size)
		actionable++
	}
	if actionable == 0 {
		fmt.Printf("  No ghost files to clean up\n")
	}
	return actionable, nil
}

// quarantineEntry represents a file in the .quarantine/ directory.
type quarantineEntry struct {
	Path  string `json:"Path"`
	Name  string `json:"Name"`
	IsDir bool   `json:"IsDir"`
	Size  int64  `json:"Size"`
}

// parseExpiredQuarantineEntries filters quarantine entries to those older than cutoff.
// Returns the paths of expired entries. Pure function — no I/O.
func parseExpiredQuarantineEntries(entries []quarantineEntry, cutoff time.Time) []string {
	var expired []string
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		name := entry.Name
		if len(name) < 17 {
			continue
		}
		tsSuffix := name[len(name)-16:] // "20060102T150405Z"
		ts, err := time.Parse("20060102T150405Z", tsSuffix)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			expired = append(expired, entry.Path)
		}
	}
	return expired
}

// PurgeExpiredQuarantine removes quarantined files older than the configured
// retention period. Called during reconciliation when delete_policy=quarantine.
// Returns the number of files purged.
func (e *Engine) PurgeExpiredQuarantine(ctx context.Context, proj config.Project) int {
	if proj.DeletePolicy(e.cfg) != config.DeleteQuarantine {
		return 0
	}

	retentionDays := proj.QuarantineRetention(e.cfg)
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)

	// List quarantine directory via rclone lsjson
	quarantineRemote := proj.Remote + "/.quarantine/"
	rclonePath := e.cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}
	args := []string{"lsjson", quarantineRemote, "--recursive", "--no-mimetype", "--no-modtime"}
	args = append(args, e.commonFlags(proj)...)
	allArgs := append(e.cfg.RcloneArgs(), args...)
	cmd := exec.CommandContext(ctx, rclonePath, allArgs...)
	out, err := cmd.Output()
	if err != nil {
		// Distinguish "no quarantine dir" (exit 3/4 = expected) from real errors
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if exitCode == 3 || exitCode == 4 {
			e.log.Debug("quarantine listing skipped (no .quarantine dir)", "project", proj.Name)
		} else {
			e.log.Warn("quarantine listing failed, auto-purge skipped", "project", proj.Name, "exit", exitCode, "error", err)
		}
		return 0
	}

	var entries []quarantineEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		e.log.Warn("failed to parse quarantine listing", "project", proj.Name, "error", err)
		return 0
	}

	expired := parseExpiredQuarantineEntries(entries, cutoff)

	purged := 0
	for _, path := range expired {
		remotePath := quarantineRemote + path
		delArgs := []string{"deletefile", remotePath}
		delArgs = append(delArgs, e.deleteFlags(proj)...)

		exitCode := e.runRclone(ctx, delArgs)
		if exitCode == 0 {
			e.log.Info("quarantine purged (expired)", "project", proj.Name, "path", path)
			purged++
		}
	}

	if purged > 0 {
		e.log.Info("quarantine auto-purge complete", "project", proj.Name, "purged", purged, "retention_days", retentionDays)
	}
	return purged
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
			return fmt.Errorf("mirror %q: invalid remote format %q (expected remote:path)", proj.Name, remote)
		}

		fmt.Printf("Checking %s -> %s ... ", proj.Name, remote)
		lsdArgs := append(cfg.RcloneArgs(), "lsd", remote, "--max-depth", "0")
		cmd := exec.Command(rclonePath, lsdArgs...)
		if err := cmd.Run(); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("mirror %q: remote %q unreachable: %w", proj.Name, remote, err)
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
// Duplicates (same path, different content) are deduplicated: newest ModTime wins,
// size breaks ties (larger wins). This handles backends like Google Drive that
// allow multiple files with the same name in the same directory.
func ListRemote(cfg *config.Global, proj config.Project) ([]RemoteFile, error) {
	start := time.Now()
	rclonePath := cfg.RclonePath
	if rclonePath == "" {
		rclonePath = "rclone"
	}

	lsjsonArgs := append(cfg.RcloneArgs(), "lsjson", proj.Remote, "--recursive", "--hash", "--no-mimetype", "--no-modtime")
	cmd := exec.Command(rclonePath, lsjsonArgs...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rclone lsjson: %w", err)
	}

	var raw []RemoteFile
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parsing lsjson: %w", err)
	}

	files := deduplicateRemoteFiles(raw, proj.Name)
	slog.Debug("ListRemote", "project", proj.Name, "files", len(files), "ms", time.Since(start).Milliseconds())
	return files, nil
}

// deduplicateRemoteFiles resolves same-path duplicates by keeping the newest
// entry (by ModTime). On ModTime tie, the larger file wins (more likely to be
// the complete write). Warnings are logged for each duplicate found.
func deduplicateRemoteFiles(files []RemoteFile, project string) []RemoteFile {
	type entry struct {
		file RemoteFile
		idx  int // insertion order for stable output
	}
	best := make(map[string]entry, len(files))
	dupes := 0

	for _, rf := range files {
		path := filepath.ToSlash(rf.Path)
		prev, exists := best[path]
		if !exists {
			best[path] = entry{file: rf, idx: len(best)}
			continue
		}

		dupes++
		winner, loser := rf, prev.file
		if remoteFileNewer(prev.file, rf) {
			winner, loser = prev.file, rf
		}

		slog.Warn("duplicate remote file, keeping newest",
			"project", project,
			"path", path,
			"kept_size", winner.Size, "kept_mod", winner.ModTime,
			"dropped_size", loser.Size, "dropped_mod", loser.ModTime)

		best[path] = entry{file: winner, idx: prev.idx}
	}

	if dupes == 0 {
		return files
	}

	// Rebuild slice in original insertion order for deterministic output.
	result := make([]RemoteFile, len(best))
	for _, e := range best {
		result[e.idx] = e.file
	}
	slog.Info("deduplicated remote listing", "project", project,
		"duplicates", dupes, "before", len(files), "after", len(result))
	return result
}

// remoteFileNewer returns true if a is newer than b.
// On tie, larger size wins (more likely to be the complete write).
func remoteFileNewer(a, b RemoteFile) bool {
	ta, errA := time.Parse(time.RFC3339, a.ModTime)
	tb, errB := time.Parse(time.RFC3339, b.ModTime)
	if errA != nil && errB != nil {
		return a.Size >= b.Size
	}
	if errA != nil {
		return false
	}
	if errB != nil {
		return true
	}
	if ta.Equal(tb) {
		return a.Size >= b.Size
	}
	return ta.After(tb)
}
