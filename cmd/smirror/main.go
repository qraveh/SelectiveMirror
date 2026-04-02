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
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	gosync "sync"
	"syscall"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/anomaly"
	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/hooks"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/lock"
	"github.com/qraveh/SelectiveMirror/internal/logging"
	"github.com/qraveh/SelectiveMirror/internal/metrics"
	"github.com/qraveh/SelectiveMirror/internal/notify"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
	"github.com/qraveh/SelectiveMirror/internal/service"
	"github.com/qraveh/SelectiveMirror/internal/state"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
	"github.com/qraveh/SelectiveMirror/internal/watcher"

	_ "modernc.org/sqlite"
)

var version = "0.7.7-dev"

// FR-CLI-07: Documented exit codes for script/CI integration.
const (
	ExitSuccess      = 0
	ExitError        = 1 // general error
	ExitConfigError  = 2 // config load/validation failure
	ExitRcloneError  = 3 // rclone-related failure (unreachable, auth, binary missing)
	ExitLockConflict = 4 // another instance is running
	ExitDrift        = 5 // diagnostic found drift (leaks, orphans, mismatches — tool worked, action needed)
)

func main() {
	// Emergency: write to a fixed path so we can diagnose service crashes.
	// Must happen before anything else — services have no console.
	earlyLog, _ := os.OpenFile(`C:\smirror-early.log`, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if earlyLog != nil {
		fmt.Fprintf(earlyLog, "%s main() entered pid=%d isSvc=%v args=%v\n",
			time.Now().Format(time.RFC3339), os.Getpid(), service.IsWindowsService(), os.Args)
		earlyLog.Close()
	}

	// If running as a Windows Service, the SCM invokes us with no args.
	// Detect this and enter service mode immediately.
	if service.IsWindowsService() {
		serviceMain()
		return
	}

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

	// Print version header for all commands (except those that handle it themselves)
	switch cmd {
	case "version", "help", "--help", "-h", "report-bug":
		// handled below
	default:
		fmt.Printf("smirror %s\n", version)
	}

	switch cmd {
	case "start":
		cmdStart(configPath, cmdArgs)
	case "sync-now":
		cmdSyncNow(configPath, cmdArgs)
	case "dry-run":
		cmdDryRun(configPath, cmdArgs)
	case "status":
		cmdStatus(configPath)
	case "test-mirrors", "doctor", "verify":
		cmdTestMirrors(configPath, cmdArgs)
	case "list-filters":
		cmdListFilters(configPath, cmdArgs)
	case "explain":
		cmdExplain(configPath, cmdArgs)
	case "project-stats", "stats":
		cmdStats(configPath)
	case "report-bug":
		cmdReportBug(configPath, cmdArgs)
	case "service":
		cmdService(configPath, cmdArgs)
	case "version":
		fmt.Println("Copyright (c) 2026 Raveh Neeman")
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
  sync-now [mirror]         Trigger immediate sync for one or all mirrors
  dry-run [mirror]          Show what would be synced without doing it
  status                    Show sync status and metrics per mirror
  test-mirrors [mirror]     Run all diagnostics and verify sync state (aliases: doctor, verify)
  list-filters [mirror]     Show effective filter rules
  explain <mirror> <path>   Explain why a file is included or excluded
  project-stats             Show file counts and line counts across all mirrors (alias: stats)
  report-bug [--stdout]     Generate diagnostic report for bug filing
  service <action>          Windows Service (background): install, uninstall, start, stop
                            ("run as administrator" elevated cmd/PowerShell required for running smirror.exe service install/uninstall)
  version                   Show version

Options:
  --config PATH      Path to config file (default: ~/.selectivemirror/config.yaml)

`, version)
}

func loadConfig(path string) *config.Global {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", path, err)
		os.Exit(ExitConfigError)
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

// hasProjectHooks returns true if any project has per-mirror hooks configured.
func hasProjectHooks(cfg *config.Global) bool {
	for _, p := range cfg.Projects {
		if p.PreSyncHook != "" || p.PostSyncHook != "" {
			return true
		}
	}
	return false
}

// preflight checks that all mirror local paths exist and rclone is usable.
// Returns a list of error strings; empty means all checks passed.
// Warnings are printed to stderr but do not cause failure.
func preflight(cfg *config.Global) []string {
	var errs []string

	for _, proj := range cfg.Projects {
		errs = append(errs, preflightPath(proj)...)
	}

	// Check rclone binary
	info, err := rclone.Detect(cfg.RclonePath)
	if err != nil {
		errs = append(errs, fmt.Sprintf("rclone: %v", err))
	} else {
		compat, msg := info.CompatCheck()
		if compat == rclone.CompatNone {
			errs = append(errs, fmt.Sprintf("rclone: %s", msg))
		} else if compat == rclone.CompatPartial {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", msg)
		}
	}

	return errs
}

// preflightPath validates a single mirror's local_path, detecting symlinks,
// junctions, reparse points, named pipes, sockets, devices, and broken links.
func preflightPath(proj config.Project) []string {
	var errs []string
	tag := fmt.Sprintf("mirror %q", proj.Name)

	// Phase 1: Lstat (does not follow symlinks/junctions)
	linfo, lerr := os.Lstat(proj.LocalPath)
	if lerr != nil {
		errs = append(errs, fmt.Sprintf("%s: local_path %q does not exist", tag, proj.LocalPath))
		return errs
	}

	// Phase 2: if Lstat reveals a symlink or reparse point, resolve it
	isLink := linfo.Mode()&os.ModeSymlink != 0
	if isLink {
		resolved, err := filepath.EvalSymlinks(proj.LocalPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: local_path %q is a broken symlink: %v", tag, proj.LocalPath, err))
			return errs
		}
		fmt.Fprintf(os.Stderr, "Warning: %s: local_path %q is a symlink → %q (rclone will sync the target)\n", tag, proj.LocalPath, resolved)
	}

	// Phase 3: Stat (follows symlinks) — check what the final target is
	info, err := os.Stat(proj.LocalPath)
	if err != nil {
		// Lstat succeeded but Stat failed → broken symlink to non-existent target
		errs = append(errs, fmt.Sprintf("%s: local_path %q resolves to a non-existent target: %v", tag, proj.LocalPath, err))
		return errs
	}

	mode := info.Mode()
	switch {
	case mode.IsDir():
		// Good — this is what we want
	case mode&os.ModeNamedPipe != 0:
		errs = append(errs, fmt.Sprintf("%s: local_path %q is a named pipe (FIFO), not a directory", tag, proj.LocalPath))
	case mode&os.ModeSocket != 0:
		errs = append(errs, fmt.Sprintf("%s: local_path %q is a Unix socket, not a directory", tag, proj.LocalPath))
	case mode&os.ModeDevice != 0:
		errs = append(errs, fmt.Sprintf("%s: local_path %q is a device node, not a directory", tag, proj.LocalPath))
	case mode&os.ModeCharDevice != 0:
		errs = append(errs, fmt.Sprintf("%s: local_path %q is a character device, not a directory", tag, proj.LocalPath))
	case mode&os.ModeIrregular != 0:
		errs = append(errs, fmt.Sprintf("%s: local_path %q is an irregular file (mode %s), not a directory", tag, proj.LocalPath, mode))
	case mode.IsRegular():
		errs = append(errs, fmt.Sprintf("%s: local_path %q is a regular file, not a directory", tag, proj.LocalPath))
	default:
		errs = append(errs, fmt.Sprintf("%s: local_path %q has unexpected file mode %s", tag, proj.LocalPath, mode))
	}

	return errs
}

func cmdStart(configPath string, args []string) {
	cfg := loadConfig(configPath)

	// Pre-flight checks: verify local paths exist and rclone is available
	// before acquiring lock or starting any services.
	if errs := preflight(cfg); len(errs) > 0 {
		fmt.Fprintln(os.Stderr, "Pre-flight checks failed:")
		allRclone := true
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  • %s\n", e)
			if !strings.HasPrefix(e, "rclone:") {
				allRclone = false
			}
		}
		if allRclone {
			os.Exit(ExitRcloneError)
		}
		os.Exit(ExitError)
	}

	// Acquire single-instance lock (in same dir as state DB)
	lk, err := lock.Acquire(dataDir(cfg))
	if err != nil {
		if err == lock.ErrAlreadyRunning {
			fmt.Fprintln(os.Stderr, "Error: another smirror instance is already running.")
			fmt.Fprintln(os.Stderr, "Use 'smirror status' to check the running instance.")
		} else {
			fmt.Fprintf(os.Stderr, "Error acquiring lock: %v\n", err)
		}
		os.Exit(ExitLockConflict)
	}
	defer func() { _ = lk.Release() }()

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

	// Record instance info so `smirror status` can report it.
	// Clear stale health errors from previous run (SM-074).
	exePath, _ := os.Executable()
	st.SetMeta("instance_pid", fmt.Sprintf("%d", os.Getpid()))
	st.SetMeta("instance_exe", exePath)
	st.SetMeta("instance_started", time.Now().Local().Format(time.RFC3339))
	st.SetMeta("instance_mode", "foreground")
	st.SetMeta("instance_user", currentUser())
	st.SetMeta("last_health_error", "")
	defer func() {
		st.SetMeta("instance_pid", "")
		st.SetMeta("instance_exe", "")
		st.SetMeta("instance_started", "")
		st.SetMeta("instance_mode", "")
		st.SetMeta("instance_user", "")
	}()

	// Build filter engines
	filters := buildFilters(cfg)

	// Create metrics collector and notifier
	m := metrics.New()
	notifier := notify.New(cfg.IsNotifyEnabled())

	// Create anomaly recorder (FR-ANOM-01..10)
	var anomalyRecorder *anomaly.Recorder
	if cfg.IsAnomalyDetectionEnabled() {
		anomalyDir := filepath.Join(dataDir(cfg), "anomalies")
		anomalyWriter := anomaly.NewFileWriter(anomalyDir)
		anomalyRecorder = anomaly.NewRecorder(anomalyWriter)
		defer anomalyRecorder.Close()
		m.AnomalySummaryFunc = anomalyRecorder.SummaryStrings
		slog.Info("anomaly detection enabled", "dir", anomalyDir)
	}

	// Create sync engine (with metrics, anomaly recorder, and hooks)
	syncEngine := msync.NewEngine(cfg, st, filters, m)
	syncEngine.Anomaly = anomalyRecorder
	if cfg.PreSyncHook != "" || cfg.PostSyncHook != "" || hasProjectHooks(cfg) {
		syncEngine.Hooks = hooks.New(30 * time.Second)
	}

	// Create watcher manager (with delete policy)
	watchMgr, err := watcher.NewManager(cfg.Projects, filters, syncEngine.Queue, cfg.DeletePolicy())
	if err != nil {
		slog.Error("watcher creation failed", "error", err)
		os.Exit(1)
	}

	watchMgr.Anomaly = anomalyRecorder

	// Auto-clean LEAKs when .syncignore filter rules change.
	// LEAKs are files excluded by current filters but still on remote —
	// distinct from delete_policy which controls user-deleted files.
	watchMgr.OnFilterChange = func(proj config.Project) {
		cleaned, err := syncEngine.CleanupLeaks(context.Background(), proj)
		if err != nil {
			slog.Warn("leak cleanup after filter change failed", "mirror", proj.Name, "error", err)
		} else if cleaned > 0 {
			slog.Info("leak cleanup after filter change", "mirror", proj.Name, "cleaned", cleaned)
		}
	}

	// SM-078: Named event for sync-now IPC (foreground mode)
	syncEvent, syncEventErr := service.CreateSyncNowEvent()
	if syncEventErr != nil {
		slog.Warn("cannot create sync-now event", "error", syncEventErr)
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

	// Detect and migrate remote path changes before reconciliation
	detectRemoteChanges(ctx, cfg, st, syncEngine)

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

	// SM-078: Listen for sync-now signals via named event
	if syncEvent != 0 {
		go func() {
			for {
				if ctx.Err() != nil {
					return
				}
				if service.WaitForSyncNowSignal(syncEvent, 1000) {
					slog.Info("sync-now signal received via named event")
					for _, proj := range cfg.Projects {
						syncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: ""})
					}
				}
			}
		}()
	}

	// Start heartbeat (writes status.json + heartbeat to DB + health checks + periodic reconciliation + auto-verify)
	go heartbeatLoop(ctx, st, cfg, m, watchMgr, syncEngine, filters, notifier)

	slog.Info("smirror running", "mirrors", cfg.ProjectNames())
	if !service.IsWindowsService() {
		fmt.Println("Press Ctrl+C to stop")
	}

	// Block until context is cancelled
	<-ctx.Done()
	slog.Info("smirror stopped")
}

func cmdSyncNow(configPath string, args []string) {
	cfg := loadConfig(configPath)
	if _, err := logging.Setup(cfg.LogLevel, "", true); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: logging setup: %v\n", err)
	}

	// SM-073/SM-076: If service is running, signal it to sync instead of racing.
	lk, err := lock.Acquire(dataDir(cfg))
	if err != nil {
		if err == lock.ErrAlreadyRunning {
			// SM-078: Signal service via named kernel event (no admin required, instant)
			fmt.Println("Service is running — sending sync-now signal...")
			if signalErr := service.SignalSyncNow(); signalErr != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", signalErr)
				os.Exit(ExitError)
			}
			fmt.Println("Sync-now signal sent. The service will perform an immediate full sync.")
			return
		}
		fmt.Fprintf(os.Stderr, "Error acquiring lock: %v\n", err)
		os.Exit(ExitLockConflict)
	}
	defer func() { _ = lk.Release() }()

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	filters := buildFilters(cfg)
	syncEngine := msync.NewEngine(cfg, st, filters, nil)

	ctx := context.Background()

	// Detect and migrate remote path changes before syncing
	detectRemoteChanges(ctx, cfg, st, syncEngine)

	// Resolve target projects once (used for both sync and ghost cleanup)
	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(ExitConfigError)
		}
		projects = []config.Project{*proj}
	}

	for _, proj := range projects {
		syncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: ""})
	}

	// Process all queued tasks synchronously: start workers, wait for them
	// to finish processing everything. We close the channel after queueing
	// so workers exit once all tasks are drained.
	syncEngine.Queue.Close()
	syncEngine.Run(ctx) // blocks until all workers complete

	// Clean up ghost files (LEAKs + ORPHANs) on remote
	totalCleaned := 0
	for _, proj := range projects {
		cleaned, err := syncEngine.CleanupGhosts(ctx, proj)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Ghost cleanup error for %s: %v\n", proj.Name, err)
			continue
		}
		if cleaned > 0 {
			fmt.Printf("Cleaned %d ghost file(s) from %s\n", cleaned, proj.Name)
			totalCleaned += cleaned
		}
	}
	if totalCleaned > 0 {
		fmt.Printf("Sync complete (%d ghost files cleaned)\n", totalCleaned)
	} else {
		fmt.Println("Sync complete")
	}
}

func cmdDryRun(configPath string, args []string) {
	cfg := loadConfig(configPath)
	if _, err := logging.Setup(cfg.LogLevel, "", true); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: logging setup: %v\n", err)
	}

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
			fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(ExitConfigError)
		}
		projects = []config.Project{*proj}
	}

	for _, proj := range projects {
		if err := syncEngine.DryRun(ctx, proj); err != nil {
			fmt.Fprintf(os.Stderr, "Dry run error for %s: %v\n", proj.Name, err)
		}
		fmt.Println()
	}

	// Show what ghost files would be cleaned
	fmt.Println("=== Ghost cleanup preview ===")
	for _, proj := range projects {
		fmt.Printf("\n%s:\n", proj.Name)
		if _, err := syncEngine.DryRunCleanup(ctx, proj); err != nil {
			fmt.Fprintf(os.Stderr, "Ghost scan error for %s: %v\n", proj.Name, err)
		}
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
			startLocal := s.StartTime
			if t, err := time.Parse(time.RFC3339, s.StartTime); err == nil {
				startLocal = t.Local().Format(time.RFC3339)
			}
			fmt.Printf("  Uptime: %s (started %s)\n", s.Uptime, startLocal)
			fmt.Printf("  Files synced: %d\n", s.FilesSynced)
			fmt.Printf("  Bytes uploaded: %d\n", s.BytesUploaded)
			fmt.Printf("  Sync errors: %d\n", s.SyncErrors)
			fmt.Printf("  Avg latency: %dms\n", s.AvgLatencyMs)
			fmt.Printf("  Queue depth: %d\n", s.QueueDepth)
			if s.LastScanTime != "" {
				scanLocal := s.LastScanTime
				if t, err := time.Parse(time.RFC3339, s.LastScanTime); err == nil {
					scanLocal = t.Local().Format(time.RFC3339)
				}
				fmt.Printf("  Last reconciliation: %s\n", scanLocal)
			}
			genLocal := s.GeneratedAt
			if t, err := time.Parse(time.RFC3339, s.GeneratedAt); err == nil {
				genLocal = t.Local().Format(time.RFC3339)
			}
			fmt.Printf("  Status generated: %s\n\n", genLocal)
		}
	}

	lastHB, _ := st.GetMeta("last_heartbeat")
	if lastHB != "" {
		hbLocal := lastHB
		if t, err := time.Parse(time.RFC3339, lastHB); err == nil {
			hbLocal = t.Local().Format(time.RFC3339)
		}
		fmt.Printf("Last heartbeat: %s\n\n", hbLocal)
	}

	// Check instance status
	locked, _ := lock.IsLocked(dataDir(cfg))
	if locked {
		iPid, _ := st.GetMeta("instance_pid")
		iExe, _ := st.GetMeta("instance_exe")
		iMode, _ := st.GetMeta("instance_mode")
		iUser, _ := st.GetMeta("instance_user")
		iStarted, _ := st.GetMeta("instance_started")

		// Build status line: "smirror.exe service running as SYSTEM: (PID 1234) C:\...\smirror.exe"
		modeStr := "instance"
		if iMode != "" {
			modeStr = iMode
		}
		parts := []string{fmt.Sprintf("smirror.exe %s running", modeStr)}
		if iUser != "" {
			parts = append(parts, fmt.Sprintf("as %s:", iUser))
		} else {
			parts = append(parts, ":")
		}
		if iPid != "" {
			parts = append(parts, fmt.Sprintf("(PID %s)", iPid))
		}
		if iExe != "" {
			parts = append(parts, iExe)
		}
		fmt.Println(strings.Join(parts, " "))
		if iStarted != "" {
			if t, err := time.Parse(time.RFC3339, iStarted); err == nil {
				fmt.Printf("  Started: %s (%s ago)\n", t.Local().Format(time.RFC3339), time.Since(t).Round(time.Second))
			}
		}
		fmt.Println()
	} else {
		fmt.Printf("Instance: not running\n\n")
	}

	for _, proj := range cfg.Projects {
		lastSync, _ := st.GetLastSyncTime(proj.Name)
		pending, _ := st.GetPendingFiles(proj.Name)
		synced, _ := st.GetAllSyncedPaths(proj.Name)

		fmt.Printf("Mirror: %s\n", proj.Name)
		fmt.Printf("  Path:    %s\n", proj.LocalPath)
		fmt.Printf("  Remote:  %s\n", proj.Remote)
		fmt.Printf("  Files synced: %d\n", len(synced))
		if !lastSync.IsZero() {
			fmt.Printf("  Last file sync: %s (%s ago)\n", lastSync.Local().Format(time.RFC3339), time.Since(lastSync).Round(time.Second))
		}
		lastFullSync, _ := st.GetMeta("last_full_sync_" + proj.Name)
		if lastFullSync != "" {
			if t, err := time.Parse(time.RFC3339, lastFullSync); err == nil {
				fmt.Printf("  Last full sync: %s (%s ago)\n", t.Local().Format(time.RFC3339), time.Since(t).Round(time.Second))
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
				fmt.Printf("  Last scan: %s (%s ago)\n", t.Local().Format(time.RFC3339), time.Since(t).Round(time.Second))
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

// cmdTestMirrors runs all diagnostics (local + remote) and verifies sync state.
// Optional argument: mirror name to test only that mirror.
// Aliases: doctor, verify (kept for backward compatibility).
func cmdTestMirrors(configPath string, args []string) {
	fmt.Println()
	passed := 0
	failed := 0
	var failures []string

	check := func(name string, fn func() error) {
		fmt.Printf("  %-45s ", name)
		start := time.Now()
		if err := fn(); err != nil {
			msg := fmt.Sprintf("%s: %v", name, err)
			fmt.Printf("FAIL: %v (%dms)\n", err, time.Since(start).Milliseconds())
			failures = append(failures, msg)
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
		os.Exit(ExitConfigError)
	}

	// Open state store for instance info lookups (best-effort)
	st, _ := state.Open(cfg.StateDB)
	if st != nil {
		defer st.Close()
	}

	// Determine which mirrors to test
	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(ExitConfigError)
		}
		projects = []config.Project{*proj}
	}

	// Mirror summary
	for _, proj := range projects {
		fmt.Printf("  %s: %s -> %s\n", proj.Name, proj.LocalPath, proj.Remote)
		ignoreFile := proj.SyncIgnoreFile()
		if _, err := os.Stat(ignoreFile); err == nil {
			fmt.Printf("    .syncignore: %s\n", ignoreFile)
		}
	}
	fmt.Println()

	// 2. Mirror paths exist
	check("All mirror paths exist", func() error {
		for _, p := range projects {
			if _, err := os.Stat(p.LocalPath); err != nil {
				return fmt.Errorf("%s: %v", p.Name, err)
			}
		}
		return nil
	})

	// 3. No duplicate names
	check("No duplicate mirror names", func() error {
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
	check("rclone binary found", func() error {
		rcloneInfo, err := rclone.Detect(cfg.RclonePath)
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

	// 5. Remote connectivity (sequential to avoid API rate limits, 30s timeout each)
	for _, proj := range projects {
		p := proj
		check(fmt.Sprintf("Remote reachable: %s", p.Remote), func() error {
			rp := cfg.RclonePath
			if rp == "" {
				rp = "rclone"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			mkdirArgs := append(cfg.RcloneArgs(), "mkdir", p.Remote)
			cmd := exec.CommandContext(ctx, rp, mkdirArgs...)
			if err := cmd.Run(); err != nil {
				if ctx.Err() == context.DeadlineExceeded {
					return fmt.Errorf("timed out after 30s")
				}
				return err
			}
			return nil
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
		f, err := logging.OpenShared(cfg.LogFile)
		if err != nil {
			return err
		}
		f.Close()
		return nil
	})

	// 9. Running instance (informational, not a pass/fail check)
	locked, _ := lock.IsLocked(dataDir(cfg))
	if locked {
		parts := []string{"smirror.exe running"}
		if st != nil {
			iPid, _ := st.GetMeta("instance_pid")
			iMode, _ := st.GetMeta("instance_mode")
			iExe, _ := st.GetMeta("instance_exe")
			if iMode != "" {
				parts[0] = fmt.Sprintf("smirror.exe %s running", iMode)
			}
			if iPid != "" {
				parts = append(parts, fmt.Sprintf("PID %s", iPid))
			}
			if iExe != "" {
				parts = append(parts, iExe)
			}
		}
		fmt.Printf("  %-45s %s\n", "Running instance", strings.Join(parts, ", "))
	} else {
		fmt.Printf("  %-45s not running\n", "Running instance")
	}

	// 10. Watcher can be created
	check("Filesystem watcher available", func() error {
		_, err := watcher.NewManager(cfg.Projects[:1], buildFilters(cfg), msync.NewFairQueue(100, 0), cfg.DeletePolicy())
		return err
	})

	// 11. Write permissions
	check("Write permissions on mirror dirs", func() error {
		for _, p := range projects {
			testPath := filepath.Join(p.LocalPath, ".smirror_write_test")
			if err := os.WriteFile(testPath, []byte("test"), 0644); err != nil {
				return fmt.Errorf("%s: %v", p.Name, err)
			}
			os.Remove(testPath)
		}
		return nil
	})

	// 12. Filters
	filters := buildFilters(cfg)
	check("Filter engines load without error", func() error {
		for _, p := range projects {
			if _, ok := filters[p.Name]; !ok {
				return fmt.Errorf("%s: filter not built", p.Name)
			}
		}
		return nil
	})

	// 13. Sync drift (per-mirror file comparison)
	fmt.Println()
	totalDrift := 0
	for _, proj := range projects {
		drift := verifyProject(cfg, proj, filters[proj.Name], st)
		totalDrift += drift
	}

	fmt.Printf("\n%d passed, %d failed", passed, failed)
	if totalDrift > 0 {
		fmt.Printf(", %d drift", totalDrift)
	}
	fmt.Println()

	if len(failures) > 0 {
		fmt.Println("\nFailed checks:")
		for _, f := range failures {
			fmt.Printf("  ✗ %s\n", f)
		}
	}

	if totalDrift > 0 {
		fmt.Println("Hint: 'smirror sync-now' may resolve most drift.")
	}

	if failed > 0 {
		os.Exit(ExitRcloneError)
	}
	if totalDrift > 0 {
		os.Exit(ExitDrift)
	}
}

func cmdListFilters(configPath string, args []string) {
	cfg := loadConfig(configPath)
	filters := buildFilters(cfg)

	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(ExitConfigError)
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
		fmt.Fprintln(os.Stderr, "Usage: smirror explain <mirror> <relative-path>")
		fmt.Fprintln(os.Stderr, "Example: smirror explain Orch CLAUDE.md")
		os.Exit(ExitConfigError)
	}

	cfg := loadConfig(configPath)
	projName := args[0]
	relPath := filepath.ToSlash(args[1])

	proj := cfg.FindProject(projName)
	if proj == nil {
		fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", projName, strings.Join(cfg.ProjectNames(), ", "))
		os.Exit(ExitConfigError)
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
		fmt.Printf("  Modified: %s\n", info.ModTime().Local().Format(time.RFC3339))

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
			fmt.Printf("  Last synced: %s\n", fs.SyncedAt.Local().Format(time.RFC3339))
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

func verifyProject(cfg *config.Global, proj config.Project, fe *filter.Engine, st *state.Store) int {
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
	leaksCounted := make(map[string]bool) // LEAKs found during local walk, to avoid double-counting in remote iteration
	drift := 0

	if walkErr := filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
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
				leaksCounted[relPath] = true
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
	}); walkErr != nil {
		fmt.Fprintf(os.Stderr, "  Warning: error walking %s: %v\n", proj.LocalPath, walkErr)
	}

	// Check for unexpected remote files (ORPHANs + LEAKs not already counted during local walk).
	// LEAKs from excluded files that exist locally are caught during the local walk above.
	// This loop catches ORPHANs (no local file at all) and LEAKs for files that don't exist
	// locally either (e.g., file excluded AND deleted locally).
	for remotePath := range remoteMap {
		if strings.HasPrefix(remotePath, ".quarantine/") {
			continue // skip quarantine directory
		}
		if leaksCounted[remotePath] {
			continue // already counted during local walk
		}
		if !localFiles[remotePath] {
			kind := msync.ClassifyGhost(fe, st, proj.Name, remotePath, proj.DeletePolicy(cfg))
			switch kind {
			case msync.GhostRetained:
				// Intentionally preserved by delete_policy=ignore — not drift
				continue
			case msync.GhostLeak:
				fmt.Printf("  LEAK: %s (excluded locally but exists on remote)\n", remotePath)
			case msync.GhostStale:
				fmt.Printf("  STALE: %s (was synced, local file gone)\n", remotePath)
			case msync.GhostOrphan:
				fmt.Printf("  ORPHAN: %s (not in local tree, no sync record)\n", remotePath)
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

// verifyProjectQuiet runs drift detection without any stdout output.
// Used by auto-verify in the heartbeat loop. Returns drift count.
func verifyProjectQuiet(cfg *config.Global, proj config.Project, fe *filter.Engine, st *state.Store) int {
	start := time.Now()

	remoteFiles, err := msync.ListRemote(cfg, proj)
	if err != nil {
		slog.Warn("auto-verify: failed to list remote", "mirror", proj.Name, "error", err)
		return 0
	}

	remoteMap := make(map[string]msync.RemoteFile)
	for _, rf := range remoteFiles {
		if !rf.IsDir {
			remoteMap[filepath.ToSlash(rf.Path)] = rf
		}
	}

	localFiles := make(map[string]bool)
	leaksCounted := make(map[string]bool) // avoid double-counting LEAKs (SM-053)
	drift := 0

	_ = filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
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
		if !d.Type().IsRegular() {
			return nil
		}

		relPath, _ := filepath.Rel(proj.LocalPath, path)
		relPath = filepath.ToSlash(relPath)

		if fe != nil && fe.IsExcluded(relPath) {
			if _, onRemote := remoteMap[relPath]; onRemote {
				slog.Warn("auto-verify: filter leak", "mirror", proj.Name, "path", relPath)
				drift++
				leaksCounted[relPath] = true
			}
			return nil
		}

		localFiles[relPath] = true

		rf, onRemote := remoteMap[relPath]
		if !onRemote {
			slog.Debug("auto-verify: missing remote", "mirror", proj.Name, "path", relPath)
			drift++
			return nil
		}

		if md5Hash, ok := rf.Hashes["md5"]; ok {
			localHash, _, err := hashFile(path)
			if err == nil && localHash != strings.ToLower(md5Hash) {
				slog.Debug("auto-verify: hash mismatch", "mirror", proj.Name, "path", relPath)
				drift++
			}
		}

		return nil
	})

	for remotePath := range remoteMap {
		if strings.HasPrefix(remotePath, ".quarantine/") {
			continue
		}
		if leaksCounted[remotePath] {
			continue // already counted during local walk (SM-053)
		}
		if !localFiles[remotePath] {
			kind := msync.ClassifyGhost(fe, st, proj.Name, remotePath, proj.DeletePolicy(cfg))
			if kind == msync.GhostRetained {
				continue // intentionally preserved — not drift
			}
			if kind == msync.GhostLeak {
				slog.Warn("auto-verify: filter leak", "mirror", proj.Name, "path", remotePath)
			} else {
				slog.Debug("auto-verify: ghost", "mirror", proj.Name, "path", remotePath, "kind", kind)
			}
			drift++
		}
	}

	elapsed := time.Since(start)
	slog.Info("auto-verify complete", "mirror", proj.Name, "drift", drift,
		"local", len(localFiles), "remote", len(remoteMap), "ms", elapsed.Milliseconds())

	return drift
}

func cmdReportBug(configPath string, args []string) {
	toStdout := false
	openBrowser := false
	for _, a := range args {
		switch a {
		case "--stdout":
			toStdout = true
		case "--open":
			openBrowser = true
		}
	}

	var b strings.Builder
	tz := time.Now().Format("-07:00")
	now := time.Now().Format("2006-01-02T15:04:05") + tz

	b.WriteString(fmt.Sprintf("smirror bug report — generated %s\n", now))
	b.WriteString(fmt.Sprintf("smirror version: %s\n", version))
	b.WriteString(fmt.Sprintf("platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	b.WriteString(fmt.Sprintf("go version: %s\n", runtime.Version()))

	// rclone info
	rcloneInfo, err := rclone.Detect("")
	if err != nil {
		b.WriteString(fmt.Sprintf("rclone: NOT FOUND (%v)\n", err))
	} else {
		compat, msg := rcloneInfo.CompatCheck()
		b.WriteString(fmt.Sprintf("rclone version: %s\n", rcloneInfo.Version))
		b.WriteString(fmt.Sprintf("rclone path: %s\n", rcloneInfo.Path))
		b.WriteString(fmt.Sprintf("rclone os: %s\n", rcloneInfo.OS))
		_ = compat
		b.WriteString(fmt.Sprintf("rclone compat: %s\n", msg))
	}

	// Config summary (sanitized)
	b.WriteString("\n--- Config ---\n")
	cfg, cfgErr := config.Load(configPath)
	if cfgErr != nil {
		b.WriteString(fmt.Sprintf("config error: %v\n", cfgErr))
	} else {
		b.WriteString(fmt.Sprintf("config path: %s\n", configPath))
		b.WriteString(fmt.Sprintf("mirrors: %d\n", len(cfg.Projects)))
		b.WriteString(fmt.Sprintf("delete_policy: %s\n", cfg.DeletePolicy()))
		b.WriteString(fmt.Sprintf("sync_workers: %d\n", cfg.Workers()))
		b.WriteString(fmt.Sprintf("reconcile_interval: %s\n", cfg.ReconcileInterval()))
		b.WriteString(fmt.Sprintf("bandwidth_limit: %s\n", cfg.BandwidthLimit))
		for _, p := range cfg.Projects {
			b.WriteString(fmt.Sprintf("  mirror: %s (%s)\n", p.Name, p.LocalPath))
			// Redact remote path — only show remote name
			parts := strings.SplitN(p.Remote, ":", 2)
			if len(parts) >= 2 {
				b.WriteString(fmt.Sprintf("    remote: %s:<REDACTED>\n", parts[0]))
			}
		}

		// State DB summary
		b.WriteString("\n--- State ---\n")
		st, stErr := state.Open(cfg.StateDB)
		if stErr != nil {
			b.WriteString(fmt.Sprintf("state db error: %v\n", stErr))
		} else {
			defer st.Close()
			for _, p := range cfg.Projects {
				count := st.CountFiles(p.Name)
				b.WriteString(fmt.Sprintf("  %s: %d synced files\n", p.Name, count))
			}
			if hb, err := st.GetMeta("heartbeat"); err == nil && hb != "" {
				b.WriteString(fmt.Sprintf("  last heartbeat: %s\n", hb))
			}
		}

		// Recent log lines (sanitized)
		b.WriteString("\n--- Recent Logs (last 30 lines) ---\n")
		logData, logErr := os.ReadFile(cfg.LogFile)
		if logErr != nil {
			b.WriteString(fmt.Sprintf("log error: %v\n", logErr))
		} else {
			lines := strings.Split(string(logData), "\n")
			start := 0
			if len(lines) > 30 {
				start = len(lines) - 30
			}
			home, _ := os.UserHomeDir()
			for _, line := range lines[start:] {
				// Redact user paths
				if home != "" {
					line = strings.ReplaceAll(line, home, "<USER_HOME>")
				}
				b.WriteString(line + "\n")
			}
		}
	}

	report := b.String()

	if toStdout {
		fmt.Print(report)
		return
	}

	if openBrowser {
		fmt.Print(report)
		fmt.Println("\n--- Opening browser ---")
		url := "https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml"
		_ = exec.Command("cmd", "/c", "start", url).Start()
		return
	}

	// Write to file
	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("smirror-bug-report-%s.txt", ts)
	if err := os.WriteFile(filename, []byte(report), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Bug report written to: %s\n", filename)
	fmt.Println("Paste into a GitHub issue at: https://github.com/qraveh/SelectiveMirror/issues/new")
}

func cmdStats(configPath string) {
	cfg := loadConfig(configPath)
	if _, err := logging.Setup(cfg.LogLevel, "", true); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: logging setup: %v\n", err)
	}
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

	fmt.Printf("smirror project-stats\n")
	fmt.Printf("=====================\n")

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

		_ = filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
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
// detectRemoteChanges checks if any mirror's remote path changed since last run.
// If so, performs a server-side migration (rclone moveto) instead of re-uploading.
func detectRemoteChanges(ctx context.Context, cfg *config.Global, st *state.Store, syncEngine *msync.Engine) {
	for _, proj := range cfg.Projects {
		key := "remote_" + proj.Name
		stored, _ := st.GetMeta(key)
		if stored != "" && stored != proj.Remote {
			slog.Info("remote path changed, migrating",
				"mirror", proj.Name,
				"old", stored, "new", proj.Remote)
			if err := syncEngine.MigrateRemote(ctx, proj, stored, proj.Remote); err != nil {
				slog.Warn("remote migration failed, will re-sync",
					"mirror", proj.Name, "error", err)
			} else {
				slog.Info("remote migration complete", "mirror", proj.Name)
			}
		}
		st.SetMeta(key, proj.Remote)
	}
}

func reconcileAll(ctx context.Context, cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, syncEngine *msync.Engine) {
	var wg gosync.WaitGroup
	for _, proj := range cfg.Projects {
		slog.Info("reconciling", "mirror", proj.Name)
		wg.Add(1)
		// Use full-project sync — single rclone invocation with filters.
		// Much faster than individual file syncs (1 rclone call vs N).
		syncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: "", Done: wg.Done})
	}

	// Ghost scan: detect orphaned files on remote that no longer exist locally.
	// Waits for all reconciliation tasks to complete before scanning (SM-054).
	// This catches rename orphans, stale files from previous bugs, etc.
	go func() {
		wg.Wait()
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
			slog.Warn("ghost scan: failed to list remote", "mirror", proj.Name, "error", err)
			continue
		}

		// Build set of local files (non-excluded, non-dir)
		localFiles := make(map[string]bool)
		_ = filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, walkErr error) error {
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
				kind := msync.ClassifyGhost(fe, st, proj.Name, remotePath, proj.DeletePolicy(cfg))
				if kind == msync.GhostRetained {
					continue // intentionally preserved — don't report as ghost
				}
				slog.Warn("ghost file on remote",
					"mirror", proj.Name,
					"path", remotePath,
					"kind", string(kind),
					"size", rf.Size)
				ghostDetails = append(ghostDetails, fmt.Sprintf("[%s] %s: %s (%d bytes)",
					kind, proj.Name, remotePath, rf.Size))
				totalGhosts++
			}
		}
	}

	if totalGhosts > 0 {
		summary := fmt.Sprintf("%d ghost files detected on remote. Run 'smirror test-mirrors' for details.", totalGhosts)
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

// reconcileAdapter manages adaptive reconciliation interval.
// Exported fields and method for testability.
type reconcileAdapter struct {
	base              time.Duration
	max               time.Duration
	current           time.Duration
	cleanCount        int
	doublingThreshold int
}

// adapt adjusts the reconciliation interval based on drift detection.
// Returns true if the interval changed (caller should reset the ticker).
func (ra *reconcileAdapter) adapt(driftFound bool) bool {
	if driftFound {
		ra.cleanCount = 0
		if ra.current != ra.base {
			ra.current = ra.base
			slog.Info("reconciliation interval reset (drift detected)", "interval", ra.current)
			return true
		}
		return false
	}

	ra.cleanCount++
	if ra.cleanCount >= ra.doublingThreshold && ra.current < ra.max {
		ra.current *= 2
		if ra.current > ra.max {
			ra.current = ra.max
		}
		ra.cleanCount = 0
		slog.Info("reconciliation interval extended (no drift)", "interval", ra.current)
		return true
	}
	return false
}

func heartbeatLoop(ctx context.Context, st *state.Store, cfg *config.Global, m *metrics.Collector, watchMgr *watcher.Manager, syncEngine *msync.Engine, filters map[string]*filter.Engine, notifier *notify.Notifier) {
	interval := cfg.HeartbeatInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ra := &reconcileAdapter{
		base:  cfg.ReconcileInterval(),
		max:   30 * time.Minute,
		doublingThreshold: 3,
	}
	ra.current = ra.base
	reconcileTicker := time.NewTicker(ra.current)
	defer reconcileTicker.Stop()

	// Auto-verify ticker (default 6h; 0 = disabled)
	verifyInterval := cfg.VerifyInterval()
	var verifyTicker *time.Ticker
	var verifyChan <-chan time.Time
	if verifyInterval > 0 {
		verifyTicker = time.NewTicker(verifyInterval)
		verifyChan = verifyTicker.C
		defer verifyTicker.Stop()
		slog.Info("auto-verify enabled", "interval", verifyInterval)
	}

	dd := dataDir(cfg)

	for {
		select {
		case <-reconcileTicker.C:
			// Periodic reconciliation: catch changes invisible to fsnotify
			// (WSL operations, network drive edits, external tools).
			if syncEngine != nil {
				slog.Info("periodic reconciliation", "interval", ra.current)
				for _, proj := range cfg.Projects {
					if ctx.Err() != nil {
						return
					}
					syncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: ""})
				}

				// FR-SYNC-09: Adaptive reconciliation interval.
				driftFound := false
				for _, proj := range cfg.Projects {
					if verifyProjectQuiet(cfg, proj, filters[proj.Name], st) > 0 {
						driftFound = true
						break
					}
				}

				if changed := ra.adapt(driftFound); changed {
					reconcileTicker.Reset(ra.current)
				}

				// Prune old sync logs (30-day retention)
				if pruned, err := st.PruneOldLogs(30); err == nil && pruned > 0 {
					slog.Info("sync log pruned", "deleted", pruned)
				}
			}

		case <-verifyChan:
			// Periodic verify: compare local vs remote and log drift.
			slog.Info("auto-verify starting")
			totalDrift := 0
			for _, proj := range cfg.Projects {
				// Check if project path still exists (may have been unmounted)
				if _, err := os.Stat(proj.LocalPath); err != nil {
					slog.Error("auto-verify: project path gone", "mirror", proj.Name, "path", proj.LocalPath)
					if notifier != nil {
						notifier.PathGone(proj.Name, proj.LocalPath)
					}
					continue
				}

				drift := verifyProjectQuiet(cfg, proj, filters[proj.Name], st)
				totalDrift += drift
				if drift > 0 {
					slog.Warn("auto-verify: drift detected", "mirror", proj.Name, "drift", drift)
					st.SetMeta("last_verify_drift_"+proj.Name,
						fmt.Sprintf("%d files at %s", drift, time.Now().UTC().Format(time.RFC3339)))
					if notifier != nil {
						notifier.VerifyDrift(proj.Name, drift)
					}
				}
			}
			if totalDrift == 0 {
				slog.Info("auto-verify: no drift")
			}
			st.SetMeta("last_auto_verify", time.Now().UTC().Format(time.RFC3339))

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

			// Check project paths still exist
			for _, proj := range cfg.Projects {
				if _, err := os.Stat(proj.LocalPath); err != nil {
					slog.Error("project path missing", "mirror", proj.Name, "path", proj.LocalPath)
					if notifier != nil {
						notifier.PathGone(proj.Name, proj.LocalPath)
					}
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

// serviceMain runs smirror as a Windows Service.
// Called from main() when IsWindowsService() returns true.
func serviceMain() {
	// Emergency diagnostic log — writes to a guaranteed location before any
	// other initialization. Services running as SYSTEM have no console/stderr;
	// if we crash before the normal log opens, this is the only evidence.
	crashLog, _ := os.OpenFile(`C:\smirror-service-crash.log`,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if crashLog != nil {
		fmt.Fprintf(crashLog, "%s serviceMain entered pid=%d args=%v\n",
			time.Now().Format(time.RFC3339), os.Getpid(), os.Args)
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(crashLog, "%s PANIC: %v\n", time.Now().Format(time.RFC3339), r)
			}
			fmt.Fprintf(crashLog, "%s serviceMain exiting\n", time.Now().Format(time.RFC3339))
			crashLog.Close()
		}()
	}

	// When the SCM starts the service, args come from the service config.
	// Parse --config from os.Args (the SCM passes the configured arguments).
	configPath := config.DefaultConfigPath()
	for i, arg := range os.Args {
		if arg == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			configPath = strings.TrimPrefix(arg, "--config=")
			break
		}
	}

	var cancel context.CancelFunc
	var liveSyncEngine *msync.Engine // shared with syncNowFunc
	var liveCfg *config.Global

	startFunc := func() {
		// Re-use cmdStart logic but with context from service handler
		if crashLog != nil {
			fmt.Fprintf(crashLog, "%s startFunc: loading config %s\n", time.Now().Format(time.RFC3339), configPath)
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			if crashLog != nil {
				fmt.Fprintf(crashLog, "%s startFunc: config load FAILED: %v\n", time.Now().Format(time.RFC3339), err)
			}
			slog.Error("config load failed", "error", err, "path", configPath)
			return
		}

		if errs := preflight(cfg); len(errs) > 0 {
			for _, e := range errs {
				slog.Error("pre-flight failed", "error", e)
				if crashLog != nil {
					fmt.Fprintf(crashLog, "%s startFunc: preflight failed: %v\n", time.Now().Format(time.RFC3339), e)
				}
			}
			return
		}

		lk, err := lock.Acquire(dataDir(cfg))
		if err != nil {
			slog.Error("lock acquire failed", "error", err)
			if crashLog != nil {
				fmt.Fprintf(crashLog, "%s startFunc: lock failed: %v\n", time.Now().Format(time.RFC3339), err)
			}
			return
		}
		defer func() { _ = lk.Release() }()

		rw, err := logging.Setup(cfg.LogLevel, cfg.LogFile, false)
		if err != nil {
			if crashLog != nil {
				fmt.Fprintf(crashLog, "%s startFunc: logging setup failed: %v\n", time.Now().Format(time.RFC3339), err)
			}
			return
		}
		if rw != nil {
			defer rw.Close()
		}

		slog.Info("smirror service starting", "version", version, "config", configPath)

		st, err := state.Open(cfg.StateDB)
		if err != nil {
			slog.Error("state db open failed", "error", err)
			return
		}
		defer st.Close()

		// Record instance info so `smirror status` can report it.
		// Clear stale health errors from previous run (SM-074).
		exePath, _ := os.Executable()
		st.SetMeta("instance_pid", fmt.Sprintf("%d", os.Getpid()))
		st.SetMeta("instance_exe", exePath)
		st.SetMeta("instance_started", time.Now().Local().Format(time.RFC3339))
		st.SetMeta("instance_mode", "service")
		st.SetMeta("instance_user", currentUser())
		st.SetMeta("last_health_error", "")
		defer func() {
			st.SetMeta("instance_pid", "")
			st.SetMeta("instance_exe", "")
			st.SetMeta("instance_started", "")
			st.SetMeta("instance_mode", "")
			st.SetMeta("instance_user", "")
		}()

		filters := buildFilters(cfg)
		m := metrics.New()
		notifier := notify.New(cfg.IsNotifyEnabled())

		// Anomaly recorder for service mode
		var anomalyRecorder *anomaly.Recorder
		if cfg.IsAnomalyDetectionEnabled() {
			anomalyDir := filepath.Join(dataDir(cfg), "anomalies")
			anomalyWriter := anomaly.NewFileWriter(anomalyDir)
			anomalyRecorder = anomaly.NewRecorder(anomalyWriter)
			defer anomalyRecorder.Close()
			m.AnomalySummaryFunc = anomalyRecorder.SummaryStrings
		}

		syncEngine := msync.NewEngine(cfg, st, filters, m)
		syncEngine.Anomaly = anomalyRecorder
		if cfg.PreSyncHook != "" || cfg.PostSyncHook != "" || hasProjectHooks(cfg) {
			syncEngine.Hooks = hooks.New(30 * time.Second)
		}

		watchMgr, err := watcher.NewManager(cfg.Projects, filters, syncEngine.Queue, cfg.DeletePolicy())
		if err != nil {
			slog.Error("watcher creation failed", "error", err)
			return
		}
		watchMgr.Anomaly = anomalyRecorder

		watchMgr.OnFilterChange = func(proj config.Project) {
			cleaned, cleanErr := syncEngine.CleanupLeaks(context.Background(), proj)
			if cleanErr != nil {
				slog.Warn("leak cleanup after filter change failed", "mirror", proj.Name, "error", cleanErr)
			} else if cleaned > 0 {
				slog.Info("leak cleanup after filter change", "mirror", proj.Name, "cleaned", cleaned)
			}
		}

		var ctx context.Context
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()

		detectRemoteChanges(ctx, cfg, st, syncEngine)
		reconcileAll(ctx, cfg, st, filters, syncEngine)
		m.RecordScanComplete()

		if err := watchMgr.Start(ctx); err != nil {
			slog.Error("watcher start failed", "error", err)
			return
		}
		defer watchMgr.Stop()

		go syncEngine.Run(ctx)
		go heartbeatLoop(ctx, st, cfg, m, watchMgr, syncEngine, filters, notifier)

		liveSyncEngine = syncEngine
		liveCfg = cfg

		// FR-SVC-08: Write lifecycle events to Windows Event Log
		elog := service.OpenEventLog()
		if elog != nil {
			elog.Info(service.EventServiceStarted, "SelectiveMirror service started, version "+version)
			defer func() {
				elog.Info(service.EventServiceStopped, "SelectiveMirror service stopped")
				elog.Close()
			}()
		}

		// SM-078: Create named event for sync-now IPC (no admin required)
		syncEvent, syncEventErr := service.CreateSyncNowEvent()
		if syncEventErr != nil {
			slog.Warn("cannot create sync-now event", "error", syncEventErr)
		} else {
			go func() {
				for {
					if ctx.Err() != nil {
						return
					}
					// Wait 1 second at a time, check context between waits
					if service.WaitForSyncNowSignal(syncEvent, 1000) {
						slog.Info("sync-now signal received via named event")
						for _, proj := range cfg.Projects {
							syncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: ""})
						}
					}
				}
			}()
			slog.Info("sync-now event listener started")
		}

		slog.Info("smirror service running", "mirrors", cfg.ProjectNames())
		<-ctx.Done()
		slog.Info("smirror service stopped")
	}

	stopFunc := func() {
		if cancel != nil {
			cancel()
		}
	}

	syncNowFunc := func() {
		if liveSyncEngine == nil || liveCfg == nil {
			slog.Warn("sync-now signal received but engine not ready")
			return
		}
		slog.Info("sync-now signal received, triggering immediate full sync")
		for _, proj := range liveCfg.Projects {
			liveSyncEngine.Queue.Enqueue(msync.Task{Project: proj, RelPath: ""})
		}
	}

	if err := service.Run(startFunc, stopFunc, syncNowFunc); err != nil {
		slog.Error("service run failed", "error", err)
		os.Exit(1)
	}
}

// currentUser returns the current username (e.g., "raveh" or "SYSTEM (LocalSystem)").
func currentUser() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	// On Windows, u.Username is "DOMAIN\user".
	if runtime.GOOS == "windows" {
		parts := strings.SplitN(u.Username, `\`, 2)
		if len(parts) == 2 {
			domain := strings.ToUpper(parts[0])
			name := parts[1]
			// NT AUTHORITY\SYSTEM, NT SERVICE\... — keep full name
			if domain == "NT AUTHORITY" || domain == "NT SERVICE" {
				return u.Username
			}
			// Machine account (COMPUTERNAME$) — means LocalSystem service
			if strings.HasSuffix(name, "$") {
				return "SYSTEM (LocalSystem)"
			}
			// Regular local user — strip domain
			return name
		}
	}
	return u.Username
}

// updateConfigKey sets a top-level YAML key in the config file.
// If the key exists, its value is replaced. If not, a new line is appended.
func updateConfigKey(configPath, key, value string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	prefix := key + ":"
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, prefix) {
			lines[i] = key + ": " + value
			found = true
			break
		}
	}
	if !found {
		lines = append(lines, key+": "+value)
	}
	return os.WriteFile(configPath, []byte(strings.Join(lines, "\n")), 0644)
}

func cmdService(configPath string, args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: smirror service <install|uninstall|start|stop>")
		os.Exit(ExitConfigError)
	}

	switch args[0] {
	case "install":
		// Resolve rclone path and config before installing the service.
		// The Windows service runs as SYSTEM with different PATH and home dir.
		if cfg, err := config.Load(configPath); err == nil {
			// Resolve rclone binary to absolute path
			if !filepath.IsAbs(cfg.RclonePath) {
				if info, err := rclone.Detect(cfg.RclonePath); err == nil {
					if err := updateConfigKey(configPath, "rclone_path", info.Path); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not update rclone_path in config: %v\n", err)
						fmt.Fprintf(os.Stderr, "Manually set rclone_path to: %s\n\n", info.Path)
					} else {
						fmt.Printf("Resolved rclone_path: %s\n", info.Path)
					}
				}
			}
			// Resolve rclone config (SYSTEM has its own %APPDATA%, no remotes there)
			if cfg.RcloneConfig == "" {
				rcloneConf := filepath.Join(os.Getenv("APPDATA"), "rclone", "rclone.conf")
				if _, err := os.Stat(rcloneConf); err == nil {
					if err := updateConfigKey(configPath, "rclone_config", rcloneConf); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: could not update rclone_config in config: %v\n", err)
						fmt.Fprintf(os.Stderr, "Manually set rclone_config to: %s\n\n", rcloneConf)
					} else {
						fmt.Printf("Resolved rclone_config: %s\n", rcloneConf)
					}
				}
			}
		}
		if err := service.Install(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		// FR-SVC-08: Register Windows Event Log source
		if err := service.InstallEventSource(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not register event log source: %v\n", err)
		}
		fmt.Println("Service 'smirror' installed successfully.")
		fmt.Printf("Config: %s\n", configPath)
		fmt.Println("Start with: smirror service start")
		fmt.Println("Or: net start smirror")

	case "uninstall":
		_ = service.RemoveEventSource() // best-effort cleanup
		if err := service.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Service 'smirror' uninstalled successfully.")

	case "start":
		if err := service.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Service 'smirror' started.")
		cfg, err := config.Load(configPath)
		if err == nil {
			fmt.Printf("Follow log: powershell -NoProfile -Command \"Get-Content '%s' -Wait -Tail 30\"\n", cfg.LogFile)
		}

	case "stop":
		if err := service.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown service action: %s\nUse: install, uninstall, start, stop\n", args[0])
		os.Exit(ExitConfigError)
	}
}
