package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/lock"
	"github.com/qraveh/SelectiveMirror/internal/service"
	"github.com/qraveh/SelectiveMirror/internal/task"
)

// cmdClean handles `smirror clean [--self|--all] [--yes]`.
//
// Modes:
//   --self (default): remove current user's scheduled task + the user's
//     data directory (~/.selectivemirror/). No admin required. Ideal for
//     "I'm done using smirror on this account" cleanup.
//   --all: everything in --self PLUS the Windows Service (if installed)
//     and %ProgramData%\SelectiveMirror. Requires admin for the service
//     uninstall; the service-data dir deletion also requires admin if it
//     exists and is admin-owned.
//
// Safety:
//   - Always prints what will be removed and prompts unless --yes is given.
//   - Refuses to run while any smirror process appears to be holding the
//     single-instance lock (for --self: the current user's lock; for --all:
//     both the user lock and the service-mode lock if present).
func cmdClean(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror clean [--self|--all] [--yes]

Remove smirror's background registration and data for the current user.

Modes:
  --self (default)  Remove the per-user Scheduled Task and ~/.selectivemirror/.
                    No admin privileges required.
  --all             --self + uninstall the Windows Service (if installed) +
                    remove %ProgramData%\SelectiveMirror. Requires admin for
                    service uninstall.

Flags:
  --yes, -y         Skip the confirmation prompt.

Examples:
  smirror clean              # interactive per-user cleanup
  smirror clean --self --yes # scripted per-user cleanup
  smirror clean --all        # full removal (prompts for UAC if service is installed)`) {
		return
	}

	mode, autoYes, err := parseCleanFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitConfigError)
	}

	plan, err := buildCleanPlan(configPath, mode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}

	if plan.nothing() {
		fmt.Println("Nothing to clean — no task, no service, and no data directory found.")
		return
	}

	plan.print()

	if !autoYes {
		fmt.Print("\nProceed? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		resp, _ := reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Println("Aborted.")
			os.Exit(0)
		}
	}

	if err := plan.execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	fmt.Println("Done.")
}

// cleanMode discriminates self vs all.
type cleanMode int

const (
	cleanSelf cleanMode = iota
	cleanAll
)

func parseCleanFlags(args []string) (cleanMode, bool, error) {
	mode := cleanSelf
	autoYes := false
	modeSet := false
	for _, a := range args {
		switch a {
		case "--self":
			if modeSet && mode != cleanSelf {
				return 0, false, fmt.Errorf("cannot combine --self and --all")
			}
			mode = cleanSelf
			modeSet = true
		case "--all":
			if modeSet && mode != cleanAll {
				return 0, false, fmt.Errorf("cannot combine --self and --all")
			}
			mode = cleanAll
			modeSet = true
		case "--yes", "-y":
			autoYes = true
		default:
			return 0, false, fmt.Errorf("unknown flag: %s\nRun 'smirror clean --help' for usage.", a)
		}
	}
	return mode, autoYes, nil
}

// cleanPlan is the list of removal actions to execute. Built up by
// buildCleanPlan so the user sees a preview before confirming.
type cleanPlan struct {
	mode cleanMode

	// Per-user removals
	taskInstalled bool
	userDataDir   string // "" if absent

	// Machine-wide removals (--all only)
	serviceInstalled bool
	serviceDataDir   string // %ProgramData%\SelectiveMirror if present; "" if absent

	// Preflight warnings (shown before executing)
	warnings []string
}

func (p *cleanPlan) nothing() bool {
	return !p.taskInstalled && p.userDataDir == "" && !p.serviceInstalled && p.serviceDataDir == ""
}

func (p *cleanPlan) print() {
	fmt.Println("The following will be removed:")
	if p.taskInstalled {
		fmt.Printf("  - Scheduled task: %s (current user)\n", task.TaskName)
	}
	if p.userDataDir != "" {
		fmt.Printf("  - User data directory: %s\n", p.userDataDir)
	}
	if p.serviceInstalled {
		fmt.Println("  - Windows Service: smirror (requires admin)")
	}
	if p.serviceDataDir != "" {
		fmt.Printf("  - Service data directory: %s (may require admin)\n", p.serviceDataDir)
	}
	for _, w := range p.warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

// buildCleanPlan inspects the system and returns the actions that would
// run for the given mode. Returns a plan + non-fatal warnings.
func buildCleanPlan(configPath string, mode cleanMode) (*cleanPlan, error) {
	p := &cleanPlan{mode: mode}

	// Per-user task
	if installed, err := task.IsInstalled(); err == nil && installed {
		p.taskInstalled = true
	} else if err != nil && !errors.Is(err, task.ErrUnsupported) {
		// Log but don't fail — we'd rather clean what we can.
		p.warnings = append(p.warnings, fmt.Sprintf("could not query scheduled task: %v", err))
	}

	// Per-user data dir — derived from configPath. The user data dir is the
	// directory containing the config file (project convention).
	//
	// We do NOT load+parse the config here: a malformed config must not cause
	// us to silently fall back to ~/.selectivemirror/ and wipe it (the user
	// would then have lost data unrelated to the path they specified).
	//
	// Home-dir fallback only applies when the user did not pass --config:
	// if they accepted the default and that dir is gone, falling back to the
	// canonical home location is safe.
	if dir := filepath.Dir(configPath); dir != "" && dir != "." {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			p.userDataDir = dir
		}
	}
	if p.userDataDir == "" && configPath == config.DefaultConfigPath() {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := filepath.Join(home, ".selectivemirror")
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				p.userDataDir = candidate
			}
		}
	}

	// SM-: Refuse if a daemon (foreground / task / service) holds the
	// single-instance lock — racing os.RemoveAll against an active daemon
	// produces partial deletions and silent state corruption.
	if p.userDataDir != "" {
		if locked, _ := lock.IsLocked(p.userDataDir); locked {
			return nil, fmt.Errorf("smirror appears to be running (lock held in %s); stop it first via `smirror task stop` or `smirror service stop`", p.userDataDir)
		}
	}

	if mode == cleanAll {
		// Windows Service
		if service.IsInstalled() {
			p.serviceInstalled = true
		}
		// Service data dir: %ProgramData%\SelectiveMirror (the recommended
		// admin-owned location). We don't try to guess user-scoped service
		// data dirs — clean --all is explicitly about the machine install.
		if pd := os.Getenv("ProgramData"); pd != "" {
			candidate := filepath.Join(pd, "SelectiveMirror")
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				p.serviceDataDir = candidate
			}
		}
		if p.serviceDataDir != "" {
			if locked, _ := lock.IsLocked(p.serviceDataDir); locked {
				return nil, fmt.Errorf("smirror service appears to be running (lock held in %s); stop it first via `smirror service stop`", p.serviceDataDir)
			}
		}
	}
	return p, nil
}

func (p *cleanPlan) execute() error {
	// 1. Stop and uninstall the task (per-user, no admin needed).
	if p.taskInstalled {
		if err := task.Stop(); err != nil && !errors.Is(err, task.ErrNotInstalled) {
			fmt.Fprintf(os.Stderr, "  Warning: could not stop task: %v\n", err)
		}
		if err := task.Uninstall(); err != nil && !errors.Is(err, task.ErrNotInstalled) {
			return fmt.Errorf("uninstall task: %w", err)
		}
		fmt.Printf("  Task %q removed.\n", task.TaskName)
	}

	// 2. Remove per-user data.
	if p.userDataDir != "" {
		if err := os.RemoveAll(p.userDataDir); err != nil {
			return fmt.Errorf("remove user data dir %s: %w", p.userDataDir, err)
		}
		fmt.Printf("  Removed %s\n", p.userDataDir)
	}

	// 3. --all: service + service data dir (admin).
	if p.mode == cleanAll {
		if p.serviceInstalled {
			if !isAdmin() {
				return fmt.Errorf("service uninstall requires administrator privileges; re-run from an elevated terminal")
			}
			if err := service.Stop(); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: could not stop service (may not be running): %v\n", err)
			}
			if err := service.Uninstall(); err != nil {
				return fmt.Errorf("uninstall service: %w", err)
			}
			fmt.Println("  Service 'smirror' removed.")
		}
		if p.serviceDataDir != "" {
			if err := os.RemoveAll(p.serviceDataDir); err != nil {
				return fmt.Errorf("remove service data dir %s (admin required?): %w", p.serviceDataDir, err)
			}
			fmt.Printf("  Removed %s\n", p.serviceDataDir)
		}
	}
	return nil
}
