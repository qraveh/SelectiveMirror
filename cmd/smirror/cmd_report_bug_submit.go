// SM-158 — `smirror report-bug --submit` pipeline.
//
// This file holds the submit-side helpers extracted from cmdReportBug:
//
//   - The bug-report payload builder (event_kind=bug_report, with
//     bucketed dimensions matching telemetry.bug_daily_rollup).
//   - The interactive stuck-user prompt for the None-tier case.
//   - The print-the-URL rule (docs/SM-158-report-bug-submit-plan.md
//     2026-05-02 update): every successful or failed --submit MUST
//     print the GitHub-issue URL so the user can file the narrative.
//     "We do not accept ownership of the data of the bug reports."
//
// The actual HTTP call lives in internal/telemetry/contribute.go; this
// file is the CLI-side glue.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/state"
	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// installMethodHint inspects the build to guess the install method
// reported in the bug_report payload. The same heuristic applies as for
// install-event payloads: MSI builds set INSTALL_METHOD via build flag;
// fall back to "unknown" when unset. This is a low-cardinality field
// for histogram bucketing — never used for routing or content gating.
func installMethodHint() string {
	// Future: a build-time injected `var installMethod = "msi"` etc.
	// would be more accurate. For now, "unknown" is the honest default.
	return "unknown"
}

// buildBugReportPayload composes the JSON-friendly map sent to
// telemetry.contribute() for a bug_report event. Fields match
// telemetry.bug_daily_rollup's PRIMARY KEY tuple plus the dimensional
// extras the server expects (reported_at, schema_version).
//
// The classifier output drives kind/surface/severity. tier and oneShot
// drive submitted_tier:
//   - oneShot=true  → "one_shot" (anonymous one-time count)
//   - tier=standard → "standard"
//   - tier=reliability → "reliability"
//
// Payload privacy. The map below contains zero free-text strings from
// the user's environment: every value is a closed-vocabulary enum, an
// ISO timestamp, or the version string. The narrative — what makes a
// bug report useful for a maintainer — is NOT in here. That goes
// through the GitHub-issue URL printed alongside.
func buildBugReportPayload(
	bundleClassification telemetry.BugClassification,
	clientVersion string,
	tier telemetry.Tier,
	oneShot bool,
) map[string]any {
	submittedTier := "standard"
	switch {
	case oneShot:
		submittedTier = "one_shot"
	case tier == telemetry.TierReliability:
		submittedTier = "reliability"
	case tier == telemetry.TierStandard:
		submittedTier = "standard"
	default:
		// Should never happen — caller has already gated. Be defensive
		// and fall through to one_shot so the count still lands in a
		// sensible bucket if someone refactors and forgets the gate.
		submittedTier = "one_shot"
	}

	return map[string]any{
		"event_kind":      "bug_report",
		"schema_version":  1,
		"reported_at":     time.Now().UTC().Format(time.RFC3339),
		"client_version":  clientVersion,
		"bug_kind":        bundleClassification.Kind,
		"bug_surface":     bundleClassification.Surface,
		"severity_hint":   bundleClassification.Severity,
		"source":          "report_bug",
		"submitted_tier":  submittedTier,
	}
}

// submitBugReport classifies the bundle, signs it, and POSTs to the
// telemetry Worker. Returns the classification (so the caller can
// surface it to the user) and any error.
//
// On a -dev build (no buildKey injected), returns telemetry.ErrNoBuildKey
// without making a network call. The caller should treat this as
// "submission unavailable in this build" and tell the user to use
// --browser or paste the bundle into a GitHub issue manually.
func submitBugReport(
	ctx context.Context,
	sanitizedBundle string,
	tier telemetry.Tier,
	oneShot bool,
) (telemetry.BugClassification, error) {
	cls := telemetry.ClassifyBugReport(sanitizedBundle)
	payload := buildBugReportPayload(cls, version, tier, oneShot)
	err := telemetry.Contribute(ctx, version, payload, telemetry.ContributeOptions{})
	return cls, err
}

// printSubmitOutcome writes a one-line outcome summary + the always-
// print URL rule. err==nil means the count contribution succeeded;
// any non-nil err means the count did NOT land — the URL is still
// printed so the user can file the narrative anyway.
func printSubmitOutcome(
	cls telemetry.BugClassification,
	tier telemetry.Tier,
	oneShot bool,
	prefilledURL string,
	err error,
) {
	tierLabel := string(tier)
	if oneShot {
		tierLabel = "one_shot"
	}

	if err == nil {
		// Stderr (not stdout) so --stdout users get a clean report
		// pipe; the submit-feedback line is informational, not data.
		fmt.Fprintf(os.Stderr,
			"Submitted: bug_kind=%s bug_surface=%s severity=%s source=report_bug submitted_tier=%s\n",
			cls.Kind, cls.Surface, cls.Severity, tierLabel)
	} else {
		// On any failure (network, HMAC, schema, no-build-key), be
		// loud about it so the user knows the count didn't land. The
		// narrative path via the URL still works.
		switch {
		case errors.Is(err, telemetry.ErrNoBuildKey):
			fmt.Fprintln(os.Stderr,
				"Note: this build was not signed at CI time and cannot submit "+
					"telemetry. The bug-report URL below is the only path.")
		case errors.Is(err, telemetry.ErrNetwork):
			fmt.Fprintf(os.Stderr,
				"Could not contact the telemetry endpoint: %v\n"+
					"The categorical count was NOT recorded; please file the narrative below.\n", err)
		case errors.Is(err, telemetry.ErrRejected):
			fmt.Fprintf(os.Stderr,
				"The telemetry server rejected this build's HMAC signature. "+
					"This usually means the build's per-version key is misconfigured. "+
					"The categorical count was NOT recorded; please file the narrative below.\n")
		case errors.Is(err, telemetry.ErrSchemaViolation):
			fmt.Fprintf(os.Stderr,
				"The telemetry server rejected this payload as malformed: %v\n"+
					"This is a client bug; please file the narrative below and "+
					"include this message.\n", err)
		default:
			fmt.Fprintf(os.Stderr,
				"Telemetry submission failed: %v\n"+
					"The categorical count was NOT recorded; please file the narrative below.\n", err)
		}
	}

	// The 2026-05-02 always-print rule. Every --submit (success or
	// failure, with or without --browser) MUST surface this URL so
	// the user knows where the narrative lives. SelectiveMirror does
	// not accept ownership of the data of the bug reports. Stderr so
	// piping with --stdout still works.
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "If you'd like the bug actually fixed, file the narrative at:")
	fmt.Fprintln(os.Stderr, "  "+prefilledURL)
}

// stuckUserPrompt shows the 5-option prompt described in the SM-158
// plan when a None-tier user invokes --submit without --one-shot. The
// bundle is shown by the [v] option (so we accept it as input rather
// than re-building it on demand).
//
// Returns one of: "one_shot", "upgrade_to_standard", "save_only",
// "cancel". A separate caller-side branch handles each.
func stuckUserPrompt(bundle string) string {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("Bug submission options (telemetry tier is None — bug reports are not")
		fmt.Println("sent automatically). Choose how to proceed:")
		fmt.Println()
		fmt.Println("  [1] Enable Standard tier and submit")
		fmt.Println("       (changes your default; future bug reports submit automatically)")
		fmt.Println("  [2] One-shot: send THIS report only, don't change tier")
		fmt.Println("       (anonymous count contributed; tier stays None)")
		fmt.Println("  [3] Save the report locally; I'll file it manually")
		fmt.Println("       (no telemetry; you paste the bundle into a GitHub issue yourself)")
		fmt.Println("  [v] View the full report bundle before deciding")
		fmt.Println("  [c] Cancel")
		fmt.Print("Choice [1/2/3/v/c]: ")

		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF / closed pipe — treat as cancel.
			fmt.Fprintln(os.Stderr, "(input closed; cancelling)")
			return "cancel"
		}
		choice := strings.TrimSpace(strings.ToLower(line))
		switch choice {
		case "1":
			return "upgrade_to_standard"
		case "2":
			return "one_shot"
		case "3":
			return "save_only"
		case "v":
			fmt.Println()
			fmt.Println("--- Bundle (sanitized) ---")
			fmt.Print(bundle)
			if !strings.HasSuffix(bundle, "\n") {
				fmt.Println()
			}
			fmt.Println("--- End bundle ---")
			// Loop: re-show options.
		case "c", "":
			return "cancel"
		default:
			fmt.Fprintf(os.Stderr, "Unrecognized choice: %q. Please enter 1, 2, 3, v, or c.\n", choice)
		}
	}
}

// upgradeToStandardForSubmit writes telemetry_tier=standard to the
// state DB, mirroring what `smirror telemetry standard` does, but
// without the os.Exit-on-error semantics of cmdTelemetrySet (we want
// the caller to keep going on best-effort failures).
//
// Returns nil on success. Any failure means the tier is unchanged;
// caller should fall back to one-shot or cancel.
func upgradeToStandardForSubmit(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		return fmt.Errorf("open state DB: %w", err)
	}
	defer st.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.SetMeta(metaTelemetryTier, string(telemetry.TierStandard)); err != nil {
		return fmt.Errorf("write tier: %w", err)
	}
	if err := st.SetMeta(metaTelemetryTierChangedAt, now); err != nil {
		return fmt.Errorf("write tier-changed-at: %w", err)
	}
	if err := st.SetMeta(metaTelemetryTierSource, "user"); err != nil {
		return fmt.Errorf("write tier-source: %w", err)
	}
	// Ensure an install_id exists (server doesn't store it but the
	// HMAC chain expects a stable value).
	if existing, _ := st.GetMeta(metaInstallID); existing == "" {
		_ = st.SetMeta(metaInstallID, telemetry.GenerateInstallID())
	}
	return nil
}

// platformLabel returns "windows-amd64" / "linux-arm64" / etc., used by
// install-event payloads. Kept here in case future payload shapes need
// it; bug_report payloads don't include OS today (the bucket is
// composed from client_version + bug_kind + bug_surface).
func platformLabel() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}
