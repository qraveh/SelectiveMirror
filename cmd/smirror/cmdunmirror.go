package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qraveh/SelectiveMirror/internal/config"
)

// cmdUnmirror handles `smirror unmirror <name_or_path>`.
func cmdUnmirror(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror unmirror <mirror_name | local_path | remote_path> [--yes]

Remove a mirror from config.yaml.
The local directory and remote files are NOT deleted.

Flags:
  --yes, -y    Skip confirmation prompt (required for non-interactive/scripted use)

Aliases: removemirror, remove-mirror, remove

Examples:
  smirror unmirror MyProject
  smirror unmirror C:\Projects\MyProject
  smirror unmirror gdrive:backup/MyProject --yes`) {
		return
	}

	// Parse --yes flag, reject unknown flags
	autoYes := false
	var positionalArgs []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			autoYes = true
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
		fmt.Fprintln(os.Stderr, `Usage: smirror unmirror <mirror_name | local_path | remote_path>

Remove a mirror from config.yaml.
The local directory and remote files are NOT deleted.

Examples:
  smirror unmirror MyProject
  smirror unmirror C:\Projects\MyProject
  smirror unmirror gdrive:backup/MyProject`)
		os.Exit(ExitConfigError)
	}

	cfg := loadConfig(configPath)
	arg := args[0]

	// Find the mirror by name, local path, or remote
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

	// Confirm
	fmt.Printf("Remove mirror '%s'?\n", match.Name)
	fmt.Printf("  Local:  %s\n", match.LocalPath)
	fmt.Printf("  Remote: %s\n", match.Remote)
	fmt.Println()
	fmt.Println("This only removes the mirror from config.yaml.")
	fmt.Println("The local directory and remote files are NOT deleted.")
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

	// Remove from config
	if err := config.RemoveMirror(configPath, match.Name); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}

	fmt.Printf("Mirror '%s' removed from config.\n", match.Name)

	// Check if config is still valid
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
