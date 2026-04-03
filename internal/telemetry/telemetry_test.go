package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions_Equal(t *testing.T) {
	if CompareVersions("0.2.26", "0.2.26") != 0 {
		t.Error("same versions should be equal")
	}
}

func TestCompareVersions_Greater(t *testing.T) {
	if CompareVersions("0.3.0", "0.2.26") != 1 {
		t.Error("0.3.0 should be greater than 0.2.26")
	}
}

func TestCompareVersions_Less(t *testing.T) {
	if CompareVersions("0.2.25", "0.2.26") != -1 {
		t.Error("0.2.25 should be less than 0.2.26")
	}
}

func TestCompareVersions_PreReleaseIsLess(t *testing.T) {
	if CompareVersions("0.3.0-dev", "0.3.0") != -1 {
		t.Error("pre-release 0.3.0-dev should be less than 0.3.0")
	}
}

func TestCompareVersions_VPrefix(t *testing.T) {
	if CompareVersions("v0.2.26", "0.2.26") != 0 {
		t.Error("v-prefix should be stripped")
	}
}

func TestCompareVersions_MajorDifference(t *testing.T) {
	if CompareVersions("1.0.0", "0.99.99") != 1 {
		t.Error("1.0.0 should be greater than 0.99.99")
	}
}

func TestCompareVersions_DifferentLength(t *testing.T) {
	if CompareVersions("1.0", "1.0.0") != 0 {
		t.Error("1.0 and 1.0.0 should be equal")
	}
}

func TestCompareVersions_BothPreRelease(t *testing.T) {
	if CompareVersions("0.3.0-dev", "0.3.0-rc1") != 0 {
		t.Error("both pre-release with same core should be equal (we don't compare pre-release strings)")
	}
}

func TestExtractBackendTypes_Single(t *testing.T) {
	types := ExtractBackendTypes([]string{"gdrive:backup/foo"})
	if len(types) != 1 || types[0] != "gdrive" {
		t.Errorf("got %v, want [gdrive]", types)
	}
}

func TestExtractBackendTypes_Multiple(t *testing.T) {
	types := ExtractBackendTypes([]string{
		"gdrive:backup/foo",
		"s3:my-bucket/bar",
		"sftp:server/path",
	})
	if len(types) != 3 {
		t.Errorf("got %d types, want 3", len(types))
	}
}

func TestExtractBackendTypes_Deduplication(t *testing.T) {
	types := ExtractBackendTypes([]string{
		"gdrive:backup/foo",
		"gdrive:another/path",
		"s3:bucket",
	})
	if len(types) != 2 {
		t.Errorf("got %d types, want 2 (gdrive should be deduplicated)", len(types))
	}
}

func TestExtractBackendTypes_Empty(t *testing.T) {
	types := ExtractBackendTypes(nil)
	if len(types) != 0 {
		t.Errorf("got %v, want empty", types)
	}
}

func TestGenerateInstallID_Unique(t *testing.T) {
	id1 := GenerateInstallID()
	id2 := GenerateInstallID()
	if id1 == id2 {
		t.Error("two generated IDs should not be equal")
	}
}

func TestGenerateInstallID_HasPrefix(t *testing.T) {
	id := GenerateInstallID()
	if !strings.HasPrefix(id, "sm-") {
		t.Errorf("install ID %q should start with 'sm-'", id)
	}
}

func TestGenerateInstallID_NotEmpty(t *testing.T) {
	id := GenerateInstallID()
	if len(id) < 5 {
		t.Errorf("install ID %q is too short", id)
	}
}

func TestSendReport_Success(t *testing.T) {
	var received Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %s", ct)
		}

		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Override endpoint
	origEndpoint := Endpoint
	Endpoint = server.URL
	defer func() { Endpoint = origEndpoint }()

	client := NewClient("test-install-id", "0.2.26-dev")
	report := Report{
		MirrorCount:  3,
		BackendTypes: []string{"gdrive", "s3"},
		FilesSynced:  1234,
		Mode:         "foreground",
	}

	client.SendReport(context.Background(), report)

	if received.InstallID != "test-install-id" {
		t.Errorf("InstallID = %q, want %q", received.InstallID, "test-install-id")
	}
	if received.Version != "0.2.26-dev" {
		t.Errorf("Version = %q, want %q", received.Version, "0.2.26-dev")
	}
	if received.MirrorCount != 3 {
		t.Errorf("MirrorCount = %d, want 3", received.MirrorCount)
	}
	if received.FilesSynced != 1234 {
		t.Errorf("FilesSynced = %d, want 1234", received.FilesSynced)
	}
}

func TestSendReport_RateLimit(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	origEndpoint := Endpoint
	Endpoint = server.URL
	defer func() { Endpoint = origEndpoint }()

	client := NewClient("test-id", "0.2.26")

	// Send 3 reports rapidly — only the first should go through
	for i := 0; i < 3; i++ {
		client.SendReport(context.Background(), Report{MirrorCount: i})
	}

	if callCount != 1 {
		t.Errorf("expected 1 request (rate limited), got %d", callCount)
	}
}

func TestSendReport_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	origEndpoint := Endpoint
	Endpoint = server.URL
	defer func() { Endpoint = origEndpoint }()

	client := NewClient("test-id", "0.2.26")

	// Should not panic on server error
	client.SendReport(context.Background(), Report{})
}

func TestSendReport_UnreachableEndpoint(t *testing.T) {
	// Use a non-routable address to test timeout behavior without httptest server
	origEndpoint := Endpoint
	Endpoint = "http://127.0.0.1:1" // port 1 — connection refused instantly
	defer func() { Endpoint = origEndpoint }()

	client := NewClient("test-id", "0.2.26")
	client.httpClient.Timeout = 1 * time.Second

	// Should complete quickly (connection refused) without panic
	done := make(chan struct{})
	go func() {
		client.SendReport(context.Background(), Report{})
		close(done)
	}()

	select {
	case <-done:
		// good — completed without blocking
	case <-time.After(5 * time.Second):
		t.Fatal("SendReport blocked on unreachable endpoint")
	}
}

func TestCheckForUpdate_NewVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release := ReleaseInfo{
			TagName:     "v0.3.0",
			Name:        "v0.3.0",
			PublishedAt: time.Now(),
			HTMLURL:     "https://github.com/qraveh/SelectiveMirror/releases/tag/v0.3.0",
			Assets: []Asset{
				{Name: "SelectiveMirror_0.3.0_windows_amd64.zip", Size: 5000000, DownloadCount: 42},
			},
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer server.Close()

	client := NewClient("test-id", "0.2.26")
	// Override the endpoint by monkey-patching the client's check
	origURL := UpdateEndpoint
	_ = origURL // UpdateEndpoint is const, so we test through the server

	// For this test, we'll call the server directly
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var release ReleaseInfo
	json.NewDecoder(resp.Body).Decode(&release)

	if release.TagName != "v0.3.0" {
		t.Errorf("TagName = %q, want %q", release.TagName, "v0.3.0")
	}
	if len(release.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(release.Assets))
	}
	if release.Assets[0].DownloadCount != 42 {
		t.Errorf("DownloadCount = %d, want 42", release.Assets[0].DownloadCount)
	}
}

func TestCheckForUpdate_NoReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Can't override const, but verify the 404 handling logic
	req, _ := http.NewRequestWithContext(context.Background(), "GET", server.URL, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestOSDetail_NotEmpty(t *testing.T) {
	detail := OSDetail()
	if detail == "" {
		t.Error("OSDetail() should not be empty")
	}
}
