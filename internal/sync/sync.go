// Package sync handles rclone invocation for file mirroring.
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/state"
)

// Task represents a file to be synced.
type Task struct {
	Project config.Project
	RelPath string // empty means full project sync
}

// Engine processes sync tasks using rclone.
type Engine struct {
	cfg      *config.Global
	state    *state.Store
	filters  map[string]*filter.Engine // project name -> filter
	TaskChan chan Task
	log      *slog.Logger
}

// NewEngine creates a sync engine.
func NewEngine(cfg *config.Global, st *state.Store, filters map[string]*filter.Engine) *Engine {
	return &Engine{
		cfg:      cfg,
		state:    st,
		filters:  filters,
		TaskChan: make(chan Task, 1000),
		log:      slog.Default().With("component", "sync"),
	}
}

// Run processes sync tasks until context is cancelled.
func (e *Engine) Run(ctx context.Context) {
	e.log.Info("sync engine started")
	for {
		select {
		case task := <-e.TaskChan:
			if task.RelPath == "" {
				e.syncFullProject(ctx, task.Project)
			} else {
				e.syncSingleFile(ctx, task.Project, task.RelPath)
			}
		case <-ctx.Done():
			e.log.Info("sync engine stopping")
			return
		}
	}
}

func (e *Engine) syncSingleFile(ctx context.Context, proj config.Project, relPath string) {
	localPath := filepath.Join(proj.LocalPath, relPath)

	// Verify file still exists (may have been deleted between detection and sync)
	info, err := os.Stat(localPath)
	if err != nil {
		e.log.Debug("file gone before sync", "project", proj.Name, "path", relPath)
		e.state.LogAction(proj.Name, relPath, "skip_gone", "file deleted before sync", 0)
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
	} else {
		e.log.Warn("sync failed", "project", proj.Name, "path", relPath, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, relPath, "error", fmt.Sprintf("rclone exit %d", exitCode), elapsed.Milliseconds())
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
	} else {
		e.log.Warn("full sync failed", "project", proj.Name, "exit", exitCode, "ms", elapsed.Milliseconds())
		e.state.LogAction(proj.Name, "", "full_sync_error", fmt.Sprintf("rclone exit %d, %dms", exitCode, elapsed.Milliseconds()), elapsed.Milliseconds())
	}
}

func (e *Engine) commonFlags() []string {
	flags := []string{
		"--retries", "3",
		"--retries-sleep", "10s",
		"--stats", "0",
		"--log-level", "NOTICE",
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

	args := []string{"copy", proj.LocalPath, proj.Remote, "--checksum", "--filter-from", filterFile, "--dry-run", "-v"}
	args = append(args, e.commonFlags()...)

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
