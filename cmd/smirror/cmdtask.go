package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/task"
)

// cmdTask handles `smirror task <action>` — per-user Scheduled Task management.
//
// Unlike `smirror service`, no admin privileges are required — each user owns
// their own scheduled tasks. This is the recommended mode for desktop
// deployments; service mode is reserved for 24/7 operation across logoffs.
func cmdTask(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror task <action>

Per-user Scheduled Task management. The task runs smirror at user logon as
the current user. No admin privileges are required to install, uninstall,
start, stop, or query the task — users own their own scheduled tasks.

This is the recommended background mode for desktop deployments. Use
'smirror service' only when you need 24/7 operation that survives user
logoff (service mode requires admin and runs as LocalSystem).

Actions:
  install             Register the task for the current user at logon
  uninstall           Remove the per-user task
  start               Run the task now (one-shot; logon trigger is separate)
  stop                Terminate any running instance of the task
  status              Show installed/running state + last run info`) {
		return
	}

	if len(args) == 0 {
		printTaskUsage()
		os.Exit(ExitConfigError)
	}

	action := args[0]
	rest := args[1:]
	if len(rest) > 0 {
		fmt.Fprintf(os.Stderr, "Error: `smirror task %s` takes no further arguments.\n", action)
		printTaskUsage()
		os.Exit(ExitConfigError)
	}

	switch action {
	case "install":
		taskDoInstall(configPath)
	case "uninstall":
		taskDoUninstall()
	case "start":
		taskDoStart()
	case "stop":
		taskDoStop()
	case "status":
		taskDoStatus()
	default:
		fmt.Fprintf(os.Stderr, "Unknown task action: %s\n", action)
		printTaskUsage()
		os.Exit(ExitConfigError)
	}
}

func printTaskUsage() {
	fmt.Fprintln(os.Stderr, `Usage: smirror task <action>

Actions:
  install     Register the task for the current user at logon
  uninstall   Remove the per-user task
  start       Run the task now
  stop        Terminate any running instance
  status      Show installed/running state`)
}

func taskDoInstall(configPath string) {
	// The config must load successfully — we refuse to register a task
	// that points at an invalid config, same pre-flight as service install.
	if _, err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nCannot install the task without a valid configuration.\n")
		fmt.Fprintf(os.Stderr, "Create a config file at: %s\n", configPath)
		fmt.Fprintf(os.Stderr, "Example: smirror --config %s start  (generates default config on first run)\n", configPath)
		os.Exit(ExitConfigError)
	}

	err := task.Install(configPath)
	switch {
	case errors.Is(err, task.ErrAlreadyInstalled):
		fmt.Fprintln(os.Stderr, "Task is already installed — uninstall first with: smirror task uninstall")
		os.Exit(ExitError)
	case errors.Is(err, task.ErrUnsupported):
		fmt.Fprintln(os.Stderr, "Scheduled tasks are only supported on Windows.")
		os.Exit(ExitError)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}

	fmt.Printf("Task %q installed for the current user.\n", task.TaskName)
	fmt.Printf("Config: %s\n", configPath)
	fmt.Println("Starts automatically on next logon. Run now with: smirror task start")
}

func taskDoUninstall() {
	err := task.Uninstall()
	switch {
	case errors.Is(err, task.ErrNotInstalled):
		fmt.Println("Task is not installed — nothing to do.")
		return
	case errors.Is(err, task.ErrUnsupported):
		fmt.Fprintln(os.Stderr, "Scheduled tasks are only supported on Windows.")
		os.Exit(ExitError)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	fmt.Printf("Task %q uninstalled.\n", task.TaskName)
}

func taskDoStart() {
	err := task.Start()
	switch {
	case errors.Is(err, task.ErrNotInstalled):
		fmt.Fprintln(os.Stderr, "Task is not installed — install first with: smirror task install")
		os.Exit(ExitError)
	case errors.Is(err, task.ErrUnsupported):
		fmt.Fprintln(os.Stderr, "Scheduled tasks are only supported on Windows.")
		os.Exit(ExitError)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	fmt.Printf("Task %q started.\n", task.TaskName)
}

func taskDoStop() {
	err := task.Stop()
	switch {
	case errors.Is(err, task.ErrNotInstalled):
		fmt.Fprintln(os.Stderr, "Task is not installed.")
		os.Exit(ExitError)
	case errors.Is(err, task.ErrUnsupported):
		fmt.Fprintln(os.Stderr, "Scheduled tasks are only supported on Windows.")
		os.Exit(ExitError)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	fmt.Printf("Task %q stopped (or was not running).\n", task.TaskName)
}

func taskDoStatus() {
	s, err := task.Query()
	if errors.Is(err, task.ErrUnsupported) {
		fmt.Fprintln(os.Stderr, "Scheduled tasks are only supported on Windows.")
		os.Exit(ExitError)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	if !s.Installed {
		fmt.Printf("Task %q is not installed.\n", task.TaskName)
		fmt.Println("Install with: smirror task install")
		return
	}
	fmt.Printf("Task %q: installed\n", task.TaskName)
	if s.Running {
		fmt.Println("  State: running")
	} else {
		fmt.Println("  State: idle (ready for next logon trigger)")
	}
	if s.LastRunTime != "" {
		fmt.Printf("  Last run: %s (result=%s)\n", s.LastRunTime, s.LastRunResult)
	}
	if s.NextRunTime != "" {
		fmt.Printf("  Next run: %s\n", s.NextRunTime)
	}
}

