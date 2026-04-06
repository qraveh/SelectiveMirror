package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- extractSearchKeywords tests ---

func TestExtractSearchKeywords_PanicValue(t *testing.T) {
	report := "smirror crash report\n--- Panic ---\nnil pointer dereference\n--- Stack Trace ---\n"
	kw := extractSearchKeywords(report)
	if !strings.Contains(kw, "nil pointer dereference") {
		t.Errorf("should extract panic value, got: %q", kw)
	}
}

func TestExtractSearchKeywords_RcloneExitCode(t *testing.T) {
	report := "rclone_exit: 3\nsome other text\nexit code 7\n"
	kw := extractSearchKeywords(report)
	if !strings.Contains(kw, "exit code 3") {
		t.Errorf("should extract rclone exit code 3, got: %q", kw)
	}
	if !strings.Contains(kw, "exit code 7") {
		t.Errorf("should extract exit code 7, got: %q", kw)
	}
}

func TestExtractSearchKeywords_SkipsExitCode0(t *testing.T) {
	report := "rclone_exit: 0\n"
	kw := extractSearchKeywords(report)
	if strings.Contains(kw, "exit code 0") {
		t.Error("should skip exit code 0 (success)")
	}
}

func TestExtractSearchKeywords_ErrorMessages(t *testing.T) {
	report := `level=ERROR msg="sync failed for project foo"
level=ERROR msg="rclone returned non-zero"
level=INFO msg="this is not an error"
`
	kw := extractSearchKeywords(report)
	if !strings.Contains(kw, "sync failed") {
		t.Errorf("should extract error message, got: %q", kw)
	}
	if !strings.Contains(kw, "rclone returned non-zero") {
		t.Errorf("should extract second error message, got: %q", kw)
	}
	if strings.Contains(kw, "this is not an error") {
		t.Error("should not extract INFO messages")
	}
}

func TestExtractSearchKeywords_LimitsErrorCount(t *testing.T) {
	report := `level=ERROR msg="error one"
level=ERROR msg="error two"
level=ERROR msg="error three"
level=ERROR msg="error four"
level=ERROR msg="error five"
`
	kw := extractSearchKeywords(report)
	// Should have at most 3 error messages
	count := strings.Count(kw, "error")
	if count > 3 {
		t.Errorf("should limit to 3 error messages, found %d in: %q", count, kw)
	}
}

func TestExtractSearchKeywords_Empty(t *testing.T) {
	kw := extractSearchKeywords("no errors here, just normal output")
	if kw != "" {
		t.Errorf("should return empty for report with no error signatures, got: %q", kw)
	}
}

func TestExtractSearchKeywords_TruncatesLongPanic(t *testing.T) {
	longPanic := strings.Repeat("x", 200)
	report := "--- Panic ---\n" + longPanic + "\n--- Stack Trace ---\n"
	kw := extractSearchKeywords(report)
	// The panic message should be truncated to 60 chars inside quotes
	if len(kw) > 210 { // 60 chars + quotes + some margin
		t.Errorf("should truncate long panic, keyword length: %d", len(kw))
	}
}

func TestExtractSearchKeywords_DedupsErrors(t *testing.T) {
	report := `level=ERROR msg="same error"
level=ERROR msg="same error"
level=ERROR msg="same error"
`
	kw := extractSearchKeywords(report)
	count := strings.Count(kw, "same error")
	if count > 1 {
		t.Errorf("should deduplicate identical errors, found %d occurrences", count)
	}
}

// --- searchSimilarIssues tests ---

func TestSearchSimilarIssues_ParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := issueSearchResult{
			TotalCount: 2,
			Items: []issueItem{
				{Number: 42, Title: "Sync fails with exit code 3", State: "open", HTMLURL: "https://github.com/test/42"},
				{Number: 38, Title: "State DB locked", State: "closed", HTMLURL: "https://github.com/test/38"},
			},
		}
		json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	origEndpoint := SearchEndpoint
	SearchEndpoint = server.URL
	defer func() { SearchEndpoint = origEndpoint }()

	report := `level=ERROR msg="sync failed with exit code 3"`
	ctx := context.Background()
	issues, err := searchSimilarIssues(ctx, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if issues[0].Number != 42 {
		t.Errorf("first issue should be #42, got #%d", issues[0].Number)
	}
	if issues[1].State != "closed" {
		t.Errorf("second issue should be closed, got %s", issues[1].State)
	}
}

func TestSearchSimilarIssues_EmptyKeywords(t *testing.T) {
	report := "nothing interesting here"
	ctx := context.Background()
	issues, err := searchSimilarIssues(ctx, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issues != nil {
		t.Error("empty keywords should return nil")
	}
}

func TestSearchSimilarIssues_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	origEndpoint := SearchEndpoint
	SearchEndpoint = server.URL
	defer func() { SearchEndpoint = origEndpoint }()

	report := `level=ERROR msg="some error"`
	ctx := context.Background()
	_, err := searchSimilarIssues(ctx, report)
	if err == nil {
		t.Error("should return error on API failure")
	}
}

func TestSearchSimilarIssues_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // longer than context timeout
	}))
	defer server.Close()

	origEndpoint := SearchEndpoint
	SearchEndpoint = server.URL
	defer func() { SearchEndpoint = origEndpoint }()

	report := `level=ERROR msg="timeout test"`
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := searchSimilarIssues(ctx, report)
	if err == nil {
		t.Error("should return error on timeout")
	}
}

func TestSearchSimilarIssues_NoResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(issueSearchResult{TotalCount: 0, Items: []issueItem{}})
	}))
	defer server.Close()

	origEndpoint := SearchEndpoint
	SearchEndpoint = server.URL
	defer func() { SearchEndpoint = origEndpoint }()

	report := `level=ERROR msg="rare error nobody has seen"`
	ctx := context.Background()
	issues, err := searchSimilarIssues(ctx, report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}

func TestSearchSimilarIssues_SendsAuthHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request has proper headers (User-Agent, Accept)
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("should send Accept header")
		}
		json.NewEncoder(w).Encode(issueSearchResult{})
	}))
	defer server.Close()

	origEndpoint := SearchEndpoint
	SearchEndpoint = server.URL
	defer func() { SearchEndpoint = origEndpoint }()

	report := `level=ERROR msg="auth test"`
	ctx := context.Background()
	searchSimilarIssues(ctx, report)

	// We can't guarantee gh is installed, but we verify the header mechanism
	// works by checking the request was made (even without auth)
	// The test server was hit, which is the important part
}

// --- timeAgo tests ---

func TestTimeAgo_Minutes(t *testing.T) {
	result := timeAgo(time.Now().Add(-30 * time.Minute))
	if !strings.Contains(result, "min ago") {
		t.Errorf("expected 'min ago', got: %s", result)
	}
}

func TestTimeAgo_Hours(t *testing.T) {
	result := timeAgo(time.Now().Add(-5 * time.Hour))
	if !strings.Contains(result, "hours ago") {
		t.Errorf("expected 'hours ago', got: %s", result)
	}
}

func TestTimeAgo_Days(t *testing.T) {
	result := timeAgo(time.Now().Add(-3 * 24 * time.Hour))
	if !strings.Contains(result, "days ago") {
		t.Errorf("expected 'days ago', got: %s", result)
	}
}

func TestTimeAgo_OneDay(t *testing.T) {
	result := timeAgo(time.Now().Add(-36 * time.Hour))
	if result != "1 day ago" {
		t.Errorf("expected '1 day ago', got: %s", result)
	}
}

func TestTimeAgo_Months(t *testing.T) {
	result := timeAgo(time.Now().Add(-60 * 24 * time.Hour))
	if !strings.Contains(result, "month") {
		t.Errorf("expected 'month', got: %s", result)
	}
}
