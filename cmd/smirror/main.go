// Command smirror is a selective near-real-time file mirror built on rclone.
package main

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/lock"
	"github.com/qraveh/SelectiveMirror/internal/logging"
	"github.com/qraveh/SelectiveMirror/internal/metrics"
	"github.com/qraveh/SelectiveMirror/internal/state"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
	"github.com/qraveh/SelectiveMirror/internal/watcher"

	_ "modernc.org/sqlite"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Find config file
	configPath := config.DefaultConfigPath()
	args := os.Args[1:]

	// Check for --config flag
	for i, arg := range args {
		if arg == "--config" && i+1 < len(args) {
			configPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			configPath = strings.TrimPrefix(arg, "--config=")
			args = append(args[:i], args[i+1:]...)
			break
		}
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	switch cmd {
	case "start":
		cmdStart(configPath, cmdArgs)
	case "sync-now":
		cmdSyncNow(configPath, cmdArgs)
	case "dry-run":
		cmdDryRun(configPath, cmdArgs)
	case "status":
		cmdStatus(configPath)
	case "validate":
		cmdValidate(configPath)
	case "list-filters":
		cmdListFilters(configPath, cmdArgs)
	case "explain":
		cmdExplain(configPath, cmdArgs)
	case "doctor":
		cmdDoctor(configPath)
	case "verify":
		cmdVerify(configPath, cmdArgs)
	case "version":
		fmt.Printf("smirror %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`smirror %s — Selective near-real-time file mirror

Usage:
  smirror <command> [options]

Commands:
  start                     Start the file watcher (foreground)
  sync-now [project]        Trigger immediate sync for one or all projects
  dry-run [project]         Show what would be synced without doing it
  status                    Show sync status and metrics per project
  validate                  Check configuration and rclone connectivity
  list-filters [project]    Show effective filter rules
  explain <project> <path>  Explain why a file is included or excluded
  doctor                    Run comprehensive self-test diagnostics
  verify [project]          Compare local vs remote and report drift
  version                   Show version

Options:
  --config PATH      Path to config file (default: ~/.selectivemirror/config.yaml)

`, version)
}

func loadConfig(path string) *config.Global {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", path, err)
		os.Exit(1)
	}
	return cfg
}

func buildFilters(cfg *config.Global) map[string]*filter.Engine {
	filters := make(map[string]*filter.Engine)
	for _, proj := range cfg.Projects {
		fe, err := filter.New(cfg.GlobalExcludes, proj.SyncIgnoreFile())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: filter error for %s: %v\n", proj.Name, err)
			fe, _ = filter.New(cfg.GlobalExcludes, "")
		}
		filters[proj.Name] = fe
	}
	return filters
}

// dataDir returns the directory containing the state DB — used for lock, heartbeat, status.json.
func dataDir(cfg *config.Global) string {
	return filepath.Dir(cfg.StateDB)
}

func cmdStart(configPath string, args []string) {
	cfg := loadConfig(configPath)

	// Acquire single-instance lock (in same dir as state DB)
	lk, err := lock.Acquire(dataDir(cfg))
	if err != nil {
		if err == lock.ErrAlreadyRunning {
			fmt.Fprintln(os.Stderr, "Error: another smirror instance is already running.")
			fmt.Fprintln(os.Stderr, "Use 'smirror doctor' to check instance status.")
		} else {
			fmt.Fprintf(os.Stderr, "Error acquiring lock: %v\n", err)
		}
		os.Exit(1)
	}
	defer lk.Release()

	// Setup logging (foreground: console + file)
	rw, err := logging.Setup(cfg.LogLevel, cfg.LogFile, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error setting up logging: %v\n", err)
		os.Exit(1)
	}
	if rw != nil {
		defer rw.Close()
	}

	slog.Info("smirror starting", "version", version, "config", configPath)

	// Open state store
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		slog.Error("state db open failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	// Build filter engines
	filters := buildFilters(cfg)

	// Create metrics collector
	m := metrics.New()

	// Create sync engine (with metrics)
	syncEngine := msync.NewEngine(cfg, st, filters, m)

	// Create watcher manager (with delete policy)
	watchMgr, err := watcher.NewManager(cfg.Projects, filters, syncEngine.TaskChan, cfg.DeletePolicy())
	if err != nil {
		slog.Error("watcher creation failed", "error", err)
		os.Exit(1)
	}

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Run startup reconciliation
	slog.Info("running startup reconciliation")
	reconcileAll(ctx, cfg, st, filters, syncEngine)
	m.RecordScanComplete()

	// Start watcher
	if err := watchMgr.Start(ctx); err != nil {
		slog.Error("watcher start failed", "error", err)
		os.Exit(1)
	}
	defer watchMgr.Stop()

	// Start sync engine (has internal panic recovery per task)
	go syncEngine.Run(ctx)

	// Start heartbeat (writes status.json + heartbeat to DB + health checks)
	go heartbeatLoop(ctx, st, cfg, m, watchMgr)

	slog.Info("smirror running", "projects", cfg.ProjectNames())
	fmt.Println("Press Ctrl+C to stop")

	// Block until context is cancelled
	<-ctx.Done()
	slog.Info("smirror stopped")
}

func cmdSyncNow(configPath string, args []string) {
	cfg := loadConfig(configPath)
	logging.Setup(cfg.LogLevel, "", true)

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	filters := buildFilters(cfg)
	syncEngine := msync.NewEngine(cfg, st, filters, nil)

	ctx := context.Background()

	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown project: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(1)
		}
		syncEngine.TaskChan <- msync.Task{Project: *proj, RelPath: ""}
	} else {
		for _, proj := range cfg.Projects {
			syncEngine.TaskChan <- msync.Task{Project: proj, RelPath: ""}
		}
	}

	// Process all queued tasks
	go syncEngine.Run(ctx)

	// Wait for queue to drain
	for len(syncEngine.TaskChan) > 0 {
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(2 * time.Second) // Allow last task to complete
	fmt.Println("Sync complete")
}

func cmdDryRun(configPath string, args []string) {
	cfg := loadConfig(configPath)
	logging.Setup(cfg.LogLevel, "", true)

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	filters := buildFilters(cfg)
	syncEngine := msync.NewEngine(cfg, st, filters, nil)

	ctx := context.Background()

	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown project: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(1)
		}
		projects = []config.Project{*proj}
	}

	for _, proj := range projects {
		if err := syncEngine.DryRun(ctx, proj); err != nil {
			fmt.Fprintf(os.Stderr, "Dry run error for %s: %v\n", proj.Name, err)
		}
		fmt.Println()
	}
}

func cmdStatus(configPath string) {
	cfg := loadConfig(configPath)

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	fmt.Printf("SelectiveMirror Status\n")
	fmt.Printf("======================\n\n")
	fmt.Printf("Config: %s\n", configPath)
	fmt.Printf("State DB: %s\n", cfg.StateDB)
	fmt.Printf("Delete policy: %s\n\n", cfg.DeletePolicy())

	// Show live metrics if status.json exists
	statusPath := filepath.Join(dataDir(cfg), "status.json")
	if data, err := os.ReadFile(statusPath); err == nil {
		var s metrics.Status
		if json.Unmarshal(data, &s) == nil {
			fmt.Printf("Live Metrics (from running instance):\n")
			fmt.Printf("  Uptime: %s (started %s)\n", s.Uptime, s.StartTime)
			fmt.Printf("  Files synced: %d\n", s.FilesSynced)
			fmt.Printf("  Bytes uploaded: %d\n", s.BytesUploaded)
			fmt.Printf("  Sync errors: %d\n", s.SyncErrors)
			fmt.Printf("  Avg latency: %dms\n", s.AvgLatencyMs)
			fmt.Printf("  Queue depth: %d\n", s.QueueDepth)
			if s.LastScanTime != "" {
				fmt.Printf("  Last reconciliation: %s\n", s.LastScanTime)
			}
			fmt.Printf("  Status generated: %s\n\n", s.GeneratedAt)
		}
	}

	lastHB, _ := st.GetMeta("last_heartbeat")
	if lastHB != "" {
		fmt.Printf("Last heartbeat: %s\n\n", lastHB)
	}

	// Check instance status
	locked, pid := lock.IsLocked(dataDir(cfg))
	if locked {
		fmt.Printf("Instance: running (PID %d)\n\n", pid)
	} else {
		fmt.Printf("Instance: not running\n\n")
	}

	for _, proj := range cfg.Projects {
		lastSync, _ := st.GetLastSyncTime(proj.Name)
		pending, _ := st.GetPendingFiles(proj.Name)
		synced, _ := st.GetAllSyncedPaths(proj.Name)

		fmt.Printf("Project: %s\n", proj.Name)
		fmt.Printf("  Path:    %s\n", proj.LocalPath)
		fmt.Printf("  Remote:  %s\n", proj.Remote)
		fmt.Printf("  Files synced: %d\n", len(synced))
		if !lastSync.IsZero() {
			fmt.Printf("  Last file sync: %s (%s ago)\n", lastSync.Format(time.RFC3339), time.Since(lastSync).Round(time.Second))
		}
		lastFullSync, _ := st.GetMeta("last_full_sync_" + proj.Name)
		if lastFullSync != "" {
			if t, err := time.Parse(time.RFC3339, lastFullSync); err == nil {
				fmt.Printf("  Last full sync: %s (%s ago)\n", lastFullSync, time.Since(t).Round(time.Second))
			} else {
				fmt.Printf("  Last full sync: %s\n", lastFullSync)
			}
		}
		if lastSync.IsZero() && lastFullSync == "" {
			fmt.Printf("  Last sync: never\n")
		}
		if len(pending) > 0 {
			fmt.Printf("  Pending retries: %d\n", len(pending))
		}
		fmt.Println()
	}
}

func cmdValidate(configPath string) {
	cfg := loadConfig(configPath)
	fmt.Printf("Config: %s - OK (%d projects)\n", configPath, len(cfg.Projects))

	for _, proj := range cfg.Projects {
		fmt.Printf("  %s: %s -> %s\n", proj.Name, proj.LocalPath, proj.Remote)
		// Check .syncignore
		ignoreFile := proj.SyncIgnoreFile()
		if _, err := os.Stat(ignoreFile); err == nil {
			fmt.Printf("    .syncignore: %s\n", ignoreFile)
		} else {
			fmt.Printf("    .syncignore: (none)\n")
		}
	}

	fmt.Println()
	if err := msync.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\nValidation FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nAll checks passed")
}

func cmdListFilters(configPath string, args []string) {
	cfg := loadConfig(configPath)
	filters := buildFilters(cfg)

	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown project: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(1)
		}
		projects = []config.Project{*proj}
	}

	for _, proj := range projects {
		fmt.Printf("=== %s ===\n", proj.Name)
		fe := filters[proj.Name]
		if fe == nil {
			fmt.Println("  (no filters)")
			continue
		}
		for _, rule := range fe.EffectiveRules() {
			fmt.Printf("  %s\n", rule)
		}
		fmt.Println()
	}
}

// cmdExplain shows why a specific file is included or excluded and its sync state.
func cmdExplain(configPath string, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: smirror explain <project> <relative-path>")
		fmt.Fprintln(os.Stderr, "Example: smirror explain Orch CLAUDE.md")
		os.Exit(1)
	}

	cfg := loadConfig(configPath)
	projName := args[0]
	relPath := filepath.ToSlash(args[1])

	proj := cfg.FindProject(projName)
	if proj == nil {
		fmt.Fprintf(os.Stderr, "Unknown project: %s\nAvailable: %s\n", projName, strings.Join(cfg.ProjectNames(), ", "))
		os.Exit(1)
	}

	// Build filter
	fe, err := filter.New(cfg.GlobalExcludes, proj.SyncIgnoreFile())
	if err != nil {
		fe, _ = filter.New(cfg.GlobalExcludes, "")
	}

	// Check exclusion
	excluded := fe.IsExcluded(relPath)
	matchedRule := ""

	fmt.Printf("=== Explain: %s / %s ===\n\n", projName, relPath)

	if excluded {
		fmt.Printf("Status: EXCLUDED\n")
		// Find which rule matched
		matchedRule = findMatchingRule(fe, relPath)
		if matchedRule != "" {
			fmt.Printf("Matched rule: %s\n", matchedRule)
		}
	} else {
		fmt.Printf("Status: INCLUDED\n")
	}

	// Remote path
	remotePath := proj.Remote + "/" + relPath
	fmt.Printf("Remote path: %s\n", remotePath)

	// Local file info
	localPath := filepath.Join(proj.LocalPath, filepath.FromSlash(relPath))
	info, err := os.Stat(localPath)
	if err != nil {
		fmt.Printf("Local file: does not exist (%v)\n", err)
	} else {
		fmt.Printf("Local file: %s\n", localPath)
		fmt.Printf("  Size: %d bytes\n", info.Size())
		fmt.Printf("  Modified: %s\n", info.ModTime().Format(time.RFC3339))

		if info.Size() > proj.MaxFileSize() {
			fmt.Printf("  WARNING: exceeds max file size (%dMB)\n", proj.MaxFileSizeMB)
		}

		// Compute current hash
		hash, _, err := hashFile(localPath)
		if err == nil {
			fmt.Printf("  MD5: %s\n", hash)
		}
	}

	// State DB info
	st, err := state.Open(cfg.StateDB)
	if err == nil {
		defer st.Close()
		fs, err := st.GetFileState(proj.Name, relPath)
		if err == nil && fs != nil {
			fmt.Printf("\nSync state:\n")
			fmt.Printf("  Last synced: %s\n", fs.SyncedAt.Format(time.RFC3339))
			fmt.Printf("  Synced hash: %s\n", fs.LocalHash)
			fmt.Printf("  Synced size: %d bytes\n", fs.FileSize)
			fmt.Printf("  rclone exit: %d", fs.RcloneExit)
			if fs.RcloneExit == 0 {
				fmt.Printf(" (success)")
			} else {
				fmt.Printf(" (FAILED)")
			}
			fmt.Println()
		} else {
			fmt.Printf("\nSync state: never synced\n")
		}
	}
}

// findMatchingRule tries to identify which filter rule caused the exclusion.
func findMatchingRule(fe *filter.Engine, relPath string) string {
	rules := fe.EffectiveRules()
	for _, rule := range rules {
		if strings.HasPrefix(rule, "#") {
			continue // skip headers
		}
		// Test each rule individually
		testFe, _ := filter.New([]string{rule}, "")
		if testFe != nil && testFe.IsExcluded(relPath) {
			return rule
		}
	}
	return "(could not determine specific rule)"
}

// cmdDoctor runs comprehensive self-test diagnostics.
func cmdDoctor(configPath string) {
	fmt.Printf("smirror doctor — %s\n\n", version)
	passed := 0
	failed := 0

	check := func(name string, fn func() error) {
		fmt.Printf("  %-45s ", name)
		if err := fn(); err != nil {
			fmt.Printf("FAIL: %v\n", err)
			failed++
		} else {
			fmt.Printf("OK\n")
			passed++
		}
	}

	// 1. Config file
	var cfg *config.Global
	check("Config file parses", func() error {
		var err error
		cfg, err = config.Load(configPath)
		return err
	})
	if cfg == nil {
		fmt.Printf("\nCannot continue without valid config. %d passed, %d failed.\n", passed, failed)
		os.Exit(1)
	}

	// 2. Project paths exist
	check("All project paths exist", func() error {
		for _, p := range cfg.Projects {
			if _, err := os.Stat(p.LocalPath); err != nil {
				return fmt.Errorf("%s: %v", p.Name, err)
			}
		}
		return nil
	})

	// 3. No duplicate names
	check("No duplicate project names", func() error {
		seen := make(map[string]bool)
		for _, p := range cfg.Projects {
			if seen[p.Name] {
				return fmt.Errorf("duplicate: %s", p.Name)
			}
			seen[p.Name] = true
		}
		return nil
	})

	// 4. rclone binary
	check("rclone binary found", func() error {
		rp := cfg.RclonePath
		if rp == "" {
			rp = "rclone"
		}
		cmd := exec.Command(rp, "version")
		_, err := cmd.Output()
		return err
	})

	// 5. Remote connectivity
	for _, proj := range cfg.Projects {
		p := proj // capture
		check(fmt.Sprintf("Remote reachable: %s", p.Remote), func() error {
			rp := cfg.RclonePath
			if rp == "" {
				rp = "rclone"
			}
			cmd := exec.Command(rp, "lsd", p.Remote, "--max-depth", "0")
			return cmd.Run()
		})
	}

	// 6. State DB
	check("State DB opens and schema valid", func() error {
		st, err := state.Open(cfg.StateDB)
		if err != nil {
			return err
		}
		st.Close()
		return nil
	})

	// 7. State DB integrity
	check("State DB integrity check", func() error {
		db, err := sql.Open("sqlite", cfg.StateDB)
		if err != nil {
			return err
		}
		defer db.Close()
		var result string
		err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
		if err != nil {
			return err
		}
		if result != "ok" {
			return fmt.Errorf("integrity_check returned: %s", result)
		}
		return nil
	})

	// 8. Log file writable
	check("Log file writable", func() error {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		f.Close()
		return nil
	})

	// 9. Single-instance lock
	check("Single-instance lock available", func() error {
		locked, pid := lock.IsLocked(dataDir(cfg))
		if locked {
			return fmt.Errorf("another instance running (PID %d)", pid)
		}
		return nil
	})

	// 10. Watcher can be created
	check("Filesystem watcher available", func() error {
		_, err := watcher.NewManager(cfg.Projects[:1], buildFilters(cfg), make(chan msync.Task, 1), cfg.DeletePolicy())
		return err
	})

	// 11. Write permissions
	check("Write permissions on project dirs", func() error {
		for _, p := range cfg.Projects {
			testPath := filepath.Join(p.LocalPath, ".smirror_write_test")
			if err := os.WriteFile(testPath, []byte("test"), 0644); err != nil {
				return fmt.Errorf("%s: %v", p.Name, err)
			}
			os.Remove(testPath)
		}
		return nil
	})

	// 12. Filters
	check("Filter engines load without error", func() error {
		for _, p := range cfg.Projects {
			_, err := filter.New(cfg.GlobalExcludes, p.SyncIgnoreFile())
			if err != nil {
				return fmt.Errorf("%s: %v", p.Name, err)
			}
		}
		return nil
	})

	fmt.Printf("\n%d passed, %d failed\n", passed, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// cmdVerify compares local state vs remote and reports drift.
func cmdVerify(configPath string, args []string) {
	cfg := loadConfig(configPath)
	logging.Setup(cfg.LogLevel, "", true)
	filters := buildFilters(cfg)

	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown project: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(1)
		}
		projects = []config.Project{*proj}
	}

	totalDrift := 0
	for _, proj := range projects {
		drift := verifyProject(cfg, proj, filters[proj.Name])
		totalDrift += drift
	}

	if totalDrift == 0 {
		fmt.Println("\nNo drift detected.")
	} else {
		fmt.Printf("\nTotal drift: %d files\n", totalDrift)
		os.Exit(1)
	}
}

func verifyProject(cfg *config.Global, proj config.Project, fe *filter.Engine) int {
	fmt.Printf("=== Verify: %s ===\n", proj.Name)
	fmt.Printf("Local:  %s\n", proj.LocalPath)
	fmt.Printf("Remote: %s\n\n", proj.Remote)

	// Get remote file list
	remoteFiles, err := msync.ListRemote(cfg, proj)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error listing remote: %v\n", err)
		return 1
	}

	// Build remote map (path -> RemoteFile)
	remoteMap := make(map[string]msync.RemoteFile)
	for _, rf := range remoteFiles {
		if !rf.IsDir {
			remoteMap[filepath.ToSlash(rf.Path)] = rf
		}
	}

	// Walk local tree
	localFiles := make(map[string]bool)
	drift := 0

	filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			relPath, _ := filepath.Rel(proj.LocalPath, path)
			if relPath != "." && fe != nil && fe.IsExcluded(relPath+"/") {
				return filepath.SkipDir
			}
			return nil
		}

		relPath, _ := filepath.Rel(proj.LocalPath, path)
		relPath = filepath.ToSlash(relPath)

		if fe != nil && fe.IsExcluded(relPath) {
			// Check for excluded leaks (excluded file exists on remote)
			if _, onRemote := remoteMap[relPath]; onRemote {
				fmt.Printf("  LEAK: %s (excluded locally but exists on remote)\n", relPath)
				drift++
			}
			return nil
		}

		localFiles[relPath] = true

		rf, onRemote := remoteMap[relPath]
		if !onRemote {
			fmt.Printf("  MISSING REMOTE: %s\n", relPath)
			drift++
			return nil
		}

		// Check hash if available
		if md5Hash, ok := rf.Hashes["md5"]; ok {
			localHash, _, err := hashFile(path)
			if err == nil && localHash != strings.ToLower(md5Hash) {
				fmt.Printf("  HASH MISMATCH: %s (local=%s remote=%s)\n", relPath, localHash[:8], strings.ToLower(md5Hash)[:8])
				drift++
			}
		}

		return nil
	})

	// Check for unexpected remote files
	for remotePath := range remoteMap {
		if strings.HasPrefix(remotePath, ".quarantine/") {
			continue // skip quarantine directory
		}
		if !localFiles[remotePath] {
			// Check if it's excluded
			if fe != nil && fe.IsExcluded(remotePath) {
				fmt.Printf("  LEAK: %s (excluded locally but exists on remote)\n", remotePath)
			} else {
				fmt.Printf("  ORPHAN REMOTE: %s (not in local tree)\n", remotePath)
			}
			drift++
		}
	}

	if drift == 0 {
		fmt.Printf("  No drift detected (%d local files, %d remote files)\n", len(localFiles), len(remoteMap))
	} else {
		fmt.Printf("  %d drift issues found\n", drift)
	}
	fmt.Println()

	return drift
}

// hashFile computes the MD5 hash of a file (local helper, mirrors state.HashFile).
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := md5.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// reconcileAll uses batch rclone copy per project (fast) instead of per-file sync.
func reconcileAll(ctx context.Context, cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, syncEngine *msync.Engine) {
	for _, proj := range cfg.Projects {
		slog.Info("reconciling", "project", proj.Name)
		// Use full-project sync — single rclone invocation with filters.
		// Much faster than individual file syncs (1 rclone call vs N).
		syncEngine.TaskChan <- msync.Task{Project: proj, RelPath: ""}
	}
}

func heartbeatLoop(ctx context.Context, st *state.Store, cfg *config.Global, m *metrics.Collector, watchMgr *watcher.Manager) {
	interval := cfg.HeartbeatInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	dd := dataDir(cfg)

	for {
		select {
		case <-ticker.C:
			ts := time.Now().UTC().Format(time.RFC3339)
			st.SetMeta("last_heartbeat", ts)

			// Write heartbeat file
			hbPath := filepath.Join(dd, "heartbeat.txt")
			os.WriteFile(hbPath, []byte(ts+"\n"), 0644)

			// Write metrics status.json
			if m != nil {
				if err := m.WriteStatusFile(dd, version); err != nil {
					slog.Warn("failed to write status.json", "error", err)
				}
			}

			// Self-health checks
			if watchMgr != nil {
				// Log watch count for diagnostics
				watchCount := watchMgr.WatchCount()
				slog.Debug("health", "watches", watchCount)

				// Report any runtime errors
				healthErrors := watchMgr.HealthErrors()
				if len(healthErrors) > 0 {
					latest := healthErrors[len(healthErrors)-1]
					slog.Warn("runtime errors recorded",
						"count", len(healthErrors),
						"latest_source", latest.Source,
						"latest_time", latest.Time.Format(time.RFC3339),
						"latest_msg", latest.Message)
					st.SetMeta("last_health_error", fmt.Sprintf("[%s] %s: %s",
						latest.Time.Format(time.RFC3339), latest.Source, latest.Message))
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
