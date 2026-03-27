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
	"github.com/qraveh/SelectiveMirror/internal/rclone"
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
	case "stats":
		cmdStats(configPath)
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
  stats                     Show file counts and line counts across all projects
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

	// Start heartbeat (writes status.json + heartbeat to DB + health checks + periodic reconciliation)
	go heartbeatLoop(ctx, st, cfg, m, watchMgr, syncEngine)

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

	// Process all queued tasks synchronously: start workers, wait for them
	// to finish processing everything. We close the channel after queueing
	// so workers exit once all tasks are drained.
	close(syncEngine.TaskChan)
	syncEngine.Run(ctx) // blocks until all workers complete
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

	// Ghost scan results
	ghostResult, _ := st.GetMeta("ghost_scan_result")
	ghostTime, _ := st.GetMeta("ghost_scan_time")
	if ghostResult != "" {
		fmt.Println("Ghost Scan:")
		if ghostTime != "" {
			if t, err := time.Parse(time.RFC3339, ghostTime); err == nil {
				fmt.Printf("  Last scan: %s (%s ago)\n", ghostTime, time.Since(t).Round(time.Second))
			}
		}
		if ghostResult == "clean" {
			fmt.Println("  Result: clean (no orphans)")
		} else {
			fmt.Printf("  Result: %s\n", ghostResult)
			ghostDetails, _ := st.GetMeta("ghost_scan_details")
			if ghostDetails != "" {
				for _, line := range strings.Split(ghostDetails, "\n") {
					fmt.Printf("    %s\n", line)
				}
			}
		}
		fmt.Println()
	}
	// Health errors
	lastHealthErr, _ := st.GetMeta("last_health_error")
	if lastHealthErr != "" {
		fmt.Printf("Last Health Error: %s\n\n", lastHealthErr)
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

	// rclone version check
	info, err := rclone.Detect(cfg.RclonePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nrclone detection FAILED: %v\n", err)
		os.Exit(1)
	}
	compat, msg := info.CompatCheck()
	fmt.Printf("\nrclone: %s at %s\n", info.Version, info.Path)
	if compat == rclone.CompatPartial {
		fmt.Printf("  WARNING: %s\n", msg)
	} else if compat == rclone.CompatNone {
		fmt.Fprintf(os.Stderr, "  ERROR: %s\n", msg)
		os.Exit(1)
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
		start := time.Now()
		if err := fn(); err != nil {
			fmt.Printf("FAIL: %v (%dms)\n", err, time.Since(start).Milliseconds())
			failed++
		} else {
			fmt.Printf("OK (%dms)\n", time.Since(start).Milliseconds())
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

	// 4. rclone binary — version, path, compatibility
	var rcloneInfo *rclone.Info
	check("rclone binary found", func() error {
		var err error
		rcloneInfo, err = rclone.Detect(cfg.RclonePath)
		if err != nil {
			return err
		}
		fmt.Printf("\n    version: %s\n    path:    %s\n    os:      %s\n  %-45s ", rcloneInfo.Version, rcloneInfo.Path, rcloneInfo.OS, "rclone version compatibility")
		compat, msg := rcloneInfo.CompatCheck()
		switch compat {
		case rclone.CompatNone:
			return fmt.Errorf("%s", msg)
		case rclone.CompatPartial:
			fmt.Printf("WARN: %s\n  %-45s ", msg, "(continuing)")
			return nil
		default:
			return nil
		}
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
	start := time.Now()
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

		// Skip non-regular files: symlinks, WSL reparse points (ModeIrregular),
		// named pipes, etc. These can't be synced to remote.
		if !d.Type().IsRegular() {
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

	elapsed := time.Since(start)
	if drift == 0 {
		fmt.Printf("  No drift detected (%d local files, %d remote files, %dms)\n", len(localFiles), len(remoteMap), elapsed.Milliseconds())
	} else {
		fmt.Printf("  %d drift issues found (%dms)\n", drift, elapsed.Milliseconds())
	}
	fmt.Println()

	return drift
}

func cmdStats(configPath string) {
	cfg := loadConfig(configPath)
	logging.Setup(cfg.LogLevel, "", true)
	filters := buildFilters(cfg)

	type category struct {
		label string
		exts  []string
	}

	categories := []category{
		{"Go", []string{".go"}},
		{"PowerShell", []string{".ps1", ".psm1", ".psd1"}},
		{"Python", []string{".py"}},
		{"Shell", []string{".sh", ".bash"}},
		{"YAML/JSON", []string{".yaml", ".yml", ".json"}},
		{"XML", []string{".xml"}},
		{"Docs/Text", []string{".md", ".txt", ".rst"}},
		{"VBScript", []string{".vbs"}},
		{"Batch/Cmd", []string{".cmd", ".bat"}},
	}

	// Per-project stats: catKey -> files, lines
	type catCount struct {
		files int
		lines int
	}
	type projectStats struct {
		name     string
		total    catCount
		ignored  int
		bytes    int64
		byCat    map[string]catCount
		other    catCount
	}

	fmt.Printf("smirror stats\n")
	fmt.Printf("=============\n")

	var allStats []projectStats
	grandTotal := catCount{}
	grandBytes := int64(0)
	grandIgnored := 0
	grandByCat := make(map[string]catCount)
	grandOther := catCount{}

	for _, proj := range cfg.Projects {
		fe := filters[proj.Name]
		ps := projectStats{
			name:  proj.Name,
			byCat: make(map[string]catCount),
		}

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
				ps.ignored++
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			lines := countLines(path)
			ps.total.files++
			ps.total.lines += lines
			ps.bytes += info.Size()

			ext := strings.ToLower(filepath.Ext(path))
			matched := false
			for _, cat := range categories {
				for _, e := range cat.exts {
					if ext == e {
						c := ps.byCat[cat.label]
						c.files++
						c.lines += lines
						ps.byCat[cat.label] = c
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				ps.other.files++
				ps.other.lines += lines
			}

			return nil
		})

		allStats = append(allStats, ps)
		grandTotal.files += ps.total.files
		grandTotal.lines += ps.total.lines
		grandBytes += ps.bytes
		grandIgnored += ps.ignored
		for k, v := range ps.byCat {
			c := grandByCat[k]
			c.files += v.files
			c.lines += v.lines
			grandByCat[k] = c
		}
		grandOther.files += ps.other.files
		grandOther.lines += ps.other.lines
	}

	// Print per-project breakdown
	for _, ps := range allStats {
		fmt.Printf("\n%s  (%d files, %d lines, %s, %d ignored)\n",
			ps.name, ps.total.files, ps.total.lines, humanBytes(ps.bytes), ps.ignored)
		for _, cat := range categories {
			if c, ok := ps.byCat[cat.label]; ok && c.files > 0 {
				fmt.Printf("  %-14s %4d files  %6d lines\n", cat.label, c.files, c.lines)
			}
		}
		if ps.other.files > 0 {
			fmt.Printf("  %-14s %4d files  %6d lines\n", "Other", ps.other.files, ps.other.lines)
		}
	}

	// Grand totals
	fmt.Printf("\nTOTAL  (%d files, %d lines, %s, %d ignored)\n",
		grandTotal.files, grandTotal.lines, humanBytes(grandBytes), grandIgnored)
	for _, cat := range categories {
		if c, ok := grandByCat[cat.label]; ok && c.files > 0 {
			fmt.Printf("  %-14s %4d files  %6d lines\n", cat.label, c.files, c.lines)
		}
	}
	if grandOther.files > 0 {
		fmt.Printf("  %-14s %4d files  %6d lines\n", "Other", grandOther.files, grandOther.lines)
	}
}

// countLines counts lines in a file. Returns 0 for binary or unreadable files.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	// Read up to 8KB to detect binary content
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if n == 0 {
		return 0
	}
	// Check for null bytes (binary file indicator)
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return 0 // binary file
		}
	}

	// Count lines in the sample
	lines := 0
	for i := 0; i < n; i++ {
		if buf[i] == '\n' {
			lines++
		}
	}

	// If file is larger than 8KB, read the rest
	if err == nil {
		scanner := make([]byte, 32*1024)
		for {
			n, err := f.Read(scanner)
			for i := 0; i < n; i++ {
				if scanner[i] == '\n' {
					lines++
				}
			}
			if err != nil {
				break
			}
		}
	}

	// Account for last line without trailing newline
	if n > 0 && buf[n-1] != '\n' {
		// Check if file ended without newline
		info, serr := os.Stat(path)
		if serr == nil && info.Size() <= int64(n) {
			lines++ // small file, last line has no newline
		}
		// For large files we already counted via scanner above
	}

	return lines
}

// humanBytes formats bytes into a human-readable string.
func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
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

// reconcileAll uses batch rclone copy per project (fast) instead of per-file sync,
// then scans for ghost files on the remote that don't exist locally.
func reconcileAll(ctx context.Context, cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, syncEngine *msync.Engine) {
	for _, proj := range cfg.Projects {
		slog.Info("reconciling", "project", proj.Name)
		// Use full-project sync — single rclone invocation with filters.
		// Much faster than individual file syncs (1 rclone call vs N).
		syncEngine.TaskChan <- msync.Task{Project: proj, RelPath: ""}
	}

	// Ghost scan: detect orphaned files on remote that no longer exist locally.
	// Runs after reconciliation so that rclone copy has finished uploading.
	// This catches rename orphans, stale files from previous bugs, etc.
	go func() {
		// Wait for reconciliation to finish (rclone copy tasks are queued above)
		time.Sleep(30 * time.Second)
		scanForGhosts(ctx, cfg, st, filters)
	}()
}

// scanForGhosts compares remote state against local filesystem and logs orphans.
// This is a diagnostic scan — it logs warnings but does NOT auto-delete, because
// some orphans are intentional (delete_policy=ignore means remote copies are preserved).
// Cleanup recommendations are written to the state DB for `smirror status` to report.
func scanForGhosts(ctx context.Context, cfg *config.Global, st *state.Store, filters map[string]*filter.Engine) {
	start := time.Now()
	totalGhosts := 0
	var ghostDetails []string

	for _, proj := range cfg.Projects {
		fe := filters[proj.Name]

		remoteFiles, err := msync.ListRemote(cfg, proj)
		if err != nil {
			slog.Warn("ghost scan: failed to list remote", "project", proj.Name, "error", err)
			continue
		}

		// Build set of local files (non-excluded, non-dir)
		localFiles := make(map[string]bool)
		filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
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
				return nil
			}
			localFiles[relPath] = true
			return nil
		})

		// Check each remote file
		for _, rf := range remoteFiles {
			if rf.IsDir {
				continue
			}
			remotePath := filepath.ToSlash(rf.Path)
			if strings.HasPrefix(remotePath, ".quarantine/") {
				continue
			}
			if !localFiles[remotePath] {
				kind := "ORPHAN"
				if fe != nil && fe.IsExcluded(remotePath) {
					kind = "LEAK" // excluded file still on remote
				}
				slog.Warn("ghost file on remote",
					"project", proj.Name,
					"path", remotePath,
					"kind", kind,
					"size", rf.Size)
				ghostDetails = append(ghostDetails, fmt.Sprintf("[%s] %s: %s (%d bytes)",
					kind, proj.Name, remotePath, rf.Size))
				totalGhosts++
			}
		}
	}

	if totalGhosts > 0 {
		summary := fmt.Sprintf("%d ghost files detected on remote. Run 'smirror verify' for details.", totalGhosts)
		slog.Warn(summary)
		st.SetMeta("ghost_scan_result", summary)
		st.SetMeta("ghost_scan_time", time.Now().UTC().Format(time.RFC3339))
		// Store detailed list (truncated to first 50)
		details := strings.Join(ghostDetails, "\n")
		if len(ghostDetails) > 50 {
			details = strings.Join(ghostDetails[:50], "\n") + fmt.Sprintf("\n... and %d more", len(ghostDetails)-50)
		}
		st.SetMeta("ghost_scan_details", details)
	} else {
		st.SetMeta("ghost_scan_result", "clean")
		st.SetMeta("ghost_scan_time", time.Now().UTC().Format(time.RFC3339))
		slog.Info("ghost scan: no orphans detected on remote")
	}
	slog.Info("ghost scan complete", "ghosts", totalGhosts, "ms", time.Since(start).Milliseconds())
}

func heartbeatLoop(ctx context.Context, st *state.Store, cfg *config.Global, m *metrics.Collector, watchMgr *watcher.Manager, syncEngine *msync.Engine) {
	interval := cfg.HeartbeatInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	reconcileInterval := cfg.ReconcileInterval()
	reconcileTicker := time.NewTicker(reconcileInterval)
	defer reconcileTicker.Stop()

	dd := dataDir(cfg)

	for {
		select {
		case <-reconcileTicker.C:
			// Periodic reconciliation: catch changes invisible to fsnotify
			// (WSL operations, network drive edits, external tools).
			if syncEngine != nil {
				slog.Info("periodic reconciliation")
				for _, proj := range cfg.Projects {
					select {
					case syncEngine.TaskChan <- msync.Task{Project: proj, RelPath: ""}:
					case <-ctx.Done():
						return
					}
				}
			}
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
