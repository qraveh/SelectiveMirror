package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

// Each bucket helper must be TOTAL: every input maps to exactly one
// ENUM value, including pathological inputs (negative, very large).
// These tests assert (a) every documented ENUM value is reachable,
// and (b) extreme inputs don't return out-of-domain strings.

func TestBucketMirrorCount_AllValues(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-5, "0"}, {0, "0"},
		{1, "1"},
		{2, "2-5"}, {5, "2-5"},
		{6, "6-20"}, {20, "6-20"},
		{21, "21+"}, {1_000_000, "21+"},
	}
	for _, c := range cases {
		if got := BucketMirrorCount(c.n); got != c.want {
			t.Errorf("BucketMirrorCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketAnomalyCount_AllValues(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "0"}, {0, "0"},
		{1, "1-5"}, {5, "1-5"},
		{6, "6-25"}, {25, "6-25"},
		{26, "26-100"}, {100, "26-100"},
		{101, "100+"}, {1_000_000, "100+"},
	}
	for _, c := range cases {
		if got := BucketAnomalyCount(c.n); got != c.want {
			t.Errorf("BucketAnomalyCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketSyncAttempts_AllValues(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{-1, "<100"}, {0, "<100"}, {99, "<100"},
		{100, "100-1k"}, {999, "100-1k"},
		{1_000, "1k-10k"}, {9_999, "1k-10k"},
		{10_000, "10k-100k"}, {99_999, "10k-100k"},
		{100_000, "100k+"}, {1_000_000_000, "100k+"},
	}
	for _, c := range cases {
		if got := BucketSyncAttempts(c.n); got != c.want {
			t.Errorf("BucketSyncAttempts(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketRestartCount_AllValues(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "0"}, {0, "0"},
		{1, "1-5"}, {5, "1-5"},
		{6, "6-25"}, {25, "6-25"},
		{26, "26-100"}, {100, "26-100"},
		{101, "100+"}, {1_000_000, "100+"},
	}
	for _, c := range cases {
		if got := BucketRestartCount(c.n); got != c.want {
			t.Errorf("BucketRestartCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketQueueDepth_AllValues(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "<100"}, {0, "<100"}, {99, "<100"},
		{100, "100-1k"}, {999, "100-1k"},
		{1_000, "1k-10k"}, {9_999, "1k-10k"},
		{10_000, "10k+"}, {1_000_000, "10k+"},
	}
	for _, c := range cases {
		if got := BucketQueueDepth(c.n); got != c.want {
			t.Errorf("BucketQueueDepth(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketDeadLetterCount_AllValues(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{-1, "0"}, {0, "0"},
		{1, "1-10"}, {10, "1-10"},
		{11, "11-100"}, {100, "11-100"},
		{101, "100+"}, {1_000_000, "100+"},
	}
	for _, c := range cases {
		if got := BucketDeadLetterCount(c.n); got != c.want {
			t.Errorf("BucketDeadLetterCount(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestBucketStateDBSize_PathBased(t *testing.T) {
	// Missing file → ENUM-valid default <10MB (FINDING 2).
	if got := BucketStateDBSize("/nonexistent/path/should-not-exist.db"); got != "<10MB" {
		t.Errorf("BucketStateDBSize on missing file = %q, want %q", got, "<10MB")
	}

	// Tiny file → <10MB.
	dir := t.TempDir()
	tiny := filepath.Join(dir, "tiny.db")
	_ = os.WriteFile(tiny, []byte("hello"), 0o600)
	if got := BucketStateDBSize(tiny); got != "<10MB" {
		t.Errorf("BucketStateDBSize on tiny file = %q, want %q", got, "<10MB")
	}
}
