// Command smirror is a selective near-real-time file mirror built on rclone.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/filter"
	"github.com/qraveh/SelectiveMirror/internal/logging"
	"github.com/qraveh/SelectiveMirror/internal/state"
	msync "github.com/qraveh/SelectiveMirror/internal/sync"
	"github.com/qraveh/SelectiveMirror/internal/watcher"

	"log/slog"
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
  start              Start the file watcher (foreground)
  sync-now [project] Trigger immediate sync for one or all projects
  dry-run [project]  Show what would be synced without doing it
  status             Show last sync times per project
  validate           Check configuration and rclone connectivity
  list-filters [project]  Show effective filter rules
  version            Show version

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

func cmdStart(configPath string, args []string) {
	cfg := loadConfig(configPath)

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

	// Create sync engine
	syncEngine := msync.NewEngine(cfg, st, filters)

	// Create watcher manager
	watchMgr, err := watcher.NewManager(cfg.Projects, filters, syncEngine.TaskChan)
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

	// Start watcher
	if err := watchMgr.Start(ctx); err != nil {
		slog.Error("watcher start failed", "error", err)
		os.Exit(1)
	}
	defer watchMgr.Stop()

	// Start sync engine
	go syncEngine.Run(ctx)

	// Start heartbeat
	go heartbeatLoop(ctx, st, cfg.HeartbeatInterval())

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
	syncEngine := msync.NewEngine(cfg, st, filters)

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
	syncEngine := msync.NewEngine(cfg, st, filters)

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
	fmt.Printf("State DB: %s\n\n", cfg.StateDB)

	lastHB, _ := st.GetMeta("last_heartbeat")
	if lastHB != "" {
		fmt.Printf("Last heartbeat: %s\n\n", lastHB)
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
			fmt.Printf("  Last sync: %s (%s ago)\n", lastSync.Format(time.RFC3339), time.Since(lastSync).Round(time.Second))
		} else {
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

// reconcileAll walks all project directories and queues changed/new files.
func reconcileAll(ctx context.Context, cfg *config.Global, st *state.Store, filters map[string]*filter.Engine, syncEngine *msync.Engine) {
	for _, proj := range cfg.Projects {
		fe := filters[proj.Name]
		synced, err := st.GetAllSyncedPaths(proj.Name)
		if err != nil {
			slog.Warn("reconcile: state query failed", "project", proj.Name, "error", err)
			continue
		}

		queued := 0
		filepath.WalkDir(proj.LocalPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				relDir, _ := filepath.Rel(proj.LocalPath, path)
				if relDir != "." && fe != nil && fe.IsExcluded(relDir+"/") {
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
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}
			if info.Size() > proj.MaxFileSize() {
				return nil
			}

			// Check if file needs sync
			existing := synced[relPath]
			if existing != nil && existing.RcloneExit == 0 {
				// Quick check: if size matches, likely unchanged (full hash check on sync)
				if existing.FileSize == info.Size() {
					return nil
				}
			}

			syncEngine.TaskChan <- msync.Task{Project: proj, RelPath: relPath}
			queued++
			return nil
		})

		if queued > 0 {
			slog.Info("reconciliation queued files", "project", proj.Name, "count", queued)
		} else {
			slog.Info("reconciliation: all synced", "project", proj.Name)
		}
	}
}

func heartbeatLoop(ctx context.Context, st *state.Store, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ts := time.Now().UTC().Format(time.RFC3339)
			st.SetMeta("last_heartbeat", ts)

			// Also write to heartbeat file
			dataDir := config.DefaultDataDir()
			hbPath := filepath.Join(dataDir, "heartbeat.txt")
			os.WriteFile(hbPath, []byte(ts+"\n"), 0644)
		case <-ctx.Done():
			return
		}
	}
}
