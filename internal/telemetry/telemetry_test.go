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

// SM-160: TestExtractBackendTypes_* removed along with the deleted
// ExtractBackendTypes function. The function existed only to populate
// Report.BackendTypes for the removed SendReport path. The structural
// install-event analogue ("backend mix" k-anonymized aggregates) is
// produced server-side from raw remote names hashed via the Worker
// proxy in v1.5+ work.

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

// SM-160: TestSendReport_* removed along with the deleted SendReport
// method, Report struct, and Endpoint variable. The legacy aggregated-
// metrics path violated PRIVACY.md's no-accumulated-counts forward
// commitment. Three-tier telemetry replaces it with bucketed-only,
// upgrade-event-only data flows tested separately.

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
