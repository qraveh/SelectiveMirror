// Package config handles YAML configuration loading and validation.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
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
	DeleteIgnore     DeletePolicy = "ignore"     // default: do nothing on remote
	DeleteDelete     DeletePolicy = "delete"     // immediately delete remote file
	DeleteMirror     DeletePolicy = "mirror"     // deprecated alias for "delete"
	DeleteQuarantine DeletePolicy = "quarantine" // move remote to .quarantine/
)

// Global holds the complete application configuration.
type Global struct {
	Projects           []Project    `yaml:"mirrors"`
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
	case DeleteDelete:
		return DeleteDelete
	case DeleteMirror:
		slog.Warn("delete_policy 'mirror' is deprecated, use 'delete' instead")
		return DeleteDelete
	case DeleteQuarantine:
		return DeleteQuarantine
	default:
		return DeleteIgnore
	}
}

// DeletePolicy returns the parsed global delete policy (defaults to "ignore").
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

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
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
		return fmt.Errorf("no mirrors defined in config")
	}

	names := make(map[string]bool)
	for i, p := range g.Projects {
		if p.Name == "" {
			return fmt.Errorf("mirror[%d]: name is required", i)
		}
		if names[p.Name] {
			return fmt.Errorf("mirror[%d]: duplicate name %q", i, p.Name)
		}
		names[p.Name] = true

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
