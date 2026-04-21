package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
	"github.com/qraveh/SelectiveMirror/internal/state"
)

// cmdUnmirror handles `smirror unmirror <name_or_path> [--purge-remote] [--yes]`.
func cmdUnmirror(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror unmirror <mirror_name | local_path | remote_path> [--purge-remote] [--yes]

Remove a mirror from config.yaml and clean its entries from the state database.
The local directory is never deleted.

Flags:
  --purge-remote   Also delete the remote directory for this mirror.
                   Only the destination itself is deleted; any sibling .bak
                   directories are left alone (smirror does not manage them).
  --yes, -y        Skip confirmation prompt (required for non-interactive/scripted use)

Aliases: removemirror, remove-mirror, remove

Examples:
  smirror unmirror MyProject
  smirror unmirror C:\Projects\MyProject
  smirror unmirror MyProject --purge-remote
  smirror unmirror gdrive:backup/MyProject --purge-remote --yes`) {
		return
	}

	// Parse flags
	autoYes := false
	purgeRemote := false
	var positionalArgs []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			autoYes = true
		case "--purge-remote":
			purgeRemote = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\nRun 'smirror unmirror --help' for usage.\n", a)
				os.Exit(ExitError)
			}
			positionalArgs = append(positionalArgs, a)
		}
	}
	args = positionalArgs
	checkMaxArgs("unmirror", args, 1)

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `Usage: smirror unmirror <mirror_name | local_path | remote_path> [--purge-remote] [--yes]

Remove a mirror from config.yaml.

Examples:
  smirror unmirror MyProject
  smirror unmirror C:\Projects\MyProject
  smirror unmirror MyProject --purge-remote`)
		os.Exit(ExitConfigError)
	}

	cfg := loadConfig(configPath)
	arg := args[0]

	match := findMirror(cfg, arg)
	if match == nil {
		fmt.Fprintf(os.Stderr, "Error: mirror not found: %s\n", arg)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Available mirrors:")
		for _, p := range cfg.Projects {
			fmt.Fprintf(os.Stderr, "  %-20s %s -> %s\n", p.Name, p.LocalPath, p.Remote)
		}
		os.Exit(ExitConfigError)
	}

	// If purging an rclone remote, detect rclone up front so we fail before the
	// confirmation prompt rather than after.
	var rcloneInfo *rclone.Info
	if purgeRemote && !isLocalPath(match.Remote) {
		info, err := rclone.Detect(cfg.RclonePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: rclone not found: %v\n", err)
			os.Exit(ExitRcloneError)
		}
		rcloneInfo = info
	}

	// Header + preview
	fmt.Printf("Remove mirror '%s'", match.Name)
	if purgeRemote {
		fmt.Print(" and purge remote")
	}
	fmt.Println("?")
	fmt.Printf("  Local:  %s   (NOT deleted)\n", match.LocalPath)

	if purgeRemote {
		pv := probePurgeTarget(match.Remote, rcloneInfo, cfg.RcloneConfig)
		if pv.exists {
			fmt.Printf("  Remote: %s   (%d file(s)) -- will be PURGED\n", match.Remote, pv.count)
		} else {
			fmt.Printf("  Remote: %s   (not present, skipped)\n", match.Remote)
		}
		fmt.Println()
		fmt.Println("This cannot be undone.")
		fmt.Println("Sibling .bak directories (if any) are NOT touched.")
	} else {
		fmt.Printf("  Remote: %s   (NOT deleted)\n", match.Remote)
		fmt.Println()
		fmt.Println("This only removes the mirror from config.yaml and cleans the state DB.")
		fmt.Println("The remote files are NOT deleted. Use --purge-remote to also delete them.")
	}
	fmt.Println()

	if autoYes {
		// Skip confirmation
	} else if isInteractive() {
		fmt.Print("Proceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("Cancelled.")
			return
		}
	} else {
		// SM-136: Non-interactive without --yes: refuse destructive operation.
		fmt.Fprintln(os.Stderr, "Error: unmirror requires confirmation.")
		fmt.Fprintln(os.Stderr, "Use --yes to skip the prompt in scripts/non-interactive mode.")
		os.Exit(ExitError)
	}

	// 1. Purge remote (if requested). Failure aborts before config/state changes.
	if purgeRemote {
		if err := purgeOne(match.Remote, rcloneInfo, cfg.RcloneConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Error purging %s: %v\n", match.Remote, err)
			fmt.Fprintln(os.Stderr, "Aborted. Config and state unchanged.")
			os.Exit(ExitRcloneError)
		}
	}

	// 2. Remove from config.
	if err := config.RemoveMirror(configPath, match.Name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}

	// 3. Clean state DB. Stale rows are harmless (PruneOrphanedProjects sweeps
	// them on next daemon start), so we warn on error instead of aborting.
	if rows, err := cleanStateForProject(cfg.StateDB, match.Name); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not clean state DB for %q: %v\n", match.Name, err)
	} else if rows > 0 {
		fmt.Printf("Cleaned %d state DB row(s) for '%s'.\n", rows, match.Name)
	}

	if purgeRemote {
		fmt.Printf("Mirror '%s' removed and remote purged.\n", match.Name)
	} else {
		fmt.Printf("Mirror '%s' removed from config.\n", match.Name)
	}

	// Check if config is still valid.
	if _, err := config.Load(configPath); err != nil {
		if strings.Contains(err.Error(), "no mirrors") {
			fmt.Println()
			fmt.Println("Warning: config has no mirrors. smirror start will fail until a mirror is added.")
			fmt.Println("Add one with: smirror addmirror <path>")
		} else {
			fmt.Fprintf(os.Stderr, "Warning: config validation: %v\n", err)
		}
	}
}

// cleanStateForProject opens the state DB and deletes all rows for the given
// project. Returns (0, nil) if the DB does not exist (nothing to clean).
func cleanStateForProject(dbPath, project string) (int64, error) {
	if dbPath == "" {
		return 0, nil
	}
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return 0, nil
	}
	st, err := state.Open(dbPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = st.Close() }()
	return st.DeleteProject(project)
}

// purgePreview summarizes a target path for the confirmation prompt.
type purgePreview struct {
	exists bool
	count  int
}

// probePurgeTarget reports whether the path exists, and if so how many
// top-level entries it has. Failures (or missing paths) return exists=false
// so the preview can annotate them as "not present, skipped".
func probePurgeTarget(path string, rcloneInfo *rclone.Info, rcloneConfig string) purgePreview {
	if isLocalPath(path) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return purgePreview{exists: false}
		}
		return purgePreview{exists: true, count: len(entries)}
	}
	if rcloneInfo == nil {
		return purgePreview{exists: false}
	}
	count, err := rclone.CountRemoteFiles(rcloneInfo.Path, rcloneConfig, path, nil)
	if err != nil {
		return purgePreview{exists: false}
	}
	return purgePreview{exists: true, count: count}
}

// purgeOne deletes a path and all its contents. A missing path is a no-op
// success. Local paths go through os.RemoveAll; remotes go through rclone purge.
func purgeOne(path string, rcloneInfo *rclone.Info, rcloneConfig string) error {
	if isLocalPath(path) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		return os.RemoveAll(path)
	}
	if rcloneInfo == nil {
		return fmt.Errorf("rclone required to purge %q but not detected", path)
	}
	if err := rclone.Purge(rcloneInfo.Path, rcloneConfig, path, nil); err != nil {
		if errors.Is(err, rclone.ErrRemoteNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// findMirror searches for a mirror by name, local path, or remote path.
func findMirror(cfg *config.Global, arg string) *config.Project {
	// Try exact name match first
	if p := cfg.FindProject(arg); p != nil {
		return p
	}

	// Try local path match (SM-139: resolve junctions/symlinks for alias equivalence)
	resolvedArg := strings.ToLower(filepath.Clean(resolveRealPath(arg)))
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		resolvedLocal := strings.ToLower(filepath.Clean(resolveRealPath(p.LocalPath)))
		if resolvedArg == resolvedLocal {
			return p
		}
	}

	// Try remote match (SM-140/SM-141: normalize trailing separators and slash style)
	normalizeRemote := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `/`)
		s = strings.TrimRight(s, `/`)
		return strings.ToLower(s)
	}
	normArg := normalizeRemote(arg)
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if normalizeRemote(p.Remote) == normArg {
			return p
		}
	}

	return nil
}
