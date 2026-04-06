package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// SearchEndpoint is the GitHub Issues search API. Override for testing.
var SearchEndpoint = "https://api.github.com/search/issues"

// SearchRepo is the GitHub repo to search. Override for testing.
var SearchRepo = "qraveh/SelectiveMirror"

// issueSearchResult is the response from GitHub's search/issues API.
type issueSearchResult struct {
	TotalCount int         `json:"total_count"`
	Items      []issueItem `json:"items"`
}

// issueItem represents a single GitHub issue from search results.
type issueItem struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	HTMLURL   string    `json:"html_url"`
}

// searchSimilarIssues queries GitHub Issues for potential duplicates of the
// user's report. Returns up to 5 matching issues, or an empty slice.
func searchSimilarIssues(ctx context.Context, report string) ([]issueItem, error) {
	keywords := extractSearchKeywords(report)
	if keywords == "" {
		return nil, nil
	}

	query := fmt.Sprintf("repo:%s is:issue %s", SearchRepo, keywords)
	reqURL := SearchEndpoint + "?q=" + url.QueryEscape(query) + "&per_page=5&sort=updated&order=desc"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", version))
	req.Header.Set("Accept", "application/vnd.github+json")

	if token := telemetry.GithubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub search API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var result issueSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return result.Items, nil
}

// rcloneExitRe matches patterns like "rclone_exit: 3" or "exit code 3" in reports.
var rcloneExitRe = regexp.MustCompile(`(?:rclone_exit|exit code)[:\s]+(\d+)`)

// errorLineRe matches slog-style error lines: level=ERROR msg="..."
var errorLineRe = regexp.MustCompile(`level=ERROR\s+msg="([^"]+)"`)

// panicLineRe matches panic values in crash reports.
var panicLineRe = regexp.MustCompile(`(?m)^--- Panic ---\n(.+)`)

// extractSearchKeywords pulls error signatures from a diagnostic report
// to build a GitHub search query. Returns a space-separated keyword string.
func extractSearchKeywords(report string) string {
	var parts []string

	// 1. Panic value (highest signal)
	if m := panicLineRe.FindStringSubmatch(report); len(m) > 1 {
		panicMsg := strings.TrimSpace(m[1])
		// Take first meaningful chunk (up to 60 chars)
		if len(panicMsg) > 60 {
			panicMsg = panicMsg[:60]
		}
		parts = append(parts, fmt.Sprintf("%q", panicMsg))
	}

	// 2. rclone exit codes
	if matches := rcloneExitRe.FindAllStringSubmatch(report, 3); len(matches) > 0 {
		seen := map[string]bool{}
		for _, m := range matches {
			code := m[1]
			if code != "0" && !seen[code] {
				seen[code] = true
				parts = append(parts, fmt.Sprintf("\"exit code %s\"", code))
			}
		}
	}

	// 3. Error messages from log lines (up to 3)
	if matches := errorLineRe.FindAllStringSubmatch(report, 5); len(matches) > 0 {
		added := 0
		seen := map[string]bool{}
		for _, m := range matches {
			msg := m[1]
			// Normalize: take first 50 chars, lowercase for dedup
			if len(msg) > 50 {
				msg = msg[:50]
			}
			key := strings.ToLower(msg)
			if !seen[key] {
				seen[key] = true
				parts = append(parts, fmt.Sprintf("%q", msg))
				added++
				if added >= 3 {
					break
				}
			}
		}
	}

	// Limit total query length (GitHub search has limits)
	result := strings.Join(parts, " ")
	if len(result) > 200 {
		result = result[:200]
	}

	return result
}

// displayDupResults shows potential duplicate issues and lets the user choose.
// Returns 0 if user wants to submit new, or 1-N if user wants to view issue N.
func displayDupResults(issues []issueItem) int {
	fmt.Println()
	fmt.Println("  Possible matches:")
	for i, issue := range issues {
		age := timeAgo(issue.CreatedAt)
		fmt.Printf("    [%d] #%d  %s (%s, %s)\n", i+1, issue.Number, issue.Title, issue.State, age)
	}

	fmt.Println()
	fmt.Printf("  [1-%d] View existing issue  [n] Submit new report  [Enter] Submit new\n", len(issues))
	fmt.Print("  Choice: ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0 // non-interactive → submit new
	}
	line = strings.TrimSpace(line)

	if line == "" || line == "n" || line == "N" {
		return 0
	}

	// Parse number
	var choice int
	if _, err := fmt.Sscanf(line, "%d", &choice); err == nil {
		if choice >= 1 && choice <= len(issues) {
			return choice
		}
	}

	return 0
}

// timeAgo returns a human-readable relative time string.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		months := int(d.Hours() / 24 / 30)
		if months == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", months)
	}
}
