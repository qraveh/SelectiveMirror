// Package hooks provides pre/post-sync hook execution for SelectiveMirror.
// Hooks are shell commands configured per-mirror or globally that run before
// and after sync operations. They receive context via environment variables.
package hooks

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// shellMetachars are characters that have special meaning to cmd.exe and
// POSIX shells. If a hook env var value contains any of these, shell
// expansion in the user's hook command (e.g., `echo %SMIRROR_FILE%` or
// `echo "$SMIRROR_FILE"`) can lead to command injection. SEC-C5.
//
// Examples:
//   Windows filename "a&calc.exe" → `echo Synced: %SMIRROR_FILE%` →
//     cmd.exe expands to `echo Synced: a&calc.exe` → runs calc.exe
//   Unix filename "$(rm -rf /)"   → `echo $SMIRROR_FILE` → shell executes
//
// We reject rather than escape because safe escaping varies by shell and by
// quoting style in the user's hook script — we cannot know.
const shellMetachars = "&|<>\"^$`();\n\r"

// containsShellMetachar reports whether s has any character that shells
// interpret specially.
func containsShellMetachar(s string) bool {
	return strings.ContainsAny(s, shellMetachars)
}

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

	// SEC-C5: Reject env values containing shell metacharacters. Hooks run
	// under `cmd.exe /C` or `sh -c`, and users typically reference env vars
	// unquoted in their hook scripts — a filename containing `&` or `$(...)`
	// chains commands.
	for field, val := range map[string]string{
		"SMIRROR_PROJECT": env.Project,
		"SMIRROR_FILE":    env.File,
		"SMIRROR_REMOTE":  env.Remote,
		"SMIRROR_EVENT":   env.Event,
	} {
		if containsShellMetachar(val) {
			r.log.Warn("hook skipped: env value contains shell metacharacter",
				"event", env.Event, "project", env.Project, "field", field)
			return fmt.Errorf("hook skipped: %s contains shell metacharacter", field)
		}
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

	// Capture stdout+stderr in a single buffer (matches the previous
	// CombinedOutput semantics) so the diagnostic log line that
	// follows still has the hook's output.
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	// SEC-M14 / PF-A5: wrap the hook subprocess in a Windows Job
	// Object. When the deferred closeJobObject below runs (on hook
	// completion OR timeout OR panic), the kernel terminates every
	// process descendant of the hook command. Without this, a
	// `pre_sync_hook: cmd /C start /B malware.exe` would orphan
	// malware.exe past the hook timeout. POSIX path is a no-op stub.
	job, jobErr := newJobObject()
	if jobErr != nil {
		// Job Object creation failure is non-fatal — we proceed
		// without the kill-tree guarantee, with a warn log so the
		// operator sees that orphan-protection is degraded.
		r.log.Warn("hook job-object creation failed; orphan-grandchildren will not be killed on timeout",
			"event", env.Event, "project", env.Project, "error", jobErr)
	}
	defer closeJobObject(job)

	if err := cmd.Start(); err != nil {
		r.log.Warn("hook start failed",
			"event", env.Event, "project", env.Project, "cmd", hookCmd, "error", err)
		return fmt.Errorf("hook start: %w", err)
	}

	if job != 0 && cmd.Process != nil {
		if err := assignPIDToJob(job, cmd.Process.Pid); err != nil {
			r.log.Warn("hook job-object assignment failed; orphan-grandchildren will not be killed on timeout",
				"event", env.Event, "project", env.Project, "error", err)
		}
	}

	err := cmd.Wait()
	output := outBuf.Bytes()
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
