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

	// Updated 0.9.84-dev: under v2 the `forget` subcommand is REMOVED
	// from the design. The Worker explicitly returns 410 Gone for
	// /v1/forget, marking the path retired. So the test now asserts
	// the OPPOSITE of v1: docs should NOT promise a working `forget`,
	// AND the Worker should treat /v1/forget as retired.
	cliDoc := readRepoFile(t, "docs", "cli-telemetry-command.md")
	worker := readRepoFile(t, "worker", "src", "index.ts")
	if !strings.Contains(cliDoc, "removed in v2") && !strings.Contains(cliDoc, "is not in the surface") {
		t.Errorf("cli-telemetry-command.md should explicitly mark forget as removed in v2")
	}
	if !strings.Contains(worker, "RETIRED_PATHS") || !strings.Contains(worker, `"/v1/forget"`) {
		t.Errorf("worker must list /v1/forget in its RETIRED_PATHS set")
	}
}

func TestTelemetryServerContract_IngestNormalizesAcceptedEvents(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_schema_ingest_processing")

	// Updated 0.9.84-dev for v2 architecture. v1's "normalize bug_report
	// from ingest_envelope" pipeline was retired; under v2 there is no
	// normalization step. The Worker calls one RPC, the function
	// dispatches by event_kind, and a counter is upserted in the same
	// transaction. The contract this test now asserts: the v2 schema
	// defines `telemetry.contribute()` as a SECURITY DEFINER function,
	// and the Worker calls it via PostgREST RPC.
	v2schema := readRepoFile(t, "docs", "telemetry-v2.sql")
	worker := readRepoFile(t, "worker", "src", "index.ts")

	if !strings.Contains(v2schema, "CREATE OR REPLACE FUNCTION telemetry.contribute") {
		t.Errorf("v2 schema must define telemetry.contribute() as the single ingest RPC")
	}
	if !strings.Contains(v2schema, "SECURITY DEFINER") {
		t.Errorf("v2 contribute() function must be SECURITY DEFINER (it accesses the vault-stored master key)")
	}
	if !strings.Contains(worker, "/rest/v1/rpc/") {
		t.Errorf("Worker must call PostgREST RPC path (/rest/v1/rpc/...) for v2 ingest")
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
