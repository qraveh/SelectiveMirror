// Install-event submit pipeline — first_seen + upgrade.
//
// Closes FINDING 16 (round-5 validation memo, 2026-05-03) for two of
// the three deferred event types. Reliability_snapshot remains
// deferred to v1.0.x (its bucket dimensions need new counters in
// internal/sync + internal/watcher; that's separate scope).
//
// Trigger conditions:
//
//   first_seen — fires once per install, ever. Conditions:
//     1. Tier is Standard or Reliability (gate)
//     2. HasBuildKey() is true (-dev / unsigned builds skip with WARN)
//     3. State-DB meta `first_seen_at` is unset
//     4. The HTTP POST succeeds (server returns 200)
//   On success: SetMeta(first_seen_at, RFC3339-now). Subsequent
//   startups skip first_seen.
//
//   upgrade — fires when version transitions between runs. Conditions:
//     1. Tier is Standard or Reliability
//     2. HasBuildKey() is true
//     3. State-DB meta `last_recorded_version` is set AND differs
//        from this build's `version`
//     4. State-DB meta `first_seen_at` is set (so we have something
//        to compute days_since_first_seen_bucket against)
//     5. The HTTP POST succeeds
//   On success: SetMeta(last_recorded_version, current).
//   First run after upgrade: if (3) is unset, just write
//   last_recorded_version with no event (we have no prior to compare).
//
// Retry: try-once-per-startup. After 5 consecutive failed attempts
// per event type, write a dead-letter file under `<configdir>/
// telemetry-dead-letter/` and stop retrying. This is the
// FINDING-17-aware design: `Queue` (queue.go) remains scaffolding;
// the simpler retry-counter approach ships v1.0 without committing
// to the durable-queue surface area.
//
// Idempotency: the server's contribute() RPC UPSERTs into bucket
// rows by (rollup_date, event_name, ...full bucket key...). A
// successful POST followed by a state-DB write failure (rare;
// would mean SQLite I/O error) results in a duplicate first_seen
// at next startup — which contributes to the same bucket key on
// the same date. Net effect: the rollup count is +1 instead of +2
// for that install, within k=5 noise. Acceptable.
//
// Concurrency: the daemon's single-instance lock guarantees one
// SendInstallEventsIfDue caller at a time. CLI commands DO NOT call
// this (FINDING-19-style "sync-now is a deterministic primitive";
// the user's daemon will fire on its next start).

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// State-DB meta keys this package writes. Documented as named
// constants per FINDING 24 (round-5: meta keys are schema by
// convention; they need a single source of truth).
const (
	MetaFirstSeenAt           = "first_seen_at"
	MetaLastRecordedVersion   = "last_recorded_version"
	MetaFirstSeenAttempts     = "first_seen_attempts"
	MetaUpgradeAttempts       = "upgrade_attempts"
	MetaLastTransitionNoticeShown = "last_transition_notice_shown"
)

// Maximum retry attempts before dead-lettering an event type.
const maxInstallEventAttempts = 5

// MetaStore is the minimal interface SendInstallEventsIfDue needs
// from the state DB. *state.Store satisfies this without import
// cycles. SetMeta is best-effort (the caller doesn't fail the
// daemon startup on meta-write errors).
type MetaStore interface {
	GetMeta(key string) (string, error)
	SetMeta(key, value string) error
}

// SendOptions tunes one SendInstallEventsIfDue call. Tests inject
// a custom Endpoint or HTTPClient; production passes the zero value.
type SendOptions struct {
	Endpoint   string                  // override SMIRROR_TELEMETRY_ENDPOINT
	HTTPClient any                     // *http.Client; threaded through to Contribute
	Now        func() time.Time        // injectable wallclock for tests
	DeadLetterDir string               // if empty, derives from configDir
}

// SendInstallEventsIfDue is the daemon-startup entry point. Returns
// nil on success (events sent or correctly skipped); returns an
// error only for non-recoverable cases the caller might want to
// log. Any returned error is informational — the daemon does NOT
// halt on this.
//
// Caller pattern (cmd/smirror/main.go::cmdStart):
//
//   go func() {
//       ctx, cancel := context.WithTimeout(daemonCtx, 30*time.Second)
//       defer cancel()
//       if err := telemetry.SendInstallEventsIfDue(ctx, view, st, configDir, opts); err != nil {
//           slog.Debug("install-event submit returned error; will retry on next startup", "error", err)
//       }
//   }()
func SendInstallEventsIfDue(
	ctx context.Context,
	view SystemView,
	st MetaStore,
	tier Tier,
	configDir string,
	opts SendOptions,
) error {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}

	// Gate 1: tier. None never sends anything, by contract.
	if tier == TierNone {
		return nil
	}

	// Gate 2: build-key. -dev / CI-without-secret builds can't sign
	// telemetry. Skip with a WARN (FINDING-6 round-5: WARN level so
	// release-day grep catches a CI secret-rotation regression that
	// nuked the buildKey).
	if !HasBuildKey() {
		slog.Warn(
			"install-event submit skipped: this build has no buildKey injected " +
				"(SMIRROR_TELEMETRY_MASTER_KEY not set at build time, or local -dev build). " +
				"Tier is set; events will fire when a CI-signed binary runs.",
			"tier", string(tier),
		)
		return nil
	}

	// Gate 3: install_id must exist. cmdTelemetrySet writes it on
	// transition out of None, so this should always be set when we
	// get here. Defensive: skip if missing.
	if view.InstallID == "" {
		slog.Warn("install-event submit skipped: install_id is empty (state DB inconsistent?)")
		return nil
	}

	// Sequence: first_seen, then upgrade. upgrade depends on
	// first_seen_at being set, so first_seen MUST go first.
	firstErr := maybeSendFirstSeen(ctx, view, st, configDir, opts, now)
	upgradeErr := maybeSendUpgrade(ctx, view, st, configDir, opts, now)

	// Combine errors — both can happen in the same startup.
	switch {
	case firstErr != nil && upgradeErr != nil:
		return fmt.Errorf("first_seen: %w; upgrade: %v", firstErr, upgradeErr)
	case firstErr != nil:
		return firstErr
	case upgradeErr != nil:
		return upgradeErr
	default:
		return nil
	}
}

// maybeSendFirstSeen runs the first_seen branch. Writes
// first_seen_at on success; increments first_seen_attempts on
// failure; dead-letters after maxInstallEventAttempts.
func maybeSendFirstSeen(
	ctx context.Context,
	view SystemView,
	st MetaStore,
	configDir string,
	opts SendOptions,
	now func() time.Time,
) error {
	// Already sent?
	if existing, _ := st.GetMeta(MetaFirstSeenAt); existing != "" {
		return nil
	}

	// Past max attempts? (Dead-lettered already.)
	if attempts := readAttempts(st, MetaFirstSeenAttempts); attempts >= maxInstallEventAttempts {
		return nil
	}

	reportedAt := now().UTC().Format(time.RFC3339)
	payload := BuildInstallationPayload("first_seen", view, reportedAt, "", "")

	contribOpts := ContributeOptions{Endpoint: opts.Endpoint}
	err := Contribute(ctx, view.ClientVersion, payload, contribOpts)
	if err == nil {
		// Success: record the timestamp; clear attempts counter.
		_ = st.SetMeta(MetaFirstSeenAt, reportedAt)
		_ = st.SetMeta(MetaFirstSeenAttempts, "0")
		slog.Info("first_seen telemetry event sent", "client_version", view.ClientVersion)
		return nil
	}

	// Failure: increment attempts; dead-letter on max.
	attempts := readAttempts(st, MetaFirstSeenAttempts) + 1
	_ = st.SetMeta(MetaFirstSeenAttempts, strconv.Itoa(attempts))
	if attempts >= maxInstallEventAttempts {
		writeDeadLetter(configDir, opts, "first_seen", payload, err)
		slog.Warn("first_seen telemetry event dead-lettered after max attempts",
			"attempts", attempts, "last_error", err)
	} else {
		slog.Debug("first_seen telemetry event failed; will retry on next startup",
			"attempts", attempts, "error", err)
	}

	// Bubble certain errors so callers can short-circuit (network
	// down, no buildKey). Most errors return nil so the daemon
	// startup proceeds normally.
	if errors.Is(err, ErrNoBuildKey) {
		return err
	}
	return nil
}

// maybeSendUpgrade runs the upgrade branch. Compares
// last_recorded_version against the build's version; fires upgrade
// on any mismatch (including downgrade). On first run after
// first_seen lands, just writes last_recorded_version without
// firing upgrade (no prior to compare).
func maybeSendUpgrade(
	ctx context.Context,
	view SystemView,
	st MetaStore,
	configDir string,
	opts SendOptions,
	now func() time.Time,
) error {
	current := view.ClientVersion

	prior, _ := st.GetMeta(MetaLastRecordedVersion)
	if prior == "" {
		// First run with this binary at non-None tier. Just record.
		_ = st.SetMeta(MetaLastRecordedVersion, current)
		return nil
	}
	if prior == current {
		// No transition; nothing to do.
		return nil
	}

	// Past max attempts? (Dead-lettered already for THIS specific
	// transition. Reset the counter when prior changes again, so
	// a NEW transition gets a fresh budget.)
	if attempts := readAttempts(st, MetaUpgradeAttempts); attempts >= maxInstallEventAttempts {
		// Dead-lettered. Don't retry; but also DON'T update
		// last_recorded_version — that would cause us to silently
		// skip future transitions. Operator must clear the meta
		// key to retry.
		return nil
	}

	// Compute days_since_first_seen_bucket from first_seen_at.
	firstSeenStr, _ := st.GetMeta(MetaFirstSeenAt)
	if firstSeenStr == "" {
		// Should not happen — first_seen runs before upgrade in
		// SendInstallEventsIfDue. But if it does, fall through
		// without firing upgrade; first_seen will fire next time.
		return nil
	}
	firstSeen, parseErr := time.Parse(time.RFC3339, firstSeenStr)
	if parseErr != nil {
		// Corrupted meta key. Reset and skip this round.
		_ = st.SetMeta(MetaFirstSeenAt, "")
		return nil
	}
	daysBucket := ComputeDaysSinceFirstSeenBucket(firstSeen, now())

	reportedAt := now().UTC().Format(time.RFC3339)
	payload := BuildInstallationPayload("upgrade", view, reportedAt, prior, daysBucket)

	contribOpts := ContributeOptions{Endpoint: opts.Endpoint}
	err := Contribute(ctx, view.ClientVersion, payload, contribOpts)
	if err == nil {
		_ = st.SetMeta(MetaLastRecordedVersion, current)
		_ = st.SetMeta(MetaUpgradeAttempts, "0")
		slog.Info("upgrade telemetry event sent",
			"prior_version", prior, "client_version", current, "days_bucket", daysBucket)
		return nil
	}

	attempts := readAttempts(st, MetaUpgradeAttempts) + 1
	_ = st.SetMeta(MetaUpgradeAttempts, strconv.Itoa(attempts))
	if attempts >= maxInstallEventAttempts {
		writeDeadLetter(configDir, opts, "upgrade", payload, err)
		slog.Warn("upgrade telemetry event dead-lettered after max attempts",
			"attempts", attempts, "last_error", err)
	} else {
		slog.Debug("upgrade telemetry event failed; will retry on next startup",
			"attempts", attempts, "error", err)
	}
	if errors.Is(err, ErrNoBuildKey) {
		return err
	}
	return nil
}

// readAttempts reads an attempts counter, defaulting to 0 on any
// parse / read error. The state DB meta keys are TEXT; we store
// the int as decimal.
func readAttempts(st MetaStore, key string) int {
	v, err := st.GetMeta(key)
	if err != nil || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// writeDeadLetter persists a failed payload to disk so an operator
// can inspect it. One file per (event_kind, timestamp) pair under
// <configdir>/telemetry-dead-letter/. Does not block on errors —
// dead-lettering is itself a best-effort path.
//
// Format: JSON-shaped wrapper around the payload + the last error
// message. Permissions 0600 so other users on the host can't read
// the install_id (anonymous, but treat as a low-sensitivity
// identifier anyway).
func writeDeadLetter(
	configDir string,
	opts SendOptions,
	eventKind string,
	payload map[string]any,
	lastErr error,
) {
	dir := opts.DeadLetterDir
	if dir == "" {
		if configDir != "" {
			dir = filepath.Join(configDir, "telemetry-dead-letter")
		} else {
			dir = filepath.Join(os.TempDir(), "smirror-telemetry-dead-letter")
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Debug("dead-letter dir mkdir failed", "dir", dir, "error", err)
		return
	}
	ts := time.Now().UTC().Format("20060102-150405")
	fname := fmt.Sprintf("%s-%s.json", eventKind, ts)
	path := filepath.Join(dir, fname)

	// Build a small wrapper. CanonicalJSON is overkill here (the file
	// is for human inspection, not HMAC); use the standard library.
	wrapper := map[string]any{
		"event_kind":  eventKind,
		"payload":     payload,
		"last_error":  lastErr.Error(),
		"written_at":  time.Now().UTC().Format(time.RFC3339),
		"max_attempts": maxInstallEventAttempts,
	}
	data, err := CanonicalJSON(wrapper)
	if err != nil {
		// Couldn't serialize. Drop without further work; this is
		// best-effort.
		return
	}
	_ = os.WriteFile(path, []byte(data), 0o600)
}

// ShouldShowTransitionNotice reports whether the daemon should
// emit a one-line stderr notice on this startup informing the user
// that install events are now actually flowing (the FINDING-22
// light-touch consent path). Returns true exactly once per install
// upgrade-to-events-shipping (tracked by the
// MetaLastTransitionNoticeShown meta key + a marker version).
//
// Marker: "first-shipped-at-0.9.10x". When the marker is unset and
// tier != None, return true and write the marker. Subsequent
// startups skip the notice.
func ShouldShowTransitionNotice(st MetaStore, tier Tier) bool {
	if tier == TierNone {
		return false
	}
	v, _ := st.GetMeta(MetaLastTransitionNoticeShown)
	if v != "" {
		return false
	}
	_ = st.SetMeta(MetaLastTransitionNoticeShown, "shown-at-0.9.10x")
	return true
}

// TransitionNoticeMessage is the operator-facing string the daemon
// prints on the one-time first-startup-after-events-shipping. Kept
// here so cmd/smirror and tests share the exact wording.
const TransitionNoticeMessage = "Telemetry update: install_census events (first_seen, upgrade) are " +
	"now sent at this tier. See `smirror telemetry policy` for details, or " +
	"`smirror telemetry none` to opt out."
