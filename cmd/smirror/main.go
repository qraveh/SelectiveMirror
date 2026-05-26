// Command smirror is a selective near-real-time file mirror built on rclone.
package main

import (
	"bufio"
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
	"github.com/qraveh/SelectiveMirror/internal/telemetry"
	"github.com/qraveh/SelectiveMirror/internal/watcher"

	_ "github.com/mattn/go-sqlite3"
)

var version = "1.0.38-dev"

// Repository coordinates. All runtime references to the GitHub repo (issue
// URLs, selfupdate API, duplicate search) derive from these two constants.
// On a repo rename/move, update these — no other source files need to change.
const (
	repoOwner = "qraveh"
	repoName  = "SelectiveMirror"
	repoURL   = "https://github.com/" + repoOwner + "/" + repoName
	issueNewURL  = repoURL + "/issues/new"
	issueBugURL  = issueNewURL + "?template=bug_report.yml"
)

// FR-CLI-07: Documented exit codes for script/CI integration.
const (
	ExitSuccess      = 0
	ExitError        = 1 // general error
	ExitConfigError  = 2 // config load/validation failure
	ExitRcloneError  = 3 // rclone-related failure (unreachable, auth, binary missing)
	ExitLockConflict = 4 // another instance is running
	ExitDrift        = 5 // diagnostic found drift (leaks, orphans, mismatches — tool worked, action needed)
	ExitUpgrade      = 6 // selfupdate: new version available but user declined or preflight failed
)

func main() {
	// Emergency: write a single-line breadcrumb to a path the running
	// principal can write. Must happen before anything else — services
	// have no console and the normal log isn't open yet.
	//
	// Service mode (LocalSystem): %ProgramData%\SelectiveMirror\early.log.
	// CLI mode (per-user): %TEMP%\smirror-early.log.
	//
	// The previous fixed `C:\smirror-early.log` failed in two ways:
	//  - Non-admin CLI invocations on locked-down boxes had no write access
	//    to C:\, so the breadcrumb was silently lost.
	//  - PID + os.Args were readable by anyone with C:\ list access (which
	//    is everyone by default).
	earlyLogPath := earlyLogTarget(service.IsWindowsService())
	if earlyLogPath != "" {
		earlyDir := filepath.Dir(earlyLogPath)
		_ = os.MkdirAll(earlyDir, 0700)

		// SM-213: lock down the service data directory ACL on Windows
		// before any state file is written. The 0700 mode passed above is
		// SILENTLY IGNORED on Windows; without an explicit DACL the
		// directory inherits %ProgramData%\'s default (BUILTIN\Users:R&X),
		// exposing state.db, status.json, anomalies/*.jsonl, early.log
		// and service-crash.log to every user on a multi-user host. We
		// reset the DACL to SYSTEM+Administrators only on every service-
		// mode startup (idempotent; re-tightens after any ad-hoc change).
		// User-mode invocations skip this — %LOCALAPPDATA% is already
		// per-user by default.
		//
		// Failures here are logged to the early-log line below but not
		// fatal: a non-functional service is worse than a degraded-
		// privacy one for v1.0. The first ACL audit (`smirror service
		// status` round-trip + `icacls`) will flag the regression.
		var aclErr error
		if service.IsWindowsService() {
			aclErr = config.RestrictDirToSystemAndAdmins(earlyDir)
		}

		earlyLog, _ := os.OpenFile(earlyLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if earlyLog != nil {
			fmt.Fprintf(earlyLog, "%s main() entered pid=%d isSvc=%v args=%v\n",
				time.Now().Format(time.RFC3339), os.Getpid(), service.IsWindowsService(), os.Args)
			if aclErr != nil {
				fmt.Fprintf(earlyLog, "%s SM-213 RestrictDirToSystemAndAdmins(%s) failed: %v\n",
					time.Now().Format(time.RFC3339), earlyDir, aclErr)
			}
			earlyLog.Close()
		}
	}

	// If running as a Windows Service, the SCM invokes us with no args.
	// Detect this and enter service mode immediately.
	if service.IsWindowsService() {
		serviceMain()
		return
	}

	// CLI mode: wrap in crash recovery so panics produce a saved report
	// instead of a raw stack trace.
	runWithCrashReport(cliMain)
}

// extractConfigPath scans args for --config and --config=<value> flags and
// removes ALL of them from the result. The last `--config` occurrence wins
// (assigns to *out); earlier occurrences are silently dropped. Args that
// are not --config are preserved in original order.
//
// last-wins for --config matches kubectl/
// docker/gh conventions and avoids the first-wins-but-stops-iterating
// behavior that left subsequent --config args in the slice.
// extractConfigPath returns the residual args after stripping --config
// flag pairs. It calls os.Exit(ExitConfigError) on user-error inputs
// (trailing --config, --config followed by another flag, empty
// --config= value). For unit-testing, use extractConfigPathErr which
// returns the error rather than exiting.
func extractConfigPath(args []string, out *string) []string {
	result, err := extractConfigPathErr(args, out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(ExitConfigError)
	}
	return result
}

// extractConfigPathErr is the testable form of extractConfigPath.
// SM-187: pre-fix, a trailing `--config` (no value) was silently
// dropped, so `smirror status --config` defaulted to
// ~/.selectivemirror/config.yaml instead of erroring. Worse: a
// `--config --other-flag` would consume the next flag as the config-
// path value. Both cases now produce a non-nil error so the user
// gets an explicit "missing value" exit.
func extractConfigPathErr(args []string, out *string) ([]string, error) {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--config" {
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--config requires a value (path to config.yaml)")
			}
			next := args[i+1]
			if strings.HasPrefix(next, "-") {
				return nil, fmt.Errorf("--config requires a value (got next-flag %q; pass the path explicitly)", next)
			}
			*out = next
			i++ // consume the value
			continue
		}
		if strings.HasPrefix(a, "--config=") {
			val := strings.TrimPrefix(a, "--config=")
			if val == "" {
				return nil, fmt.Errorf("--config= requires a value (path to config.yaml)")
			}
			*out = val
			continue
		}
		result = append(result, a)
	}
	return result, nil
}

// earlyLogTarget returns the path for the very-early diagnostic log written
// before any normal logging is set up.
func earlyLogTarget(isService bool) string {
	if isService {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "SelectiveMirror", "early.log")
		}
		if sr := os.Getenv("SystemRoot"); sr != "" {
			return filepath.Join(sr, "Logs", "smirror-early.log")
		}
		return ""
	}
	return filepath.Join(os.TempDir(), "smirror-early.log")
}

// serviceCrashLogTarget returns the path for the service-mode crash log
// captured between SCM startup and normal logging being available.
func serviceCrashLogTarget() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return filepath.Join(pd, "SelectiveMirror", "service-crash.log")
	}
	if sr := os.Getenv("SystemRoot"); sr != "" {
		return filepath.Join(sr, "Logs", "smirror-service-crash.log")
	}
	return ""
}

// cliMain is the CLI entry point, called from main() wrapped in runWithCrashReport.
func cliMain() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Find config file. # last-wins
	// semantics if --config is given multiple times. Previously the first
	// occurrence was kept and subsequent ones were left in args, which
	// confused downstream parsers and made the "winner" depend on an
	// undocumented break point. Last-wins matches most CLI conventions
	// (kubectl, docker, gh) and gives an obvious result for typo'd
	// `smirror --config bogus --config good version`.
	configPath := config.DefaultConfigPath()
	args := extractConfigPath(os.Args[1:], &configPath)

	// SM-181: record the user's active --config so a panic recovery via
	// runWithCrashReport's deferred handler (in crashreport.go) can
	// sanitize against the user's actual mirror set rather than the
	// default config. The package-level setter is safe to call
	// repeatedly; subsequent commands' bumps don't break in-flight
	// crash reporting.
	SetActiveConfigPath(configPath)

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	cmdArgs := args[1:]

	// Print version header for all commands (except those that handle it themselves)
	switch cmd {
	case "version", "help", "--help", "-h", "report-bug", "selfupdate":
		// handled below
	default:
		fmt.Printf("smirror %s\n", version)
	}

	// Check for unsent crash reports on interactive commands
	switch cmd {
	case "start", "status", "test-mirrors", "doctor", "verify":
		checkUnsentCrashReports(configPath)
	}

	switch cmd {
	case "start":
		cmdStart(configPath, cmdArgs)
	case "sync-now", "syncnow":
		cmdSyncNow(configPath, cmdArgs)
	case "dry-run":
		cmdDryRun(configPath, cmdArgs)
	case "status":
		cmdStatus(configPath, cmdArgs)
	case "test-mirrors", "doctor", "verify":
		cmdTestMirrors(configPath, cmdArgs)
	case "list-filters":
		cmdListFilters(configPath, cmdArgs)
	case "explain":
		cmdExplain(configPath, cmdArgs)
	case "project-stats", "stats":
		cmdStats(configPath, cmdArgs)
	case "report-bug":
		cmdReportBug(configPath, cmdArgs)
	case "selfupdate":
		cmdSelfUpdate(configPath, cmdArgs)
	case "remote":
		cmdRemote(configPath, cmdArgs)
	case "addmirror", "add-mirror", "add":
		cmdAddMirror(configPath, cmdArgs)
	case "unmirror", "removemirror", "remove-mirror", "remove":
		cmdUnmirror(configPath, cmdArgs)
	case "clean":
		cmdClean(configPath, cmdArgs)
	case "task":
		cmdTask(configPath, cmdArgs)
	case "service":
		cmdService(configPath, cmdArgs)
	case "telemetry":
		// SM-157: runtime tier management.
		cmdTelemetry(configPath, cmdArgs)
	case "version":
		fmt.Printf("smirror %s\n", version)
		fmt.Println("Copyright (c) 2026 Raveh Neeman")
		// Telemetry build-key fingerprint: confirms whether this binary
		// was built with the production HMAC derived key (CI release with
		// SMIRROR_TELEMETRY_MASTER_KEY) or is a -dev/local build that
		// cannot submit to the production endpoint. Never reveals the key.
		fmt.Printf("telemetry build-key: %s\n", telemetry.BuildKeyFingerprint())
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
  start                      Start the file watcher (foreground)
  sync-now [mirror]          Immediate full sync + ghost cleanup (aliases: syncnow)
  dry-run [mirror]           Show what would sync + ghost cleanup preview
  status [mirror]            Show sync status, metrics, instance state
  test-mirrors [mirror]      Run diagnostics and verify sync state (aliases: doctor, verify)
  list-filters [mirror]      Show effective filter rules
  explain <mirror> <path>    Explain include/exclude status, matched rule, sync state
  project-stats [mirror]     Show file counts and line counts per mirror (alias: stats)
  report-bug [flags]         Generate diagnostic report (--stdout, --open)
  remote [remote_path]       Show or set the default rclone remote for new mirrors
  addmirror <path...> [flags]  Add directories as mirrors (aliases: add-mirror, add)
                             Flags: -dest <remote>, --delete, --initial-sync
  unmirror <name|path> [flags]  Remove a mirror from config and clean state DB
                             Flags: --purge-remote, --yes (aliases: removemirror, remove-mirror, remove)
  clean [--self|--all] [--yes]  Remove user data and background registration
                             --self: remove current user's task + ~/.selectivemirror/ (no admin; default)
                             --all:  --self + service if installed + %%ProgramData%%\SelectiveMirror (admin for service)
  selfupdate [flags]         Check for and install updates (--check, --whatsnew, --yes, --include-rclone)
  task <action>              Per-user Scheduled Task (recommended background mode; no admin)
                             Actions: install, uninstall, start, stop, status
  service <action...>        Windows Service: install [start], stop, uninstall [--clean] [--yes]
                             Compound: "service install start", "service stop uninstall [--clean]"
                             ("run as administrator" elevated cmd/PowerShell required; advanced 24/7 mode)
  telemetry <action>         View / change telemetry tier (see 'smirror telemetry --help')
                             Actions: status, none, standard, reliability, policy, inspect
  version                    Show version

Options:
  --config PATH      Path to config file (default: ~/.selectivemirror/config.yaml)

`, version)
}

// subcommandHelp checks if args contain --help or -h and prints the given
// help text if so. Returns true if help was shown (caller should return).
// Must be called before any config loading, state DB access, or lock
// acquisition to avoid side effects (SM-128).
func subcommandHelp(args []string, helpText string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			fmt.Println(helpText)
			return true
		}
	}
	return false
}

// rejectUnknownFlags checks if any positional args look like flags (start with -)
// and exits with an error if so. Used by commands that take only positional
// arguments (mirror names, paths) and should not silently swallow unknown flags.
func rejectUnknownFlags(command string, args []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			fmt.Fprintf(os.Stderr, "unknown flag: %s\nRun 'smirror %s --help' for usage.\n", a, command)
			os.Exit(ExitError)
		}
	}
}

// checkMaxArgs exits with an error if more than maxArgs positional arguments
// are provided. Call after rejectUnknownFlags to catch extra trailing args.
func checkMaxArgs(command string, args []string, maxArgs int) {
	if len(args) > maxArgs {
		fmt.Fprintf(os.Stderr, "too many arguments\nRun 'smirror %s --help' for usage.\n", command)
		os.Exit(ExitError)
	}
}

func loadConfig(path string) *config.Global {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", path, err)
		os.Exit(ExitConfigError)
	}
	return cfg
}

// loadConfigBestEffort returns a parsed config even if validation fails, so
// callers that only need a few top-level fields (rclone_path, rclone_config,
// default_remote) work on configs with no mirrors yet or other validation
// issues. Returns nil if the file cannot be parsed at all.
func loadConfigBestEffort(path string) *config.Global {
	if cfg, err := config.Load(path); err == nil {
		return cfg
	}
	if cfg, err := config.LoadRaw(path); err == nil {
		return cfg
	}
	return nil
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

// wireAnomalyRecorder attaches a fresh anomaly.Recorder to syncEngine
// when anomaly detection is enabled in cfg. Returns the recorder so
// the caller can defer Close on it; returns nil if anomaly detection
// is disabled.
//
// NEW-R10-1: cmdSyncNow / runInitialSync used to skip this wiring
// (only cmdStart and serviceMain configured the engine's recorder),
// so failures from those code paths produced zero anomaly files even
// with anomaly_detection_enabled=true. The persistent failure counter
// in `internal/sync/sync.go::recordPersistentFullSyncFailure` provides
// the cross-process state; this helper provides the recorder that
// actually writes the anomaly JSONL files.
func wireAnomalyRecorder(syncEngine *msync.Engine, cfg *config.Global) *anomaly.Recorder {
	if !cfg.IsAnomalyDetectionEnabled() {
		return nil
	}
	anomalyDir := filepath.Join(dataDir(cfg), "anomalies")
	rec := anomaly.NewRecorder(anomaly.NewFileWriter(anomalyDir))
	syncEngine.Anomaly = rec

	// SEC-M5: register per-mirror local_path prefixes for anomaly
	// path-sanitization (matches cmdStart / serviceMain wiring).
	paths := make([]string, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		paths = append(paths, p.LocalPath)
	}
	anomaly.SetExtraSanitizePrefixes(paths)
	return rec
}

// severityAtLeast returns true if sev is at or above the threshold.
// Order: info < warning < error < critical.
func severityAtLeast(sev, threshold string) bool {
	order := map[string]int{"info": 0, "warning": 1, "error": 2, "critical": 3}
	return order[sev] >= order[threshold]
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
	if subcommandHelp(args, `Usage: smirror start [--config PATH]

Start the file watcher in the foreground. Watches all configured mirrors
for file changes and syncs them to their remote destinations.

A single-instance lock prevents duplicate instances. Runs an initial
full sync on startup, then switches to incremental (on-change) mode.

Press Ctrl+C to stop.`) {
		return
	}

	// Non-blocking update check (rate-limited to once/24h)
	go checkForUpdateOnStartup(configPath)

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

	// Check for multiple smirror installations on PATH.
	if instInfo := findInstallations(); instInfo.HasDuplicates {
		fmt.Fprintln(os.Stderr)
		warnMultipleInstallations(instInfo)
		fmt.Fprintln(os.Stderr)
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

	// # surface a one-line warning if state.db was missing or
	// zero-byte at open time. A user who accidentally rm'd the DB is
	// about to lose all sync history (everything will be re-uploaded
	// on the next reconciliation). The daemon path proceeds anyway —
	// "starting fresh" is acceptable behavior — but the user should
	// see the message.
	if st.WasFreshOpen {
		slog.Warn("state DB was created fresh — no sync history exists; first reconciliation will re-upload all included files", "path", cfg.StateDB)
	} else if st.WasZeroByteOpen {
		slog.Warn("state DB was zero-byte at open — possibly truncated by a crash; treated as fresh", "path", cfg.StateDB)
	}

	// SM-083: Clean up orphaned project state entries on startup
	if pruned, err := st.PruneOrphanedProjects(cfg.ProjectNames()); err == nil && pruned > 0 {
		slog.Info("pruned orphaned project state entries", "removed", pruned)
	}

	// Record instance info so `smirror status` can report it.
	// Clear stale health errors from previous run (SM-074).
	st.MarkDaemonStartup()
	exePath, _ := os.Executable()
	st.SetMeta("instance_pid", fmt.Sprintf("%d", os.Getpid()))
	st.SetMeta("instance_exe", exePath)
	st.SetMeta("instance_started", time.Now().Local().Format(time.RFC3339))
	st.SetMeta("instance_mode", "foreground")
	st.SetMeta("instance_version", version)
	st.SetMeta("instance_user", currentUser())
	st.SetMeta("instance_config_fingerprint", cfg.MirrorFingerprint()) // SM-122
	// SM-122: Store active mirror config so status can show what's actually running.
	if mirrorsJSON, err := json.Marshal(cfg.Projects); err == nil {
		st.SetMeta("instance_mirrors", string(mirrorsJSON))
	}
	st.SetMeta("last_health_error", "")
	defer func() {
		st.SetMeta("instance_pid", "")
		st.SetMeta("instance_exe", "")
		st.SetMeta("instance_started", "")
		st.SetMeta("instance_mode", "")
		st.SetMeta("instance_user", "")
		st.SetMeta("instance_config_fingerprint", "")
		st.SetMeta("instance_mirrors", "")
	}()

	// FINDING 16 closure: install-event submit pipeline. Fires
	// first_seen + upgrade events asynchronously at startup so the
	// daemon doesn't block on network I/O. Single-instance lock
	// (acquired above) guarantees no concurrent submission. Tier
	// gate + buildKey gate inside SendInstallEventsIfDue mean -dev
	// builds and None-tier installs are silent no-ops.
	//
	// Detached goroutine, but bounded by a 30s context. Daemon
	// shutdown won't wait for this; the goroutine cancels via the
	// context's deadline in the worst case.
	go fireInstallEventsAtStartup(cfg, st)

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

	// SEC-M5: register all per-mirror local_path prefixes so
	// anomaly.SanitizePath redacts them in addition to the user home
	// dir. Without this, in service mode (LocalSystem, home =
	// C:\Windows\System32) project paths outside the user-home tree
	// (e.g. C:\Projects\MyApp, D:\Work) would leak into anomaly logs
	// and webhook payloads unredacted.
	{
		paths := make([]string, 0, len(cfg.Projects))
		for _, p := range cfg.Projects {
			paths = append(paths, p.LocalPath)
		}
		anomaly.SetExtraSanitizePrefixes(paths)
	}

	// Create sync engine (with metrics, anomaly recorder, and hooks)
	syncEngine := msync.NewEngine(cfg, st, filters, m)
	syncEngine.Anomaly = anomalyRecorder
	// v1.0.37 Symlink-asymmetry close-out: foreground mode now aligns
	// with service mode's SEC-H5 / PF-A3 default-reject behavior. Pre-
	// v1.0.37 foreground followed symlinks (RejectSymlinkedFiles default
	// false); now defaults to reject unless cfg.AllowSymlinks=true.
	syncEngine.RejectSymlinkedFiles = !cfg.AllowSymlinks
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

	// Wire queue overflow → anomaly
	syncEngine.Queue.SetOnOverflow(func() {
		anomalyRecorder.Record(anomaly.KindQueueDepthWarning, "", "",
			"queue depth exceeds 50,000 items",
			"possible event storm or stalled workers; check 'smirror status'")
	})

	// Wire webhook alerting (incident-based)
	var webhookSender *notify.WebhookSender
	if cfg.AlertWebhookURL != "" {
		if anomalyRecorder == nil {
			slog.Warn("alert_webhook_url is set but anomaly_detection is disabled; webhook will receive no events")
		} else {
			webhookSender = notify.NewWebhookSender(cfg.AlertWebhookURL)
			webhookSender.SanitizePath = anomaly.SanitizePath
			minSev := cfg.AlertMinSeverity
			if minSev == "" {
				minSev = "error"
			}
			anomalyRecorder.SetOnRecord(func(a *anomaly.Anomaly) {
				if severityAtLeast(string(a.Severity), minSev) {
					webhookSender.Record(string(a.Kind), string(a.Severity), a.Project, a.Path, a.Message, a.Detail)
				}
			})
		}
	}

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
	go heartbeatLoop(ctx, st, cfg, m, watchMgr, syncEngine, filters, notifier, webhookSender)

	slog.Info("smirror running", "mirrors", cfg.ProjectNames())
	if !service.IsWindowsService() {
		fmt.Println("Press Ctrl+C to stop")
	}

	// Block until context is cancelled
	<-ctx.Done()
	slog.Info("smirror stopped")
}

func cmdSyncNow(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror sync-now [mirror]

Run an immediate full sync for all mirrors (or a specific mirror).
Also performs ghost cleanup (removes orphaned remote files).

If the service is running, signals it to sync instead of starting
a second instance.

Aliases: syncnow

Examples:
  smirror sync-now              Sync all mirrors
  smirror sync-now MyProject    Sync only the "MyProject" mirror`) {
		return
	}
	rejectUnknownFlags("sync-now", args)
	checkMaxArgs("sync-now", args, 1)

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
	if rec := wireAnomalyRecorder(syncEngine, cfg); rec != nil {
		defer rec.Close()
	}

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

	// SM-109: Use SyncFullProject directly (not queue) so errors propagate.
	syncFailed := false
	for _, proj := range projects {
		if err := syncEngine.SyncFullProject(ctx, proj); err != nil {
			// # surface a concrete next-step instead of a bare error.
			fmt.Fprintf(os.Stderr, "Sync failed for %s: %v\n  Try: smirror test-mirrors %s\n",
				proj.Name, err, proj.Name)
			syncFailed = true
		}
	}

	// Clean up ghost files (LEAKs + ORPHANs) on remote
	totalCleaned := 0
	ghostFailed := false // # ghost-cleanup error must change the exit code
	for _, proj := range projects {
		cleaned, err := syncEngine.CleanupGhosts(ctx, proj)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Ghost cleanup error for %s: %v\n", proj.Name, err)
			ghostFailed = true
			continue
		}
		if cleaned > 0 {
			fmt.Printf("Cleaned %d ghost file(s) from %s\n", cleaned, proj.Name)
			totalCleaned += cleaned
		}
	}

	if syncFailed {
		fmt.Fprintln(os.Stderr, "Sync completed with errors.")
		os.Exit(ExitError)
	}
	if ghostFailed {
		// # previously logged-and-continued silently. Sync-now's
		// contract includes ghost cleanup, so a partial failure must
		// surface as a non-zero exit. ExitDrift is the natural code:
		// "diagnostic found drift" — ghost-cleanup failures leave drift
		// on the remote.
		fmt.Fprintln(os.Stderr, "Sync complete but ghost cleanup had errors.")
		os.Exit(ExitDrift)
	}
	if totalCleaned > 0 {
		fmt.Printf("Sync complete (%d ghost files cleaned)\n", totalCleaned)
	} else {
		fmt.Println("Sync complete")
	}
}

func cmdDryRun(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror dry-run [mirror]

Show what would be synced without actually transferring files.
Also shows a ghost cleanup preview (orphaned remote files).

Examples:
  smirror dry-run              Preview all mirrors
  smirror dry-run MyProject    Preview only "MyProject"`) {
		return
	}
	rejectUnknownFlags("dry-run", args)
	checkMaxArgs("dry-run", args, 1)

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

	// SM-112/115: Track failures and exit non-zero if any mirror fails.
	hadError := false
	for _, proj := range projects {
		if err := syncEngine.DryRun(ctx, proj); err != nil {
			fmt.Fprintf(os.Stderr, "Dry run error for %s: %v\n", proj.Name, err)
			hadError = true
		}
		fmt.Println()
	}

	// Show what ghost files would be cleaned
	fmt.Println("=== Ghost cleanup preview ===")
	for _, proj := range projects {
		fmt.Printf("\n%s:\n", proj.Name)
		if _, err := syncEngine.DryRunCleanup(ctx, proj); err != nil {
			fmt.Fprintf(os.Stderr, "Ghost scan error for %s: %v\n", proj.Name, err)
			hadError = true
		}
	}

	if hadError {
		os.Exit(ExitError)
	}
}

// statusEmitSanitized reads status.json and writes a redacted form
// to stdout. SEC-M-4: sharing-time sanitizer for the
// status payload. Per validation-session recommendation D
// (system-validation/MEMO-TO-IMPL-2026-04-29.md):
//
//   - The raw status.json on disk is preserved as-is — local debug
//     readability is the contract there (matches the log-file
//     local-only-raw convention).
//   - This entry point pipes the same payload through the shared
//     telemetry sanitizer (the same one used by report-bug) and
//     emits the redacted JSON to stdout. Users can pipe directly:
//     `smirror status --sanitize > status.sanitized.json` or
//     `smirror status --sanitize | clip` to share.
//
// The redactor handles: HomeDir, ConfigDir, per-mirror local_paths,
// per-mirror names, credential-style key=value pairs, rclone-style
// remote URIs. Same sanitization scope as report-bug --stdout.
//
// On config load failure, falls back to a best-effort sanitization
// using only HomeDir + ConfigDir (the report-bug fallback path).
func statusEmitSanitized(configPath string) {
	cfg, _ := config.Load(configPath)

	// Both branches below assign statusPath; the prior `:= configPath`
	// initializer was dead (ineffassign).
	var statusPath string
	if cfg != nil {
		statusPath = filepath.Join(dataDir(cfg), "status.json")
	} else {
		// Best-effort: assume status.json sits beside the config file
		// (default layout). If not, the read fails below and we exit.
		statusPath = filepath.Join(filepath.Dir(configPath), "status.json")
	}

	data, err := os.ReadFile(statusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot read %s: %v\n", statusPath, err)
		os.Exit(ExitError)
	}

	sanOpts := telemetry.SanitizeOptions{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		sanOpts.HomeDir = home
	}
	if configDir := filepath.Dir(configPath); configDir != "" && configDir != "." {
		sanOpts.ConfigDir = configDir
	}
	if cfg != nil {
		sanOpts.MirrorPaths = make([]string, len(cfg.Projects))
		sanOpts.MirrorNames = make([]string, len(cfg.Projects))
		for i, p := range cfg.Projects {
			sanOpts.MirrorPaths[i] = p.LocalPath
			sanOpts.MirrorNames[i] = p.Name
		}
	}

	sanitized := telemetry.SanitizeReport(string(data), sanOpts)
	fmt.Print(sanitized)
	if !strings.HasSuffix(sanitized, "\n") {
		fmt.Println()
	}
}

// cmdStatus is a top-level CLI command that emits a multi-section status
// report (service state, metrics, per-mirror sync state, ghosts, anomalies).
// It is sequential reporting code — each section is its own conditional block
// driven by config / state availability. Refactoring into per-section helpers
// is tracked as a v1.0.x cleanup; cyclomatic complexity is intrinsic to the
// reporting surface, not branch logic.
//nolint:gocyclo // sequential reporting function; per-section split is a v1.0.x cleanup
func cmdStatus(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror status [mirror] [--sanitize]

Show sync status, metrics, and instance state.

Displays: service state, live/last-known metrics (files synced, bytes
uploaded, errors, latency), per-mirror sync state, ghost scan results,
and recent anomalies.

Flags:
  --sanitize    Print the contents of status.json with paths, mirror
                names, and credential-style values redacted. Use this
                form when sharing diagnostic output (bug reports,
                support requests) — the bare 'smirror status' keeps
                raw paths in service of local debugging readability.
                Aliases: --for-sharing.

Examples:
  smirror status              Show full status (raw, local-debug form)
  smirror status MyProject    Show status for one mirror only
  smirror status --sanitize   Emit redacted status.json to stdout`) {
		return
	}

	// SEC-M-4: extract --sanitize / --for-sharing flag
	// before passing args through rejectUnknownFlags. Sharing-time
	// sanitizer recommended by the validation session as the
	// resolution to the SEC-L4 (raw status.json for local debug)
	// vs. SEC-M-4 (uniform sanitization) tension.
	sanitizeFlag := false
	{
		filtered := args[:0:len(args)]
		for _, a := range args {
			if a == "--sanitize" || a == "--for-sharing" {
				sanitizeFlag = true
				continue
			}
			filtered = append(filtered, a)
		}
		args = filtered
	}
	rejectUnknownFlags("status", args)
	checkMaxArgs("status", args, 1)

	if sanitizeFlag {
		statusEmitSanitized(configPath)
		return
	}

	// Non-blocking update check (rate-limited to once/24h)
	go checkForUpdateOnStartup(configPath)

	fmt.Printf("SelectiveMirror Status\n")
	fmt.Printf("======================\n\n")

	// Show service state (works without config — queries SCM directly).
	svcInstalled, svcRunning := service.IsRunning()
	if svcInstalled {
		if svcRunning {
			fmt.Println("Service: running")
		} else {
			fmt.Println("Service: stopped")
		}
	} else {
		fmt.Println("Service: not installed")
	}

	cfg, cfgErr := config.Load(configPath)
	if cfgErr != nil {
		// Distinguish file-not-found from validation errors (SM-135).
		if _, statErr := os.Stat(configPath); statErr != nil {
			fmt.Printf("Config: %s (not found)\n", configPath)
			fmt.Fprintln(os.Stderr, "\nCreate a config file to see full status.")
		} else {
			fmt.Printf("Config: %s (invalid)\n", configPath)
			fmt.Fprintf(os.Stderr, "\nError: %v\n", cfgErr)
		}
		os.Exit(ExitConfigError)
	}

	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	fmt.Printf("Config: %s\n", configPath)
	fmt.Printf("State DB: %s\n\n", cfg.StateDB)

	// SM-124: Check lock early so we can label metrics correctly.
	instanceRunning, _ := lock.IsLocked(dataDir(cfg))

	// Show metrics if status.json exists
	statusPath := filepath.Join(dataDir(cfg), "status.json")
	if data, err := os.ReadFile(statusPath); err == nil {
		var s metrics.Status
		if json.Unmarshal(data, &s) == nil {
			if instanceRunning {
				fmt.Printf("Live Metrics (from running instance):\n")
			} else {
				fmt.Printf("Last Known Metrics (instance not running):\n")
			}
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

	// Check instance status (reuse instanceRunning from above)
	configMismatch := false
	if instanceRunning {
		iPid, _ := st.GetMeta("instance_pid")
		iExe, _ := st.GetMeta("instance_exe")
		iMode, _ := st.GetMeta("instance_mode")
		iVersion, _ := st.GetMeta("instance_version")
		iUser, _ := st.GetMeta("instance_user")
		iStarted, _ := st.GetMeta("instance_started")

		// Build status line: "smirror.exe service running as SYSTEM: (PID 1234) C:\...\smirror.exe"
		modeStr := "instance"
		if iMode != "" {
			modeStr = iMode
		}
		versionStr := ""
		if iVersion != "" {
			versionStr = " v" + iVersion
		}
		parts := []string{fmt.Sprintf("smirror.exe %s running%s", modeStr, versionStr)}
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

		// SM-122: Warn if on-disk config has changed since the running instance started.
		iFingerprint, _ := st.GetMeta("instance_config_fingerprint")
		configMismatch = iFingerprint != "" && iFingerprint != cfg.MirrorFingerprint()
		if configMismatch {
			fmt.Printf("  WARNING: config.yaml has changed since this instance started.\n")
			fmt.Printf("  Mirror details below are from the config file on disk,\n")
			fmt.Printf("  NOT from the running instance. Restart to apply changes.\n")
		}

		fmt.Println()
	} else if !svcInstalled {
		// No service and no foreground instance — nothing running
		fmt.Printf("Not running\n\n")
	} else {
		fmt.Println() // service state already shown above
	}

	// SM-122: When config has changed since the running instance started, use
	// the instance's stored mirror config (from state DB) for display, not the
	// potentially-changed config file on disk.
	displayProjects := cfg.Projects
	if configMismatch {
		if stored, sErr := st.GetMeta("instance_mirrors"); sErr == nil && stored != "" {
			var runningMirrors []config.Project
			if json.Unmarshal([]byte(stored), &runningMirrors) == nil && len(runningMirrors) > 0 {
				displayProjects = runningMirrors
			}
		}
	}

	filters := buildFilters(cfg)

	projects := displayProjects
	if len(args) > 0 {
		// Look up by name in the display projects
		var found *config.Project
		for i := range projects {
			if projects[i].Name == args[0] {
				found = &projects[i]
				break
			}
		}
		if found == nil {
			// Fall back to disk config for name lookup
			proj := cfg.FindProject(args[0])
			if proj == nil {
				var names []string
				for _, p := range displayProjects {
					names = append(names, p.Name)
				}
				fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(names, ", "))
				os.Exit(ExitConfigError)
			}
			found = proj
		}
		projects = []config.Project{*found}
	}

	for _, proj := range projects {
		lastSync, _ := st.GetLastSyncTime(proj.Name)
		pending, _ := st.GetPendingFiles(proj.Name)
		synced, _ := st.GetAllSyncedPaths(proj.Name)

		if configMismatch {
			fmt.Printf("Mirror: %s (from running instance)\n", proj.Name)
		} else {
			fmt.Printf("Mirror: %s\n", proj.Name)
		}
		fmt.Printf("  Path:    %s\n", proj.LocalPath)
		fmt.Printf("  Remote:  %s\n", proj.Remote)

		// Count local files and filter status
		if fe, ok := filters[proj.Name]; ok {
			total, excluded := countFilteredFiles(proj.LocalPath, fe)
			syncable := total - excluded
			fmt.Printf("  Local files: %d  Excluded: %d  Syncable: %d\n", total, excluded, syncable)
		}

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

	// Recent anomalies — only from current service session
	iStarted, _ := st.GetMeta("instance_started")
	var startedAfter time.Time
	if iStarted != "" {
		startedAfter, _ = time.Parse(time.RFC3339, iStarted)
	}

	recent, err := anomaly.ReadRecent(dataDir(cfg), 100)
	if err == nil && len(recent) > 0 {
		var current, older int
		for _, a := range recent {
			aTime, _ := time.Parse(time.RFC3339, a.Time)
			if !startedAfter.IsZero() && aTime.Before(startedAfter) {
				older++
				continue
			}
			if current == 0 {
				fmt.Println("Anomalies (this session):")
			}
			proj := a.Project
			if proj == "" {
				proj = "-"
			}
			ts := aTime.Local().Format("15:04:05")
			fmt.Printf("  [%-8s] %s %-25s %-15s %s\n", a.Severity, ts, a.Kind, proj, a.Message)
			current++
		}
		if current == 0 {
			fmt.Println("Anomalies: none this session")
		}
		if older > 0 {
			anomalyDir := filepath.Join(dataDir(cfg), "anomalies")
			fmt.Printf("  (%d anomalies from previous sessions — review or delete: %s)\n", older, anomalyDir)
		}
		fmt.Println()
	}
}

// countFilteredFiles walks a directory and counts total files vs excluded
// files. Returns (total, excluded). Directories themselves are not counted.
//
// Adversarial review #17: previously every excluded directory triggered a
// SECOND full walk to count files inside. For a 50k-file .git/ that's 50k
// extra os.DirEntry traversals per `smirror status`. Now we walk excluded
// subtrees in the same pass, just attributing every file we see to
// `excluded`. SkipDir is no longer used; one pass total. Net effect: same
// (total, excluded) numbers, half the syscalls when the project has large
// excluded subtrees.
func countFilteredFiles(root string, fe *filter.Engine) (total, excluded int) {
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		if path == root || d.IsDir() {
			return nil // directories are not counted; we walk into them
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		total++
		if fe.IsExcluded(rel) {
			excluded++
		}
		return nil
	})
	return total, excluded
}

// cmdTestMirrors runs all diagnostics (local + remote) and verifies sync state.
// Optional argument: mirror name to test only that mirror.
// Aliases: doctor, verify (kept for backward compatibility).
func cmdTestMirrors(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror test-mirrors [mirror]

Run diagnostics and verify sync state for all mirrors (or one mirror).
Checks: config validity, rclone connectivity, remote reachability,
local path accessibility, filter compilation, and file integrity.

Aliases: doctor, verify

Examples:
  smirror test-mirrors              Test all mirrors
  smirror test-mirrors MyProject    Test one mirror
  smirror doctor                    Same as test-mirrors`) {
		return
	}
	rejectUnknownFlags("test-mirrors", args)
	checkMaxArgs("test-mirrors", args, 1)

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
		db, err := sql.Open("sqlite3", cfg.StateDB)
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

	// SM-116: Show hint when drift exists. When there are also failures,
	// qualify the hint — some drift may still be resolvable even if some remotes are down.
	if totalDrift > 0 {
		if failed > 0 {
			fmt.Println("Hint: 'smirror sync-now' may resolve drift on reachable mirrors.")
		} else {
			fmt.Println("Hint: 'smirror sync-now' may resolve drift.")
		}
	}

	if failed > 0 {
		os.Exit(ExitRcloneError)
	}
	if totalDrift > 0 {
		os.Exit(ExitDrift)
	}
}

func cmdListFilters(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror list-filters [mirror]

Show effective filter rules for all mirrors (or one mirror).
Displays global excludes and per-project .syncignore rules.

Examples:
  smirror list-filters              Show all mirrors' filters
  smirror list-filters MyProject    Show one mirror's filters`) {
		return
	}
	rejectUnknownFlags("list-filters", args)
	checkMaxArgs("list-filters", args, 1)

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
	if subcommandHelp(args, `Usage: smirror explain <mirror> <relative-path>

Explain why a file is included or excluded from sync.
Shows: filter status, matched rule, remote path, local file info
(size, modified, MD5), max_file_size_mb check, and sync state.

Examples:
  smirror explain MyProject README.md
  smirror explain MyProject src/main.go`) {
		return
	}
	rejectUnknownFlags("explain", args)
	checkMaxArgs("explain", args, 2)

	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: smirror explain <mirror> <relative-path>")
		fmt.Fprintln(os.Stderr, "Example: smirror explain MyProject README.md")
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

	// Detect whether the path is a directory (locally or by trailing slash)
	localPath := filepath.Join(proj.LocalPath, filepath.FromSlash(relPath))
	info, statErr := os.Stat(localPath)
	isDir := (statErr == nil && info.IsDir()) || strings.HasSuffix(relPath, "/")

	// For filter checking, directories need a trailing slash
	filterPath := relPath
	if isDir && !strings.HasSuffix(filterPath, "/") {
		filterPath = filterPath + "/"
	}

	excluded := fe.IsExcluded(filterPath)
	matchedRule := ""

	fmt.Printf("=== Explain: %s / %s ===\n\n", projName, relPath)

	// Detect filesystem object type
	if statErr == nil {
		linfo, lerr := os.Lstat(localPath)
		if lerr == nil {
			mode := linfo.Mode()
			switch {
			case mode&os.ModeSymlink != 0:
				target, _ := os.Readlink(localPath)
				if isDir {
					fmt.Printf("Type: SYMLINK (directory) -> %s\n", target)
				} else {
					fmt.Printf("Type: SYMLINK (file) -> %s\n", target)
				}
			case isDir:
				// Check for Windows reparse points (junctions, mount points)
				if mode&os.ModeIrregular != 0 {
					fmt.Printf("Type: JUNCTION/REPARSE POINT\n")
				} else {
					fmt.Printf("Type: DIRECTORY\n")
				}
			case mode&os.ModeNamedPipe != 0:
				fmt.Printf("Type: NAMED PIPE (cannot sync)\n")
			case mode&os.ModeSocket != 0:
				fmt.Printf("Type: SOCKET (cannot sync)\n")
			case mode&os.ModeDevice != 0:
				fmt.Printf("Type: DEVICE (cannot sync)\n")
			case mode&os.ModeCharDevice != 0:
				fmt.Printf("Type: CHAR DEVICE (cannot sync)\n")
			case mode&os.ModeIrregular != 0:
				fmt.Printf("Type: IRREGULAR FILE (may not sync correctly)\n")
			case mode.IsRegular():
				// normal file — don't print type, it's the default
			default:
				fmt.Printf("Type: UNKNOWN (%s)\n", mode)
			}
		}
	} else if isDir {
		fmt.Printf("Type: DIRECTORY\n")
	}

	if excluded {
		fmt.Printf("Status: EXCLUDED\n")
		matchedRule = findMatchingRule(fe, filterPath)
		if matchedRule != "" {
			fmt.Printf("Matched rule: %s\n", matchedRule)
		}
	} else {
		fmt.Printf("Status: INCLUDED\n")
	}

	// Remote path
	remotePath := proj.Remote + "/" + relPath
	fmt.Printf("Remote path: %s\n", remotePath)

	// Local info
	if statErr != nil {
		fmt.Printf("Local path: does not exist (%v)\n", statErr)
	} else if isDir {
		fmt.Printf("Local path: %s\n", localPath)
		fmt.Printf("  Modified: %s\n", info.ModTime().Local().Format(time.RFC3339))

		// Count contents
		entries, err := os.ReadDir(localPath)
		if err == nil {
			files, dirs := 0, 0
			for _, e := range entries {
				if e.IsDir() {
					dirs++
				} else {
					files++
				}
			}
			fmt.Printf("  Contents: %d files, %d subdirs\n", files, dirs)
		}

		// Check if any children are synced
		st, err := state.Open(cfg.StateDB)
		if err == nil {
			defer st.Close()
			syncedFiles, _ := st.GetFilesUnderDir(proj.Name, relPath)
			if len(syncedFiles) > 0 {
				fmt.Printf("  Synced children: %d files tracked in state DB\n", len(syncedFiles))
			} else {
				fmt.Printf("  Synced children: none\n")
			}
		}
	} else {
		fmt.Printf("Local file: %s\n", localPath)
		fmt.Printf("  Size: %d bytes\n", info.Size())
		fmt.Printf("  Modified: %s\n", info.ModTime().Local().Format(time.RFC3339))

		if info.Size() > proj.MaxFileSize() {
			fmt.Printf("  WARNING: exceeds max file size (%dMB)\n", proj.MaxFileSizeMB)
		}

		hash, _, err := hashFile(localPath)
		if err == nil {
			fmt.Printf("  MD5: %s\n", hash)
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

				// SM-083: Show remote verification status
				if fs.IsRemoteVerified() {
					fmt.Printf("  Remote verified: %s", fs.RemoteVerifiedAt.Local().Format(time.RFC3339))
					if fs.RemoteHash == fs.LocalHash {
						fmt.Printf(" (hash match)")
					} else if fs.RemoteHash != "" {
						fmt.Printf(" (hash STALE: local=%s remote=%s)", fs.LocalHash[:8], fs.RemoteHash[:8])
					}
					fmt.Println()

					// Check if file changed locally after last verification
					if fs.MtimeNs > 0 {
						localMtime := time.Unix(0, fs.MtimeNs)
						if localMtime.After(fs.RemoteVerifiedAt) {
							fmt.Printf("  WARNING: local file modified AFTER last remote verification\n")
						}
					}
				} else {
					fmt.Printf("  Remote verified: not yet (sync attempt only, use 'smirror test-mirrors' to verify)\n")
				}
			} else {
				fmt.Printf("\nSync state: no per-file record in state DB\n")
				if !excluded {
					fmt.Printf("  (file may have been synced by batch reconciliation, which does not record per-file state)\n")
				}
			}
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
			} else if err == nil {
				// SM-083: Hash matches — record remote verification
				if st != nil {
					_ = st.UpdateRemoteVerification(proj.Name, relPath, strings.ToLower(md5Hash), rf.Size)
				}
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
			} else if err == nil {
				// SM-083: Hash matches — record remote verification
				if st != nil {
					_ = st.UpdateRemoteVerification(proj.Name, relPath, strings.ToLower(md5Hash), rf.Size)
				}
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

// openBrowserURL opens a URL in the default browser.
// Uses rundll32 on Windows to avoid cmd.exe interpreting & as command separator.
func openBrowserURL(rawURL string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
}

// readReportBugTier reads the user's telemetry tier the same way the
// (deferred) submit flow will: state DB first, registry fallback,
// fail-closed to TierNone on read errors. Used by report-bug --submit
// to distinguish the None / Standard / Reliability code paths. SM-158.
func readReportBugTier(configPath string) telemetry.Tier {
	cfg, err := config.Load(configPath)
	if err != nil {
		return telemetry.ReadTier(nil)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		return telemetry.ReadTier(nil)
	}
	defer st.Close()
	return telemetry.ReadTier(st)
}

func cmdReportBug(configPath string, args []string) {
	toStdout := false
	openBrowser := false
	clipboardFlag := false
	submitFlag := false
	oneShot := false
	for _, a := range args {
		switch a {
		case "--help", "-h":
			fmt.Println(`Usage: smirror report-bug [flags]

Generate a diagnostic report for bug filing.

Flags:
  --stdout      Print report to stdout instead of saving to file
  --browser     Open a pre-filled GitHub issue in the browser after
                generating the report
  --open        Deprecated alias for --browser; will be removed in a
                future release
  --clipboard   Copy the sanitized report to the OS clipboard. You
                paste manually into a fresh issue — the diagnostic
                content never goes through a URL query string and so
                doesn't end up in browser history. Recommended when
                privacy matters more than convenience.
  --submit      Submit the sanitized report through the telemetry
                bug-report endpoint (per-event approval; requires
                Standard or Reliability tier — or pair with --one-shot)
  --one-shot    Allow a single bug-report submission while remaining
                on tier None: per-event consent, no ongoing telemetry

The report includes: version, platform, rclone info, config summary
(sanitized), state DB stats, and recent log lines. All paths are
sanitized, remote paths are redacted, and credential-style values
(token=, password=, bearer …) are stripped before output.

See "smirror telemetry policy" or docs/PRIVACY.md for the contract
covering each submit / browser / one-shot path.`)
			return
		case "--stdout":
			toStdout = true
		case "--browser":
			openBrowser = true
		case "--open":
			// SM-158 transition: --open is the legacy spelling. We
			// continue to honor it but the help text marks it
			// deprecated. New code/docs should use --browser.
			openBrowser = true
		case "--clipboard":
			// PF-E5: avoid the URL-history leak from --browser by
			// piping the report to the OS clipboard instead. User
			// pastes manually into a fresh issue.
			clipboardFlag = true
		case "--submit":
			submitFlag = true
		case "--one-shot":
			oneShot = true
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "unknown flag: %s\nRun 'smirror report-bug --help' for usage.\n", a)
				os.Exit(ExitError)
			}
		}
	}

	// SM-158 — pre-decision for --submit. We figure out whether the
	// submit pipeline should run, and under what tier label, BEFORE
	// the bundle is built. The actual contribute() call happens later
	// (after sanitization) so the classifier sees the same bytes the
	// user sees. Three outcomes here:
	//
	//   submitDecision == "submit"    → run telemetry.Contribute after sanitize
	//   submitDecision == "save_only" → do not submit; still file-write/print
	//   submitDecision == "cancel"    → return (user declined entirely)
	//
	// On non-interactive None tier without --one-shot, we exit 1 with
	// a hint rather than spinning a prompt that nothing will answer.
	submitDecision := "no_submit"
	currentTier := telemetry.Tier("none")
	if submitFlag {
		currentTier = readReportBugTier(configPath)
		switch {
		case currentTier == telemetry.TierNone && !oneShot && !isInteractive():
			fmt.Fprintln(os.Stderr,
				"bug submission requires telemetry tier 'standard' or 'reliability', "+
					"or pass --one-shot for per-event consent.")
			fmt.Fprintln(os.Stderr, "View options:  smirror telemetry policy")
			os.Exit(ExitError)
		case currentTier == telemetry.TierNone && !oneShot && isInteractive():
			// The actual prompt runs AFTER the bundle is built (so the
			// [v] view-bundle option has something to show). Mark the
			// decision as deferred for now.
			submitDecision = "prompt"
		default:
			// Standard / Reliability tiers, or any tier with --one-shot.
			submitDecision = "submit"
		}
	}

	var b strings.Builder
	tz := time.Now().Format("-07:00")
	now := time.Now().Format("2006-01-02T15:04:05") + tz

	b.WriteString(fmt.Sprintf("smirror bug report — generated %s\n", now))
	b.WriteString(fmt.Sprintf("smirror version: %s\n", version))
	b.WriteString(fmt.Sprintf("platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	b.WriteString(fmt.Sprintf("go version: %s\n", runtime.Version()))

	// Load config up front so we can pass rclone_path to Detect. A broken
	// config is tolerated — fall back to Detect("") and report the config
	// error in its own section below.
	cfg, cfgErr := config.Load(configPath)
	var configuredRclonePath string
	if raw := loadConfigBestEffort(configPath); raw != nil {
		configuredRclonePath = raw.RclonePath
	}

	// rclone info
	rcloneInfo, err := rclone.Detect(configuredRclonePath)
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
	if cfgErr != nil {
		b.WriteString(fmt.Sprintf("config error: %v\n", cfgErr))
	} else {
		b.WriteString(fmt.Sprintf("config path: %s\n", configPath))
		b.WriteString(fmt.Sprintf("mirrors: %d\n", len(cfg.Projects)))
		b.WriteString(fmt.Sprintf("delete_policy: %s\n", cfg.DeletePolicy()))
		b.WriteString(fmt.Sprintf("sync_workers: %d\n", cfg.Workers()))
		b.WriteString(fmt.Sprintf("reconcile_interval: %s\n", cfg.ReconcileInterval()))
		// SM-164: bandwidth_limit value omitted (the limit STRING itself
		// is config the user might consider sensitive: revealing exact
		// upload caps in a public bug report. Show only whether one is
		// set; the policy gate flag in PRIVACY.md ("has_bandwidth_limit
		// (boolean — never the value)") aligns this with the structural
		// install-event field).
		hasBW := strings.TrimSpace(cfg.BandwidthLimit) != ""
		b.WriteString(fmt.Sprintf("bandwidth_limit_set: %v\n", hasBW))
		// SM-164: mirror names are user-chosen and may identify the
		// user's context ("Acme_Internal", "ClientNameProject"). Index-
		// based labels keep the report useful for cross-referencing
		// without leaking the names. The local path is omitted entirely
		// — even with home-dir sanitization, absolute Windows paths
		// outside the home directory bypass redaction.
		for i, p := range cfg.Projects {
			b.WriteString(fmt.Sprintf("  mirror_%d:\n", i))
			b.WriteString(fmt.Sprintf("    delete_policy: %s\n", p.DeletePolicy(cfg)))
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
			for i, p := range cfg.Projects {
				count := st.CountFiles(p.Name)
				b.WriteString(fmt.Sprintf("  mirror_%d: %d synced files\n", i, count))
			}
			if hb, err := st.GetMeta("last_heartbeat"); err == nil && hb != "" {
				b.WriteString(fmt.Sprintf("  last heartbeat: %s\n", hb))
			}
		}

		// SM-164: the previous "--- Live Metrics ---" section
		// (uptime, files_synced, bytes_uploaded, sync_errors,
		// avg_latency_ms, queue_depth, generated_at) was removed. Those
		// numerics overlap exactly with the "no accumulated counts"
		// forbidden-list in docs/PRIVACY.md. report-bug --open posts
		// the report to GitHub, where it's then public, so even with
		// per-event consent these counters become part of the public
		// record. If a maintainer needs them to triage a specific bug,
		// the user can attach `smirror status` output manually after
		// reviewing it.

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
			for _, line := range lines[start:] {
				b.WriteString(line + "\n")
			}
		}
	}

	// SM-103 + SM-164 + SM-171: route the report through the shared
	// telemetry sanitizer (internal/telemetry/sanitize.go). It covers
	// path prefixes, mirror names, mirror local paths, credential-style
	// key=value pairs, rclone-style remote URIs, and trailing-path
	// redaction. Both the bug-report and crash-report paths use this.
	report := b.String()
	sanOpts := telemetry.SanitizeOptions{}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		sanOpts.HomeDir = home
	}
	if configDir := filepath.Dir(configPath); configDir != "" && configDir != "." {
		sanOpts.ConfigDir = configDir
	}
	if cfg != nil {
		sanOpts.MirrorPaths = make([]string, len(cfg.Projects))
		sanOpts.MirrorNames = make([]string, len(cfg.Projects))
		for i, p := range cfg.Projects {
			sanOpts.MirrorPaths[i] = p.LocalPath
			sanOpts.MirrorNames[i] = p.Name
		}
	}
	report = telemetry.SanitizeReport(report, sanOpts)

	// SM-158 — submit pipeline. Runs BEFORE the output branches so the
	// telemetry contribution lands regardless of how the bundle is
	// presented (stdout / clipboard / browser / file). The always-print
	// URL rule (docs/SM-158-report-bug-submit-plan.md, 2026-05-02) is
	// satisfied by printSubmitOutcome, which writes the GitHub-issue
	// URL to stderr after a contribute() success or failure.
	if submitDecision == "prompt" {
		// None tier + interactive + !oneShot. Now that the bundle is
		// built, the user can [v]iew it before deciding.
		choice := stuckUserPrompt(report)
		switch choice {
		case "one_shot":
			oneShot = true
			submitDecision = "submit"
		case "upgrade_to_standard":
			if err := upgradeToStandardForSubmit(configPath); err != nil {
				fmt.Fprintf(os.Stderr, "Could not change tier to Standard: %v\n", err)
				fmt.Fprintln(os.Stderr, "Falling back to one-shot submission for this report only.")
				oneShot = true
			} else {
				currentTier = telemetry.TierStandard
				fmt.Fprintln(os.Stderr, "Telemetry tier set to Standard.")
			}
			submitDecision = "submit"
		case "save_only":
			submitDecision = "save_only"
		case "cancel":
			fmt.Fprintln(os.Stderr, "Cancelled.")
			return
		}
	}
	if submitDecision == "submit" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cls, submitErr := submitBugReport(ctx, report, currentTier, oneShot)
		cancel()
		printSubmitOutcome(cls, currentTier, oneShot, prefilledIssueURL(report), submitErr)
	}

	if toStdout {
		fmt.Print(report)
		return
	}

	// PF-E5: --clipboard alternative to --browser. Browser-based submit
	// puts the diagnostic in a URL query string; URL is then retained in
	// browser history (which sync's to other devices, can be exfiltrated
	// by extensions, etc.). --clipboard pipes the sanitized report into
	// the OS clipboard (clip.exe on Windows, pbcopy on macOS, xclip /
	// wl-copy on Linux). The user pastes manually into a fresh issue —
	// the diagnostic never touches the URL bar.
	if clipboardFlag {
		if err := copyToClipboard(report); err != nil {
			fmt.Fprintf(os.Stderr, "Could not copy to clipboard: %v\nReport content follows; paste manually:\n\n%s\n", err, report)
			fmt.Fprintf(os.Stderr, "Open a new issue at: %s\n", issueNewURL)
			os.Exit(ExitError)
		}
		fmt.Printf("Sanitized bug report copied to clipboard (%d bytes).\n", len(report))
		fmt.Printf("Open a new issue and paste into the body: %s\n", issueNewURL)
		return
	}

	if openBrowser {
		fmt.Print(report)

		// Search for similar existing issues before submitting
		fmt.Println("\nSearching for similar issues...")
		searchCtx, searchCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer searchCancel()
		if issues, err := searchSimilarIssues(searchCtx, report); err == nil && len(issues) > 0 {
			choice := displayDupResults(issues)
			if choice > 0 {
				// User chose to view an existing issue
				_ = openBrowserURL(issues[choice-1].HTMLURL)
				return
			}
			// choice == 0: user wants to submit new report
		} else if err != nil {
			// Silently skip — don't block submission on search failure
		} else {
			fmt.Println("  No similar issues found.")
		}

		fmt.Println("\n--- Opening browser ---")
		_ = openBrowserURL(prefilledIssueURL(report))
		return
	}

	// Write to file. Output goes under the user data dir (the directory
	// containing config.yaml) — never the current working directory. If the
	// caller invokes report-bug from inside a watched mirror, writing the
	// report into the CWD would round-trip the (sanitized) report up to the
	// configured remote, which is surprising for the user. The user data
	// dir is generally not itself watched.
	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("smirror-bug-report-%s.txt", ts)
	reportDir := bugReportDir(configPath)
	if err := os.MkdirAll(reportDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to prepare report directory %s: %v\n", reportDir, err)
		os.Exit(1)
	}
	fullPath := filepath.Join(reportDir, filename)
	if err := os.WriteFile(fullPath, []byte(report), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write report: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Bug report written to: %s\n", fullPath)
	fmt.Printf("Paste into a GitHub issue at: %s\n", issueNewURL)
}

// bugReportDir returns the directory in which `smirror report-bug` writes
// its output file. Preference order:
//  1. <configdir>/reports — the user data dir, alongside state.db etc.
//  2. ~/.selectivemirror/reports — falls through if configdir is unusable.
//  3. os.TempDir()/smirror-reports — last-ditch.
//
// Mode-0700 directory; mode-0600 files. Reports contain sanitized but still
// potentially sensitive details (mirror names, log lines, rclone version).
func bugReportDir(configPath string) string {
	if dir := filepath.Dir(configPath); dir != "" && dir != "." {
		return filepath.Join(dir, "reports")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".selectivemirror", "reports")
	}
	return filepath.Join(os.TempDir(), "smirror-reports")
}

func cmdStats(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror project-stats [mirror]

Show file counts and line counts per mirror, grouped by file type.
Counts only syncable files (excluded files are not counted).

Alias: stats

Examples:
  smirror project-stats              Stats for all mirrors
  smirror project-stats MyProject    Stats for one mirror`) {
		return
	}
	rejectUnknownFlags("project-stats", args)
	checkMaxArgs("project-stats", args, 1)

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

	projects := cfg.Projects
	if len(args) > 0 {
		proj := cfg.FindProject(args[0])
		if proj == nil {
			fmt.Fprintf(os.Stderr, "Unknown mirror: %s\nAvailable: %s\n", args[0], strings.Join(cfg.ProjectNames(), ", "))
			os.Exit(ExitConfigError)
		}
		projects = []config.Project{*proj}
	}

	var allStats []projectStats
	grandTotal := catCount{}
	grandBytes := int64(0)
	grandIgnored := 0
	grandByCat := make(map[string]catCount)
	grandOther := catCount{}

	for _, proj := range projects {
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
	scanFailed := false
	var ghostDetails []string

	for _, proj := range cfg.Projects {
		fe := filters[proj.Name]

		remoteFiles, err := msync.ListRemote(cfg, proj)
		if err != nil {
			slog.Warn("ghost scan: failed to list remote", "mirror", proj.Name, "error", err)
			scanFailed = true
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
	} else if scanFailed {
		// SM-114: Don't report "clean" when scan failed on one or more mirrors
		st.SetMeta("ghost_scan_result", "incomplete (remote listing failed)")
		st.SetMeta("ghost_scan_time", time.Now().UTC().Format(time.RFC3339))
		slog.Warn("ghost scan: incomplete due to remote listing failure")
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

func heartbeatLoop(ctx context.Context, st *state.Store, cfg *config.Global, m *metrics.Collector, watchMgr *watcher.Manager, syncEngine *msync.Engine, filters map[string]*filter.Engine, notifier *notify.Notifier, webhookSender *notify.WebhookSender) {
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

				// SM-157 / BUG-R5-1: rotate anomaly archive files. Defaults
				// keep the last 30 days, capped at 50 MB total — matches the
				// retention window of PruneOldLogs above. Rotate is no-op
				// when the anomaly dir is missing (anomaly detection
				// disabled), so unconditional invocation is safe.
				if removed, err := anomaly.Rotate(filepath.Join(dd, "anomalies"), anomaly.DefaultRotation()); err == nil && removed > 0 {
					slog.Info("anomaly archives rotated", "removed", removed)
				}

				// # VACUUM the state
				// DB at most once a week. PruneOldLogs above frees pages
				// internally but does not return them to the OS; without
				// VACUUM the file grows monotonically. Errors are non-fatal
				// — the DB remains usable, just bloated.
				if vacuumed, err := st.VacuumIfStale(); err != nil {
					slog.Warn("state vacuum failed", "error", err)
				} else if vacuumed {
					slog.Info("state vacuumed")
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
					syncEngine.Anomaly.Record(anomaly.KindPathGone, proj.Name, proj.LocalPath,
						"project directory missing or unmounted", "")
					if notifier != nil {
						notifier.PathGone(proj.Name, proj.LocalPath)
					}
					continue
				}

				drift := verifyProjectQuiet(cfg, proj, filters[proj.Name], st)
				totalDrift += drift
				if drift > 0 {
					syncEngine.Anomaly.Record(anomaly.KindReconcileStale, proj.Name, "",
						fmt.Sprintf("%d files out of sync", drift),
						"auto-verify detected drift; next reconciliation should resolve it")
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

			// Write heartbeat file. SEC-H6 baseline: files under the
			// user data dir are 0600 (owner-only). Heartbeat content
			// is just a timestamp — not sensitive — but consistency
			// with the SECURITY.md baseline matters for the audit.
			hbPath := filepath.Join(dd, "heartbeat.txt")
			os.WriteFile(hbPath, []byte(ts+"\n"), 0600)

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
					syncEngine.Anomaly.Record(anomaly.KindPathGone, proj.Name, proj.LocalPath,
						"project directory missing or unmounted", "")
					if notifier != nil {
						notifier.PathGone(proj.Name, proj.LocalPath)
					}
				}
			}

			// Check for resolved incidents (silence window passed)
			webhookSender.CheckResolved()

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
	// Lives under %ProgramData%\SelectiveMirror (admin-owned, the canonical
	// service data dir). Previous version targeted C:\ root which leaked
	// PID + os.Args to anyone with read access.
	crashLogPath := serviceCrashLogTarget()
	if crashLogPath != "" {
		_ = os.MkdirAll(filepath.Dir(crashLogPath), 0700)
	}
	crashLog, _ := os.OpenFile(crashLogPath,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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
	// Parse --config with the same last-wins semantics as cliMain
	//.
	configPath := config.DefaultConfigPath()
	_ = extractConfigPath(os.Args[1:], &configPath)

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

		// SEC-C5: When running as a Windows Service (LocalSystem), the config
		// file must be owned by an administrative principal — ALWAYS, not only
		// when hooks are configured. Rationale:
		//   * Hook-bearing configs already require this (original SEC-C5 scope).
		//   * Even hook-free configs that LocalSystem reads are security-critical:
		//     rclone_path / rclone_extra_flags (SEC-H1/H2) give a non-admin writer
		//     arbitrary-binary-execution-as-SYSTEM. delete_policy, filter rules,
		//     and remote paths control what LocalSystem touches on the filesystem.
		//   * Task mode (smirror task install) runs as the user and does not
		//     hit this path, so no-hooks desktop deployments are unaffected.
		// The fix is always to move config to an admin-owned location such as
		// %ProgramData%\SelectiveMirror\config.yaml.
		{
			adminOwned, aclErr := config.IsAdminOwnedPath(configPath)
			if aclErr != nil {
				slog.Error("config ACL check failed", "path", configPath, "error", aclErr)
				if crashLog != nil {
					fmt.Fprintf(crashLog, "%s startFunc: config ACL check failed: %v\n", time.Now().Format(time.RFC3339), aclErr)
				}
				return
			}
			if !adminOwned {
				slog.Error("refusing to start: service-mode config must be admin-owned (SEC-C5)",
					"path", configPath,
					"has_hooks", cfg.HasHooks(),
					"remedy", "move config to %ProgramData%\\SelectiveMirror\\config.yaml (admin-owned), or run in per-user task mode instead: smirror task install")
				if crashLog != nil {
					fmt.Fprintf(crashLog, "%s startFunc: SEC-C5 refuse: non-admin-owned config (hooks=%v)\n", time.Now().Format(time.RFC3339), cfg.HasHooks())
				}
				return
			}
		}

		// SEC-H2: in service mode the rclone binary runs as LocalSystem, so a
		// user-writable rclone.exe is a privilege-escalation path. The
		// admin-owned-config gate above prevents non-admins from changing
		// rclone_path itself, but a binary swap on disk after install would
		// still bypass it. Re-check at startup (defense in depth).
		if cfg.RclonePath != "" {
			rclonePathToCheck := cfg.RclonePath
			if !filepath.IsAbs(rclonePathToCheck) {
				if info, err := rclone.Detect(rclonePathToCheck); err == nil {
					rclonePathToCheck = info.Path
				}
			}
			if filepath.IsAbs(rclonePathToCheck) {
				if owned, err := config.IsAdminOwnedPath(rclonePathToCheck); err == nil && !owned {
					slog.Error("refusing to start: service-mode rclone binary must be admin-owned (SEC-H2)",
						"rclone_path", rclonePathToCheck,
						"remedy", "move rclone.exe to an admin-only directory (e.g. %ProgramFiles%\\rclone\\) and update rclone_path in config")
					if crashLog != nil {
						fmt.Fprintf(crashLog, "%s startFunc: SEC-H2 refuse: rclone binary not admin-owned\n", time.Now().Format(time.RFC3339))
					}
					return
				}
			}
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

		// SM-083: Clean up orphaned project state entries on startup
		if pruned, pErr := st.PruneOrphanedProjects(cfg.ProjectNames()); pErr == nil && pruned > 0 {
			slog.Info("pruned orphaned project state entries", "removed", pruned)
		}

		// Record instance info so `smirror status` can report it.
		// Clear stale health errors from previous run (SM-074).
		st.MarkDaemonStartup()
		exePath, _ := os.Executable()
		st.SetMeta("instance_pid", fmt.Sprintf("%d", os.Getpid()))
		st.SetMeta("instance_exe", exePath)
		st.SetMeta("instance_started", time.Now().Local().Format(time.RFC3339))
		st.SetMeta("instance_mode", "service")
		st.SetMeta("instance_version", version)
		st.SetMeta("instance_user", currentUser())
		st.SetMeta("instance_config_fingerprint", cfg.MirrorFingerprint()) // SM-122
		if mirrorsJSON, jErr := json.Marshal(cfg.Projects); jErr == nil {
			st.SetMeta("instance_mirrors", string(mirrorsJSON))
		}
		st.SetMeta("last_health_error", "")
		defer func() {
			st.SetMeta("instance_pid", "")
			st.SetMeta("instance_exe", "")
			st.SetMeta("instance_started", "")
			st.SetMeta("instance_mode", "")
			st.SetMeta("instance_user", "")
			st.SetMeta("instance_config_fingerprint", "")
			st.SetMeta("instance_mirrors", "")
		}()

		// FINDING 16 closure (service-mode arm): same install-event
		// submit hook as cmdStart. Service-mode is actually the more
		// likely path for many users — MSI installs that opt to run
		// as a Windows Service skip cmdStart entirely.
		go fireInstallEventsAtStartup(cfg, st)

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

		// SEC-M5: register per-mirror prefixes for anomaly sanitization
		// (service-mode equivalent of the cmdStart wiring above).
		{
			paths := make([]string, 0, len(cfg.Projects))
			for _, p := range cfg.Projects {
				paths = append(paths, p.LocalPath)
			}
			anomaly.SetExtraSanitizePrefixes(paths)
		}

		syncEngine := msync.NewEngine(cfg, st, filters, m)
		syncEngine.Anomaly = anomalyRecorder
		// PF-A3 / audit SEC-H5: in service mode (LocalSystem), refuse to
		// follow any symlink in a watched directory. A symlink in any
		// watched mirror (e.g. C:\Projects\MyApp\.tricky) targeting
		// C:\Windows\System32\config\SAM would otherwise sync the SAM
		// hive to the configured remote.
		syncEngine.RejectSymlinkedFiles = true
		if cfg.PreSyncHook != "" || cfg.PostSyncHook != "" || hasProjectHooks(cfg) {
			syncEngine.Hooks = hooks.New(30 * time.Second)
		}

		watchMgr, err := watcher.NewManager(cfg.Projects, filters, syncEngine.Queue, cfg.DeletePolicy())
		if err != nil {
			slog.Error("watcher creation failed", "error", err)
			return
		}
		watchMgr.Anomaly = anomalyRecorder

		// Wire queue overflow → anomaly (service mode)
		syncEngine.Queue.SetOnOverflow(func() {
			anomalyRecorder.Record(anomaly.KindQueueDepthWarning, "", "",
				"queue depth exceeds 50,000 items",
				"possible event storm or stalled workers; check 'smirror status'")
		})

		// Wire webhook alerting (service mode)
		var webhookSender *notify.WebhookSender
		if cfg.AlertWebhookURL != "" {
			if anomalyRecorder == nil {
				slog.Warn("alert_webhook_url is set but anomaly_detection is disabled; webhook will receive no events")
			} else {
				webhookSender = notify.NewWebhookSender(cfg.AlertWebhookURL)
				webhookSender.SanitizePath = anomaly.SanitizePath // SEC-C3
				minSev := cfg.AlertMinSeverity
				if minSev == "" {
					minSev = "error"
				}
				anomalyRecorder.SetOnRecord(func(a *anomaly.Anomaly) {
					if severityAtLeast(string(a.Severity), minSev) {
						webhookSender.Record(string(a.Kind), string(a.Severity), a.Project, a.Path, a.Message, a.Detail)
					}
				})
			}
		}

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
		go heartbeatLoop(ctx, st, cfg, m, watchMgr, syncEngine, filters, notifier, webhookSender)

		liveSyncEngine = syncEngine
		liveCfg = cfg

		// FR-SVC-08: Write lifecycle events to Windows Event Log
		elog := service.OpenEventLog()
		if elog != nil {
			elog.Info(service.EventID, "SelectiveMirror service started, version "+version)
			defer func() {
				elog.Info(service.EventID, "SelectiveMirror service stopped")
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
						if elog != nil {
							elog.Info(service.EventID, "Immediate sync requested via sync-now signal")
						}
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

// currentUser returns the current username (e.g., "alice" or "SYSTEM (LocalSystem)").
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

// (removed) updateConfigKey: replaced by config.SetField, which handles
// top-level-only matching, comment skipping, and mode-preserving writes.
// See internal/config/edit.go.

func cmdService(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror service <action...> [flags]

Windows Service management (requires "run as administrator").

⚠️  Service mode runs smirror as LocalSystem — the highest-privileged
    service account on Windows. Most users don't need this. Prefer
    'smirror task install' (per-user, no admin elevation) unless you
    specifically need 24×7 background sync without an interactive
    session. The differences:

      task install        per-user task; runs at logon; no admin needed
      service install     LocalSystem; runs 24×7 even without login;
                          requires admin-owned config (SEC-C5);
                          if you have configured pre/post-sync hooks
                          (Phase 7 — currently experimental, not part
                          of v1.0 stability surface), they execute as
                          LocalSystem too and can read/write any file
                          on the system. The install path refuses to
                          load a hook-bearing config from a user-
                          writable location, but the broader privilege
                          posture means: only use service mode if you
                          understand why your background sync needs
                          system-wide privileges.

Actions:
  install [start]                Install service (and optionally start it)
  start                          Start the service
  stop [uninstall [--clean]]     Stop the service (and optionally uninstall it)
  uninstall [--clean] [--yes]    Uninstall the service

Flags:
  --clean    Also remove user data (config, state DB, logs)
  --yes      Skip confirmation prompts

Compound commands:
  smirror service install start            Install and start in one step
  smirror service stop uninstall           Stop and uninstall in one step
  smirror service stop uninstall --clean   Stop, uninstall, and remove all data`) {
		return
	}

	if len(args) == 0 {
		printServiceUsage()
		os.Exit(ExitConfigError)
	}

	// Parse actions and flags from args.
	actions, flags := parseServiceArgs(args)
	if len(actions) == 0 {
		printServiceUsage()
		os.Exit(ExitConfigError)
	}

	// Normalize and validate compound sequences.
	actions = normalizeServiceActions(actions)
	if actions == nil {
		// normalizeServiceActions printed the error
		os.Exit(ExitConfigError)
	}

	uflags := parseServiceUninstallFlags(flags)

	// Execute actions sequentially.
	for i, action := range actions {
		if i > 0 {
			fmt.Println()
		}
		switch action {
		case "install":
			serviceDoInstall(configPath, len(actions) > 1)
		case "start":
			serviceDoStart(configPath, len(actions) > 1)
		case "stop":
			serviceDoStop(len(actions) > 1)
		case "uninstall":
			serviceDoUninstall(configPath, uflags.clean, uflags.autoYes)
		}
	}
}

// printServiceUsage prints the service subcommand usage.
func printServiceUsage() {
	fmt.Fprintln(os.Stderr, `Usage: smirror service <action...> [flags]

Actions:
  install [start]                Install service (and optionally start it)
  start                          Start the service
  stop [uninstall [--clean]]     Stop the service (and optionally uninstall it)
  uninstall [--clean] [--yes]    Uninstall the service

Flags:
  --clean    Also remove user data (config, state DB, logs)
  --yes      Skip confirmation prompts

Compound commands:
  smirror service install start            Install and start in one step
  smirror service stop uninstall           Stop and uninstall in one step
  smirror service stop uninstall --clean   Stop, uninstall, and remove all data

Requires "run as administrator" elevated cmd/PowerShell.`)
}

// parseServiceArgs separates actions (install, start, stop, uninstall) from
// flags (--clean, --yes, -y). Unknown tokens are treated as invalid actions.
func parseServiceArgs(args []string) (actions, flags []string) {
	for _, a := range args {
		switch a {
		case "install", "start", "stop", "uninstall":
			actions = append(actions, a)
		case "--clean", "--yes", "-y":
			flags = append(flags, a)
		default:
			fmt.Fprintf(os.Stderr, "Unknown service action: %s\n", a)
			printServiceUsage()
			os.Exit(ExitConfigError)
		}
	}
	return actions, flags
}

// normalizeServiceActions reorders and validates compound action sequences.
// Returns nil if the sequence is invalid (error already printed).
func normalizeServiceActions(actions []string) []string {
	if len(actions) == 1 {
		return actions
	}
	if len(actions) > 2 {
		fmt.Fprintln(os.Stderr, "Error: at most two service actions can be combined.")
		printServiceUsage()
		return nil
	}

	a, b := actions[0], actions[1]

	// Canonical pairs and their reversed forms.
	switch {
	case a == "install" && b == "start":
		return []string{"install", "start"}
	case a == "start" && b == "install":
		// Auto-reorder: user meant install then start
		return []string{"install", "start"}
	case a == "stop" && b == "uninstall":
		return []string{"stop", "uninstall"}
	case a == "uninstall" && b == "stop":
		// Auto-reorder: user meant stop then uninstall
		return []string{"stop", "uninstall"}
	default:
		fmt.Fprintf(os.Stderr, "Error: invalid service action combination: %s %s\n", a, b)
		fmt.Fprintln(os.Stderr, "Valid combinations: install start, stop uninstall")
		return nil
	}
}

// serviceDoInstall handles `smirror service install`.
// When compound is true, the "start with:" hint is suppressed (start follows).
func serviceDoInstall(configPath string, compound bool) {
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "Error: service installation requires administrator privileges.")
		fmt.Fprintln(os.Stderr, "Run this command from an elevated (administrator) terminal.")
		os.Exit(1)
	}
	// Preflight: config must exist and be valid before registering the service.
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Cannot install the service without a valid configuration.")
		fmt.Fprintf(os.Stderr, "Create a config file at: %s\n", configPath)
		fmt.Fprintf(os.Stderr, "Example: smirror --config %s start  (generates default config on first run)\n", configPath)
		os.Exit(ExitConfigError)
	}

	// SEC-C5: the running service reads this config as LocalSystem. If the
	// config lives in a user-writable location, any standard user can pivot
	// to SYSTEM via rclone_path / rclone_extra_flags / hooks. Fail at install
	// time so the user doesn't end up with a broken-at-start service after
	// a clean UAC prompt.
	adminOwned, aclErr := config.IsAdminOwnedPath(configPath)
	if aclErr != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot verify config ACL: %v\n", aclErr)
		os.Exit(ExitConfigError)
	}
	if !adminOwned {
		fmt.Fprintln(os.Stderr, "Error: service install refused (SEC-C5).")
		fmt.Fprintf(os.Stderr, "\nThe service runs as LocalSystem and would read:\n  %s\n", configPath)
		fmt.Fprintln(os.Stderr, "That file is not owned by Administrators / LocalSystem, so any user who")
		fmt.Fprintln(os.Stderr, "can edit it could execute arbitrary code as SYSTEM via rclone_path,")
		fmt.Fprintln(os.Stderr, "rclone_extra_flags, or pre/post-sync hooks.")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Fix one of the following and re-run:")
		fmt.Fprintln(os.Stderr, "  1. Move the config to %ProgramData%\\SelectiveMirror\\config.yaml")
		fmt.Fprintln(os.Stderr, "     and re-run: smirror --config \"%ProgramData%\\SelectiveMirror\\config.yaml\" service install")
		fmt.Fprintln(os.Stderr, "  2. Use per-user task mode instead (no admin required):")
		fmt.Fprintln(os.Stderr, "     smirror task install")
		os.Exit(ExitConfigError)
	}

	// Resolve rclone path and config before installing the service.
	// The Windows service runs as SYSTEM with different PATH and home dir.
	if cfg != nil {
		// Resolve rclone binary to absolute path
		if !filepath.IsAbs(cfg.RclonePath) {
			if info, err := rclone.Detect(cfg.RclonePath); err == nil {
				if err := config.SetField(configPath, "rclone_path", info.Path); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not update rclone_path in config: %v\n", err)
					fmt.Fprintf(os.Stderr, "Manually set rclone_path to: %s\n\n", info.Path)
				} else {
					fmt.Printf("Resolved rclone_path: %s\n", info.Path)
				}
				// SEC-H2: in service mode the rclone binary runs as LocalSystem,
				// so the resolved path MUST be admin-owned. A user-writable
				// rclone.exe lets any non-admin who can write that path execute
				// arbitrary code as SYSTEM via the next sync. Refuse install if
				// the resolution landed on a user-writable binary.
				if ownedByAdmin, ownErr := config.IsAdminOwnedPath(info.Path); ownErr != nil {
					fmt.Fprintf(os.Stderr, "Warning: cannot verify admin ownership of rclone at %q: %v\n", info.Path, ownErr)
				} else if !ownedByAdmin {
					fmt.Fprintf(os.Stderr, "Error: rclone binary at %q is not admin-owned. Service mode runs as LocalSystem; a user-writable rclone is a privilege escalation path. Move rclone to an admin-only directory (e.g. %%ProgramFiles%%\\rclone\\) and update rclone_path in config.\n", info.Path)
					os.Exit(ExitConfigError)
				}
			}
		} else {
			// User-supplied absolute path — same admin-ownership check applies
			// (same threat model: rclone runs as SYSTEM in service mode).
			if ownedByAdmin, ownErr := config.IsAdminOwnedPath(cfg.RclonePath); ownErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: cannot verify admin ownership of rclone at %q: %v\n", cfg.RclonePath, ownErr)
			} else if !ownedByAdmin {
				fmt.Fprintf(os.Stderr, "Error: rclone binary at %q is not admin-owned. Service mode runs as LocalSystem; a user-writable rclone is a privilege escalation path. Move rclone to an admin-only directory and update rclone_path in config.\n", cfg.RclonePath)
				os.Exit(ExitConfigError)
			}
		}
		// Resolve rclone config (SYSTEM has its own %APPDATA%, no remotes there)
		if cfg.RcloneConfig == "" {
			rcloneConf := filepath.Join(os.Getenv("APPDATA"), "rclone", "rclone.conf")
			if _, err := os.Stat(rcloneConf); err == nil {
				if err := config.SetField(configPath, "rclone_config", rcloneConf); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: could not update rclone_config in config: %v\n", err)
					fmt.Fprintf(os.Stderr, "Manually set rclone_config to: %s\n\n", rcloneConf)
				} else {
					fmt.Printf("Resolved rclone_config: %s\n", rcloneConf)
				}
			}
		}
	}
	// Warn about multiple smirror installations.
	if instInfo := findInstallations(); instInfo.HasDuplicates {
		fmt.Println()
		warnMultipleInstallations(instInfo)
		fmt.Println()
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
	if !compound {
		fmt.Println("Start with: smirror service start")
		fmt.Println("Or: net start smirror")
	}

	// Warn about .syncignore files present in project directories
	{
		var syncignoreFiles []string
		for _, p := range cfg.Projects {
			si := p.SyncIgnoreFile()
			if _, err := os.Stat(si); err == nil {
				syncignoreFiles = append(syncignoreFiles, si)
			}
		}
		if len(syncignoreFiles) > 0 {
			fmt.Println()
			fmt.Println("Note: .syncignore files detected in project directories:")
			for _, si := range syncignoreFiles {
				fmt.Printf("  %s\n", si)
			}
			fmt.Println("These files control which files are synced per project.")
		}
	}
}

// serviceDoStart handles `smirror service start`.
// When compound is true, "already running" is non-fatal (continues to next action).
func serviceDoStart(configPath string, compound bool) {
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "Error: starting the service requires administrator privileges.")
		fmt.Fprintln(os.Stderr, "Run this command from an elevated (administrator) terminal.")
		os.Exit(1)
	}
	if err := service.Start(); err != nil {
		if strings.Contains(err.Error(), "already running") || strings.Contains(err.Error(), "already been started") {
			fmt.Fprintf(os.Stderr, "Warning: service is already running.\n")
			if !compound {
				os.Exit(0)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Service 'smirror' started.")
	cfg, err := config.Load(configPath)
	if err == nil {
		fmt.Printf("Follow log: powershell -NoProfile -Command \"Get-Content '%s' -Wait -Tail 30\"\n", cfg.LogFile)
	}
}

// serviceDoStop handles `smirror service stop`.
// When compound is true, "not running" is non-fatal (continues to next action).
func serviceDoStop(compound bool) {
	if !isAdmin() {
		fmt.Fprintln(os.Stderr, "Error: stopping the service requires administrator privileges.")
		fmt.Fprintln(os.Stderr, "Run this command from an elevated (administrator) terminal.")
		os.Exit(1)
	}
	if err := service.Stop(); err != nil {
		errMsg := err.Error()
		benign := strings.Contains(errMsg, "not been started") ||
			strings.Contains(errMsg, "not running") ||
			strings.Contains(errMsg, "is not installed")
		if benign {
			fmt.Fprintf(os.Stderr, "Service 'smirror' was not running.\n")
			if !compound {
				os.Exit(0)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Service 'smirror' stopped.")
}

// serviceDoUninstall handles `smirror service uninstall [--clean] [--yes]`.
func serviceDoUninstall(configPath string, clean, autoYes bool) {
	// Check if service is installed — if so, require admin to uninstall.
	// If not installed, allow proceeding (--clean still works without admin).
	if service.IsInstalled() && !isAdmin() {
		fmt.Fprintln(os.Stderr, "Error: service is installed. Uninstalling requires administrator privileges.")
		fmt.Fprintln(os.Stderr, "Run this command from an elevated (administrator) terminal.")
		os.Exit(1)
	}
	_ = service.RemoveEventSource() // best-effort cleanup
	if err := service.Uninstall(); err != nil {
		// Distinguish "not installed" from real errors
		if strings.Contains(err.Error(), "is not installed") {
			fmt.Fprintln(os.Stderr, "Service 'smirror' is not installed.")
			if !clean {
				os.Exit(0)
			}
			// If --clean, continue to data removal even if service wasn't installed
			fmt.Println()
		} else {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Println("Service 'smirror' uninstalled successfully.")
	}

	if clean {
		serviceUninstallClean(configPath, autoYes)
	} else {
		fmt.Println()
		instInfo := findInstallations()
		dataPath := config.DefaultDataDir()

		fmt.Printf("Binary:     %s\n", instInfo.CurrentExe)
		fmt.Printf("Data dir:   %s\n", dataPath)

		if instInfo.HasDuplicates {
			fmt.Println()
			warnMultipleInstallations(instInfo)
		} else if len(instInfo.AllFound) == 1 {
			fmt.Printf("On PATH:    %s\n", instInfo.AllFound[0])
		}

		fmt.Println()
		fmt.Printf("User data preserved at: %s\n", dataPath)
		fmt.Println("  config.yaml           — configuration")
		fmt.Println("  state.db              — sync state database")
		fmt.Println("  selectivemirror.log   — application logs")

		// Offer PATH cleanup
		cleanPATHEntries(instInfo, autoYes)

		fmt.Println()
		fmt.Println("To fully remove SelectiveMirror:")
		fmt.Println("  MSI install:    Settings > Apps > SelectiveMirror > Uninstall")
		fmt.Printf("  Manual install: Delete %s and remove %s from PATH\n",
			instInfo.CurrentExe, filepath.Dir(instInfo.CurrentExe))
		fmt.Println()
		fmt.Println("To also remove user data: smirror service uninstall --clean")
	}
}

// serviceUninstallFlags holds parsed flags for `smirror service uninstall`.
type serviceUninstallFlags struct {
	clean   bool
	autoYes bool
}

// parseServiceUninstallFlags parses service uninstall flags from args.
func parseServiceUninstallFlags(args []string) serviceUninstallFlags {
	var f serviceUninstallFlags
	for _, a := range args {
		switch a {
		case "--clean":
			f.clean = true
		case "--yes", "-y":
			f.autoYes = true
		}
	}
	return f
}

// dryTestRemovability checks whether each named file in dir can be opened for
// read-write (i.e. is not locked by another process and has correct permissions).
// Returns a list of human-readable descriptions of blocked files.
// Files that do not exist are silently skipped.
func dryTestRemovability(dir string, fileNames []string) []string {
	var blocked []string
	for _, name := range fileNames {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue // file doesn't exist, nothing to remove
		}
		if err != nil {
			blocked = append(blocked, fmt.Sprintf("  %s: %v", name, err))
			continue
		}
		// Skip open-file test for directories (RemoveAll handles them)
		if info.IsDir() {
			continue
		}
		fh, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			blocked = append(blocked, fmt.Sprintf("  %s: %v", name, err))
			continue
		}
		fh.Close()
	}
	return blocked
}

// serviceUninstallClean removes SelectiveMirror user data (config, state DB, logs).
// Uses a dry-test approach: first verifies every file can be removed (no locks,
// permissions OK), then either proceeds or aborts the entire operation.
// Requires double confirmation unless --yes is passed.
func serviceUninstallClean(configPath string, autoYes bool) {
	dataPath := config.DefaultDataDir()

	// --- Phase 1: Inventory — scan everything in the data directory ---
	type fileEntry struct {
		path string
		desc string
		size int64
	}

	// Known file descriptions
	knownDescs := map[string]string{
		"config.yaml":         "configuration",
		"state.db":            "sync state database",
		"state.db-wal":        "SQLite WAL journal",
		"state.db-shm":        "SQLite shared memory",
		"selectivemirror.log": "application logs",
		"smirror.lock":        "instance lock file",
		"status.json":         "status snapshot",
		"heartbeat.txt":       "heartbeat timestamp",
	}

	var found []fileEntry
	entries, err := os.ReadDir(dataPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", dataPath, err)
		os.Exit(ExitError)
	}
	for _, e := range entries {
		p := filepath.Join(dataPath, e.Name())
		if e.IsDir() {
			// Sum directory contents
			var totalSize int64
			var fileCount int
			_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, _ error) error {
				if d != nil && !d.IsDir() {
					if fi, err := d.Info(); err == nil {
						totalSize += fi.Size()
						fileCount++
					}
				}
				return nil
			})
			found = append(found, fileEntry{
				path: p,
				desc: fmt.Sprintf("%s/ (%d files)", e.Name(), fileCount),
				size: totalSize,
			})
		} else {
			fi, _ := e.Info()
			size := int64(0)
			if fi != nil {
				size = fi.Size()
			}
			desc := knownDescs[e.Name()]
			if desc == "" {
				// Rotated logs, unknown files — describe by extension
				if strings.HasPrefix(e.Name(), "selectivemirror.log.") {
					desc = "rotated log"
				} else {
					desc = "user data"
				}
			}
			found = append(found, fileEntry{path: p, desc: desc, size: size})
		}
	}

	if len(found) == 0 {
		fmt.Printf("No user data found at %s — nothing to clean.\n", dataPath)
		return
	}

	// --- Phase 2: Dry-test — verify every file is removable ---
	fmt.Println("Dry-test: checking all files can be removed...")
	var fileNames []string
	for _, f := range found {
		fileNames = append(fileNames, filepath.Base(f.path))
	}
	blocked := dryTestRemovability(dataPath, fileNames)

	if len(blocked) > 0 {
		fmt.Println()
		fmt.Println("ABORT: the following files are locked or inaccessible:")
		for _, b := range blocked {
			fmt.Println(b)
		}
		fmt.Println()
		fmt.Println("Possible causes:")
		fmt.Println("  - smirror is still running (stop with: smirror service stop)")
		fmt.Println("  - Another process holds a file open")
		fmt.Println("  - Insufficient permissions")
		fmt.Println()
		fmt.Println("Resolve the locks and retry: smirror service uninstall --clean")
		os.Exit(ExitError)
	}
	fmt.Println("  All files are removable.")

	// --- Phase 3: Show what will be removed ---
	fmt.Println()
	fmt.Println("The following user data will be permanently deleted:")
	fmt.Println()
	var totalSize int64
	for _, f := range found {
		totalSize += f.size
		fmt.Printf("  %s (%s, %s)\n", filepath.Base(f.path), f.desc, humanBytes(f.size))
	}
	fmt.Printf("\n  Total: %s in %s\n", humanBytes(totalSize), dataPath)

	// Warn about .syncignore files in project directories (NOT deleted)
	if cfg, err := config.Load(configPath); err == nil {
		var syncignoreFiles []string
		for _, p := range cfg.Projects {
			si := p.SyncIgnoreFile()
			if _, err := os.Stat(si); err == nil {
				syncignoreFiles = append(syncignoreFiles, si)
			}
		}
		if len(syncignoreFiles) > 0 {
			fmt.Println()
			fmt.Println("  Note: .syncignore files in your project directories are NOT removed")
			fmt.Println("  (they belong to the project, not to smirror):")
			for _, si := range syncignoreFiles {
				fmt.Printf("    %s\n", si)
			}
		}
	}

	// --- Phase 4: Double confirmation ---
	if !autoYes {
		fmt.Println()
		fmt.Print("Remove all user data? This cannot be undone. [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("Clean cancelled. User data preserved.")
			return
		}

		// Second confirmation
		fmt.Print("Are you sure? Type 'DELETE' to confirm: ")
		line, _ = reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "DELETE" {
			fmt.Println("Clean cancelled. User data preserved.")
			return
		}
	}

	// --- Phase 5: Remove (all pre-verified removable) ---
	fmt.Println()
	for _, f := range found {
		var err error
		if info, statErr := os.Stat(f.path); statErr == nil && info.IsDir() {
			err = os.RemoveAll(f.path)
		} else {
			err = os.Remove(f.path)
		}
		if err != nil {
			// Should not happen — dry-test passed. Treat as fatal.
			fmt.Fprintf(os.Stderr, "UNEXPECTED: failed to remove %s: %v\n", filepath.Base(f.path), err)
			fmt.Fprintln(os.Stderr, "Aborting. Some files may have been removed. Check manually:")
			fmt.Fprintf(os.Stderr, "  %s\n", dataPath)
			os.Exit(ExitError)
		}
		fmt.Printf("  Removed %s\n", filepath.Base(f.path))
	}

	// Try to remove the data directory itself (only succeeds if empty)
	if err := os.Remove(dataPath); err == nil {
		fmt.Printf("  Removed %s\n", dataPath)
	}

	fmt.Printf("\nClean complete: %d files removed.\n", len(found))

	// --- Phase 6: PATH cleanup ---
	instInfo := findInstallations()
	fmt.Println()
	fmt.Printf("Binary: %s\n", instInfo.CurrentExe)
	if instInfo.HasDuplicates {
		fmt.Println()
		warnMultipleInstallations(instInfo)
	}

	pathCleaned := cleanPATHEntries(instInfo, autoYes)

	fmt.Println()
	fmt.Println("To fully remove SelectiveMirror:")
	fmt.Println("  MSI install:    Settings > Apps > SelectiveMirror > Uninstall")
	if pathCleaned {
		fmt.Printf("  Manual install: Delete %s\n", instInfo.CurrentExe)
	} else {
		fmt.Printf("  Manual install: Delete %s and remove %s from PATH\n",
			instInfo.CurrentExe, filepath.Dir(instInfo.CurrentExe))
	}
}
