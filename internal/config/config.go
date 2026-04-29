// Package config handles YAML configuration loading and validation.
package config

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Project defines a watched directory and its sync destination.
type Project struct {
	Name             string   `yaml:"name"`
	LocalPath        string   `yaml:"local_path"`
	Remote           string   `yaml:"remote"`
	DebounceSec      int      `yaml:"debounce_sec"`
	MaxFileSizeMB    int      `yaml:"max_file_size_mb"`
	SyncIgnorePath   string   `yaml:"syncignore_path"`    // override; default: <local_path>/.syncignore
	RcloneExtraFlags []string `yaml:"rclone_extra_flags"` // per-mirror rclone flags (appended after global)
	DeletePolicyStr  string   `yaml:"delete_policy"`      // per-mirror override (empty = use global)
	QuarantineDays   int      `yaml:"quarantine_days"`    // per-mirror override (0 = use global)
	PreSyncHook      string   `yaml:"pre_sync_hook"`      // shell command to run before sync (empty = none)
	PostSyncHook     string   `yaml:"post_sync_hook"`     // shell command to run after sync (empty = none)
}

// EffectivePreSyncHook returns the per-mirror hook, falling back to global.
func (p Project) EffectivePreSyncHook(global *Global) string {
	if p.PreSyncHook != "" {
		return p.PreSyncHook
	}
	return global.PreSyncHook
}

// EffectivePostSyncHook returns the per-mirror hook, falling back to global.
func (p Project) EffectivePostSyncHook(global *Global) string {
	if p.PostSyncHook != "" {
		return p.PostSyncHook
	}
	return global.PostSyncHook
}

// DebounceDuration returns the quiet-window interval as a time.Duration.
// Returns 0 when DebounceSec <= 0, which signals queue-based fairness mode:
// events enqueue immediately into the FairQueue (dedup + move-to-back).
// A positive value enables static debounce: events are delayed by this
// duration with the timer resetting on each new event (for Office-style saves).
func (p Project) DebounceDuration() time.Duration {
	if p.DebounceSec <= 0 {
		return 0 // queue-based fairness (default)
	}
	return time.Duration(p.DebounceSec) * time.Second
}

// MaxFileSize returns the max file size in bytes.
func (p Project) MaxFileSize() int64 {
	if p.MaxFileSizeMB <= 0 {
		return 100 * 1024 * 1024 // 100 MB default
	}
	return int64(p.MaxFileSizeMB) * 1024 * 1024
}

// SyncIgnoreFile returns the path to the .syncignore file for this project.
func (p Project) SyncIgnoreFile() string {
	if p.SyncIgnorePath != "" {
		return p.SyncIgnorePath
	}
	return filepath.Join(p.LocalPath, ".syncignore")
}

// DeletePolicy controls what happens when a local file is deleted.
type DeletePolicy string

const (
	DeleteIgnore     DeletePolicy = "ignore"     // do nothing on remote
	DeleteDelete     DeletePolicy = "delete"     // immediately delete remote file
	DeleteMirror     DeletePolicy = "mirror"     // deprecated alias for "delete"
	DeleteQuarantine DeletePolicy = "quarantine" // move remote to .quarantine/
)

// Global holds the complete application configuration.
type Global struct {
	Projects           []Project    `yaml:"mirrors"`
	DefaultRemote      string       `yaml:"default_remote"`  // default rclone remote for new mirrors (e.g., "gdrive:smirror")
	GlobalExcludes     []string     `yaml:"global_excludes"`
	StateDB            string       `yaml:"state_db"`
	LogFile            string       `yaml:"log_file"`
	LogLevel           string       `yaml:"log_level"`
	RclonePath         string       `yaml:"rclone_path"`
	RcloneConfig       string       `yaml:"rclone_config"`       // path to rclone.conf (for service/SYSTEM account)
	RcloneExtraFlags   []string     `yaml:"rclone_extra_flags"`
	BandwidthLimit     string       `yaml:"bandwidth_limit"`
	HeartbeatIntervalS int          `yaml:"heartbeat_interval_sec"`
	ReconcileIntervalS int         `yaml:"reconcile_interval_sec"` // periodic full sync interval (default 300 = 5 min)
	SyncWorkers        int          `yaml:"sync_workers"`          // concurrent sync workers (default 4)
	DeletePolicyStr    string       `yaml:"delete_policy"`    // "ignore", "mirror", "quarantine"
	QuarantineDays     int          `yaml:"quarantine_days"`  // days to keep quarantined files (default 30)
	VerifyIntervalS    int          `yaml:"verify_interval_sec"`  // periodic verify interval (default 21600 = 6h, 0 = disabled)
	NotifyEnabled      *bool        `yaml:"notify_enabled"`       // Windows toast notifications (default true)
	AnomalyDetectionEnabled     *bool        `yaml:"anomaly_detection_enabled"`      // Anomaly detection and recording (default true)
	PreSyncHook                string       `yaml:"pre_sync_hook"`                  // global default pre-sync hook (empty = none)
	PostSyncHook               string       `yaml:"post_sync_hook"`                 // global default post-sync hook (empty = none)
	AlertWebhookURL            string       `yaml:"alert_webhook_url"`              // HTTP endpoint for anomaly alerts (empty = disabled)
	AlertMinSeverity           string       `yaml:"alert_min_severity"`             // minimum severity to alert: info, warning, error, critical (default: error)
}

// IsAnomalyDetectionEnabled returns whether anomaly detection is enabled (default true).
func (g Global) IsAnomalyDetectionEnabled() bool {
	if g.AnomalyDetectionEnabled == nil {
		return true
	}
	return *g.AnomalyDetectionEnabled
}

// RcloneArgs returns global rclone flags (--config if set).
// Prepend these to any rclone command's arguments.
func (g Global) RcloneArgs() []string {
	if g.RcloneConfig != "" {
		return []string{"--config", g.RcloneConfig}
	}
	return nil
}

// HeartbeatInterval returns the heartbeat interval as a time.Duration.
func (g Global) HeartbeatInterval() time.Duration {
	if g.HeartbeatIntervalS <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(g.HeartbeatIntervalS) * time.Second
}

// ReconcileInterval returns the periodic reconciliation interval.
// Default is 5 minutes. Catches changes invisible to fsnotify (WSL, external tools).
func (g Global) ReconcileInterval() time.Duration {
	if g.ReconcileIntervalS <= 0 {
		return 5 * time.Minute
	}
	return time.Duration(g.ReconcileIntervalS) * time.Second
}

// Workers returns the number of concurrent sync workers (default 4).
func (g Global) Workers() int {
	if g.SyncWorkers <= 0 {
		return 4
	}
	if g.SyncWorkers > 16 {
		return 16 // cap to avoid API rate limiting
	}
	return g.SyncWorkers
}

// parseDeletePolicy converts a string to a DeletePolicy value.
// Handles the "mirror" → "delete" deprecation.
func parseDeletePolicy(s string) DeletePolicy {
	switch DeletePolicy(s) {
	case DeleteIgnore:
		return DeleteIgnore
	case DeleteDelete:
		return DeleteDelete
	case DeleteMirror:
		slog.Warn("delete_policy 'mirror' is deprecated, use 'delete' instead")
		return DeleteDelete
	case DeleteQuarantine:
		return DeleteQuarantine
	default:
		return DeleteDelete // no policy or unrecognized → delete (mirror the deletion)
	}
}

// DeletePolicy returns the parsed global delete policy (defaults to "delete").
func (g Global) DeletePolicy() DeletePolicy {
	return parseDeletePolicy(g.DeletePolicyStr)
}

// QuarantineRetention returns the quarantine retention in days (default 30).
func (g Global) QuarantineRetention() int {
	if g.QuarantineDays <= 0 {
		return 30
	}
	return g.QuarantineDays
}

// DeletePolicy returns the per-mirror delete policy, falling back to global.
func (p Project) DeletePolicy(global *Global) DeletePolicy {
	if p.DeletePolicyStr != "" {
		return parseDeletePolicy(p.DeletePolicyStr)
	}
	return global.DeletePolicy()
}

// QuarantineRetention returns the per-mirror quarantine retention, falling back to global.
func (p Project) QuarantineRetention(global *Global) int {
	if p.QuarantineDays > 0 {
		return p.QuarantineDays
	}
	return global.QuarantineRetention()
}

// VerifyInterval returns the periodic verify interval (default 6 hours, 0 = disabled).
func (g Global) VerifyInterval() time.Duration {
	if g.VerifyIntervalS < 0 {
		return 0 // disabled
	}
	if g.VerifyIntervalS == 0 {
		return 6 * time.Hour
	}
	return time.Duration(g.VerifyIntervalS) * time.Second
}

// NotifyEnabled returns whether Windows toast notifications are enabled (default true).
func (g Global) IsNotifyEnabled() bool {
	if g.NotifyEnabled == nil {
		return true
	}
	return *g.NotifyEnabled
}

// HasHooks reports whether any hook (global or per-mirror, pre or post) is
// configured. Used by SEC-C5 to decide whether to enforce admin-owned config.
func (g *Global) HasHooks() bool {
	if g.PreSyncHook != "" || g.PostSyncHook != "" {
		return true
	}
	for _, p := range g.Projects {
		if p.PreSyncHook != "" || p.PostSyncHook != "" {
			return true
		}
	}
	return false
}

// DefaultDataDir returns the default data directory (~/.selectivemirror/).
func DefaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		if runtime.GOOS == "windows" {
			home = os.Getenv("USERPROFILE")
		} else {
			home = os.Getenv("HOME")
		}
	}
	return filepath.Join(home, ".selectivemirror")
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	return filepath.Join(DefaultDataDir(), "config.yaml")
}

// Load reads and parses a YAML config file, applying defaults.
// LoadRaw parses the config YAML without running validation. Use for commands
// that need config values before the config is fully set up (e.g., `smirror remote`
// reads default_remote before any mirrors are defined).
func LoadRaw(path string) (*Global, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	cfg := &Global{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, friendlyYAMLError(err)
	}
	return cfg, nil
}

func Load(path string) (*Global, error) {
	// Resolve to absolute path so configDir is always absolute,
	// even if the user passed a relative --config path.
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving config path: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	cfg := &Global{
		LogLevel:           "info",
		RclonePath:         "rclone",
		HeartbeatIntervalS: 300,
	}

	// SM-081: Use decoder with KnownFields to detect typos in config keys.
	// Unknown fields (e.g., "delet_policy") cause a warning, not a hard error,
	// so existing configs with forward-compatible fields still load.
	//
	// SEC-L1: route the warning through both slog.Warn and os.Stderr.
	// Previously it only hit stderr — service-mode invocations have no
	// stderr the user sees, so a typo'd key was silently ignored. slog
	// emits to the smirror.log too, where the user finds it via
	// `smirror status` / report-bug.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		// Check if it's an unknown field error — warn but don't fail
		if strings.Contains(err.Error(), "not found") {
			fmt.Fprintf(os.Stderr, "Warning: config %s: %v (check for typos)\n", path, err)
			slog.Warn("config has unknown fields (check for typos); strict mode declined, parsing non-strict",
				"path", path, "yaml_error", err.Error())
			// Re-parse without strict mode so the config still loads
			if err2 := yaml.Unmarshal(data, cfg); err2 != nil {
				return nil, friendlyYAMLError(err2)
			}
		} else {
			return nil, friendlyYAMLError(err)
		}
	}

	// Apply defaults for paths — use the config file's own directory so that
	// the state DB and log are always co-located with the config, regardless
	// of which user account is running (foreground or Windows service as SYSTEM).
	configDir := filepath.Dir(path)
	if cfg.StateDB == "" || cfg.StateDB == "~/.selectivemirror/state.db" {
		cfg.StateDB = filepath.Join(configDir, "state.db")
	}
	if cfg.LogFile == "" || cfg.LogFile == "~/.selectivemirror/selectivemirror.log" {
		cfg.LogFile = filepath.Join(configDir, "selectivemirror.log")
	}

	// Expand ~ in paths
	cfg.StateDB = expandHome(cfg.StateDB)
	cfg.LogFile = expandHome(cfg.LogFile)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate checks the configuration for errors.
func (g *Global) Validate() error {
	if len(g.Projects) == 0 {
		// Tier-2 #29 (validation panel 2026-04-29): explicit hint that
		// "no mirrors defined" can be the symptom of a YAML structural
		// issue (a mirror entry whose first non-comment child is a
		// `key: value` rather than `- name:` makes the parser read
		// `mirrors:` as a map and silently drop entries). Point users
		// at the most common cause and the diagnostic command.
		return fmt.Errorf("no mirrors defined in config — if your config has a `mirrors:` section, this often means a YAML structural issue silently zeroed it out (each entry must start with `- name:` and use consistent 2-space indentation). Run `smirror addmirror <path>` against an empty config to see a known-good example, or paste your config into a YAML linter")
	}

	// BUG-1 fix: dedup case-insensitively. On Windows (case-insensitive
	// NTFS) `WorkProject` and `workproject` resolve to the same on-disk
	// path and the same state-DB lookup key. Even on case-sensitive file
	// systems, mirror names are user-facing labels and a case-only
	// collision is almost always a typo the user wants flagged.
	names := make(map[string]string) // lower-name -> original-name (for diag)
	for i, p := range g.Projects {
		if p.Name == "" {
			return fmt.Errorf("mirror[%d]: name is required", i)
		}
		key := strings.ToLower(p.Name)
		if existing, dup := names[key]; dup {
			if existing == p.Name {
				return fmt.Errorf("mirror[%d]: duplicate name %q", i, p.Name)
			}
			return fmt.Errorf("mirror[%d]: name %q collides with earlier %q (case-only difference; mirror names must be unique case-insensitively to avoid state-DB races on Windows NTFS)", i, p.Name, existing)
		}
		names[key] = p.Name

		if p.LocalPath == "" {
			return fmt.Errorf("mirror %q: local_path is required", p.Name)
		}
		if p.Remote == "" {
			return fmt.Errorf("mirror %q: remote is required", p.Name)
		}

		// Check local path exists
		info, err := os.Stat(p.LocalPath)
		if err != nil {
			return fmt.Errorf("mirror %q: local_path %q: %w", p.Name, p.LocalPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("mirror %q: local_path %q is not a directory", p.Name, p.LocalPath)
		}

		// Validate per-mirror delete policy if set
		if p.DeletePolicyStr != "" {
			switch DeletePolicy(p.DeletePolicyStr) {
			case DeleteIgnore, DeleteDelete, DeleteMirror, DeleteQuarantine:
				// valid
			default:
				return fmt.Errorf("mirror %q: invalid delete_policy %q (must be ignore, delete, or quarantine)", p.Name, p.DeletePolicyStr)
			}
		}

		// Apply defaults (DebounceSec 0 = dynamic debounce, don't override)
		if p.MaxFileSizeMB <= 0 {
			g.Projects[i].MaxFileSizeMB = 100
		}

		// GAP-4 (panel review 2026-04-28): reject drive-root and other
		// system-wide local_path values. Watching `C:\` (or %SystemRoot%,
		// %ProgramFiles%, etc.) recurses across millions of entries,
		// exhausts ReadDirectoryChangesW handle buffers, and is almost
		// always a typo or misconfiguration. Reject with a friendly hint.
		if reason := isUnsafeLocalPath(p.LocalPath); reason != "" {
			return fmt.Errorf("mirror %q: local_path %q rejected: %s", p.Name, p.LocalPath, reason)
		}

		// GAP-5: reject traversal-shaped remote paths (e.g. `local:../../etc`).
		// rclone-remote syntax is `remote:path`. After the colon, `..`
		// segments are almost always either a typo or a deliberate escape
		// attempt. Cheap defense-in-depth — failure is otherwise deferred
		// to first sync, leaving status output saying "OK" until then.
		if reason := isUnsafeRemote(p.Remote); reason != "" {
			return fmt.Errorf("mirror %q: remote %q rejected: %s", p.Name, p.Remote, reason)
		}
	}

	// GAP-3: detect overlapping mirror local_paths. If parent and child
	// are both mirrored, every event under the child fires on both
	// watchers, FairQueue gets two tasks, two rclone processes burn API
	// quota, and the remote diverges based on which finishes first.
	if err := validateNoLocalPathOverlap(g.Projects); err != nil {
		return err
	}

	// GAP-2: validate rclone_config path if set. rclone treats this as
	// the credentials store; pointing it at a missing/non-regular file
	// silently degrades sync, and combined with GAP-1 it's a pivot
	// vector if the file is later created by an attacker who can write
	// the path.
	if g.RcloneConfig != "" {
		info, err := os.Stat(g.RcloneConfig)
		if err != nil {
			return fmt.Errorf("rclone_config %q: %w", g.RcloneConfig, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("rclone_config %q is not a regular file", g.RcloneConfig)
		}
	}

	// GAP-1 (CRITICAL): reject dangerous rclone_extra_flags. The list is
	// appended verbatim into every rclone invocation; flags like --rc
	// expose an unauthenticated control plane on localhost (full
	// filesystem access as the smirror principal — LocalSystem in service
	// mode), and flags like --log-file / --config let anyone with config
	// write access pivot to arbitrary file overwrite or backend swap.
	if err := validateRcloneExtraFlags("global", g.RcloneExtraFlags); err != nil {
		return err
	}
	for _, p := range g.Projects {
		if err := validateRcloneExtraFlags(fmt.Sprintf("mirror %q", p.Name), p.RcloneExtraFlags); err != nil {
			return err
		}
	}

	// SM-089: Validate rclone_path if explicitly set to an absolute or relative path.
	// Bare command names like "rclone" are resolved at runtime via PATH — skip validation.
	if g.RclonePath != "" && (filepath.IsAbs(g.RclonePath) || strings.ContainsRune(g.RclonePath, filepath.Separator)) {
		info, err := os.Stat(g.RclonePath)
		if err != nil {
			return fmt.Errorf("rclone_path %q: %w", g.RclonePath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("rclone_path %q is a directory, not an executable", g.RclonePath)
		}
	}

	// SEC-C4: Validate alert_webhook_url. Reject plaintext HTTP, loopback, and
	// RFC1918/link-local IPs to prevent SSRF and cleartext credential exfil.
	if g.AlertWebhookURL != "" {
		if err := validateWebhookURL(g.AlertWebhookURL); err != nil {
			return fmt.Errorf("alert_webhook_url: %w", err)
		}
	}

	// PR-S6 (panel review pre-release 2026-04-28): validate alert_min_severity
	// against the canonical severity set. Without this, a typo like
	// `alert_min_severity: erro` silently demotes filtering — the lookup
	// in severityAtLeast() falls to the default-0 branch, so every severity
	// (including info) compares "at or above" the unknown threshold. Empty
	// string is allowed: it means "use the default" (error).
	if g.AlertMinSeverity != "" {
		switch g.AlertMinSeverity {
		case "info", "warning", "error", "critical":
			// valid
		default:
			return fmt.Errorf("alert_min_severity %q is not recognized (must be one of: info, warning, error, critical; empty defaults to error)", g.AlertMinSeverity)
		}
	}

	return nil
}

// validateWebhookURL rejects URLs with unsafe schemes or addresses that could
// enable SSRF attacks. SEC-C4.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("scheme %q not allowed; only https:// is permitted (http:// is rejected to prevent cleartext exfil)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("host is empty")
	}
	// If the host is a literal IP, check it's not loopback/private/link-local.
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return fmt.Errorf("host %s is a loopback, private, or link-local address (SSRF blocked)", ip)
		}
	}
	// For hostnames, the webhook client's DialContext will re-check each
	// resolved IP at connection time (SEC-C4 DNS-rebind defense in webhook.go).
	return nil
}

// isBlockedIP reports whether an IP is in a range that webhooks should never
// target: loopback, private (RFC1918), link-local, multicast, unspecified,
// and IPv6 unique-local. SEC-C4.
func isBlockedIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast()
}

// isUnsafeLocalPath returns a human-readable reason if the given path is
// system-wide (drive root, SystemRoot, ProgramFiles, ProgramData) and
// should not be a mirror source. Empty string means OK to use.
//
// GAP-4 (panel review 2026-04-28). On Windows these paths recurse over
// millions of entries, blow past ReadDirectoryChangesW buffer caps, and
// are almost always misconfigurations. On other platforms `/` is
// similarly never a valid mirror source. Volumes (`E:\`, etc.) are also
// rejected — those are entire physical/logical drives.
func isUnsafeLocalPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "" // let downstream Stat surface the real problem
	}
	cleaned := filepath.Clean(abs)

	// Drive-root: `C:\`, `D:\`, etc. on Windows; `/` on POSIX.
	vol := filepath.VolumeName(cleaned)
	if vol != "" && cleaned == vol+string(filepath.Separator) {
		return "drive roots are not valid mirror sources (recurses over the entire volume; choose a sub-directory)"
	}
	if cleaned == "/" {
		return "filesystem root '/' is not a valid mirror source (choose a sub-directory)"
	}

	// Windows system directories. Compare case-insensitively.
	envVars := []string{"SystemRoot", "ProgramFiles", "ProgramFiles(x86)", "ProgramData", "windir"}
	cleanedLower := strings.ToLower(cleaned)
	for _, ev := range envVars {
		if val := os.Getenv(ev); val != "" {
			if cleanedLower == strings.ToLower(filepath.Clean(val)) {
				return "system directory %" + ev + "% is not a valid mirror source"
			}
		}
	}

	return ""
}

// isUnsafeRemote returns a non-empty reason if the rclone remote spec
// contains traversal segments after the colon. rclone remote syntax is
// `remote-name:relative/path`; `..` segments are either typos or
// attempts to escape the remote root. GAP-5 (panel review 2026-04-28).
func isUnsafeRemote(r string) string {
	colon := strings.IndexByte(r, ':')
	if colon < 0 {
		// No colon — likely a local-fs path. Fall through to existing
		// validators (we don't second-guess unprefixed forms here).
		return ""
	}
	tail := r[colon+1:]
	if tail == "" {
		return ""
	}
	// Walk segments separated by `/` or `\`. Reject any literal `..`.
	for _, sep := range []string{"/", "\\"} {
		for _, seg := range strings.Split(tail, sep) {
			if seg == ".." {
				return "remote path contains a '..' traversal segment after the ':'"
			}
		}
	}
	return ""
}

// validateNoLocalPathOverlap rejects configurations where one mirror's
// local_path is a strict prefix of another's (parent/child overlap).
// GAP-3 (panel review 2026-04-28).
func validateNoLocalPathOverlap(projects []Project) error {
	type entry struct {
		name string
		abs  string
	}
	resolved := make([]entry, 0, len(projects))
	for _, p := range projects {
		abs, err := filepath.Abs(p.LocalPath)
		if err != nil {
			continue
		}
		resolved = append(resolved, entry{name: p.Name, abs: filepath.Clean(abs)})
	}
	sep := string(filepath.Separator)
	for i, a := range resolved {
		for j, b := range resolved {
			if i == j {
				continue
			}
			// strings.HasPrefix is case-sensitive; on Windows local paths
			// are case-insensitive. Compare lowercase to catch typo'd cases.
			al, bl := strings.ToLower(a.abs), strings.ToLower(b.abs)
			if al == bl {
				// Same path; the BUG-1 path covers same-name dup, but two
				// mirrors with different names AND same path is also wrong.
				return fmt.Errorf("mirrors %q and %q resolve to the same local_path %q", a.name, b.name, a.abs)
			}
			if strings.HasPrefix(bl, al+sep) {
				return fmt.Errorf("mirror %q local_path %q is a parent of mirror %q local_path %q (overlapping mirrors double-sync every file under the child)", a.name, a.abs, b.name, b.abs)
			}
		}
	}
	return nil
}

// rcloneExtraFlagDenylist is the set of rclone flag names that smirror
// refuses to accept in `rclone_extra_flags` (global or per-mirror).
// Each is rejected because it changes WHAT rclone executes vs HOW a
// transfer behaves — see GAP-1 (panel review 2026-04-28).
//
// Categories:
//   - --rc, --rc-*       : exposes a control-plane HTTP listener
//   - --log-file         : redirects log output to an arbitrary file
//                          (under service mode this is arbitrary-file-write
//                          as LocalSystem)
//   - --log-format       : same vector via formatted log lines
//   - --config           : swaps out the rclone config; combined with the
//                          ability to write a malicious rclone.conf this
//                          pivots the entire backend
//   - --password-command : invokes a shell command rclone trusts to
//                          produce a password; arbitrary command exec
//   - --ask-password     : prompts on stderr; broken in service mode
//                          and a UI-injection vector in foreground
//
// We intentionally use prefix matching for `--rc` so `--rc-addr`,
// `--rc-no-auth`, etc. are all caught.
var rcloneExtraFlagDenylist = struct {
	exact   map[string]bool
	prefix  []string
}{
	exact: map[string]bool{
		"--log-file":         true,
		"--log-format":       true,
		"--config":           true,
		"--password-command": true,
		"--ask-password":     true,
	},
	prefix: []string{"--rc"}, // matches --rc, --rc-addr, --rc-no-auth, --rcfile, etc.
}

// validateRcloneExtraFlags rejects any flag in the denylist. `where` is
// a human-readable origin label ("global" or `mirror "name"`) included in
// the error message. Both separate-form (`--flag value`) and `=`-form
// (`--flag=value`) are caught.
//
// PR-S6 (panel review pre-release 2026-04-28): the denylist match is
// done after a strict ASCII check on the flag name. rclone's flag
// namespace is entirely ASCII; a non-ASCII glyph in the flag name (e.g.
// Cyrillic 'с' U+0441 standing in for ASCII 'c' in `--rc`) is either a
// configuration typo or a deliberate denylist-bypass attempt via Unicode
// confusables. We reject before prefix matching so `--rс` (Cyrillic 'с')
// can't slip past the `--rc` prefix check.
func validateRcloneExtraFlags(where string, flags []string) error {
	for _, raw := range flags {
		if !strings.HasPrefix(raw, "--") {
			continue
		}
		name := raw
		if eq := strings.Index(raw, "="); eq > 0 {
			name = raw[:eq]
		}
		// PR-S6: ASCII-only check on flag name. r > 127 catches every
		// non-ASCII byte, including the BMP confusables (Cyrillic а/с/о/р/у,
		// Greek ο/α, fullwidth forms, etc.) and non-BMP characters.
		for _, r := range name {
			if r > 127 {
				return fmt.Errorf("%s rclone_extra_flags: %q contains a non-ASCII character in the flag name (rclone flag names are ASCII; non-ASCII glyphs are rejected to block confusable-lookalike bypass of the denylist)", where, raw)
			}
		}
		if rcloneExtraFlagDenylist.exact[name] {
			return fmt.Errorf("%s rclone_extra_flags: %q is not allowed (denylist; this flag changes what rclone executes — see docs/SECURITY.md GAP-1)", where, name)
		}
		for _, p := range rcloneExtraFlagDenylist.prefix {
			// Any flag whose name starts with `--rc` is rejected. This
			// catches `--rc`, `--rc-addr`, `--rc-no-auth`, `--rcfile`,
			// `--rcjob-expire-duration`, etc. The rclone CLI namespace
			// reserves the `--rc*` prefix for the remote-control plane,
			// so there are no false positives among rclone's other flags.
			if strings.HasPrefix(name, p) {
				return fmt.Errorf("%s rclone_extra_flags: %q is not allowed (denylist; --rc* flags expose an unauthenticated control plane — see docs/SECURITY.md GAP-1)", where, name)
			}
		}
	}
	return nil
}

// FindProject returns the project config for the given name, or nil.
func (g *Global) FindProject(name string) *Project {
	for i := range g.Projects {
		if g.Projects[i].Name == name {
			return &g.Projects[i]
		}
	}
	return nil
}

// ProjectNames returns a list of all project names.
func (g *Global) ProjectNames() []string {
	names := make([]string, len(g.Projects))
	for i, p := range g.Projects {
		names[i] = p.Name
	}
	return names
}

// MirrorFingerprint returns a short hex hash of the mirror configuration
// (names, paths, remotes). Used by SM-122 to detect when the on-disk config
// has changed while a running instance still uses the old config.
func (g *Global) MirrorFingerprint() string {
	var parts []string
	for _, p := range g.Projects {
		parts = append(parts, fmt.Sprintf("%s|%s|%s", p.Name, p.LocalPath, p.Remote))
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return fmt.Sprintf("%x", h[:8])
}

// friendlyYAMLError wraps a yaml.v3 unmarshal error with a user-facing
// explanation for the most common config-shape mistakes. The raw error is
// always preserved via %w so callers can still inspect it.
func friendlyYAMLError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "cannot unmarshal !!map into []config.Project"):
		return fmt.Errorf(`"mirrors:" must be a YAML list (each entry starts with "- "), not a map.
YAML infers the type of "mirrors:" from its first non-comment child — a child starting with "- " makes it a list, a child of the form "key: value" makes it a map. Commented-out lines don't count as structure.
Likely cause: every "- name: ..." entry is commented out, leaving a sibling field (e.g. "delete_policy:") indented under "mirrors:", which YAML then reads as a map value.
Fix: put sibling fields at the same indent as "mirrors:" (not deeper), or write an explicit empty list ("mirrors: []"). Example:
mirrors:
  - name: MyProject
    local_path: C:\path\to\project
    remote: "remote:dest"
delete_policy: delete
(raw: %w)`, err)
	case strings.Contains(msg, "cannot unmarshal !!seq into config.Global"):
		return fmt.Errorf(`config root must be a YAML mapping (key: value), not a list.
(raw: %w)`, err)
	}
	return fmt.Errorf("parsing config: %w", err)
}

func expandHome(path string) string {
	if len(path) > 1 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
