package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrRemoteNotFound is returned by Purge when the remote path does not exist.
// Callers treating missing paths as a no-op should compare with errors.Is.
var ErrRemoteNotFound = errors.New("remote path not found")

// ListRemotes runs `rclone listremotes` and returns the remote names
// (e.g., ["gdrive:", "s3:"]). Each name includes the trailing colon.
func ListRemotes(rclonePath, rcloneConfig string) ([]string, error) {
	args := []string{"listremotes"}
	if rcloneConfig != "" {
		args = append([]string{"--config", rcloneConfig}, args...)
	}
	cmd := exec.Command(rclonePath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rclone listremotes: %w", err)
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			remotes = append(remotes, line)
		}
	}
	return remotes, nil
}

// HasRemote checks if a remote name (without colon) exists in the rclone config.
func HasRemote(rclonePath, rcloneConfig, remoteName string) (bool, error) {
	remotes, err := ListRemotes(rclonePath, rcloneConfig)
	if err != nil {
		return false, err
	}
	target := remoteName + ":"
	for _, r := range remotes {
		if strings.EqualFold(r, target) {
			return true, nil
		}
	}
	return false, nil
}

// RunConfig runs `rclone config` interactively, inheriting stdin/stdout/stderr.
// This allows the user to set up new remotes via rclone's interactive flow.
func RunConfig(rclonePath, rcloneConfig string) error {
	args := []string{"config"}
	if rcloneConfig != "" {
		args = append([]string{"--config", rcloneConfig}, args...)
	}
	cmd := exec.Command(rclonePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// TestRemote tests connectivity to an rclone remote by running `rclone mkdir`.
// This is an idempotent operation. Returns nil on success.
// Uses a 30-second timeout.
func TestRemote(rclonePath, rcloneConfig, remote string, extraArgs []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{}
	if rcloneConfig != "" {
		args = append(args, "--config", rcloneConfig)
	}
	args = append(args, extraArgs...)
	args = append(args, "mkdir", remote)

	cmd := exec.CommandContext(ctx, rclonePath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after 30s")
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CountRemoteFiles runs `rclone lsjson --max-depth 1` on a remote path and
// returns the number of entries found. Returns -1 on error.
func CountRemoteFiles(rclonePath, rcloneConfig, remote string, extraArgs []string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := []string{}
	if rcloneConfig != "" {
		args = append(args, "--config", rcloneConfig)
	}
	args = append(args, extraArgs...)
	args = append(args, "lsjson", "--max-depth", "1", remote)

	cmd := exec.CommandContext(ctx, rclonePath, args...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return -1, fmt.Errorf("timed out after 30s")
		}
		return -1, err
	}

	var entries []json.RawMessage
	if err := json.Unmarshal(out, &entries); err != nil {
		return 0, nil // empty or unparseable = assume 0
	}
	return len(entries), nil
}

// Purge removes a remote directory and all its contents via `rclone purge`.
// Returns ErrRemoteNotFound if the path does not exist (safe to ignore).
// Uses a 10-minute timeout to accommodate large directories.
func Purge(rclonePath, rcloneConfig, remote string, extraArgs []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	args := []string{}
	if rcloneConfig != "" {
		args = append(args, "--config", rcloneConfig)
	}
	args = append(args, extraArgs...)
	args = append(args, "purge", remote)

	cmd := exec.CommandContext(ctx, rclonePath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("rclone purge timed out after 10m")
		}
		low := strings.ToLower(string(out))
		if strings.Contains(low, "directory not found") || strings.Contains(low, "object not found") {
			return ErrRemoteNotFound
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoteNameFromPath extracts the remote name from a remote path.
// e.g., "gdrive:backup/folder" -> "gdrive"
func RemoteNameFromPath(remote string) string {
	idx := strings.Index(remote, ":")
	if idx < 0 {
		return remote
	}
	return remote[:idx]
}
