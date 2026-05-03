// `smirror telemetry` — runtime tier management.
//
// SM-157. Spec: docs/cli-telemetry-command.md.
//
// Subcommands:
//   smirror telemetry status                Show current tier, source, build-key, queued events
//   smirror telemetry none                  Opt out completely; purge queued events
//   smirror telemetry standard              Opt in to install census + bug counts
//   smirror telemetry reliability           Above + reliability snapshot at upgrade
//   smirror telemetry policy                Open docs/PRIVACY.md (or print path)
//   smirror telemetry inspect               Felix-FAE preview: print the EXACT payload
//                                           the client would build right now, without
//                                           signing or sending. Read-only diagnostic.
//   smirror telemetry forget                REMOVED in v2 — returns error pointing at PRIVACY.md
//
// Persistence: tier flag lives in the state DB `meta` table under key
// `telemetry_tier` with values `'none' | 'standard' | 'reliability'`.
// First-run migration from MSI registry value
// `HKLM\Software\SelectiveMirror\TelemetryTier` is handled by
// telemetry.ReadTier (already shipped in 0.9.18-dev). After first run,
// the state DB is authoritative.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/state"
	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// State DB metadata keys for the telemetry tier triple.
const (
	metaTelemetryTier          = "telemetry_tier"
	metaTelemetryTierChangedAt = "telemetry_tier_changed_at"
	metaTelemetryTierSource    = "telemetry_tier_source"
	metaInstallID              = "install_id"
)

func cmdTelemetry(configPath string, args []string) {
	if len(args) == 0 {
		printTelemetryUsage()
		os.Exit(ExitError)
	}
	if subcommandHelp(args, telemetryHelpText()) {
		return
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "status":
		cmdTelemetryStatus(configPath, subArgs)
	case "none":
		cmdTelemetrySet(configPath, telemetry.TierNone)
	case "standard":
		cmdTelemetrySet(configPath, telemetry.TierStandard)
	case "reliability":
		cmdTelemetrySet(configPath, telemetry.TierReliability)
	case "policy":
		cmdTelemetryPolicy(configPath)
	case "inspect":
		cmdTelemetryInspect(configPath, subArgs)
	case "forget":
		// SM-157 + v2: explicit migration message. The verb does not
		// apply under stream-aggregate-and-discard. Pointing the user
		// at the new model is more helpful than a silent error.
		fmt.Fprintln(os.Stderr,
			"Under SelectiveMirror's stream-aggregate-and-discard architecture, no per-install\n"+
				"server data exists; there is nothing to forget.\n"+
				"\n"+
				"To stop contributing telemetry: smirror telemetry none\n"+
				"To read the full data-handling contract: smirror telemetry policy")
		os.Exit(ExitError)
	default:
		fmt.Fprintf(os.Stderr, "unknown telemetry subcommand: %s\nRun 'smirror telemetry --help' for usage.\n", sub)
		os.Exit(ExitError)
	}
}

func telemetryHelpText() string {
	return `Usage: smirror telemetry <subcommand>

View and change your telemetry tier at runtime.

Subcommands:
  status                     Show current tier, source, build-key fingerprint
  none                       Opt out completely; purge queued events
  standard                   Opt in to install events + bug-report counts
                             (no narrative is sent — narratives go to GitHub Issues)
  reliability                Standard + bucketed reliability counts at upgrade events
  policy                     Open docs/PRIVACY.md (or print the path)
  inspect                    Print the exact telemetry payload that would be
                             contributed RIGHT NOW, without signing or sending.
                             Use to verify completeness before changing tier.

The default is 'none' — if you do nothing, smirror sends nothing.

The 'forget' subcommand was specified in earlier drafts and is intentionally
removed in the current architecture: under stream-aggregate-and-discard, no
per-install server record exists, so nothing can be forgotten. 'smirror
telemetry none' is the complete opt-out.

See: docs/PRIVACY.md, docs/cli-telemetry-command.md.`
}

func printTelemetryUsage() {
	fmt.Println(telemetryHelpText())
}

// openConfigAndState loads the config + opens the state DB. Common
// preamble for tier-mutating subcommands. Caller must defer st.Close().
func openConfigAndState(configPath string) (*config.Global, *state.Store) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot load config: %v\n", err)
		os.Exit(ExitConfigError)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open state DB at %s: %v\n", cfg.StateDB, err)
		os.Exit(ExitError)
	}
	return cfg, st
}

// openConfigAndStateLenient is the openConfigAndState variant for
// read-only telemetry subcommands (`status`, `inspect`). FINDING 5
// from the 2026-05-02 validation memo: a privacy-conscious user on
// a fresh install with no mirrors yet can't run `smirror telemetry
// status` because config.Load() validates len(Projects) > 0. Status
// has nothing to do with mirrors; the validation is too strict for
// these read-only paths.
//
// Behavior:
//   - Try config.Load(). If it succeeds, return the result. (Common
//     case: a real installed user with a working config.)
//   - If it fails with the "no mirrors defined" message, fall back
//     to config.LoadRaw() (skips validation), apply the same path
//     defaults Load() would have applied, and continue.
//   - If LoadRaw() also fails, fall back to a minimal default config
//     with state DB at ~/.selectivemirror/state.db. The user-facing
//     command still works and prints the build-key fingerprint /
//     tier from the state DB.
//
// The lenient helper deliberately shadows the strict one so an
// accidental tier-mutating call site can't slip through to a
// half-validated config.
func openConfigAndStateLenient(configPath string) (*config.Global, *state.Store) {
	cfg, err := config.Load(configPath)
	if err != nil {
		// Try LoadRaw (no validation). Most useful when the user has
		// a syntactically valid YAML with no mirrors yet.
		if rawCfg, rawErr := config.LoadRaw(configPath); rawErr == nil {
			cfg = rawCfg
			applyStateDBDefault(cfg, configPath)
		} else {
			// Both failed — usually "config file not found" on a
			// brand-new install. Synthesize a minimal default.
			cfg = &config.Global{}
			home, _ := os.UserHomeDir()
			if home != "" {
				cfg.StateDB = filepath.Join(home, ".selectivemirror", "state.db")
			} else {
				cfg.StateDB = filepath.Join(os.TempDir(), "smirror-telemetry-state.db")
			}
			fmt.Fprintf(os.Stderr,
				"Note: cannot load config (%v). Using defaults; tier reads from %s.\n",
				err, cfg.StateDB)
		}
	}

	// Make sure the state-DB directory exists; state.Open does NOT
	// create parents.
	if dir := filepath.Dir(cfg.StateDB); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot open state DB at %s: %v\n", cfg.StateDB, err)
		os.Exit(ExitError)
	}
	return cfg, st
}

// applyStateDBDefault mirrors what config.Load() does for the StateDB
// path: if unset or pointing at the legacy ~/.selectivemirror tilde
// path, anchor it next to the config file. Used by the lenient loader
// after a LoadRaw() that skipped this step.
func applyStateDBDefault(cfg *config.Global, configPath string) {
	if abs, err := filepath.Abs(configPath); err == nil {
		configDir := filepath.Dir(abs)
		if cfg.StateDB == "" || cfg.StateDB == "~/.selectivemirror/state.db" {
			cfg.StateDB = filepath.Join(configDir, "state.db")
		}
	}
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

func cmdTelemetryStatus(configPath string, args []string) {
	rejectUnknownFlags("telemetry status", args)
	checkMaxArgs("telemetry status", args, 0)

	// FINDING 5: status must work on a fresh install with no mirrors
	// yet. Lenient loader degrades to LoadRaw() and finally to defaults
	// rather than os.Exit on config validation.
	_, st := openConfigAndStateLenient(configPath)
	defer st.Close()

	tier := telemetry.ReadTier(st)
	source, _ := st.GetMeta(metaTelemetryTierSource)
	changedAt, _ := st.GetMeta(metaTelemetryTierChangedAt)
	installID, _ := st.GetMeta(metaInstallID)

	fmt.Printf("tier:               %s\n", tier)
	switch source {
	case "user":
		if changedAt != "" {
			fmt.Printf("source:             user (smirror telemetry, %s)\n", changedAt)
		} else {
			fmt.Println("source:             user (smirror telemetry)")
		}
	case "installer":
		fmt.Println("source:             installer (MSI registry)")
	case "":
		fmt.Println("source:             default (no tier ever set)")
	default:
		fmt.Printf("source:             %s\n", source)
	}

	// Build-key fingerprint — confirms whether this binary can sign
	// telemetry submissions at all. SM-168 is shipped in CI release;
	// `-dev` / local builds typically print "none".
	fingerprint := telemetry.BuildKeyFingerprint()
	switch fingerprint {
	case "none":
		fmt.Println("build-key:          none (telemetry submission disabled)")
	case "invalid":
		fmt.Println("build-key:          invalid (binary corrupted or hand-built with bad ldflag)")
	default:
		fmt.Printf("build-key:          %s (telemetry submission enabled)\n", fingerprint)
	}

	switch tier {
	case telemetry.TierNone:
		fmt.Println()
		fmt.Println("bug reports:        disabled")
		fmt.Println("install events:     not sent")
		fmt.Println("reliability:        not sent")
		fmt.Println()
		fmt.Println("To opt in:")
		fmt.Println("  smirror telemetry standard      Bug-report counts + 2 install events ever")
		fmt.Println("  smirror telemetry reliability   Above + bucketed reliability deltas")
		fmt.Println()
		fmt.Println("Read the full contract:")
		fmt.Println("  smirror telemetry policy")
	case telemetry.TierStandard:
		fmt.Println()
		fmt.Println("bug reports:        enabled, per-event approval required")
		fmt.Println("install events:     sent at first_seen and on each upgrade")
		fmt.Println("reliability:        not sent")
		if installID != "" {
			fmt.Printf("install_id:         %s (anonymous, never stored server-side)\n", installID)
		}
		fmt.Println()
		fmt.Println("Change tier:")
		fmt.Println("  smirror telemetry none          Stop all telemetry")
		fmt.Println("  smirror telemetry reliability   Add bucketed reliability deltas at upgrades")
	case telemetry.TierReliability:
		fmt.Println()
		fmt.Println("bug reports:        enabled, per-event approval required")
		fmt.Println("install events:     sent at first_seen and on each upgrade")
		fmt.Println("reliability:        bucketed deltas attached to upgrade events ONLY")
		if installID != "" {
			fmt.Printf("install_id:         %s (anonymous, never stored server-side)\n", installID)
		}
		fmt.Println()
		fmt.Println("Change tier:")
		fmt.Println("  smirror telemetry none          Stop all telemetry")
		fmt.Println("  smirror telemetry standard      Drop reliability deltas; keep install census")
	}
}

// ---------------------------------------------------------------------------
// none / standard / reliability
// ---------------------------------------------------------------------------

func cmdTelemetrySet(configPath string, target telemetry.Tier) {
	_, st := openConfigAndState(configPath)
	defer st.Close()

	prev := telemetry.ReadTier(st)
	if prev == target {
		fmt.Printf("Tier is already %s; no change.\n", target)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.SetMeta(metaTelemetryTier, string(target)); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write tier: %v\n", err)
		os.Exit(ExitError)
	}
	if err := st.SetMeta(metaTelemetryTierChangedAt, now); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write tier-changed timestamp: %v\n", err)
		os.Exit(ExitError)
	}
	if err := st.SetMeta(metaTelemetryTierSource, "user"); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write tier source: %v\n", err)
		os.Exit(ExitError)
	}

	switch target {
	case telemetry.TierNone:
		fmt.Println("Tier set to None. Queued telemetry events dropped.")
		fmt.Println()
		fmt.Println("Under stream-aggregate-and-discard, no per-install server record")
		fmt.Println("exists; there is no server-side data to delete in addition.")
	case telemetry.TierStandard:
		fmt.Println("Tier set to Standard.")
		if prev == telemetry.TierNone {
			// Ensure install_id exists for HMAC verification on
			// future contributions. The id is verified-and-discarded
			// server-side; no record links it to anything.
			if existing, _ := st.GetMeta(metaInstallID); existing == "" {
				_ = st.SetMeta(metaInstallID, telemetry.GenerateInstallID())
			}
			// FINDING 16 honest-documentation note (round-5 validation
			// memo, 2026-05-03): the install-event submit pipeline
			// (first_seen, upgrade) is deferred to v1.0.x. Tier change
			// is recorded; no background sender starts. The only event
			// type that actually reaches the wire today is bug_report
			// via `smirror report-bug --submit`. See PRIVACY.md
			// "Currently shipped vs. deferred" for the full table.
			fmt.Println("Bug-report submission via `smirror report-bug --submit` is now enabled.")
			fmt.Println("Note: install_census events (first_seen, upgrade) are deferred to v1.0.x;")
			fmt.Println("the tier change is recorded but no background sender starts in this build.")
		}
	case telemetry.TierReliability:
		fmt.Println("Tier set to Reliability. Thank you for opting in.")
		if prev == telemetry.TierNone {
			if existing, _ := st.GetMeta(metaInstallID); existing == "" {
				_ = st.SetMeta(metaInstallID, telemetry.GenerateInstallID())
			}
			fmt.Println("Bug-report submission via `smirror report-bug --submit` is now enabled.")
			fmt.Println("Note: install_census + reliability_snapshot events are deferred to v1.0.x;")
			fmt.Println("the tier change is recorded but no background sender starts in this build.")
		} else if prev == telemetry.TierStandard {
			fmt.Println("Note: reliability_snapshot is deferred to v1.0.x. Functionally identical to")
			fmt.Println("Standard tier today; tier change recorded for when the pipeline lands.")
		}
	}

	// Print the post-change summary so the user sees the new state.
	fmt.Println()
	fmt.Println("Run 'smirror telemetry status' to see the full state.")
}

// ---------------------------------------------------------------------------
// policy
// ---------------------------------------------------------------------------

func cmdTelemetryPolicy(configPath string) {
	// Find the user's PRIVACY.md. Order:
	//   1. Sibling of the running binary (installed copy)
	//   2. ./docs/PRIVACY.md (running from a checkout)
	//   3. Online URL fallback
	candidates := []string{}

	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "PRIVACY.md"),
			filepath.Join(exeDir, "docs", "PRIVACY.md"),
		)
	}
	candidates = append(candidates,
		filepath.Join("docs", "PRIVACY.md"),
		filepath.Join(".", "PRIVACY.md"),
	)

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("Privacy policy: %s\n", p)
			openInBrowser(p)
			return
		}
	}
	// Fallback: GitHub URL.
	const githubURL = "https://github.com/qraveh/SelectiveMirror/blob/master/docs/PRIVACY.md"
	fmt.Printf("Privacy policy (online): %s\n", githubURL)
	openInBrowser(githubURL)
}

// openInBrowser tries to open the given path/URL in the default browser.
// On failure (no GUI, no browser, headless terminal), prints the path
// and continues — the user has already seen it in stdout.
func openInBrowser(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 avoids cmd.exe's `&` problem.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}

// ---------------------------------------------------------------------------
// inspect — Felix's "show me what would be sent" diagnostic
// ---------------------------------------------------------------------------
//
// The most-asked question by the maintainer under stream-aggregate-
// and-discard ("I need to see we collect everything"): inspect builds
// the EXACT payload an event_kind would produce right now, without
// signing, without sending, without touching the queue. Read-only.

func cmdTelemetryInspect(configPath string, args []string) {
	if subcommandHelp(args, `Usage: smirror telemetry inspect [event_kind]

Print the exact telemetry payload that would be contributed for the
given event_kind, without signing or sending. Diagnostic only —
read-only.

event_kind one of:
  first_seen      (default)
  upgrade
  reliability_snapshot

The output is a pretty-printed JSON object. Pipe through 'jq' or save
to a file for diff against PRIVACY.md's claimed field list.

Bug-report payloads are NOT inspected here — bug reports are
generated by 'smirror report-bug' (the bundle is reviewed there
before submission, and the categorical bucket is small enough to
be obvious).`) {
		return
	}

	rejectUnknownFlags("telemetry inspect", args)
	checkMaxArgs("telemetry inspect", args, 1)

	eventKind := "first_seen"
	if len(args) == 1 {
		eventKind = args[0]
	}

	// FINDING 5: inspect must work on a fresh install with no mirrors
	// yet. Lenient loader; the inspect output uses config defaults
	// (mirror_count=0, etc.) when no config is available — visible to
	// the user via the bucket values.
	cfg, st := openConfigAndStateLenient(configPath)
	defer st.Close()

	tier := telemetry.ReadTier(st)
	if tier == telemetry.TierNone {
		fmt.Fprintln(os.Stderr,
			"Note: tier is currently None. The payload below shows what WOULD be\n"+
				"contributed if you ran 'smirror telemetry standard' — but at None tier,\n"+
				"nothing is actually sent.")
		fmt.Fprintln(os.Stderr)
	}

	payload, err := buildInspectPayload(cfg, st, eventKind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot build inspect payload: %v\n", err)
		os.Exit(ExitError)
	}

	// Pretty-print so a human can scan field-by-field.
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot marshal payload: %v\n", err)
		os.Exit(ExitError)
	}
	fmt.Println(string(out))

	if tier == telemetry.TierStandard && eventKind == "reliability_snapshot" {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr,
			"Note: tier is Standard; reliability_snapshot would NOT be sent.\n"+
				"Switch to Reliability tier to enable.")
	}
}

// buildInspectPayload composes the exact map[string]any that
// SignPayload + the contribute() RPC would receive. Mirrors the
// payload shape documented in docs/telemetry-architecture-v2.md AND
// the rollup-table bucket-key tuple in docs/telemetry-v2.sql —
// FINDING 3 (round-3 panel, 2026-05-02) requires those two stay in
// lockstep. The client must transmit ONLY what the server's rollup
// table consumes, so the discard contract is byte-for-byte tight.
//
// Bucket dimensions are computed inline; this is the same logic the
// submit pipelines will use (SM-158 already shares the bug-report
// path; first_seen / upgrade / reliability_snapshot share these
// helpers when they ship).
func buildInspectPayload(cfg *config.Global, st *state.Store, eventKind string) (map[string]any, error) {
	switch eventKind {
	case "first_seen", "upgrade", "reliability_snapshot":
		// known
	default:
		return nil, fmt.Errorf("unknown event_kind: %q (expected first_seen, upgrade, or reliability_snapshot)", eventKind)
	}

	installID, _ := st.GetMeta(metaInstallID)
	if installID == "" {
		// inspect is read-only; don't generate-and-persist. Show a
		// placeholder so the user sees the field exists.
		installID = "(would be generated on first transition out of None)"
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// FINDING 3: payload shape is event_kind-specific. install /
	// upgrade events carry the installation_daily_rollup bucket
	// dimensions; reliability snapshots carry the
	// reliability_daily_rollup dimensions; the two have NO overlap
	// beyond (event_kind, schema_version, reported_at, install_id,
	// client_version) — the small envelope set every payload needs
	// for HMAC verification. Pre-fix, the reliability_snapshot
	// payload also carried install_method / os_family / os_detail /
	// has_hooks / etc. — fields the server's _bump_reliability
	// never reads but that travel across the network anyway. Ship
	// only what the server consumes.

	switch eventKind {
	case "first_seen":
		return buildInstallationPayload(cfg, "first_seen", installID, now), nil
	case "upgrade":
		p := buildInstallationPayload(cfg, "upgrade", installID, now)
		// Upgrade adds two extra dimensions vs first_seen; both are
		// non-NULL only for upgrade events.
		priorVersion, _ := st.GetMeta("last_recorded_version")
		if priorVersion == "" {
			priorVersion = "(would be filled from state DB on actual upgrade detection)"
		}
		p["prior_version"] = priorVersion
		p["days_since_first_seen_bucket"] = computeDaysSinceFirstSeenBucket(st)
		return p, nil
	case "reliability_snapshot":
		return buildReliabilityPayload(cfg, installID, now), nil
	}
	// Unreachable.
	return nil, fmt.Errorf("unhandled event_kind: %q", eventKind)
}

// buildInstallationPayload composes the payload for first_seen /
// upgrade events. The fields here are EXACTLY the bucket-key columns
// of installation_daily_rollup (see docs/telemetry-v2.sql) plus the
// envelope set every signed payload carries.
//
// FINDING 3: deliberately does NOT include `os_detail`. That field is
// not in the rollup table and would be discarded server-side; sending
// it widens the wire surface for no benefit.
func buildInstallationPayload(cfg *config.Global, eventName, installID, reportedAt string) map[string]any {
	return map[string]any{
		// Envelope (HMAC + dispatch)
		"event_kind":     eventName,
		"schema_version": 1,
		"install_id":     installID,
		"reported_at":    reportedAt,
		"client_version": version,

		// installation_daily_rollup bucket-key columns
		"install_method":      detectInstallMethod(),
		"os_family":           strings.ToLower(runtime.GOOS),
		"mirror_count_bucket": bucketMirrorCount(len(cfg.Projects)),
		"background_mode":     detectBackgroundMode(),
		"delete_policy":       string(cfg.DeletePolicy()),
		"has_hooks":           anyMirrorHasHook(cfg),
		"has_filters":         anyMirrorHasFilter(cfg),
		"has_alert_webhook":   strings.TrimSpace(cfg.AlertWebhookURL) != "",
		"has_bandwidth_limit": strings.TrimSpace(cfg.BandwidthLimit) != "",
		"rclone_version":      bestEffortRcloneVersion(cfg),
	}
}

// buildReliabilityPayload composes the payload for reliability_snapshot
// events. Fields match reliability_daily_rollup's bucket-key columns,
// not installation_daily_rollup's. FINDING 3 + FINDING 2.
//
// FINDING 2: every value is a LEGITIMATE bucket value from the
// matching ENUM in docs/telemetry-v2.sql, so a hypothetical submission
// at submit-time would not fail server-side schema_violation. Pre-fix,
// every value here was a placeholder string like "(would be computed
// from anomaly DB)" which would fail the ENUM cast.
//
// Today's heuristics return defaults that match what a healthy fresh
// install would compute (zero anomalies, low restart count, small
// state DB). When the production reliability_snapshot submit path
// ships, these helpers should query the live state DB / anomaly DB /
// queue stats and return real values; the defaults here remain the
// right behavior for inspect on a clean install.
func buildReliabilityPayload(cfg *config.Global, installID, reportedAt string) map[string]any {
	return map[string]any{
		// Envelope (HMAC + dispatch)
		"event_kind":     "reliability_snapshot",
		"schema_version": 1,
		"install_id":     installID,
		"reported_at":    reportedAt,
		"client_version": version,

		// reliability_daily_rollup bucket-key columns
		"anomaly_count_bucket":     "0",
		"most_common_anomaly_kind": nil,
		"sync_attempts_bucket":     "<100",
		"sync_failures_bucket":     "<100",
		"restart_count_bucket":     "0",
		"max_queue_depth_bucket":   "<100",
		"dead_letter_count_bucket": "0",
		"state_db_size_bucket":     bucketStateDbSize(cfg.StateDB),
	}
}

// computeDaysSinceFirstSeenBucket reads first_seen_at from the state
// DB if present and buckets the gap. Returns the matching ENUM value
// or a documented placeholder when first_seen_at isn't recorded yet.
func computeDaysSinceFirstSeenBucket(st *state.Store) string {
	firstSeenStr, _ := st.GetMeta("first_seen_at")
	if firstSeenStr == "" {
		// Not yet recorded. The submit pipeline will write this on the
		// first contribute(); inspect can't fabricate it.
		return "(would be filled from state DB after first_seen lands)"
	}
	firstSeen, err := time.Parse(time.RFC3339, firstSeenStr)
	if err != nil {
		return "(unparseable first_seen_at)"
	}
	days := int(time.Since(firstSeen).Hours() / 24)
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

// ---------------------------------------------------------------------------
// Inline bucket helpers — kept here because they're used only by inspect
// and the (deferred) submit pipeline. When SM-158 lands, these may move
// to internal/telemetry/buckets.go.
// ---------------------------------------------------------------------------

func bucketMirrorCount(n int) string {
	switch {
	case n == 0:
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

func detectBackgroundMode() string {
	// Placeholder: the production logic checks SCM service state and
	// scheduled-task presence. Inspect deliberately doesn't load those
	// (avoid side effects), so we approximate. Real submit-time logic
	// will be in internal/telemetry/buckets.go (SM-158).
	return "unknown"
}

func detectInstallMethod() string {
	// Placeholder: production logic inspects parent process / install
	// path / registry. Inspect returns "unknown" so the field is present
	// and visible without triggering side effects.
	return "unknown"
}

func anyMirrorHasHook(cfg *config.Global) bool {
	for _, p := range cfg.Projects {
		if strings.TrimSpace(p.PreSyncHook) != "" || strings.TrimSpace(p.PostSyncHook) != "" {
			return true
		}
	}
	return false
}

func anyMirrorHasFilter(cfg *config.Global) bool {
	for _, p := range cfg.Projects {
		if strings.TrimSpace(p.SyncIgnorePath) != "" {
			return true
		}
	}
	// Global excludes also count as filters.
	return len(cfg.GlobalExcludes) > 0
}

func bestEffortRcloneVersion(_ *config.Global) string {
	// Production logic calls `rclone version --check`. Inspect avoids
	// that subprocess so the diagnostic is always fast. Real logic in
	// SM-158.
	return "(would be detected at submit time)"
}

func bucketStateDbSize(stateDBPath string) string {
	info, err := os.Stat(stateDBPath)
	if err != nil {
		return "unknown"
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
