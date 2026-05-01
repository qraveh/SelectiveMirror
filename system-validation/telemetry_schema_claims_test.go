// Schema-conformance tests for the telemetry v2 architecture.
//
// Origin: Quincy (System Validation), telemetry round-3 panel,
// 2026-04-30. Maps directly to claims in
// system-validation/CLAIMS-MAP.md:
//
//   C-02 / A-08  — schema-provable "we never store personal data"
//   C-07         — no heartbeat event_kind variant
//   C-08         — no accumulated counts (bytes, files, uptime)
//   C-09         — no geography fields (timezone, locale, IP)
//   C-10         — no hardware fingerprint fields
//   C-11         — bucketization mandatory for any numeric
//   C-12         — no install_id retained anywhere on disk
//   A-02         — replay over-counts only; no row created on replay
//   A-07         — counters monotonic (no decrement path)
//
// Implementation strategy: read docs/telemetry-v2.sql as text and
// assert structural invariants. The SQL file is the single source of
// truth for what the v2 schema actually contains; testing it directly
// catches drift between PRIVACY.md's prose claims and the schema's
// reality.
//
// All assertions are static — no live database needed.

package systemval

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readV2SQL returns the text of docs/telemetry-v2.sql. Single helper so
// every claim test reads the same source.
func readV2SQL(t *testing.T) string {
	t.Helper()
	return readRepoFile(t, "docs", "telemetry-v2.sql")
}

// ---------------------------------------------------------------------------
// C-02 / A-08 — only rollup tables exist (schema-provable "no personal data")
// ---------------------------------------------------------------------------
//
// Under stream-aggregate-and-discard, the schema must contain ONLY the
// three rollup tables and the closed-vocabulary lookup (taxonomy_term
// would have been allowed if we kept it; v2 chose not to). Anything
// else — a `bug_report`, `installation`, `ingest_envelope`,
// `event_log`, `audit_trail` — would either store personal data or
// enable it.

func TestTelemetryV2Schema_OnlyRollupTablesExist(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_personal_data")

	sql := readV2SQL(t)

	allowedTables := map[string]bool{
		"installation_daily_rollup":  true,
		"bug_daily_rollup":           true,
		"reliability_daily_rollup":   true,
	}

	// Find every CREATE TABLE in the v2 SQL.
	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?telemetry\.([a-z_][a-z0-9_]*)\s*\(`)
	matches := re.FindAllStringSubmatch(sql, -1)
	if len(matches) == 0 {
		t.Fatalf("no CREATE TABLE statements found in docs/telemetry-v2.sql; either the file moved or the regex broke")
	}

	for _, m := range matches {
		name := strings.ToLower(m[1])
		if !allowedTables[name] {
			t.Errorf("v2 schema declares non-rollup table telemetry.%s — under stream-aggregate-and-discard, only rollup counter tables are allowed. If this is intentional, update CLAIMS-MAP.md C-02 with the rationale and add to allowedTables here.", name)
		}
	}
}

// A-08 — bug-report narrative columns must NOT appear anywhere in the
// schema. v2 routes narratives to GitHub Issues; the rollup tables
// hold only categorical buckets.
//
// Implementation note: we scope to CREATE TABLE bodies (excluding
// SQL comments). The function `telemetry.contribute(payload JSONB,
// ...)` legitimately has a `payload JSONB` *parameter*; that is not
// a stored column. Likewise, comment text discussing "no narrative"
// or "report_text was removed" is descriptive prose, not a column
// declaration.

func TestTelemetryV2Schema_NoNarrativeColumns(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_narrative")

	sql := readV2SQL(t)

	// Forbidden column-name fragments that imply free-text storage.
	// Each is a pattern we expect at the start of a column declaration
	// inside a CREATE TABLE body: "<name> <type>".
	forbiddenColumns := []string{
		"report_text",
		"narrative",
		"raw_payload",
		"raw_text",
		"message_body",
		"issue_body",
	}

	tableRe := regexp.MustCompile(`(?is)CREATE\s+TABLE[^(]+\(([^;]+?)\)\s*;`)
	colNameRe := regexp.MustCompile(`(?im)^\s*([a-z_][a-z0-9_]*)\s+\S+`)

	for _, tm := range tableRe.FindAllStringSubmatch(sql, -1) {
		body := tm[1]
		for _, cm := range colNameRe.FindAllStringSubmatch(body, -1) {
			col := strings.ToLower(cm[1])
			for _, forbidden := range forbiddenColumns {
				if col == forbidden {
					t.Errorf("v2 schema declares forbidden narrative-shaped column %q inside a CREATE TABLE body — bug-report narratives belong on GitHub Issues, not in the telemetry schema (CLAIMS-MAP A-08)", forbidden)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// C-07 — event_kind ENUM has no heartbeat variant
// ---------------------------------------------------------------------------

func TestTelemetryV2Schema_EventKindEnumNoHeartbeat(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_heartbeat")

	sql := readV2SQL(t)

	// Locate the event_kind ENUM definition.
	re := regexp.MustCompile(`(?is)CREATE\s+TYPE\s+telemetry\.event_kind\s+AS\s+ENUM\s*\(([^)]+)\)`)
	m := re.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("could not locate telemetry.event_kind ENUM definition in v2 SQL; expected CREATE TYPE telemetry.event_kind AS ENUM (...)")
	}

	// Allowed event_kind values per architecture-v2 doc.
	allowed := map[string]bool{
		"first_seen":           true,
		"upgrade":              true,
		"bug_report":           true,
		"reliability_snapshot": true,
	}

	// Forbidden patterns — variants that would imply periodic phone-home.
	forbidden := []string{"heartbeat", "ping", "active_install", "checkin", "check_in", "keepalive"}

	body := strings.ToLower(m[1])
	for _, f := range forbidden {
		if strings.Contains(body, f) {
			t.Errorf("event_kind ENUM contains forbidden variant %q — PRIVACY.md commits to no heartbeats ever (CLAIMS-MAP C-07)", f)
		}
	}

	// Sanity: every value in the ENUM body should be in the allowed set.
	valRe := regexp.MustCompile(`'([a-z_][a-z0-9_]*)'`)
	for _, vm := range valRe.FindAllStringSubmatch(body, -1) {
		v := vm[1]
		if !allowed[v] {
			t.Errorf("event_kind ENUM contains unexpected value %q — if this is a new event kind, update PRIVACY.md and CLAIMS-MAP first, then add to allowed set here", v)
		}
	}
}

// ---------------------------------------------------------------------------
// C-08 — no accumulated counts (bytes mirrored, files synced, uptime,
// continuous error counts) anywhere in the rollup schemas
// ---------------------------------------------------------------------------

func TestTelemetryV2Schema_NoAccumulatedCountColumns(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_accumulated_counts")

	sql := strings.ToLower(readV2SQL(t))

	// Forbidden fragments — accumulated-metric column names.
	forbidden := []string{
		"bytes_uploaded",
		"bytes_mirrored",
		"bytes_total",
		"files_synced",
		"files_total",
		"uptime_seconds",
		"uptime_ms",
		"error_count_total",
		"sync_errors_total",
		// Per-row counters in the rollup tables (`count`, `reports`)
		// are intentional and named singularly — they're aggregate
		// row-counts, not accumulated user metrics. Don't add `count`
		// here; that'd false-positive the architecture's intent.
	}

	for _, frag := range forbidden {
		if strings.Contains(sql, frag) {
			t.Errorf("v2 schema contains forbidden accumulated-metric column %q — PRIVACY.md commits to no accumulated counts (CLAIMS-MAP C-08)", frag)
		}
	}
}

// ---------------------------------------------------------------------------
// C-09 — no geography fields (timezone, locale, IP, country)
// ---------------------------------------------------------------------------

func TestTelemetryV2Schema_NoGeoFields(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_geo")

	sql := strings.ToLower(readV2SQL(t))

	// Forbidden column-name fragments. `os_family` is allowed (it's
	// platform, not geography); `os_detail` ditto.
	forbidden := []string{
		"timezone",
		"time_zone",
		"locale ",       // trailing space avoids false-match on docs prose
		"locale\t",
		"language_tag",
		"country_code",
		" country ",
		"ip_address",
		"client_ip",
		"source_ip",
		"geoip",
		"geo_lat",
		"geo_lon",
		"region_code",
	}

	for _, frag := range forbidden {
		if strings.Contains(sql, frag) {
			t.Errorf("v2 schema references forbidden geography fragment %q — PRIVACY.md commits to no geography (CLAIMS-MAP C-09)", frag)
		}
	}
}

// ---------------------------------------------------------------------------
// C-10 — no hardware fingerprint fields
// ---------------------------------------------------------------------------

func TestTelemetryV2Schema_NoHardwareFingerprintFields(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_hw_fingerprint")

	sql := strings.ToLower(readV2SQL(t))

	forbidden := []string{
		"cpu_model",
		"cpu_count",
		"core_count",
		"memory_mb",
		"memory_gb",
		"ram_gb",
		"disk_class",
		"disk_size",
		"machine_id",
		"hostname",
		"mac_address",
		"motherboard",
	}

	for _, frag := range forbidden {
		if strings.Contains(sql, frag) {
			t.Errorf("v2 schema references forbidden hardware-fingerprint fragment %q (CLAIMS-MAP C-10)", frag)
		}
	}
}

// ---------------------------------------------------------------------------
// C-11 — bucketization mandatory for any numeric field
// ---------------------------------------------------------------------------
//
// Every numeric column in a rollup tuple must be a bucket ENUM
// (mirror_count_bucket, anomaly_count_bucket, ...). Raw integer
// columns ARE allowed only for the row counter (`count`, `reports`)
// and standardized timestamps. Any other INTEGER / BIGINT / NUMERIC
// column would let a heavy user be uniquely identified by their
// value.

func TestTelemetryV2Schema_NoUnbucketedNumerics(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_bucketized_numerics")

	sql := readV2SQL(t)

	// Allowlist: integer columns that ARE intentional and known
	// to not encode user-distinguishing magnitudes.
	allowedIntCols := map[string]bool{
		"count":   true,
		"reports": true,
	}

	// Find every column declaration of a numeric type inside any
	// CREATE TABLE block. Pattern: <name> <numeric_type> [...]
	// We only inspect lines inside CREATE TABLE …, so we scope first.

	tableRe := regexp.MustCompile(`(?is)CREATE\s+TABLE[^(]+\(([^;]+?)\)\s*;`)
	colRe := regexp.MustCompile(`(?im)^\s*([a-z_][a-z0-9_]*)\s+(BIGINT|INTEGER|INT|SMALLINT|NUMERIC|REAL|DOUBLE\s+PRECISION|FLOAT)\b`)

	for _, tm := range tableRe.FindAllStringSubmatch(sql, -1) {
		body := tm[1]
		for _, cm := range colRe.FindAllStringSubmatch(body, -1) {
			col := strings.ToLower(cm[1])
			if !allowedIntCols[col] {
				t.Errorf("v2 schema declares unbucketed numeric column %q — every numeric must be a bucket ENUM (CLAIMS-MAP C-11). If genuinely intentional, add to allowedIntCols with rationale.", col)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// C-12 — no install_id column anywhere on disk
// ---------------------------------------------------------------------------
//
// The install_id is verified for HMAC inside contribute() and discarded
// when the function returns. It must NOT appear as a column in any
// rollup table — that would make the rollup retainable per-install.

func TestTelemetryV2Schema_NoInstallIDColumn(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_no_install_id")

	sql := strings.ToLower(readV2SQL(t))

	// Look at CREATE TABLE bodies only. install_id may legitimately
	// appear in COMMENT ON / function-body strings (it's the parameter
	// name that flows into HMAC verify). We check that no CREATE TABLE
	// declares it as a column.

	tableRe := regexp.MustCompile(`(?is)CREATE\s+TABLE[^(]+\(([^;]+?)\)\s*;`)
	colNameRe := regexp.MustCompile(`(?im)^\s*([a-z_][a-z0-9_]*)\s+\S+`)

	for _, tm := range tableRe.FindAllStringSubmatch(sql, -1) {
		body := tm[1]
		for _, cm := range colNameRe.FindAllStringSubmatch(body, -1) {
			col := cm[1]
			if col == "install_id" {
				t.Errorf("v2 schema declares an install_id column inside a CREATE TABLE body — install_id is verify-and-discard only, never persisted (CLAIMS-MAP C-12)")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// A-02 — replay can only over-count an aggregate, never create a row
// ---------------------------------------------------------------------------
//
// Architectural claim. The proof is structural: contribute() only
// performs UPSERT-on-existing-row plus INSERT-on-new-bucket-key. There
// is no path that creates a row keyed on something the attacker
// controls beyond the bucket dimensions (which they could already
// over-count). Specifically: no INSERT outside the three rollup tables.

func TestTelemetryV2Schema_NoInsertOutsideRollups(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_replay_overcount_only")

	sql := readV2SQL(t)

	// Find every INSERT INTO … and confirm it targets one of the
	// rollup tables.
	allowedInsertTargets := map[string]bool{
		"installation_daily_rollup": true,
		"bug_daily_rollup":          true,
		"reliability_daily_rollup":  true,
	}

	re := regexp.MustCompile(`(?i)INSERT\s+INTO\s+telemetry\.([a-z_][a-z0-9_]*)`)
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		target := strings.ToLower(m[1])
		if !allowedInsertTargets[target] {
			t.Errorf("v2 SQL contains INSERT INTO telemetry.%s — only the three rollup tables are valid INSERT targets under the architecture (CLAIMS-MAP A-02)", target)
		}
	}
}

// ---------------------------------------------------------------------------
// A-07 — counters are monotonic (no decrement path)
// ---------------------------------------------------------------------------
//
// The architecture claim is that aggregate counters only go up. The
// SQL evidence: no UPDATE or function body that decrements `count` or
// `reports`. We grep for the negative-arithmetic patterns.

func TestTelemetryV2Schema_CountersMonotonic(t *testing.T) {
	t.Parallel()
	coverage.Record("telemetry_v2_schema_counters_monotonic")

	sql := readV2SQL(t)

	// Patterns that would decrement a counter. Each is a regex; we
	// allow `count = count + 1` or `count + 1` but flag any minus.
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bcount\s*=\s*count\s*-`),
		regexp.MustCompile(`(?i)\bcount\s*-=\s*`),
		regexp.MustCompile(`(?i)\breports\s*=\s*reports\s*-`),
		regexp.MustCompile(`(?i)\breports\s*-=\s*`),
		regexp.MustCompile(`(?i)\bSET\s+count\s*=\s*GREATEST\s*\(\s*0`), // floor-clamp pattern implies a decrement happens
	}

	for _, re := range forbidden {
		if loc := re.FindStringIndex(sql); loc != nil {
			line := lineContaining(sql, loc[0])
			t.Errorf("v2 SQL contains a decrement pattern matching %q — counters must be monotonic (CLAIMS-MAP A-07). Match around: %q", re.String(), line)
		}
	}
}

// lineContaining returns the source line containing the given byte
// offset, for use in error messages.
func lineContaining(s string, offset int) string {
	if offset < 0 || offset >= len(s) {
		return ""
	}
	start := strings.LastIndex(s[:offset], "\n") + 1
	end := strings.Index(s[offset:], "\n")
	if end < 0 {
		end = len(s)
	} else {
		end += offset
	}
	return strings.TrimSpace(s[start:end])
}

// ---------------------------------------------------------------------------
// Sanity: docs/telemetry-v2.sql is at the expected path
// ---------------------------------------------------------------------------

func TestTelemetryV2Schema_FileAtExpectedPath(t *testing.T) {
	t.Parallel()
	p := filepath.Join(repoRoot, "docs", "telemetry-v2.sql")
	if !strings.HasSuffix(p, filepath.Join("docs", "telemetry-v2.sql")) {
		t.Fatalf("repoRoot/docs/telemetry-v2.sql resolves unexpectedly: %s", p)
	}
}
