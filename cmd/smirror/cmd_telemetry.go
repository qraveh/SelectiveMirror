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
//   smirror telemetry inspect               Print the EXACT payload
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
	"context"
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
		// The earlier "bucketed deltas attached to upgrade events ONLY"
		// description was inaccurate: reliability_snapshot has no
		// production writer in v1.0.0 (not yet implemented). The
		// first_seen + upgrade payloads ship today; reliability_snapshot
		// does not. Status display below reflects the actual shipped
		// behavior, matching cmdTelemetrySet.
		fmt.Println("reliability:        identical to standard tier on the wire (the tier")
		fmt.Println("                    choice is recorded; reliability dimensions ship")
		fmt.Println("                    in a later release)")
		if installID != "" {
			fmt.Printf("install_id:         %s (anonymous, never stored server-side)\n", installID)
		}
		fmt.Println()
		fmt.Println("Change tier:")
		fmt.Println("  smirror telemetry none          Stop all telemetry")
		fmt.Println("  smirror telemetry standard      Same wire output today; preserves your choice")
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

	// DEFECT-1 fix: install_id must exist whenever target tier is
	// non-None, regardless of what prev is. The previous logic gated
	// install_id generation on `prev == TierNone`, which silently
	// failed when the registry tier was set non-None by another path
	// (e.g., MSI INSTALL_TELEMETRY_TIER property writes the registry
	// directly). In that case prev reads as Standard/Reliability,
	// install_id stays empty, and all subsequent telemetry submissions
	// silently no-op.
	//
	// Idempotent: only generates if state DB has no install_id yet.
	if target != telemetry.TierNone {
		if existing, _ := st.GetMeta(metaInstallID); existing == "" {
			_ = st.SetMeta(metaInstallID, telemetry.GenerateInstallID())
		}
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
			// The install-event submit pipeline ships in this build:
			// first_seen fires on the next `smirror start` (or service
			// start). reliability_snapshot is not yet implemented — see
			// PRIVACY.md "Currently shipped vs. deferred" for the
			// scope-of-shipped-behavior section.
			fmt.Println("Bug-report submission (`smirror report-bug --submit`) is now enabled.")
			fmt.Println("first_seen telemetry event will fire on the next `smirror start` or service start.")
		}
	case telemetry.TierReliability:
		fmt.Println("Tier set to Reliability. Thank you for opting in.")
		if prev == telemetry.TierNone {
			fmt.Println("Bug-report submission (`smirror report-bug --submit`) is now enabled.")
			fmt.Println("first_seen telemetry event will fire on the next `smirror start` or service start.")
			fmt.Println("Note: Reliability tier is identical to Standard on the wire today;")
			fmt.Println("reliability dimensions ship in a later release.")
		} else if prev == telemetry.TierStandard {
			fmt.Println("Note: Reliability is identical to Standard on the wire today; the")
			fmt.Println("tier change is recorded for when reliability dimensions ship.")
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
// inspect — the "show me what would be sent" diagnostic
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
// FINDING 3 requires those two stay in
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

	view := cfgToSystemView(cfg, st)
	if view.InstallID == "" {
		// inspect is read-only; don't generate-and-persist. Show a
		// placeholder so the user sees the field exists.
		view.InstallID = "(would be generated on first transition out of None)"
	}

	reportedAt := time.Now().UTC().Format(time.RFC3339)

	// FINDING 3: payload shape is event_kind-specific. install /
	// upgrade events carry the installation_daily_rollup bucket
	// dimensions; reliability snapshots carry the
	// reliability_daily_rollup dimensions; no overlap beyond the
	// envelope set. Inspect uses the same builders as the production
	// submit path (internal/telemetry/payloads.go) so what you see
	// here IS what would be sent.

	switch eventKind {
	case "first_seen":
		return telemetry.BuildInstallationPayload("first_seen", view, reportedAt, "", ""), nil
	case "upgrade":
		// Compute upgrade-specific dimensions from state DB.
		priorVersion, _ := st.GetMeta(telemetry.MetaLastRecordedVersion)
		if priorVersion == "" {
			priorVersion = "(would be filled from state DB on actual upgrade detection)"
		}
		daysBucket := computeInspectDaysSinceFirstSeenBucket(st)
		return telemetry.BuildInstallationPayload("upgrade", view, reportedAt, priorVersion, daysBucket), nil
	case "reliability_snapshot":
		// Reliability is not yet implemented in production — see
		// internal/telemetry/payloads.go::BuildReliabilitySnapshotPayload
		// doc comment. Inspect shows the shape; production submit
		// doesn't fire it yet.
		p := telemetry.BuildReliabilitySnapshotPayload(view, reportedAt)
		// state_db_size_bucket needs the path; the builder doesn't
		// carry it on SystemView.
		p["state_db_size_bucket"] = telemetry.BucketStateDBSize(cfg.StateDB)
		return p, nil
	}
	// Unreachable.
	return nil, fmt.Errorf("unhandled event_kind: %q", eventKind)
}

// fireInstallEventsAtStartup is the daemon-startup hook that
// invokes telemetry.SendInstallEventsIfDue. Called from a goroutine
// in cmdStart and serviceMain. Bounded by a 30-second context;
// daemon shutdown does NOT wait for completion (the goroutine is
// detached but self-canceling).
//
// FINDING 16 closure: this is the function that turns "install
// census documented but never sent" into "install census actually
// sent." Idempotent across restarts via state-DB meta keys
// (first_seen_at, last_recorded_version).
//
// On the first daemon-startup at a tier ∈ {Standard, Reliability}
// this also writes the one-time transition notice to stderr (FINDING
// R12/R14 light-touch consent: tell the user that install events
// are now flowing without forcing a re-prompt).
func fireInstallEventsAtStartup(cfg *config.Global, st *state.Store) {
	tier := telemetry.ReadTier(st)

	// One-time transition notice. Cheap; runs before the (possibly
	// failing) network call so the user always sees the heads-up.
	if telemetry.ShouldShowTransitionNotice(st, tier) {
		fmt.Fprintln(os.Stderr, telemetry.TransitionNoticeMessage)
		// Also goes to the rotating log so service-mode users (no
		// stderr visible) eventually see it too.
		_ = st // (st unused here directly; ShouldShowTransitionNotice already wrote the marker)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	view := cfgToSystemView(cfg, st)
	configDir := filepath.Dir(cfg.StateDB)

	if err := telemetry.SendInstallEventsIfDue(ctx, view, st, tier, configDir, telemetry.SendOptions{}); err != nil {
		// Errors are informational; daemon does not fail on them.
		// The retry-counter + dead-letter mechanism inside
		// SendInstallEventsIfDue handles the persistence story.
		// This branch only gets hit on ErrNoBuildKey, which
		// SendInstallEventsIfDue itself logs at WARN level.
		_ = err
	}
}

// cfgToSystemView translates the CLI-side *config.Global + state DB
// into the import-cycle-safe telemetry.SystemView the payload
// builders consume.
func cfgToSystemView(cfg *config.Global, st *state.Store) telemetry.SystemView {
	installID, _ := st.GetMeta(metaInstallID)
	return telemetry.SystemView{
		InstallID:         installID,
		ClientVersion:     version,
		InstallMethod:     detectInstallMethod(),
		BackgroundMode:    detectBackgroundMode(),
		MirrorCountBucket: telemetry.BucketMirrorCount(len(cfg.Projects)),
		DeletePolicy:      string(cfg.DeletePolicy()),
		HasHooks:          anyMirrorHasHook(cfg),
		HasFilters:        anyMirrorHasFilter(cfg),
		HasAlertWebhook:   strings.TrimSpace(cfg.AlertWebhookURL) != "",
		HasBandwidthLimit: strings.TrimSpace(cfg.BandwidthLimit) != "",
		RcloneVersion:     bestEffortRcloneVersion(cfg),
	}
}

// computeInspectDaysSinceFirstSeenBucket is the inspect-only variant
// of telemetry.ComputeDaysSinceFirstSeenBucket: it returns a human-
// readable placeholder when first_seen_at isn't set yet (so users
// running inspect on a fresh install see "(would be filled...)"
// instead of an empty string that doesn't communicate intent).
//
// Production submit (install_events.go::maybeSendUpgrade) requires
// first_seen_at to be set (upgrade can't fire before first_seen) so
// it never sees the placeholder branch.
func computeInspectDaysSinceFirstSeenBucket(st *state.Store) string {
	firstSeenStr, _ := st.GetMeta(telemetry.MetaFirstSeenAt)
	if firstSeenStr == "" {
		return "(would be filled from state DB after first_seen lands)"
	}
	firstSeen, err := time.Parse(time.RFC3339, firstSeenStr)
	if err != nil {
		return "(unparseable first_seen_at)"
	}
	bucket := telemetry.ComputeDaysSinceFirstSeenBucket(firstSeen, time.Now())
	if bucket == "" {
		return "(unparseable first_seen_at)"
	}
	return bucket
}

// ---------------------------------------------------------------------------
// Inline runtime-detection helpers used by both inspect and the production
// submit pipeline (cfgToSystemView calls these).
//
// Bucket helpers themselves now live in internal/telemetry/buckets.go;
// only the runtime-detection (cfg/state-DB → SystemView field) functions
// remain here because they need cmd-level imports.
// ---------------------------------------------------------------------------

func detectBackgroundMode() string {
	// Placeholder: the production logic checks SCM service state and
	// scheduled-task presence. Inspect + first_seen submit deliberately
	// don't load those (avoid side effects + cross-package imports);
	// production telemetry today reports "unknown" for this dimension.
	// Real detection logic is deferred — when it lands it will move to
	// internal/telemetry/buckets.go for both inspect and submit to use.
	return "unknown"
}

func detectInstallMethod() string {
	// Placeholder: production logic inspects parent process / install
	// path / registry. Inspect + first_seen submit return "unknown" so
	// the field is present and ENUM-valid without triggering side
	// effects. Real detection deferred.
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
	// Placeholder: production logic should call `rclone version` once
	// at startup and cache. Inspect avoids the subprocess for speed;
	// first_seen submit currently does the same. When the rclone
	// detection caching lands, both consumers benefit.
	return "(would be detected at submit time)"
}
