package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mattn/go-isatty"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
	"github.com/qraveh/SelectiveMirror/internal/state"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
)

// resolveRealPath resolves symlinks, junctions, and other reparse points to
// the final physical path. On Windows, uses GetFinalPathNameByHandle which
// resolves NTFS junctions that filepath.EvalSymlinks does not (SM-138).
// Falls back to filepath.EvalSymlinks → filepath.Abs on failure.
func resolveRealPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	p, err := syscall.UTF16PtrFromString(abs)
	if err != nil {
		return abs
	}
	h, err := syscall.CreateFile(p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0)
	if err != nil {
		// Fall back to EvalSymlinks
		if r, err := filepath.EvalSymlinks(abs); err == nil {
			return r
		}
		return abs
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getFinalPath := kernel32.NewProc("GetFinalPathNameByHandleW")

	buf := make([]uint16, 512)
	n, _, _ := getFinalPath.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		0,
	)
	if n == 0 || n >= uintptr(len(buf)) {
		return abs
	}
	result := syscall.UTF16ToString(buf[:n])
	result = strings.TrimPrefix(result, `\\?\`)
	return result
}

// cmdAddMirror handles `smirror addmirror <path> [<path2> ...] [-dest <remote>]`.
func cmdAddMirror(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror addmirror <local_path> [<local_path2> ...] [flags]

Add one or more directories as mirrors.

Aliases: add-mirror, add

Flags:
  -dest <remote_path>    Override the default remote (e.g., "gdrive:backup/MyProject")
                         Also accepts local paths (e.g., "C:\MyDrive\AI-hub")
  --delete, -d           If destination has content, set delete_policy: delete (non-interactive)
  --initial-sync         Run initial sync immediately after adding the mirror

If the destination already has content, addmirror aborts unless --delete is set
(or you clean the destination manually, then retry). smirror does not manage
backups of pre-existing destination content.

Examples:
  smirror addmirror C:\Projects\MyApp
  smirror addmirror C:\Work -dest C:\MyDrive\backups --initial-sync
  smirror addmirror C:\Docs -dest s3:my-bucket/mirrors --delete`) {
		return
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `Usage: smirror addmirror <local_path> [<local_path2> ...] [flags]

Add one or more directories as mirrors.

Flags:
  -dest <remote_path>    Override the default remote (e.g., "gdrive:backup/MyProject")
                         Also accepts local paths (e.g., "C:\MyDrive\AI-hub")
  --delete, -d           If destination has content, set delete_policy: delete (non-interactive)
  --initial-sync         Run initial sync immediately after adding the mirror

Examples:
  smirror addmirror C:\Projects\MyApp
  smirror addmirror C:\Work -dest C:\MyDrive\backups --initial-sync
  smirror addmirror C:\Docs -dest s3:my-bucket/mirrors --delete`)
		os.Exit(ExitConfigError)
	}

	// Parse args: separate paths from flags
	var localPaths []string
	var destRemote string
	var conflictMode string // "" or "delete"
	var initialSync bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-dest", "--dest":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "Error: -dest requires a remote path argument")
				os.Exit(ExitConfigError)
			}
			destRemote = args[i+1]
			i++
		case "--delete", "-d":
			conflictMode = "delete"
		case "--initial-sync":
			initialSync = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\nRun 'smirror addmirror --help' for usage.\n", a)
				os.Exit(ExitError)
			}
			localPaths = append(localPaths, a)
		}
	}

	if len(localPaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one local path is required")
		os.Exit(ExitConfigError)
	}

	// SM-133: Pre-validate all paths before modifying config (atomicity).
	// Without this, `addmirror valid_path bogus` adds the first mirror to
	// config.yaml, then errors on "bogus" — leaving config in a partial state.
	// SM-134: Also deduplicate by resolved absolute path.
	seen := make(map[string]bool, len(localPaths))
	var validPaths []string
	for _, lp := range localPaths {
		abs, err := filepath.Abs(lp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path %s: %v\n", lp, err)
			os.Exit(ExitError)
		}
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s does not exist: %v\n", abs, err)
			os.Exit(ExitError)
		}
		if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "Error: %s is not a directory\n", abs)
			os.Exit(ExitError)
		}
		// SM-138: Resolve symlinks/junctions to detect aliases to the same directory.
		key := strings.ToLower(filepath.Clean(resolveRealPath(abs)))
		if seen[key] {
			fmt.Fprintf(os.Stderr, "Warning: duplicate path %s (skipped)\n", abs)
			continue
		}
		seen[key] = true
		validPaths = append(validPaths, lp)
	}
	localPaths = validPaths

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

	// Detect rclone — honor rclone_path from config if loaded.
	var rclonePath, rcloneConfig string
	if cfg != nil {
		rclonePath = cfg.RclonePath
		rcloneConfig = cfg.RcloneConfig
	}
	rcloneInfo, err := rclone.Detect(rclonePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: rclone not found: %v\n", err)
		os.Exit(ExitRcloneError)
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
		// SEC-H6 regression closure (panel R4): fresh config.yaml MUST be
		// 0600 to match SECURITY.md baseline. The 0644 here was a creation-
		// path leak that survived the 0.9.9-dev fix because that change
		// only addressed the EDIT path (config.SetField + writePreservingMode).
		// Owner-only mode prevents another local user from reading rclone
		// remote URIs, webhook URLs, and pre/post-sync hook commands.
		if err := os.WriteFile(configPath, []byte(initial), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating config: %v\n", err)
			os.Exit(ExitError)
		}
	}

	// Collect existing mirror names for collision avoidance
	var existingNames []string
	if cfg != nil {
		existingNames = cfg.ProjectNames()
	}

	// SM-137/SM-138: Check that none of the new paths are already configured as
	// mirrors. Resolves symlinks/junctions so aliases to the same directory are caught.
	if cfg != nil {
		existingPaths := make(map[string]string) // normalized path → mirror name
		for _, p := range cfg.Projects {
			if abs, err := filepath.Abs(p.LocalPath); err == nil {
				existingPaths[strings.ToLower(filepath.Clean(resolveRealPath(abs)))] = p.Name
			}
		}
		for _, lp := range localPaths {
			abs, _ := filepath.Abs(lp) // already validated in pre-validation
			key := strings.ToLower(filepath.Clean(resolveRealPath(abs)))
			if name, exists := existingPaths[key]; exists {
				fmt.Fprintf(os.Stderr, "Error: %s is already configured as mirror '%s'\n", abs, name)
				os.Exit(ExitError)
			}
		}
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

	// SM-133: Exit non-zero if any mirrors failed to add (defense in depth).
	if len(addedMirrors) < len(localPaths) {
		os.Exit(ExitError)
	}
}

// addOneMirror adds a single directory as a mirror. Returns the mirror name on
// success, or "" on failure.
// conflictMode: "" = interactive prompt, "delete" = set delete_policy on new mirror.
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

		// Resolve conflict: flag overrides interactive prompt.
		choice := conflictMode
		if choice == "" {
			if !isInteractive() {
				fmt.Fprintln(os.Stderr, "  Error: destination has existing content.")
				fmt.Fprintln(os.Stderr, "  Either pass --delete to set delete_policy, or clean the destination manually and retry.")
				return ""
			}
			reader := bufio.NewReader(os.Stdin)
			for {
				fmt.Println("  Options:")
				fmt.Println("    [d] Delete: remove existing destination files before syncing")
				fmt.Println("    [a] Abort  (you can clean the destination manually and retry)")
				fmt.Print("  Choice [d/a]: ")
				line, _ := reader.ReadString('\n')
				choice = strings.TrimSpace(strings.ToLower(line))
				if choice == "d" || choice == "a" {
					break
				}
				fmt.Printf("  Invalid choice: %q\n", choice)
			}
		}

		switch choice {
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
