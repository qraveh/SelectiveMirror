// Payload builders for v2 telemetry events.
//
// Extracted from cmd/smirror/cmd_telemetry.go in 0.9.10x-dev. The
// inspect path (`smirror telemetry inspect`) and the install-event
// submit path (`internal/telemetry/install_events.go`) both call
// these. Keeping a single source for "what does a telemetry payload
// for event X look like" is essential for FINDING 3's invariant
// (the wire payload is exactly the rollup-bucket-key columns + the
// envelope; nothing more, nothing less).
//
// Each builder takes a SystemView snapshot — a small interface that
// the caller fills in from config + state DB + runtime — rather than
// taking a *config.Global directly. That avoids an import cycle:
// internal/telemetry can't import internal/config without making
// internal/config import-cycle-prone for everything that uses it.
// Callers in cmd/smirror translate from *config.Global to SystemView
// before invoking these builders.

package telemetry

import (
	"runtime"
	"strings"
	"time"
)

// SystemView is the structured inputs an installation-event payload
// builder needs from the calling environment. Decoupled from
// *config.Global so internal/telemetry stays import-free.
type SystemView struct {
	InstallID         string // anonymous install UUID; verified server-side then discarded
	ClientVersion     string // semver string of the running smirror.exe
	InstallMethod     string // "msi" / "winget" / "zip" / "manual" / "unknown"
	BackgroundMode    string // "foreground" / "service" / "task" / "unknown"
	MirrorCountBucket string // already-bucketed; one of: "0" / "1" / "2-5" / "6-20" / "21+"
	DeletePolicy      string // "ignore" / "delete" / "quarantine"
	HasHooks          bool
	HasFilters        bool
	HasAlertWebhook   bool
	HasBandwidthLimit bool
	RcloneVersion     string // e.g. "v1.73.5"; empty string is acceptable (server stores as-is)
}

// BuildInstallationPayload composes the wire payload for a
// `first_seen` or `upgrade` event. finding (review,
// 2026-05-02): the field set is EXACTLY the bucket-key columns of
// installation_daily_rollup plus the envelope. No `os_detail`, no
// install-specific extras — the server reads only what's listed
// here and would discard anything else.
//
// reportedAt is the timestamp the server records (becomes
// rollup_date after `(reported_at)::DATE`). Caller passes
// time.Now().UTC().Format(time.RFC3339) in production; tests can
// inject deterministic values.
//
// upgrade events MUST set priorVersion AND daysSinceFirstSeenBucket;
// first_seen events MUST leave both empty (the function omits them
// from the map when empty so the JSON shape matches what the
// inspect path produces).
func BuildInstallationPayload(
	eventName string,
	view SystemView,
	reportedAt string,
	priorVersion string,
	daysSinceFirstSeenBucket string,
) map[string]any {
	if view.InstallID == "" {
		// Defensive: never send an empty install_id; the server's
		// HMAC chain expects a stable value.
		view.InstallID = "(missing)"
	}
	if reportedAt == "" {
		reportedAt = time.Now().UTC().Format(time.RFC3339)
	}

	payload := map[string]any{
		// Envelope (HMAC + dispatch)
		"event_kind":     eventName,
		"schema_version": 1,
		"install_id":     view.InstallID,
		"reported_at":    reportedAt,
		"client_version": view.ClientVersion,

		// installation_daily_rollup bucket-key columns
		"install_method":      view.InstallMethod,
		"os_family":           strings.ToLower(runtime.GOOS),
		"mirror_count_bucket": view.MirrorCountBucket,
		"background_mode":     view.BackgroundMode,
		"delete_policy":       view.DeletePolicy,
		"has_hooks":           view.HasHooks,
		"has_filters":         view.HasFilters,
		"has_alert_webhook":   view.HasAlertWebhook,
		"has_bandwidth_limit": view.HasBandwidthLimit,
		"rclone_version":      view.RcloneVersion,
	}

	// upgrade-only dimensions. first_seen omits these; upgrade always
	// includes both. Server-side rollup uses `NULLS NOT DISTINCT`,
	// so an upgrade with an unknown prior_version (NULL) is still a
	// valid bucket key.
	if eventName == "upgrade" {
		payload["prior_version"] = priorVersion
		payload["days_since_first_seen_bucket"] = daysSinceFirstSeenBucket
	}

	return payload
}

// BuildReliabilitySnapshotPayload — DEFERRED to v1.0.x.
//
// The reliability_snapshot event ships its bucket dimensions
// (anomaly_count_bucket, sync_attempts_bucket, sync_failures_bucket,
// restart_count_bucket, max_queue_depth_bucket,
// dead_letter_count_bucket, state_db_size_bucket,
// most_common_anomaly_kind). Each requires a counter that doesn't
// exist in production today:
//
//   - anomaly_count    : iterate <configdir>/anomalies/*.jsonl + tally `kind`
//   - sync_attempts/failures : new state-DB lifetime counters incremented in
//                              internal/sync/sync.go on every sync
//   - restart_count    : new state-DB counter incremented at every daemon
//                        startup
//   - max_queue_depth  : new state-DB high-water-mark written by
//                        internal/watcher/watcher.go
//   - dead_letter      : queue.DeadLetterCount() (queue is FINDING-17
//                        scaffolding; not wired)
//   - state_db_size    : os.Stat(cfg.StateDB) — already in inspect
//
// The cmd_telemetry.go inspect path uses ENUM-valid defaults
// ("0" / "<100" / "<10MB") so the displayed payload doesn't lie
// about what the wire would carry — but those defaults are not
// what a long-running install should actually contribute. Shipping
// a real reliability_snapshot writer requires the counters above
// to be wired and exercised, which is a separate scope (panel
// estimate: +800 LOC + benchmarking).
//
// Until that writer lands, Reliability tier is functionally identical
// to Standard tier on the wire (both contribute first_seen + upgrade
// + bug_report). PRIVACY.md "Currently shipped vs. deferred" table
// marks reliability_snapshot as ❌ not yet implemented.
func BuildReliabilitySnapshotPayload(view SystemView, reportedAt string) map[string]any {
	// This builder is intentionally NOT called by install_events.go.
	// It is called only by the inspect path so users can preview the
	// shape. When the writer lands in a later release, it will
	// replace the ENUM-valid defaults below with real values from
	// the new counters listed in the doc comment above.
	if reportedAt == "" {
		reportedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"event_kind":     "reliability_snapshot",
		"schema_version": 1,
		"install_id":     view.InstallID,
		"reported_at":    reportedAt,
		"client_version": view.ClientVersion,

		// reliability_daily_rollup bucket-key columns. ENUM-valid
		// defaults today; replaced by real values in v1.0.x.
		"anomaly_count_bucket":     "0",
		"most_common_anomaly_kind": nil,
		"sync_attempts_bucket":     "<100",
		"sync_failures_bucket":     "<100",
		"restart_count_bucket":     "0",
		"max_queue_depth_bucket":   "<100",
		"dead_letter_count_bucket": "0",
		// state_db_size_bucket is set by the caller because computing
		// it requires a path the SystemView doesn't carry.
	}
}

// ComputeDaysSinceFirstSeenBucket buckets the gap between firstSeenAt
// and now. Returns the matching ENUM value or empty string when
// firstSeenAt is unparseable / unset.
//
// ENUM domain (telemetry-v2.sql days_since_first_seen_bucket):
//   "1-7" / "8-30" / "31-90" / "91-365" / ">365"
//
// Empty string is returned only for "we genuinely don't know yet"
// (no first_seen_at recorded) and the caller is responsible for not
// putting that into a payload — upgrade events depend on
// first_seen_at being set.
func ComputeDaysSinceFirstSeenBucket(firstSeenAt time.Time, now time.Time) string {
	if firstSeenAt.IsZero() {
		return ""
	}
	days := int(now.Sub(firstSeenAt).Hours() / 24)
	switch {
	case days <= 7:
		return "1-7"
	case days <= 30:
		return "8-30"
	case days <= 90:
		return "31-90"
	case days <= 365:
		return "91-365"
	default:
		return ">365"
	}
}
