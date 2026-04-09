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

	if isInteractive() {
		fmt.Print("Proceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("Cancelled.")
			return
		}
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

	// Try local path match
	absArg, _ := filepath.Abs(arg)
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		absLocal, _ := filepath.Abs(p.LocalPath)
		if strings.EqualFold(filepath.Clean(absArg), filepath.Clean(absLocal)) {
			return p
		}
	}

	// Try remote match
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if strings.EqualFold(p.Remote, arg) {
			return p
		}
	}

	return nil
}
