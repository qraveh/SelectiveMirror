package systemval

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryCLI_DefaultNoneStatus(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_telemetry")
	coverage.Record("telemetry_default_none")

	env := newTestEnv(t)
	r := runSmirror(t, env.CfgPath, "telemetry", "status")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertOutputContains(t, r, "tier:")
	assertOutputContains(t, r, "none")
	assertOutputContains(t, r, "bug reports:")
	assertOutputContains(t, r, "install events:")
}

func TestTelemetryCLI_TierTransitionPersists(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_tier_transition")

	env := newTestEnv(t)
	r := runSmirror(t, env.CfgPath, "telemetry", "standard")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertOutputContains(t, r, "Standard")

	r = runSmirror(t, env.CfgPath, "telemetry", "status")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertOutputContains(t, r, "tier:")
	assertOutputContains(t, r, "standard")

	r = runSmirror(t, env.CfgPath, "telemetry", "none")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertOutputContains(t, r, "None")
}

func TestTelemetryReportBug_SubmitAtNoneIsConsentAware(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_report_bug_submit")

	env := newTestEnv(t)
	r := runSmirror(t, env.CfgPath, "report-bug", "--submit")
	assertNoPanic(t, r)
	if r.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1 for non-interactive None-tier submit\nstdout: %s\nstderr: %s",
			r.ExitCode, truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}
	combined := strings.ToLower(r.Stdout + r.Stderr)
	if strings.Contains(combined, "unknown flag") {
		t.Errorf("report-bug --submit must be a recognized consent flow, got unknown flag\nstdout: %s\nstderr: %s",
			truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}
	if !strings.Contains(combined, "one-shot") {
		t.Errorf("None-tier --submit must tell users about --one-shot\nstdout: %s\nstderr: %s",
			truncate(r.Stdout, 400), truncate(r.Stderr, 400))
	}
}

func TestTelemetryReportBug_HelpDocumentsSubmitBrowserAndOneShot(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_report_bug_browser")

	r := runSmirrorRaw(t, "report-bug", "--help")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	for _, want := range []string{"--submit", "--one-shot", "--browser"} {
		assertStdoutContains(t, r, want)
	}
	assertStdoutContains(t, r, "--open")
	if !strings.Contains(strings.ToLower(r.Stdout), "deprecated") {
		t.Errorf("report-bug help should mark --open as deprecated alias for --browser\nstdout: %s",
			truncate(r.Stdout, 600))
	}
}

func TestTelemetryVersionReportsBuildKeyFingerprint(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_build_key_diag")

	r := runSmirrorRaw(t, "version")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertStdoutContains(t, r, "telemetry build-key:")
}

func TestTelemetryPrivacyContract_NoLegacyUsageReportShape(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_privacy_contract")

	srcPath := filepath.Join(repoRoot, "internal", "telemetry", "telemetry.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("parse telemetry.go: %v", err)
	}

	prohibitedIdentifiers := map[string]bool{
		"FilesSynced":     true,
		"SyncErrors":      true,
		"BytesUploaded":   true,
		"UptimeSeconds":   true,
		"SyncWorkers":     true,
		"DynamicDebounce": true,
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == "Report" {
					t.Errorf("telemetry client still defines legacy Report type; privacy policy allows only first_seen/upgrade structural events and per-event bug reports")
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name == "SendReport" {
				t.Errorf("telemetry client still defines legacy SendReport method; privacy policy allows only first_seen/upgrade structural events and per-event bug reports")
			}
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && prohibitedIdentifiers[ident.Name] {
			t.Errorf("telemetry client still contains legacy usage-report identifier %q; privacy policy allows only first_seen/upgrade structural events and per-event bug reports", ident.Name)
		}
		return true
	})
}

func TestTelemetryPrivacyContract_NoStartupUpdatePingAtDefaultNone(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_no_startup_update_ping")

	src := readRepoFile(t, "cmd", "smirror", "selfupdate.go")
	fnStart := strings.Index(src, "func checkForUpdateOnStartup")
	if fnStart < 0 {
		t.Fatalf("checkForUpdateOnStartup not found; update the None-tier update-ping validation")
	}
	fn := src[fnStart:]
	readTier := strings.Index(fn, "telemetry.ReadTier")
	allowsNetwork := strings.Index(fn, ".AllowsNetwork()")
	checkForUpdate := strings.Index(fn, "client.CheckForUpdate")
	if readTier < 0 || allowsNetwork < 0 || checkForUpdate < 0 {
		t.Errorf("checkForUpdateOnStartup must read telemetry tier and gate client.CheckForUpdate; missing readTier=%t allowsNetwork=%t checkForUpdate=%t",
			readTier >= 0, allowsNetwork >= 0, checkForUpdate >= 0)
		return
	}
	if readTier > checkForUpdate || allowsNetwork > checkForUpdate {
		t.Errorf("checkForUpdateOnStartup calls client.CheckForUpdate before the telemetry-tier AllowsNetwork gate; None tier promises no update pings")
	}
}

func TestTelemetryServerContract_WorkerExposesForgetEndpoint(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_worker_paths")

	cliDoc := readRepoFile(t, "docs", "cli-telemetry-command.md")
	worker := readRepoFile(t, "worker", "src", "index.ts")
	if strings.Contains(cliDoc, "smirror telemetry forget") && !strings.Contains(worker, `"/v1/forget"`) {
		t.Errorf("docs specify smirror telemetry forget, but worker allowed paths do not expose /v1/forget")
	}
}

func TestTelemetryServerContract_IngestNormalizesAcceptedEvents(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_schema_ingest_processing")

	arch := readRepoFile(t, "docs", "telemetry-microserver-architecture.md")
	schema := readRepoFile(t, "docs", "telemetry-microserver.sql")
	worker := readRepoFile(t, "worker", "src", "index.ts")

	if !strings.Contains(arch, "insert normalized `bug_report` row") {
		t.Fatalf("architecture doc no longer states normalized bug_report insertion; update this validation test with the new contract")
	}
	hasTrigger := strings.Contains(schema, "CREATE TRIGGER") || strings.Contains(schema, "CREATE OR REPLACE TRIGGER")
	hasIngestFunction := strings.Contains(schema, "CREATE OR REPLACE FUNCTION telemetry.ingest") ||
		strings.Contains(schema, "CREATE OR REPLACE FUNCTION telemetry.process_ingest")
	workerCallsRPC := strings.Contains(worker, "/rest/v1/rpc/")
	if !hasTrigger && !hasIngestFunction && !workerCallsRPC {
		t.Errorf("accepted telemetry envelopes are only inserted into ingest_envelope; no trigger/RPC/function normalizes bug_report/installation_event rows or queues classification jobs")
	}
}

func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{repoRoot}, parts...)...)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}
