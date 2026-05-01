package systemval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryReportBug_SanitizesPathsFilenamesAndSecrets(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_report_bug_sanitization")

	cfgRoot := t.TempDir()
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	sensitiveFile := filepath.Join(srcRoot, "CustomerAlpha", "QuarterlyPlan.txt")
	createFile(t, sensitiveFile, "private")

	dataDir := filepath.Join(cfgRoot, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dataDir, "smirror.log")
	logContent := fmt.Sprintf("sync failed path=%s remote=gdrive:AI-hub/CustomerAlpha token=abc123secret\n", sensitiveFile)
	if err := os.WriteFile(logFile, []byte(logContent), 0600); err != nil {
		t.Fatal(err)
	}

	noNotify := boolPtr(false)
	noAnomaly := boolPtr(false)
	cfgPath := createConfig(t, cfgRoot, configOpts{
		Mirrors: []mirrorDef{{
			Name:      "CustomerAlpha",
			LocalPath: filepath.Join(srcRoot, "CustomerAlpha"),
			Remote:    dstRoot,
		}},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           logFile,
		LogLevel:          "debug",
		SyncWorkers:       1,
		NotifyEnabled:     noNotify,
		AnomalyEnabled:    noAnomaly,
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfgPath, "report-bug", "--stdout")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	report := r.Stdout
	for _, forbidden := range []string{
		srcRoot,
		filepath.ToSlash(srcRoot),
		"CustomerAlpha",
		"QuarterlyPlan.txt",
		"gdrive:AI-hub",
		"abc123secret",
	} {
		if strings.Contains(report, forbidden) {
			t.Errorf("report-bug --stdout leaked %q\nreport excerpt:\n%s", forbidden, truncate(report, 1200))
		}
	}
}

func TestTelemetryReleaseBuild_EmbedsBuildKeyForMSI(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_release_build_key")

	buildScript := readRepoFile(t, "installer", "build-msi.ps1")
	releaseWorkflow := readRepoFile(t, ".github", "workflows", "release.yml")
	requiredLdflag := "github.com/qraveh/SelectiveMirror/internal/telemetry.buildKey"
	if strings.Contains(releaseWorkflow, "installer/build-msi.ps1") && !strings.Contains(buildScript, requiredLdflag) {
		t.Errorf("MSI build path calls installer/build-msi.ps1 but build script does not embed telemetry.buildKey ldflag")
	}
}

// TestTelemetryRLS_EnvelopeFieldsAreAuthenticated — DELETED 0.9.84-dev.
// v1's RLS bound the payload signature to envelope columns on a stored
// ingest_envelope row. Under v2 (stream-aggregate-and-discard) there is
// no envelope row, so the binding has nothing to bind to. The replay-
// only-over-counts property is now covered structurally by
// TestTelemetryV2Schema_NoInsertOutsideRollups (CLAIMS-MAP A-02).
//
// TestTelemetryRLS_ServerOwnedColumnsCannotBeClientSet — DELETED 0.9.84-dev.
// v1 had server-owned classification_state / classify_after columns on
// the ingest_envelope row. v2 has no such row; classification is
// client-side at submit time (via the closed bucket-key tuple), and
// the rollup tables have no client-mutable server-owned columns by
// construction.

func TestTelemetryWorker_PrivacyAndEdgeLimits(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_worker_edge_privacy")

	worker := readRepoFile(t, "worker", "src", "index.ts")

	// SM-163 sub-item 1: rate-limit KV key must NOT be `rl:${ip}` —
	// the salted-hash form `rl:${hex}` keeps KV at rest non-reversible.
	// Updated 0.9.84-dev: the SM-163 fix landed in 0.9.71-dev; the key
	// is built via rateLimitKey(ip, salt) which returns `rl:${hex}`
	// with hex computed from HMAC-SHA256(salt, ip+date)[:16].
	if strings.Contains(worker, "`rl:${ip}`") {
		t.Errorf("worker stores raw client IP in RATE_LIMIT_KV key; SM-163 fix should use a salted hash. Pattern `rl:${ip}` should not appear.")
	}

	// SM-163 sub-item 2: body cap on actual bytes, not just header.
	// The current Worker uses `await request.arrayBuffer()` and checks
	// `byteLength`. Either spelling (with or without `.clone()`) works.
	// We assert the byteLength check is present.
	if strings.Contains(worker, `headers.get("Content-Length")`) && !strings.Contains(worker, "byteLength") {
		t.Errorf("worker body cap should enforce on actual byteLength of request body; SM-163 fix landed in 0.9.71-dev")
	}

	// SM-163 sub-item 3: non-atomic kv.get → kv.put rate-limit race.
	// Documented "accept-the-slack" posture as of 0.9.71-dev (SM-163's
	// status under v2: 2/3 sub-items shipped, this remainder
	// intentionally accepted; see SM-server-side-deferred.md). This
	// test is informational only — the assertion was removed when the
	// posture was decided.
	_ = worker
}

func TestTelemetryDigest_PrivacyAndMarkdownEscaping(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_digest_privacy")

	script := readRepoFile(t, "scripts", "telemetry-report.py")
	if strings.Contains(script, "install_id_4") {
		t.Errorf("weekly digest includes install_id prefixes; privacy note says reports never write install_id")
	}
	if !strings.Contains(script, "k_anon_filter") || strings.Contains(script, "Q_BUGS_THIS_WEEK") && !strings.Contains(script, "k_anon_filter(bugs_this_week") {
		t.Errorf("weekly digest does not apply k-anonymity filtering to per-report bug rows")
	}
	if !strings.Contains(script, "escape_markdown") && !strings.Contains(script, "md_cell_escape") && strings.Contains(script, "md_table(") {
		t.Errorf("weekly digest renders markdown table cells without escaping pipes/newlines/links")
	}
}

func TestTelemetryCanonicalJSON_DoesNotHTMLEscapeStrings(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_canonical_html_escape")

	impl := readRepoFile(t, "internal", "telemetry", "canonical.go")
	if !strings.Contains(impl, "SetEscapeHTML(false)") {
		t.Errorf("canonical JSON implementation does not disable Go HTML escaping; PostgreSQL jsonb::text keeps '<', '>', and '&' literal for HMAC")
	}

	testSrc := readRepoFile(t, "internal", "telemetry", "canonical_test.go")
	if !strings.Contains(testSrc, "TestCanonicalJSON_NoHTMLEscape") {
		t.Errorf("canonical JSON tests do not include the no-HTML-escape regression test")
	}
	for _, want := range []string{"<a> & <b>", "A & B", "2>&1 |"} {
		if !strings.Contains(testSrc, want) {
			t.Errorf("canonical JSON no-HTML-escape tests do not cover %q", want)
		}
	}
}

func TestTelemetryCrashReport_SanitizationAndConsent(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_crash_report_sanitization")

	src := readRepoFile(t, "cmd", "smirror", "crashreport.go")
	if strings.Contains(src, `fmt.Fprint(os.Stderr, "Submit this report to help fix the issue? [Y/n] ")`) {
		t.Errorf("crash-report submission defaults to yes; telemetry/crash reports should require an explicit affirmative submit action")
	}
	if strings.Contains(src, "strings.ReplaceAll(line, home, \"<USER_HOME>\")") &&
		!strings.Contains(src, "sanitize") &&
		!strings.Contains(src, "redact") {
		t.Errorf("crash-report path only redacts home directory and does not share report-bug sanitization for filenames, remotes, or secrets")
	}
	if strings.Contains(src, "url.QueryEscape(string(report))") {
		t.Errorf("crash-report browser submission encodes the raw crash report directly instead of a sanitized telemetry-safe report")
	}
}

// TestTelemetryRetention_PurgesNormalizedRawText — DELETED 0.9.84-dev.
// Under v2 there is no retention janitor because there is no raw to
// retain. The structural invariant that replaces it is covered by
// TestTelemetryV2Schema_OnlyRollupTablesExist (CLAIMS-MAP C-02) and
// TestTelemetryV2Schema_NoNarrativeColumns (A-08): if the schema
// cannot store narrative or raw payloads, no janitor is needed.

func TestTelemetryTierGate_FailsClosedOnStateReadError(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_tier_fail_closed")

	src := readRepoFile(t, "internal", "telemetry", "tier.go")
	if strings.Contains(src, `err == nil && v != ""`) && strings.Contains(src, "readTierFromRegistry()") &&
		!strings.Contains(src, "err != nil") {
		t.Errorf("ReadTier appears to fall through to registry fallback when state DB GetMeta returns an error; privacy gates should fail closed when runtime state is unreadable")
	}
}

func TestTelemetryGithubToken_HasTimeoutOrCallerContext(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_github_token_timeout")

	src := readRepoFile(t, "internal", "telemetry", "telemetry.go")
	fnStart := strings.Index(src, "func GithubToken")
	if fnStart < 0 {
		t.Fatalf("GithubToken not found; update this validation test")
	}
	fn := src[fnStart:]
	if strings.Contains(fn, `exec.CommandContext(context.Background(), "gh", "auth", "token")`) {
		t.Errorf("GithubToken shells out to gh auth token with context.Background and no timeout; telemetry/update checks can hang before the HTTP timeout starts")
	}
}

func TestTelemetryDocs_OperationsViewsExist(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_ops_docs_views")

	// Updated 0.9.84-dev: target telemetry-v2.sql (the v1 sql files
	// were deleted in 0.9.82-dev). Asserts that any view name the
	// runbook references is actually defined in v2's SQL.
	ops := readRepoFile(t, "docs", "operations", "telemetry-ops.md")
	v2sql := readRepoFile(t, "docs", "telemetry-v2.sql")

	// Every view name the runbook references must exist as a
	// CREATE OR REPLACE VIEW telemetry.<name> in v2 SQL.
	for _, viewName := range []string{"version_dist"} {
		if strings.Contains(ops, "telemetry."+viewName) {
			needle := "VIEW telemetry." + viewName
			if !strings.Contains(v2sql, needle) {
				t.Errorf("telemetry-ops.md references telemetry.%s, but telemetry-v2.sql does not define that view", viewName)
			}
		}
	}
}

// TestTelemetryRollup_TaxonomyJoinsDoNotCrossProduct — DELETED 0.9.84-dev.
// v1 had a bug-rollup query that joined taxonomy_term twice without
// per-namespace filtering, producing cross-product rollup rows. Under
// v2 there is no taxonomy_term table and no rollup-refresh function;
// classification is client-side at submit time, encoded directly in
// the bug_daily_rollup bucket-key tuple. The class of bug this test
// guarded against cannot exist in the v2 schema.

func TestTelemetryValidationHarness_CoverageDoesNotMaskFailedTests(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_validation_harness")

	helpers := readRepoFile(t, "system-validation", "helpers_test.go")
	if strings.Contains(helpers, "g.actual++") && !strings.Contains(helpers, "RecordPass") {
		t.Errorf("system-validation coverage records goals when tests start, so failed telemetry tests can still print the goals as met")
	}
}

func TestTelemetryValidationHarness_StaticChecksDoNotRequireRclone(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_validation_rclone_gate")

	main := readRepoFile(t, "system-validation", "main_test.go")
	if strings.Contains(main, "FATAL: rclone not found in PATH") {
		t.Errorf("system-validation TestMain hard-fails when rclone is absent, even for static telemetry/doc/RLS tests that do not need rclone")
	}
}
