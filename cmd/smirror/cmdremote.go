package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
)

// cmdRemote handles `smirror remote [remote_path]`.
func cmdRemote(configPath string, args []string) {
	if len(args) == 0 {
		cmdRemoteShow(configPath)
		return
	}
	cmdRemoteSet(configPath, args[0])
}

// cmdRemoteShow displays the current default remote and lists remotes in use.
func cmdRemoteShow(configPath string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		// Config may not exist yet — that's OK for this command
		fmt.Printf("Default remote: (not configured)\n")
		fmt.Printf("Config: %s (not found or invalid)\n", configPath)
		return
	}

	if cfg.DefaultRemote != "" {
		fmt.Printf("Default remote: %s\n", cfg.DefaultRemote)
	} else {
		fmt.Println("Default remote: (not configured)")
		fmt.Println("Set with: smirror remote <remote_path>")
		fmt.Println("Example:  smirror remote gdrive:smirror")
	}

	// List remotes used by existing mirrors
	if len(cfg.Projects) > 0 {
		fmt.Println()
		fmt.Println("Remotes in use:")
		seen := make(map[string]bool)
		for _, p := range cfg.Projects {
			name := rclone.RemoteNameFromPath(p.Remote)
			if !seen[name] {
				seen[name] = true
				fmt.Printf("  %s: (used by %s)\n", name, p.Name)
			}
		}
	}
}

// cmdRemoteSet sets the default remote after validating it.
func cmdRemoteSet(configPath string, remotePath string) {
	// Check if this is a local path or an rclone remote
	localDest := isLocalPath(remotePath)

	if localDest {
		// Local path: resolve and verify
		abs, err := filepath.Abs(remotePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path: %v\n", err)
			os.Exit(ExitConfigError)
		}
		remotePath = abs
		info, err := os.Stat(remotePath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "Error: directory does not exist: %s\n", remotePath)
			} else {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			os.Exit(ExitConfigError)
		}
		if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "Error: %s is not a directory\n", remotePath)
			os.Exit(ExitConfigError)
		}
		fmt.Printf("Local destination verified: %s\n", remotePath)
	} else {
		// Rclone remote: validate format and connectivity
		if !strings.Contains(remotePath, ":") {
			fmt.Fprintf(os.Stderr, "Error: remote must be in rclone format (<remote_name>:<path>) or a local path.\n")
			fmt.Fprintf(os.Stderr, "Examples:\n")
			fmt.Fprintf(os.Stderr, "  rclone remote: gdrive:smirror\n")
			fmt.Fprintf(os.Stderr, "  local path:    C:\\MyDrive\\AI-hub\n")
			os.Exit(ExitConfigError)
		}

		rcloneInfo, err := rclone.Detect("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: rclone not found: %v\n", err)
			fmt.Fprintln(os.Stderr, "Install rclone: https://rclone.org/install/")
			os.Exit(ExitRcloneError)
		}

		var rcloneConfig string
		if cfg, err := config.Load(configPath); err == nil {
			rcloneConfig = cfg.RcloneConfig
		}

		remoteName := rclone.RemoteNameFromPath(remotePath)
		exists, err := rclone.HasRemote(rcloneInfo.Path, rcloneConfig, remoteName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not list remotes: %v\n", err)
		}

		if !exists {
			fmt.Printf("Remote '%s' not found in rclone configuration.\n", remoteName)

			if !isatty.IsTerminal(os.Stdin.Fd()) && !isatty.IsCygwinTerminal(os.Stdin.Fd()) {
				fmt.Fprintln(os.Stderr, "Error: cannot run rclone config in non-interactive mode.")
				fmt.Fprintln(os.Stderr, "Run 'rclone config' manually to set up the remote, then retry.")
				os.Exit(ExitConfigError)
			}

			fmt.Print("Run rclone config to set it up? [Y/n] ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line != "" && line != "y" && line != "yes" {
				fmt.Println("Aborted.")
				return
			}

			fmt.Println()
			if err := rclone.RunConfig(rcloneInfo.Path, rcloneConfig); err != nil {
				fmt.Fprintf(os.Stderr, "Error running rclone config: %v\n", err)
				os.Exit(ExitRcloneError)
			}

			exists, _ = rclone.HasRemote(rcloneInfo.Path, rcloneConfig, remoteName)
			if !exists {
				fmt.Fprintf(os.Stderr, "Remote '%s' still not found after rclone config.\n", remoteName)
				fmt.Fprintln(os.Stderr, "Check the remote name and try again.")
				os.Exit(ExitConfigError)
			}
			fmt.Println()
		}

		fmt.Printf("Testing connectivity to %s...", remotePath)
		if err := rclone.TestRemote(rcloneInfo.Path, rcloneConfig, remotePath, nil); err != nil {
			fmt.Println(" FAIL")
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(ExitRcloneError)
		}
		fmt.Println(" OK")
	}

	// Write to config
	if err := config.SetField(configPath, "default_remote", fmt.Sprintf("%q", remotePath)); err != nil {
		fmt.Fprintf(os.Stderr, "Error updating config: %v\n", err)
		os.Exit(ExitError)
	}

	fmt.Printf("Default remote set to: %s\n", remotePath)
}
