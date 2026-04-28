// Package telemetry provides the building blocks for SelectiveMirror's
// opt-in three-tier consent model and the always-allowed update check
// (post-tier-gate; see selfupdate.go).
//
// THIS FILE'S SCOPE has shrunk substantially since v0.9.4: the old
// always-on aggregated telemetry payload (Report struct, SendReport
// method, accumulated counters like FilesSynced/BytesUploaded/UptimeSeconds)
// has been REMOVED. Those fields are explicitly forbidden by
// docs/PRIVACY.md's forward-commitment ("no accumulated counts, no
// heartbeats"). They were never wired to a live endpoint, but their
// presence in the codebase contradicted the privacy contract and risked
// future re-enablement. SM-160 deletes them.
//
// What remains in THIS file is the GitHub-release polling client used
// by selfupdate. It has no overlap with consent-tier telemetry; selfupdate
// itself enforces the tier gate before calling CheckForUpdate (see
// cmd/smirror/selfupdate.go::checkForUpdateOnStartup).
//
// Privacy guarantees that survive the cleanup:
//   - No PII (no usernames, machine names, IP addresses)
//   - No file names, paths, or remote URLs
//   - At tier None (the default), CheckForUpdate is NOT called by smirror
//     — see selfupdate.go's tier gate. This file's CheckForUpdate is
//     usable via `smirror selfupdate --check`, a deliberate user action.
//   - All bug-report and install-event submission lives in the
//     three-tier subsystem; it does not flow through this file.
package telemetry

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// UpdateEndpoint is the update check URL (GitHub API). Override for testing
// or repo relocation.
var UpdateEndpoint = "https://api.github.com/repos/qraveh/SelectiveMirror/releases/latest"

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

// Client handles GitHub release polling for the selfupdate path. It is
// intentionally narrow: the legacy SendReport/Report path was removed in
// SM-160. Three-tier telemetry (bug reports, install events) is handled
// elsewhere in this package.
type Client struct {
	installID  string
	version    string
	httpClient *http.Client
	log        *slog.Logger
}

// NewClient creates a release-poll client. installID is retained as a
// constructor parameter for forward-compat with future tier-gated paths
// that need a stable per-install identifier; this struct's release-poll
// methods do not currently transmit it.
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

// CheckForUpdate checks GitHub releases for a newer version.
// Returns the latest release info, or nil if no releases exist yet.
// Authenticates automatically: tries `gh auth token`, then GITHUB_TOKEN env,
// then falls back to unauthenticated (sufficient for public repos).
//
// Tier gate: callers MUST verify telemetry tier permits network traffic
// before calling this. At tier None, the function should never run.
// `cmd/smirror/selfupdate.go::checkForUpdateOnStartup` enforces this.
// `smirror selfupdate --check` is a deliberate user action and bypasses
// the gate (the user explicitly invoked an update check).
func (c *Client) CheckForUpdate(ctx context.Context) (*ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UpdateEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", c.version))
	req.Header.Set("Accept", "application/vnd.github+json")

	// Authenticate if possible (required for private repos). Pass the
	// caller's ctx through so a hanging `gh auth token` can't outlast
	// the request budget. SM-174.
	if token := GithubTokenContext(ctx); token != "" {
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

// ghAuthTokenTimeout caps the time we'll spend shelling out to
// `gh auth token`. SM-174: a malicious or broken `gh.exe` earlier on
// PATH (or just one that hangs on a slow keyring backend) would
// otherwise stall every selfupdate / report-bug invocation
// indefinitely, since the HTTP client's timeout doesn't start until
// AFTER the subprocess returns.
//
// Two seconds is generous for a credential-helper read and short
// enough that a hang stays unnoticeable on the typical user-action
// path. Tests can override via withGhAuthTokenTimeout.
var ghAuthTokenTimeout = 2 * time.Second

// GithubTokenContext returns a GitHub API token if available, bounded
// by ctx. Priority:
//  1. gh CLI `gh auth token` (subject to ghAuthTokenTimeout)
//  2. GITHUB_TOKEN environment variable
//  3. empty string (caller falls back to unauthenticated)
//
// The function never returns an error: a token-lookup failure means
// "no token available" and is identical from the caller's standpoint
// to "user has no token configured."
func GithubTokenContext(ctx context.Context) string {
	// Apply the gh-specific timeout on top of whatever ctx the caller
	// brought. Whichever expires first wins.
	subCtx, cancel := context.WithTimeout(ctx, ghAuthTokenTimeout)
	defer cancel()

	cmd := exec.CommandContext(subCtx, "gh", "auth", "token")
	out, err := cmd.Output()
	if err == nil {
		if token := strings.TrimSpace(string(out)); token != "" {
			return token
		}
	}
	// Fall back to environment variable.
	return os.Getenv("GITHUB_TOKEN")
}

// GithubToken is the legacy entry point for callers that don't have a
// context handy. It uses a fresh background context bounded by
// ghAuthTokenTimeout, so a hanging gh.exe still can't stall longer
// than that.
func GithubToken() string {
	return GithubTokenContext(context.Background())
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

// GenerateInstallID creates a random anonymous install ID. Used by the
// three-tier telemetry subsystem when first persisting an install_id to
// the state DB on transition out of tier None.
func GenerateInstallID() string {
	b := make([]byte, 16)
	_, err := io.ReadFull(crand.Reader, b)
	if err != nil {
		// Fallback: use timestamp-based ID
		now := time.Now().UnixNano()
		return fmt.Sprintf("sm-%016x", now)
	}
	return fmt.Sprintf("sm-%x", b)
}
