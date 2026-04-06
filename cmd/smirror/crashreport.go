package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
)

// crashReportDir is the directory where crash reports are saved.
// Defaults to config.DefaultDataDir(). Override in tests.
var crashReportDir string

// runWithCrashReport wraps fn in a top-level panic recovery.
// On panic: saves a crash report to ~/.selectivemirror/ and offers to submit it.
// This is used for CLI mode only — service mode has its own recovery in serviceMain().
func runWithCrashReport(fn func()) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		stack := string(debug.Stack())

		// Build and save crash report
		reportPath := saveCrashReport(r, stack)

		// Print user-friendly message
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "smirror encountered an unexpected error.")
		fmt.Fprintln(os.Stderr)
		if reportPath != "" {
			fmt.Fprintf(os.Stderr, "Crash report saved: %s\n", reportPath)
		}
		fmt.Fprintln(os.Stderr, "No file contents or personal data are included.")
		fmt.Fprintln(os.Stderr)

		// Offer to submit (interactive CLI only)
		offerCrashSubmission(reportPath)

		os.Exit(ExitError)
	}()
	fn()
}

// buildCrashReport assembles the crash report content from a panic.
func buildCrashReport(panicVal interface{}, stack string) string {
	var b strings.Builder
	tz := time.Now().Format("-07:00")
	now := time.Now().Format("2006-01-02T15:04:05") + tz

	b.WriteString(fmt.Sprintf("smirror crash report — %s\n", now))
	b.WriteString(fmt.Sprintf("smirror version: %s\n", version))
	b.WriteString(fmt.Sprintf("platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	b.WriteString(fmt.Sprintf("go version: %s\n", runtime.Version()))

	b.WriteString("\n--- Panic ---\n")
	b.WriteString(fmt.Sprintf("%v\n", panicVal))

	b.WriteString("\n--- Stack Trace ---\n")
	b.WriteString(stack)

	// Environment — best-effort, don't panic in the panic handler
	b.WriteString("\n--- Environment ---\n")
	if info, err := rclone.Detect(""); err == nil {
		b.WriteString(fmt.Sprintf("rclone: %s (%s)\n", info.Version.String(), info.Path))
	} else {
		b.WriteString(fmt.Sprintf("rclone: not found (%v)\n", err))
	}

	configPath := config.DefaultConfigPath()
	if cfg, err := config.Load(configPath); err == nil {
		b.WriteString(fmt.Sprintf("config: %s (%d mirrors)\n", configPath, len(cfg.Projects)))

		// Recent log lines — redacted
		b.WriteString("\n--- Recent Logs (last 30 lines) ---\n")
		if logData, err := os.ReadFile(cfg.LogFile); err == nil {
			lines := strings.Split(string(logData), "\n")
			start := 0
			if len(lines) > 30 {
				start = len(lines) - 30
			}
			home, _ := os.UserHomeDir()
			for _, line := range lines[start:] {
				if home != "" {
					line = strings.ReplaceAll(line, home, "<USER_HOME>")
				}
				b.WriteString(line + "\n")
			}
		} else {
			b.WriteString(fmt.Sprintf("(cannot read log: %v)\n", err))
		}
	} else {
		b.WriteString(fmt.Sprintf("config: %s (load error: %v)\n", configPath, err))
	}

	return b.String()
}

// saveCrashReport writes a crash report file to ~/.selectivemirror/.
// Returns the path to the saved file, or "" if saving failed.
func saveCrashReport(panicVal interface{}, stack string) string {
	report := buildCrashReport(panicVal, stack)

	dataDir := crashReportDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		// Last resort: print to stderr
		fmt.Fprintln(os.Stderr, report)
		return ""
	}

	ts := time.Now().Format("20060102-150405")
	filename := filepath.Join(dataDir, fmt.Sprintf("crash-%s.txt", ts))
	if err := os.WriteFile(filename, []byte(report), 0644); err != nil {
		fmt.Fprintln(os.Stderr, report)
		return ""
	}

	return filename
}

// offerCrashSubmission prompts the user to submit the crash report as a GitHub issue.
func offerCrashSubmission(reportPath string) {
	if reportPath == "" {
		return
	}

	fmt.Fprint(os.Stderr, "Submit this report to help fix the issue? [Y/n] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		// Non-interactive (piped, service) — don't submit
		fmt.Fprintln(os.Stderr, "Report saved. Submit later: smirror report-bug --open")
		return
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "n" || line == "no" {
		fmt.Fprintln(os.Stderr, "Report saved. Submit later: smirror report-bug --open")
		return
	}

	// Read the report and open browser with pre-filled issue
	report, err := os.ReadFile(reportPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot read report: %v\n", err)
		return
	}

	issueURL := "https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml"
	issueURL += "&title=" + url.QueryEscape("[Crash] panic in smirror "+version)
	encoded := url.QueryEscape(string(report))
	fullURL := issueURL + "&environment=" + encoded

	// Windows cmd.exe has ~8191 char limit for URLs
	if len(fullURL) > 8000 {
		truncated := string(report)
		if len(truncated) > 3500 {
			truncated = truncated[:3500] + "\n... (truncated, paste full report from " + reportPath + ")"
		}
		fullURL = issueURL + "&environment=" + url.QueryEscape(truncated)
	}

	fmt.Fprintln(os.Stderr, "Opening browser...")
	_ = exec.Command("cmd", "/c", "start", fullURL).Start()

	// Rename to .sent to avoid re-prompting
	_ = os.Rename(reportPath, reportPath+".sent")
}

// checkUnsentCrashReports scans for crash-*.txt files (not .sent) and offers
// to submit them. Call this on interactive commands like start, status.
func checkUnsentCrashReports(configPath string) {
	dataDir := crashReportDir
	if dataDir == "" {
		dataDir = config.DefaultDataDir()
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, "crash-*.txt"))
	if err != nil || len(matches) == 0 {
		return
	}

	// Filter out .sent files (Glob matched crash-*.txt but not crash-*.txt.sent)
	var unsent []string
	for _, m := range matches {
		if !strings.HasSuffix(m, ".sent") {
			unsent = append(unsent, m)
		}
	}
	if len(unsent) == 0 {
		return
	}

	// Show notice
	if len(unsent) == 1 {
		info, _ := os.Stat(unsent[0])
		age := ""
		if info != nil {
			age = fmt.Sprintf(" from %s", info.ModTime().Format("2006-01-02"))
		}
		fmt.Fprintf(os.Stderr, "Note: 1 unsent crash report found%s.\n", age)
	} else {
		fmt.Fprintf(os.Stderr, "Note: %d unsent crash reports found.\n", len(unsent))
	}

	fmt.Fprint(os.Stderr, "Submit now? [Y/n] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return // non-interactive
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "n" || line == "no" {
		fmt.Fprintln(os.Stderr)
		return
	}

	// Submit the most recent one
	offerCrashSubmission(unsent[len(unsent)-1])

	// Mark all as sent
	for _, f := range unsent {
		_ = os.Rename(f, f+".sent")
	}
	fmt.Fprintln(os.Stderr)
}
