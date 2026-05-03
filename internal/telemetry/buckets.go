// Bucket helpers for v2 telemetry payloads.
//
// Every numeric value the wire payload carries is bucketed (PRIVACY.md
// "Bucketization mandatory for any numeric field"). Bucket helpers
// turn a count into the matching ENUM-valid string for the
// rollup-table column. Each helper is total: every input maps to
// exactly one ENUM value, including pathological inputs (negative,
// very large).
//
// Centralized here so both the inspect path (cmd/smirror/cmd_telemetry.go)
// and the submit path (internal/telemetry/install_events.go) emit the
// same buckets for the same inputs. Drift between inspect and submit
// would mean `inspect` lies about what would be sent.

package telemetry

import (
	"os"
)

// BucketMirrorCount maps a project count to mirror_count_bucket.
// ENUM domain: "0" / "1" / "2-5" / "6-20" / "21+"
func BucketMirrorCount(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n == 1:
		return "1"
	case n <= 5:
		return "2-5"
	case n <= 20:
		return "6-20"
	default:
		return "21+"
	}
}

// BucketStateDBSize maps the state DB file size to state_db_size_bucket.
// ENUM domain: "<10MB" / "10-100MB" / "100MB-1GB" / "1GB+"
// Returns "<10MB" for unreadable / nonexistent files (defensive default
// matches "fresh install with no state").
func BucketStateDBSize(stateDBPath string) string {
	info, err := os.Stat(stateDBPath)
	if err != nil {
		return "<10MB"
	}
	mb := info.Size() / (1024 * 1024)
	switch {
	case mb < 10:
		return "<10MB"
	case mb < 100:
		return "10-100MB"
	case mb < 1024:
		return "100MB-1GB"
	default:
		return "1GB+"
	}
}

// BucketAnomalyCount — reliability_snapshot bucket. Deferred-helper
// for v1.0.x; the install_events writer doesn't call this today.
// ENUM domain: "0" / "1-5" / "6-25" / "26-100" / "100+"
func BucketAnomalyCount(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 5:
		return "1-5"
	case n <= 25:
		return "6-25"
	case n <= 100:
		return "26-100"
	default:
		return "100+"
	}
}

// BucketSyncAttempts — reliability_snapshot bucket. Deferred-helper
// for v1.0.x.
// ENUM domain: "<100" / "100-1k" / "1k-10k" / "10k-100k" / "100k+"
func BucketSyncAttempts(n int64) string {
	switch {
	case n < 100:
		return "<100"
	case n < 1_000:
		return "100-1k"
	case n < 10_000:
		return "1k-10k"
	case n < 100_000:
		return "10k-100k"
	default:
		return "100k+"
	}
}

// BucketRestartCount — reliability_snapshot bucket. Deferred-helper
// for v1.0.x.
// ENUM domain: "0" / "1-5" / "6-25" / "26-100" / "100+"
func BucketRestartCount(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 5:
		return "1-5"
	case n <= 25:
		return "6-25"
	case n <= 100:
		return "26-100"
	default:
		return "100+"
	}
}

// BucketQueueDepth — reliability_snapshot bucket. Deferred-helper
// for v1.0.x.
// ENUM domain: "<100" / "100-1k" / "1k-10k" / "10k+"
func BucketQueueDepth(n int) string {
	switch {
	case n < 100:
		return "<100"
	case n < 1_000:
		return "100-1k"
	case n < 10_000:
		return "1k-10k"
	default:
		return "10k+"
	}
}

// BucketDeadLetterCount — reliability_snapshot bucket. Deferred-helper
// for v1.0.x.
// ENUM domain: "0" / "1-10" / "11-100" / "100+"
func BucketDeadLetterCount(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 10:
		return "1-10"
	case n <= 100:
		return "11-100"
	default:
		return "100+"
	}
}
