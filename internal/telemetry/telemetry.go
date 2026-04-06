// Package telemetry provides opt-in anonymous usage analytics and update checking.
//
// Privacy guarantees:
//   - No PII (no usernames, machine names, IP addresses stored)
//   - No file names, paths, or remote URLs
//   - Opt-in only: telemetry is disabled by default (telemetry: false)
//   - ZERO outbound network traffic when disabled — no pings, no checks, nothing
//   - All data is anonymous and aggregated
//   - Users can disable at any time via config
//
// Data collected (when enabled):
//   - smirror version, OS version, architecture
//   - mirror count, backend types (e.g. "gdrive", "s3")
//   - files synced count, error count, uptime
//   - installation method (msi, zip, manual)
package telemetry

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Endpoint is the telemetry server URL. Override for testing.
var Endpoint = "https://telemetry.selectivemirror.dev/v1/report"

// UpdateEndpoint is the update check URL (GitHub API).
const UpdateEndpoint = "https://api.github.com/repos/qraveh/SelectiveMirror/releases/latest"

// Report is the anonymous telemetry payload sent periodically.
type Report struct {
	// Identity (anonymous)
	InstallID string `json:"install_id"` // random UUID, generated once on first run

	// Version info
	Version  string `json:"version"`
	GoVer    string `json:"go_version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	OSDetail string `json:"os_detail,omitempty"` // e.g. "Windows 11 23H2"

	// Usage stats
	MirrorCount   int      `json:"mirror_count"`
	BackendTypes  []string `json:"backend_types"`  // e.g. ["gdrive", "s3"]
	FilesSynced   int64    `json:"files_synced"`
	SyncErrors    int64    `json:"sync_errors"`
	BytesUploaded int64    `json:"bytes_uploaded"`
	UptimeSeconds int64    `json:"uptime_seconds"`
	Mode          string   `json:"mode"` // "foreground" or "service"

	// Configuration (non-identifying)
	DeletePolicy   string `json:"delete_policy"`
	SyncWorkers    int    `json:"sync_workers"`
	DynamicDebounce bool  `json:"dynamic_debounce"` // any mirror uses dynamic debounce

	// Timestamps
	ReportedAt string `json:"reported_at"`
}

// ReleaseInfo holds version information from GitHub releases.
type ReleaseInfo struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	Assets      []Asset   `json:"assets"`
}

// Asset represents a downloadable file in a release.
type Asset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
	DownloadCount      int    `json:"download_count"`
}

// Client handles telemetry reporting and update checking.
type Client struct {
	installID  string
	version    string
	httpClient *http.Client
	log        *slog.Logger
	mu         sync.Mutex
	lastReport time.Time
}

// NewClient creates a telemetry client.
func NewClient(installID, version string) *Client {
	return &Client{
		installID: installID,
		version:   version,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		log: slog.Default().With("component", "telemetry"),
	}
}

// SendReport sends an anonymous telemetry report. Non-blocking: errors are logged, not returned.
func (c *Client) SendReport(ctx context.Context, report Report) {
	c.mu.Lock()
	// Rate limit: at most once per hour
	if time.Since(c.lastReport) < time.Hour {
		c.mu.Unlock()
		return
	}
	c.lastReport = time.Now()
	c.mu.Unlock()

	// Fill in standard fields
	report.InstallID = c.installID
	report.Version = c.version
	report.GoVer = runtime.Version()
	report.OS = runtime.GOOS
	report.Arch = runtime.GOARCH
	report.ReportedAt = time.Now().UTC().Format(time.RFC3339)

	data, err := json.Marshal(report)
	if err != nil {
		c.log.Debug("telemetry marshal error", "error", err)
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(data))
	if err != nil {
		c.log.Debug("telemetry request error", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", c.version))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.Debug("telemetry send error", "error", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.log.Debug("telemetry report sent")
	} else {
		c.log.Debug("telemetry server returned non-success", "status", resp.StatusCode)
	}
}

// CheckForUpdate checks GitHub releases for a newer version.
// Returns the latest release info, or nil if already up to date.
// Authenticates automatically: tries `gh auth token`, then GITHUB_TOKEN env,
// then falls back to unauthenticated (sufficient for public repos).
func (c *Client) CheckForUpdate(ctx context.Context) (*ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UpdateEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", c.version))
	req.Header.Set("Accept", "application/vnd.github+json")

	// Authenticate if possible (required for private repos)
	if token := GithubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No releases yet
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var release ReleaseInfo
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("parsing release info: %w", err)
	}

	return &release, nil
}

// GithubToken returns a GitHub API token if available.
// Priority: 1) gh CLI auth token, 2) GITHUB_TOKEN env var, 3) empty string.
func GithubToken() string {
	// Try gh CLI (most common for developers)
	if out, err := exec.CommandContext(context.Background(), "gh", "auth", "token").Output(); err == nil {
		if token := strings.TrimSpace(string(out)); token != "" {
			return token
		}
	}
	// Fall back to environment variable
	return os.Getenv("GITHUB_TOKEN")
}

// FindAsset returns the first asset whose name contains the given substring,
// or nil if no match. Useful for locating platform-specific downloads.
func FindAsset(assets []Asset, substring string) *Asset {
	for i := range assets {
		if strings.Contains(strings.ToLower(assets[i].Name), strings.ToLower(substring)) {
			return &assets[i]
		}
	}
	return nil
}

// ExtractBackendTypes extracts unique backend type names from remote strings.
// e.g. "gdrive:backup/foo" -> "gdrive", "s3:my-bucket/bar" -> "s3"
func ExtractBackendTypes(remotes []string) []string {
	seen := make(map[string]bool)
	var types []string
	for _, remote := range remotes {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) >= 1 && parts[0] != "" {
			name := parts[0]
			if !seen[name] {
				seen[name] = true
				types = append(types, name)
			}
		}
	}
	return types
}

// CompareVersions compares two semver-like version strings.
// Returns: -1 if a < b, 0 if a == b, 1 if a > b.
// Strips leading "v" and ignores pre-release suffixes for comparison.
func CompareVersions(a, b string) int {
	a = strings.TrimPrefix(a, "v")
	b = strings.TrimPrefix(b, "v")

	// Strip pre-release suffix (e.g. "-dev", "-rc1")
	aParts := strings.SplitN(a, "-", 2)
	bParts := strings.SplitN(b, "-", 2)
	aCore := aParts[0]
	bCore := bParts[0]

	aNums := parseVersion(aCore)
	bNums := parseVersion(bCore)

	// Compare numeric components
	maxLen := len(aNums)
	if len(bNums) > maxLen {
		maxLen = len(bNums)
	}
	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(aNums) {
			av = aNums[i]
		}
		if i < len(bNums) {
			bv = bNums[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}

	// Core versions equal. Pre-release < release.
	aHasPre := len(aParts) > 1
	bHasPre := len(bParts) > 1
	if aHasPre && !bHasPre {
		return -1 // a is pre-release, b is release
	}
	if !aHasPre && bHasPre {
		return 1 // a is release, b is pre-release
	}

	return 0
}

// parseVersion splits "1.2.3" into [1, 2, 3].
func parseVersion(s string) []int {
	parts := strings.Split(s, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		for _, c := range p {
			if c >= '0' && c <= '9' {
				n = n*10 + int(c-'0')
			} else {
				break
			}
		}
		nums = append(nums, n)
	}
	return nums
}

// GenerateInstallID creates a simple random-ish install ID using timestamp + random bytes.
// Not cryptographically secure — just needs to be unique enough for anonymous analytics.
func GenerateInstallID() string {
	// Use crypto/rand for better uniqueness
	b := make([]byte, 16)
	// Try crypto/rand first
	_, err := io.ReadFull(crand.Reader, b)
	if err != nil {
		// Fallback: use timestamp-based ID
		now := time.Now().UnixNano()
		return fmt.Sprintf("sm-%016x", now)
	}
	return fmt.Sprintf("sm-%x", b)
}
