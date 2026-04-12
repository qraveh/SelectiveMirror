package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
	"github.com/qraveh/SelectiveMirror/internal/state"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// cmdAddMirror handles `smirror addmirror <path> [<path2> ...] [-dest <remote>]`.
func cmdAddMirror(configPath string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `Usage: smirror addmirror <local_path> [<local_path2> ...] [flags]

Add one or more directories as mirrors.

Flags:
  -dest <remote_path>    Override the default remote (e.g., "gdrive:backup/MyProject")
                         Also accepts local paths (e.g., "C:\MyDrive\AI-hub")
  --backup, -b           If destination has content, rename it to .bak (non-interactive)
  --delete, -d           If destination has content, set delete_policy: delete (non-interactive)
  --initial-sync         Run initial sync immediately after adding the mirror

Examples:
  smirror addmirror C:\Projects\MyApp
  smirror addmirror C:\Work -dest C:\MyDrive\backups --backup --initial-sync
  smirror addmirror C:\Docs -dest s3:my-bucket/mirrors --delete`)
		os.Exit(ExitConfigError)
	}

	// Parse args: separate paths from flags
	var localPaths []string
	var destRemote string
	var conflictMode string // "", "backup", or "delete"
	var initialSync bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-dest", "--dest":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: -dest requires a remote path argument")
				os.Exit(ExitConfigError)
			}
			destRemote = args[i+1]
			i++
		case "--backup", "-b":
			conflictMode = "backup"
		case "--delete", "-d":
			conflictMode = "delete"
		case "--initial-sync":
			initialSync = true
		default:
			localPaths = append(localPaths, args[i])
		}
	}

	if len(localPaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one local path is required")
		os.Exit(ExitConfigError)
	}

	// SM-104: Use LoadRaw first to read default_remote even when config has no
	// mirrors yet (validation would reject it). Fall back to full Load for the
	// rest of addmirror logic.
	cfg, cfgErr := config.Load(configPath)
	if cfgErr != nil {
		// Config exists but invalid (e.g., no mirrors yet) — try raw parse for default_remote
		if rawCfg, rawErr := config.LoadRaw(configPath); rawErr == nil {
			cfg = rawCfg
			cfgErr = nil // config exists and parsed, just didn't pass validation
		}
	}

	// Determine base remote
	baseRemote := destRemote
	if baseRemote == "" && cfg != nil {
		baseRemote = cfg.DefaultRemote
	}
	if baseRemote == "" {
		fmt.Fprintln(os.Stderr, "Error: no destination specified and no default_remote configured.")
		fmt.Fprintln(os.Stderr, "Either use -dest <remote_path> or set a default:")
		fmt.Fprintf(os.Stderr, "  smirror remote gdrive:smirror\n")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Local paths are also supported (e.g., Google Drive mapped folder):")
		fmt.Fprintf(os.Stderr, "  smirror addmirror C:\\Work -dest C:\\MyDrive\\backups\n")
		os.Exit(ExitConfigError)
	}

	// Resolve local paths: if dest is a local filesystem path (C:\..., \\..., /...),
	// resolve it to absolute and verify it exists. rclone handles local-to-local natively.
	localDest := isLocalPath(baseRemote)
	if localDest {
		abs, err := filepath.Abs(baseRemote)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path %s: %v\n", baseRemote, err)
			os.Exit(ExitConfigError)
		}
		baseRemote = abs
		// Verify destination directory exists (or create it)
		if info, err := os.Stat(baseRemote); err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("Destination directory does not exist: %s\n", baseRemote)
				if isInteractive() {
					fmt.Print("Create it? [Y/n] ")
					if confirmDefault(true) {
						if err := os.MkdirAll(baseRemote, 0755); err != nil {
							fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
							os.Exit(ExitError)
						}
						fmt.Printf("Created %s\n", baseRemote)
					} else {
						fmt.Println("Aborted.")
						return
					}
				} else {
					fmt.Fprintln(os.Stderr, "Create the directory first, then retry.")
					os.Exit(ExitConfigError)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error accessing %s: %v\n", baseRemote, err)
				os.Exit(ExitError)
			}
		} else if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "Error: %s is not a directory\n", baseRemote)
			os.Exit(ExitConfigError)
		}
	} else if !strings.Contains(baseRemote, ":") {
		fmt.Fprintf(os.Stderr, "Error: remote must be in rclone format (<remote_name>:<path>) or a local path.\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  rclone remote: gdrive:backup/MyProject\n")
		fmt.Fprintf(os.Stderr, "  local path:    C:\\MyDrive\\AI-hub\n")
		os.Exit(ExitConfigError)
	}

	// Detect rclone
	rcloneInfo, err := rclone.Detect("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: rclone not found: %v\n", err)
		os.Exit(ExitRcloneError)
	}
	var rcloneConfig string
	if cfg != nil {
		rcloneConfig = cfg.RcloneConfig
	}

	// Verify rclone remote exists (skip for local paths — rclone handles them natively)
	if !localDest {
		remoteName := rclone.RemoteNameFromPath(baseRemote)
		exists, _ := rclone.HasRemote(rcloneInfo.Path, rcloneConfig, remoteName)
		if !exists {
			fmt.Printf("Remote '%s' not found.\n", remoteName)
			if !isInteractive() {
				fmt.Fprintln(os.Stderr, "Run 'rclone config' to set up the remote, then retry.")
				os.Exit(ExitConfigError)
			}
			fmt.Print("Run rclone config to set it up? [Y/n] ")
			if !confirmDefault(true) {
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
				fmt.Fprintf(os.Stderr, "Remote '%s' still not found.\n", remoteName)
				os.Exit(ExitConfigError)
			}
			fmt.Println()
		}
	}

	// Handle config not existing — create with sensible defaults
	if cfgErr != nil {
		fmt.Printf("Config %s does not exist. Creating it.\n", configPath)
		dir := filepath.Dir(configPath)
		_ = os.MkdirAll(dir, 0755)
		initial := fmt.Sprintf(`default_remote: %q

# Patterns applied to ALL mirrors (in addition to per-mirror .syncignore)
# Uses .gitignore syntax
global_excludes:
  - .git/
  - __pycache__/
  - "*.pyc"
  - node_modules/
  - venv/
  - .venv/
  - "*.log"
  - "*.tar"
  - "*.tar.gz"
  - "*.zip"
  - "*.sqlite"
  - .env
  - "~$*"
  - "*.tmp"
  - ".~lock.*"
  - "*~"
`, baseRemote)
		if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating config: %v\n", err)
			os.Exit(ExitError)
		}
	}

	// Collect existing mirror names for collision avoidance
	var existingNames []string
	if cfg != nil {
		existingNames = cfg.ProjectNames()
	}

	// Process each path
	var addedMirrors []string
	for _, lp := range localPaths {
		name := addOneMirror(configPath, lp, baseRemote, conflictMode, initialSync, rcloneInfo, rcloneConfig, &existingNames)
		if name != "" {
			addedMirrors = append(addedMirrors, name)
		}
	}

	// Run initial sync for all successfully added mirrors
	if len(addedMirrors) > 0 {
		doSync := initialSync
		if !doSync && isInteractive() {
			fmt.Println()
			fmt.Print("Run initial sync now? [Y/n] ")
			doSync = confirmDefault(true)
		}
		if doSync {
			runInitialSync(configPath, addedMirrors)
		}
	}
}

// addOneMirror adds a single directory as a mirror. Returns the mirror name on
// success, or "" on failure.
// conflictMode: "" = interactive prompt, "backup" = rename .bak, "delete" = delete policy.
func addOneMirror(configPath, localPath, baseRemote string, conflictMode string, initialSync bool, rcloneInfo *rclone.Info, rcloneConfig string, existingNames *[]string) string {
	// Resolve path
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving path %s: %v\n", localPath, err)
		return ""
	}
	info, err := os.Stat(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s does not exist: %v\n", absPath, err)
		return ""
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: %s is not a directory\n", absPath)
		return ""
	}

	// Derive mirror name
	baseName := filepath.Base(absPath)
	mirrorName := config.UniqueMirrorName(baseName, *existingNames)
	*existingNames = append(*existingNames, mirrorName)

	// Determine remote/destination path.
	// -dest always specifies the parent directory; mirror name is appended.
	var remote string
	if isLocalPath(baseRemote) {
		// Local destination: use filepath.Join for proper OS separators
		remote = filepath.Join(baseRemote, mirrorName)
	} else {
		// Rclone remote: append mirror name with forward slash
		remote = strings.TrimRight(baseRemote, "/") + "/" + mirrorName
	}

	// For local destinations, ensure the target directory exists
	if isLocalPath(remote) {
		if _, err := os.Stat(remote); os.IsNotExist(err) {
			if err := os.MkdirAll(remote, 0755); err != nil {
				fmt.Fprintf(os.Stderr, "Error creating destination directory %s: %v\n", remote, err)
				return ""
			}
		}
	}

	localDest := isLocalPath(remote)

	fmt.Printf("\nAdding mirror: %s\n", mirrorName)
	fmt.Printf("  Local:  %s\n", absPath)
	fmt.Printf("  Dest:   %s\n", remote)

	// Test connectivity (skip for local paths — already verified)
	if !localDest {
		fmt.Print("  Testing remote... ")
		if err := rclone.TestRemote(rcloneInfo.Path, rcloneConfig, remote, nil); err != nil {
			fmt.Println("FAIL")
			fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
			return ""
		}
		fmt.Println("OK")
	}

	// Check if destination has existing content
	deletePolicy := ""
	var count int
	if localDest {
		entries, err := os.ReadDir(remote)
		if err == nil {
			count = len(entries)
		}
	} else {
		count, _ = rclone.CountRemoteFiles(rcloneInfo.Path, rcloneConfig, remote, nil)
	}
	if count > 0 {
		fmt.Printf("  Destination already has %d file(s).\n", count)

		// Resolve conflict: flag overrides interactive prompt
		choice := conflictMode
		if choice == "" {
			if !isInteractive() {
				fmt.Fprintln(os.Stderr, "  Error: destination has existing content. Use --backup or --delete.")
				return ""
			}
			reader := bufio.NewReader(os.Stdin)
			for {
				fmt.Println("  Options:")
				fmt.Println("    [b] Backup: rename existing destination to .bak before syncing")
				fmt.Println("    [d] Delete: remove existing destination files before syncing")
				fmt.Println("    [a] Abort")
				fmt.Print("  Choice [b/d/a]: ")
				line, _ := reader.ReadString('\n')
				choice = strings.TrimSpace(strings.ToLower(line))
				if choice == "b" || choice == "d" || choice == "a" {
					break
				}
				fmt.Printf("  Invalid choice: %q\n", choice)
			}
		}

		switch choice {
		case "b", "backup":
			if !backupDestination(remote, localDest, rcloneInfo, rcloneConfig) {
				return ""
			}
		case "d", "delete":
			deletePolicy = "delete"
		case "a", "abort":
			fmt.Println("  Skipped.")
			return ""
		}
	}

	// Handle .syncignore
	syncIgnorePath := filepath.Join(absPath, ".syncignore")
	if _, err := os.Stat(syncIgnorePath); err == nil {
		fmt.Println("  Found existing .syncignore")
	} else if isInteractive() {
		fmt.Print("  No .syncignore found. Create a default one? [Y/n] ")
		if confirmDefault(true) {
			// Load config for global_excludes
			cfg, _ := config.Load(configPath)
			if err := createDefaultSyncIgnore(absPath, cfg); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: %v\n", err)
			} else {
				fmt.Printf("  Created %s\n", syncIgnorePath)
			}
		}
	}

	// Add to config
	p := config.Project{
		Name:            mirrorName,
		LocalPath:       absPath,
		Remote:          remote,
		DeletePolicyStr: deletePolicy,
	}
	if err := config.AddMirror(configPath, p); err != nil {
		fmt.Fprintf(os.Stderr, "  Error adding to config: %v\n", err)
		return ""
	}

	// Validate — if broken, undo the add
	if _, err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "  Error: config validation failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "  Reverting: removing mirror from config.")
		if undoErr := config.RemoveMirror(configPath, mirrorName); undoErr != nil {
			fmt.Fprintf(os.Stderr, "  Error reverting: %v\n", undoErr)
			fmt.Fprintf(os.Stderr, "  Config may need manual repair: %s\n", configPath)
		}
		return ""
	}

	fmt.Printf("  Mirror '%s' added successfully.\n", mirrorName)
	return mirrorName
}

// runInitialSync runs a one-shot sync for the named mirrors.
// Reuses the sync-now logic: load config, open state, build filters, enqueue, run.
func runInitialSync(configPath string, mirrorNames []string) {
	cfg := loadConfig(configPath)

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening state DB: %v\n", err)
		return
	}
	defer st.Close()

	filters := buildFilters(cfg)
	syncEngine := msync.NewEngine(cfg, st, filters, nil)

	ctx := context.Background()

	fmt.Println()
	for _, name := range mirrorNames {
		proj := cfg.FindProject(name)
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Warning: mirror %q not found in config, skipping sync\n", name)
			continue
		}
		fmt.Printf("Syncing %s...\n", name)
		syncEngine.Queue.Enqueue(msync.Task{Project: *proj, RelPath: ""})
	}

	syncEngine.Queue.Close()
	syncEngine.Run(ctx)
	fmt.Println("Initial sync complete.")
}

// backupDestination renames the destination to <dest>.bak, rotating previous
// backups: .bak -> .bak.2, .bak.2 dropped. Keeps at most 2 backup generations.
// For local paths, uses os.Rename. For rclone remotes, uses rclone moveto.
// Returns true on success, false on failure (error already printed).
func backupDestination(remote string, localDest bool, rcloneInfo *rclone.Info, rcloneConfig string) bool {
	bak1 := remote + ".bak"
	bak2 := remote + ".bak.2"
	fmt.Printf("  Backing up: %s -> %s\n", filepath.Base(remote), filepath.Base(bak1))

	if localDest {
		// Rotate: drop .bak.2, move .bak -> .bak.2
		if _, err := os.Stat(bak2); err == nil {
			fmt.Printf("  Removing oldest backup %s\n", filepath.Base(bak2))
			if err := os.RemoveAll(bak2); err != nil {
				fmt.Fprintf(os.Stderr, "  Error removing %s: %v\n", filepath.Base(bak2), err)
				return false
			}
		}
		if _, err := os.Stat(bak1); err == nil {
			fmt.Printf("  Rotating: %s -> %s\n", filepath.Base(bak1), filepath.Base(bak2))
			if err := os.Rename(bak1, bak2); err != nil {
				fmt.Fprintf(os.Stderr, "  Error rotating backup: %v\n", err)
				return false
			}
		}
		if err := os.Rename(remote, bak1); err != nil {
			fmt.Fprintf(os.Stderr, "  Error renaming: %v\n", err)
			return false
		}
		// Recreate the destination directory
		if err := os.MkdirAll(remote, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "  Error recreating directory: %v\n", err)
			return false
		}
	} else {
		// Rclone remote: rotate via moveto (server-side where supported)
		rcloneMove := func(src, dst string) error {
			args := []string{}
			if rcloneConfig != "" {
				args = append(args, "--config", rcloneConfig)
			}
			args = append(args, "moveto", src, dst)
			cmd := exec.Command(rcloneInfo.Path, args...)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
		rclonePurge := func(path string) error {
			args := []string{}
			if rcloneConfig != "" {
				args = append(args, "--config", rcloneConfig)
			}
			args = append(args, "purge", path)
			cmd := exec.Command(rcloneInfo.Path, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		}
		// Drop .bak.2 (best-effort)
		_ = rclonePurge(bak2)
		// Rotate .bak -> .bak.2 (best-effort — may not exist)
		_ = rcloneMove(bak1, bak2)
		// Move dest -> .bak
		if err := rcloneMove(remote, bak1); err != nil {
			fmt.Fprintf(os.Stderr, "  Error: rclone moveto failed: %v\n", err)
			return false
		}
	}
	fmt.Println("  Backup complete.")
	return true
}

// isLocalPath returns true if the string looks like a local filesystem path
// rather than an rclone remote. Detects Windows drive letters (C:\...), UNC
// paths (\\server\share), and Unix absolute paths (/...).
func isLocalPath(s string) bool {
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	if strings.HasPrefix(s, `\\`) || strings.HasPrefix(s, "/") {
		return true
	}
	return false
}

// isInteractive returns true if stdin is a terminal.
func isInteractive() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// confirmDefault reads a y/n answer from stdin. If input is empty, returns defaultYes.
func confirmDefault(defaultYes bool) bool {
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}
