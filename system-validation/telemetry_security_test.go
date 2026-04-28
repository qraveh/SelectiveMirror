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

func TestTelemetryRLS_EnvelopeFieldsAreAuthenticated(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_rls_envelope_binding")

	rls := readRepoFile(t, "docs", "telemetry-rls.sql")
	requiredBindings := []string{
		"payload->>'client_version' = client_version",
		"payload->>'install_id' = install_id",
		"payload_sha256 =",
		"payload->>'schema_version'",
		"payload->>'ingest_kind'",
	}
	for _, binding := range requiredBindings {
		if !strings.Contains(rls, binding) {
			t.Errorf("RLS policy does not bind signed payload to envelope field/check %q", binding)
		}
	}
}

func TestTelemetryRLS_ServerOwnedColumnsCannotBeClientSet(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_rls_server_owned_columns")

	rls := readRepoFile(t, "docs", "telemetry-rls.sql")
	for _, guard := range []string{
		"classification_state = 'pending'",
		"classified_at IS NULL",
		"classification_error IS NULL",
		"classify_after",
	} {
		if !strings.Contains(rls, guard) {
			t.Errorf("RLS policy lacks server-owned column guard %q", guard)
		}
	}
}

func TestTelemetryWorker_PrivacyAndEdgeLimits(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_worker_edge_privacy")

	worker := readRepoFile(t, "worker", "src", "index.ts")
	if strings.Contains(worker, "CF-Connecting-IP") && strings.Contains(worker, "`rl:${ip}`") {
		t.Errorf("worker stores raw client IP in RATE_LIMIT_KV key; privacy policy says IPs are not stored")
	}
	if strings.Contains(worker, `headers.get("Content-Length")`) && !strings.Contains(worker, "request.clone().arrayBuffer") {
		t.Errorf("worker body cap trusts Content-Length only; missing/chunked lengths can bypass edge 100KB cap")
	}
	if strings.Contains(worker, "kv.get(key)") && strings.Contains(worker, "kv.put(key") {
		t.Errorf("worker rate limit is non-atomic get-then-put; parallel same-IP requests can exceed the limit")
	}
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

func TestTelemetryRetention_PurgesNormalizedRawText(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_retention_raw_purge")

	privacy := readRepoFile(t, "docs", "PRIVACY.md")
	if !strings.Contains(privacy, "raw payloads are stripped") {
		t.Fatalf("privacy retention contract changed; update this validation test")
	}
	workerSQL := readRepoFile(t, "docs", "telemetry-worker.sql")
	for _, rawField := range []string{"report_text", "anomaly_summary", "status_snapshot"} {
		if strings.Contains(workerSQL, "SET payload = '{}'::jsonb") && !strings.Contains(workerSQL, rawField) {
			t.Errorf("retention janitor strips ingest_envelope.payload but does not purge normalized raw field %s", rawField)
		}
	}
}

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

	ops := readRepoFile(t, "docs", "operations", "telemetry-ops.md")
	views := readRepoFile(t, "docs", "telemetry-views.sql")
	if strings.Contains(ops, "version_dist") && !strings.Contains(views, "version_dist") {
		t.Errorf("telemetry ops runbook references version_dist, but telemetry-views.sql does not define that view")
	}
}

func TestTelemetryRollup_TaxonomyJoinsDoNotCrossProduct(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_rollup_taxonomy_join")

	sql := readRepoFile(t, "docs", "telemetry-worker.sql")
	kindJoin := "LEFT JOIN telemetry.bug_report_taxonomy_assignment a_kind\n        ON a_kind.bug_report_id = br.id"
	surfaceJoin := "LEFT JOIN telemetry.bug_report_taxonomy_assignment a_surface\n        ON a_surface.bug_report_id = br.id"
	if strings.Contains(sql, kindJoin) && strings.Contains(sql, surfaceJoin) {
		t.Errorf("telemetry bug rollup joins taxonomy assignments twice without filtering each assignment join by namespace; multi-tag reports can cross-product into extra rollup segments")
	}
}

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
