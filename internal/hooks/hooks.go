// Package hooks provides pre/post-sync hook execution for SelectiveMirror.
// Hooks are shell commands configured per-mirror or globally that run before
// and after sync operations. They receive context via environment variables.
package hooks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Env provides context to hook scripts via environment variables.
type Env struct {
	Project string // SMIRROR_PROJECT: mirror name
	File    string // SMIRROR_FILE: relative path (empty for full-project sync)
	Remote  string // SMIRROR_REMOTE: rclone remote path
	Event   string // SMIRROR_EVENT: "pre_sync" or "post_sync"
}

// Runner executes hook commands with timeout and environment.
type Runner struct {
	log     *slog.Logger
	timeout time.Duration
}

// New creates a hook runner with the given timeout.
// Default timeout is 30 seconds if timeout <= 0.
func New(timeout time.Duration) *Runner {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Runner{
		log:     slog.Default().With("component", "hooks"),
		timeout: timeout,
	}
}

// Run executes a hook command with the given environment.
// Returns nil if hookCmd is empty (no hook configured).
// Errors are logged as warnings — hooks never block sync operations.
// Nil-safe: no-op if receiver is nil.
func (r *Runner) Run(ctx context.Context, hookCmd string, env Env) error {
	if r == nil || hookCmd == "" {
		return nil
	}

	hookCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(hookCtx, "cmd.exe", "/C", hookCmd)
	} else {
		cmd = exec.CommandContext(hookCtx, "sh", "-c", hookCmd)
	}

	// Set environment variables
	cmd.Env = append(os.Environ(),
		"SMIRROR_PROJECT="+env.Project,
		"SMIRROR_FILE="+env.File,
		"SMIRROR_REMOTE="+env.Remote,
		"SMIRROR_EVENT="+env.Event,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if hookCtx.Err() == context.DeadlineExceeded {
			r.log.Warn("hook timed out",
				"event", env.Event, "project", env.Project, "timeout", r.timeout)
			return fmt.Errorf("hook timed out after %s", r.timeout)
		}
		r.log.Warn("hook failed",
			"event", env.Event, "project", env.Project,
			"cmd", hookCmd, "error", err, "output", string(output))
		return fmt.Errorf("hook failed: %w", err)
	}

	if len(output) > 0 {
		r.log.Debug("hook output",
			"event", env.Event, "project", env.Project, "output", string(output))
	}

	return nil
}
