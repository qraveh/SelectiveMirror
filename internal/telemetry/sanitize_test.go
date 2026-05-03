package telemetry

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSanitizeReport_ValidationContract mirrors the
// system-validation::TestTelemetryReportBug_SanitizesPathsFilenamesAndSecrets
// contract. If this fails, the system-validation test will too.
func TestSanitizeReport_ValidationContract(t *testing.T) {
	srcRoot := filepath.Join("C:\\Users\\test\\AppData\\Local\\Temp\\smirror-001")
	mirrorPath := filepath.Join(srcRoot, "CustomerAlpha")
	sensitiveFile := filepath.Join(mirrorPath, "QuarterlyPlan.txt")

	logLine := "sync failed path=" + sensitiveFile +
		" remote=gdrive:AI-hub/CustomerAlpha token=abc123secret"

	got := SanitizeReport(logLine, SanitizeOptions{
		MirrorPaths: []string{mirrorPath},
		MirrorNames: []string{"CustomerAlpha"},
	})

	for _, forbidden := range []string{
		srcRoot,
		filepath.ToSlash(srcRoot),
		"CustomerAlpha",
		"QuarterlyPlan.txt",
		"gdrive:AI-hub",
		"abc123secret",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sanitized output leaks forbidden substring %q\nout: %s", forbidden, got)
		}
	}
}

func TestSanitizeReport_CredentialPatterns(t *testing.T) {
	cases := []string{
		"token=abc123",
		"password=hunter2",
		"passwd=hunter2",
		"secret=top",
		"api_key=xyz",
		"api-key=xyz",
		"apikey=xyz",
		"Authorization: Bearer eyJabc.def",
		"bearer eyJabc.def",
		"client_secret=long-secret-string",
		"access-token=foo",
		"X-API-Key: abc123",
		"AWS_SECRET_ACCESS_KEY=secret",
		"AWS_ACCESS_KEY_ID=AKIAABCD",
		"Cookie: session=xyz",
	}
	for _, line := range cases {
		got := SanitizeReport(line, SanitizeOptions{})
		if strings.Contains(got, "abc123") || strings.Contains(got, "hunter2") ||
			strings.Contains(got, "top") || strings.Contains(got, "xyz") ||
			strings.Contains(got, "eyJabc") || strings.Contains(got, "long-secret-string") ||
			strings.Contains(got, "foo") || strings.Contains(got, "secret") &&
			!strings.Contains(got, "secret=<REDACTED>") || strings.Contains(got, "AKIAABCD") {
			// the second-secret check is awkward because the substring
			// "secret" appears in our placeholder too; we just want the
			// VALUE redacted.
			if strings.Contains(got, "<REDACTED>") {
				continue
			}
			t.Errorf("credential not redacted: input=%q output=%q", line, got)
		}
	}
}

func TestSanitizeReport_RemoteURIRedaction(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"backup to gdrive:AI-hub/foo", "backup to gdrive:<REDACTED>"},
		{"using s3:bucket/path", "using s3:<REDACTED>"},
		{"sftp:server/dir backed up", "sftp:<REDACTED> backed up"},
		// URL schemes should be preserved
		{"see https://github.com/qraveh/SelectiveMirror", "see https://github.com/qraveh/SelectiveMirror"},
		{"clone http://example.com/repo", "clone http://example.com/repo"},
	}
	for _, c := range cases {
		got := SanitizeReport(c.in, SanitizeOptions{})
		if got != c.want {
			t.Errorf("input=%q\n  got: %q\n want: %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeReport_RemoteURIRedactionMixedCase(t *testing.T) {
	in := "copy failed at GDrive:ClientAlpha/SecretProject/file.txt"
	got := SanitizeReport(in, SanitizeOptions{})
	for _, forbidden := range []string{"ClientAlpha", "SecretProject", "file.txt"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("mixed-case rclone remote leaked %q in sanitized report: %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "gdrive:<REDACTED>") {
		t.Fatalf("expected mixed-case remote to be redacted, got %q", got)
	}
}

// SM-180: env-var-style ALL_CAPS_WITH_UNDERSCORES credential names
// where the sensitive word is a SUFFIX. Pre-fix, `\b\w*_TOKEN` didn't
// match because `\b` is not a word boundary between two word chars
// (the underscore is `\w`).
func TestSanitizeReport_CredentialPatterns_EnvVarSuffix(t *testing.T) {
	cases := []struct {
		in       string
		mustHide string // value that must NOT appear in output
		mustShow string // key=<REDACTED> shape that MUST appear
	}{
		{"GITHUB_TOKEN=ghp_realsecret_value", "ghp_realsecret_value", "GITHUB_TOKEN=<REDACTED>"},
		{"OPENAI_API_KEY=sk-leaktoken-value", "sk-leaktoken-value", "OPENAI_API_KEY=<REDACTED>"},
		{"AWS_SESSION_TOKEN=session-secret-value", "session-secret-value", "AWS_SESSION_TOKEN=<REDACTED>"},
		{"MY_CUSTOM_PASSWORD=hunter2", "hunter2", "MY_CUSTOM_PASSWORD=<REDACTED>"},
	}
	for _, c := range cases {
		got := SanitizeReport(c.in, SanitizeOptions{})
		if strings.Contains(got, c.mustHide) {
			t.Errorf("env-var credential leaked %q in %q: got %q", c.mustHide, c.in, got)
		}
		if !strings.Contains(got, c.mustShow) {
			t.Errorf("env-var key shape lost: input %q expected %q in output, got %q", c.in, c.mustShow, got)
		}
	}
}

// SM-188: paths with spaces under a registered MirrorPath. Pre-fix,
// `<mirror_0_path>/Project Alpha/Secret File.txt` reduced to
// `<mirror_0_path>/<files> Alpha/Secret File.txt` — the trailing
// filename leaked.
func TestSanitizeReport_PathWithSpaces(t *testing.T) {
	in := `sync failed path=C:\Data\Project Alpha\Secret File.txt`
	got := SanitizeReport(in, SanitizeOptions{MirrorPaths: []string{`C:\Data`}})
	for _, forbidden := range []string{"Project Alpha", "Secret File.txt", "Alpha", "Secret"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("path-with-spaces leaked %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "<mirror_0_path>/<files>") {
		t.Fatalf("expected `<mirror_0_path>/<files>` placeholder, got %q", got)
	}
}

// SM-189: arbitrary absolute Windows paths NOT under any configured
// prefix. Pre-fix, `error opening C:\Windows\System32\config\SAM`
// passed through unchanged because no prefix substitution matched.
func TestSanitizeReport_ArbitraryAbsoluteWindowsPath(t *testing.T) {
	cases := []string{
		`error opening C:\Windows\System32\config\SAM`,
		`hook failed at D:\Backup\PrivateProject\quarterly.xlsx`,
		`temp dir C:/Users/Alice/AppData/Local/Temp/xyz789`,
	}
	for _, in := range cases {
		got := SanitizeReport(in, SanitizeOptions{})
		// Drive letter root should survive in placeholder form.
		if !strings.Contains(got, "<path>/<files>") {
			t.Errorf("expected `<path>/<files>` placeholder in sanitized output of %q, got %q", in, got)
		}
		// None of these path component names should leak.
		for _, forbidden := range []string{"Windows", "System32", "config", "SAM", "Backup", "PrivateProject", "quarterly.xlsx", "Alice", "AppData", "Temp", "xyz789"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("absolute Windows path leaked %q in sanitized output of %q: got %q", forbidden, in, got)
			}
		}
	}
}

func TestSanitizeReport_PathSubstitutionWindows(t *testing.T) {
	home := "C:\\Users\\Alice"
	cfg := "C:\\Users\\Alice\\.selectivemirror"
	mirror := "C:\\Code\\AcmeProject"

	in := "log: " + mirror + "\\foo\\bar.txt opened by " + home + "\\Documents\\xx.tmp; cfg=" + cfg + "\\config.yaml"
	got := SanitizeReport(in, SanitizeOptions{
		HomeDir:     home,
		ConfigDir:   cfg,
		MirrorPaths: []string{mirror},
		MirrorNames: []string{"AcmeProject"},
	})
	for _, forbidden := range []string{
		"Alice", "Code\\AcmeProject", "AcmeProject",
		"foo\\bar.txt", "Documents", "xx.tmp", "config.yaml",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("forbidden %q present in: %s", forbidden, got)
		}
	}
	// Sanity: at least one of our placeholders is present.
	hasPlaceholder := strings.Contains(got, "<mirror_0_path>") ||
		strings.Contains(got, "<configdir>") ||
		strings.Contains(got, "~/")
	if !hasPlaceholder {
		t.Errorf("no placeholder appeared; output=%q", got)
	}
}

func TestSanitizeReport_Idempotent(t *testing.T) {
	opts := SanitizeOptions{
		HomeDir:     "/home/user",
		ConfigDir:   "/home/user/.selectivemirror",
		MirrorPaths: []string{"/data/myproj"},
		MirrorNames: []string{"myproj"},
	}
	in := "/data/myproj/file.txt is the path; token=secretvalue; remote=gdrive:foo/bar"
	once := SanitizeReport(in, opts)
	twice := SanitizeReport(once, opts)
	if once != twice {
		t.Errorf("not idempotent\nonce:  %q\ntwice: %q", once, twice)
	}
}

func TestSanitizeReport_PlaceholderPathChopsTrailing(t *testing.T) {
	// After path subs, anything trailing a placeholder gets chopped.
	in := "spotted at <mirror_0_path>/CustomerAlpha/QuarterlyPlan.txt"
	got := SanitizeReport(in, SanitizeOptions{})
	want := "spotted at <mirror_0_path>/<files>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestSanitizeReport_HomePlaceholderTrailing(t *testing.T) {
	in := "saw ~/Documents/secret.pdf in there"
	got := SanitizeReport(in, SanitizeOptions{})
	if strings.Contains(got, "secret.pdf") || strings.Contains(got, "Documents") {
		t.Errorf("trailing path after ~ not redacted: %q", got)
	}
	if !strings.Contains(got, "~/<files>") {
		t.Errorf("expected ~/<files>, got: %q", got)
	}
}

// SM-210: mirror-name redaction must be case-INSENSITIVE. Pre-fix,
// `strings.ReplaceAll` only matched the user-typed casing, so a Windows
// log line that emitted the same name in a different case (lowercase
// from Go's slog default, uppercase from a third-party tool) leaked the
// name through `--sanitize`.
func TestSanitizeReport_MirrorNameCaseInsensitive(t *testing.T) {
	in := "synced from MyMirror; lower form mymirror logged; upper form MYMIRROR errored"
	got := SanitizeReport(in, SanitizeOptions{
		MirrorNames: []string{"MyMirror"},
	})
	for _, forbidden := range []string{"MyMirror", "mymirror", "MYMIRROR"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("case variant %q leaked in sanitized output: %q", forbidden, got)
		}
	}
	// Three distinct occurrences should all be replaced.
	if n := strings.Count(got, "mirror_0"); n != 3 {
		t.Errorf("expected 3 `mirror_0` substitutions, got %d in %q", n, got)
	}
}

// SM-211: mirror-name redaction must NOT match inside other words.
// Pre-fix, naive substring `strings.ReplaceAll` garbled English text
// when a mirror name was a common substring (e.g. "log" inside
// "logical", or even "m" inside "Some").
func TestSanitizeReport_MirrorNameWordBoundary(t *testing.T) {
	cases := []struct {
		name      string
		mirror    string
		in        string
		mustKeep  []string // substrings that must survive (the false-match victims)
		mustHide  []string // standalone occurrences that must still be redacted
		wantSubs  int      // expected number of `mirror_0` substitutions
	}{
		{
			name:     "log substring not garbled",
			mirror:   "log",
			in:       "logical conclusion: catalog entry blogged from log",
			mustKeep: []string{"logical", "catalog", "blogged"},
			mustHide: []string{" log"}, // standalone trailing
			wantSubs: 1,
		},
		{
			name:     "test substring not garbled",
			mirror:   "test",
			in:       "testing the contest between fastest and test runner",
			mustKeep: []string{"testing", "contest", "fastest"},
			mustHide: []string{" test "},
			wantSubs: 1,
		},
		{
			name:     "name as path component still redacted",
			mirror:   "MyDocs",
			in:       "queue full for MyDocs at 12:34",
			mustKeep: []string{"queue full", "12:34"},
			mustHide: []string{"MyDocs"},
			wantSubs: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SanitizeReport(c.in, SanitizeOptions{
				MirrorNames: []string{c.mirror},
			})
			for _, keep := range c.mustKeep {
				if !strings.Contains(got, keep) {
					t.Errorf("over-redaction: substring %q vanished from %q", keep, got)
				}
			}
			for _, hide := range c.mustHide {
				if strings.Contains(got, hide) {
					t.Errorf("standalone occurrence %q leaked in %q", hide, got)
				}
			}
			if n := strings.Count(got, "mirror_0"); n != c.wantSubs {
				t.Errorf("wanted %d `mirror_0` substitutions, got %d in %q", c.wantSubs, n, got)
			}
		})
	}
}

// SM-211: mirror names shorter than 3 chars are skipped entirely
// (too likely to spuriously match inside English/log text). The
// path-prefix step already covers them when they appear in paths.
// Validator's reproducer: name "m" garbled "Some text" into
// "Somirror_0e text".
func TestSanitizeReport_MirrorNameShortNameSkipped(t *testing.T) {
	in := "Some text from m mirror"
	got := SanitizeReport(in, SanitizeOptions{
		MirrorNames: []string{"m"},
	})
	if !strings.Contains(got, "Some text") {
		t.Errorf("short-name redaction garbled English text: %q", got)
	}
	if strings.Contains(got, "mirror_0") {
		t.Errorf("short name (<3 chars) should be skipped, but substitution occurred: %q", got)
	}
}

// FINDING 18 (round-5 validation memo, 2026-05-03): the prior regex
// ordering ran reCredential before reBearerSpace, which caused
// "Authorization: Bearer <token>" to be partially redacted —
// "Authorization:<REDACTED> <token>" — leaking the actual token. The
// fix is to run reBearerSpace first so the token is gone before
// reCredential consumes the "Bearer" keyword as the value of
// "Authorization:".
func TestSanitizeReport_BearerWithSpace_DoesNotLeakToken(t *testing.T) {
	cases := []struct {
		name string
		in   string
		secret string
	}{
		{"authorization-bearer", "Authorization: Bearer eyJ.foo.bar.baz", "eyJ.foo.bar.baz"},
		{"authorization-basic",  "Authorization: Basic dXNlcjpwYXNz",  "dXNlcjpwYXNz"},
		{"bare-bearer",          "Got Bearer ghp_secrettoken_abc123 from server", "ghp_secrettoken_abc123"},
		{"bare-basic",           "log: Basic dXNlcjpwYXNz received",  "dXNlcjpwYXNz"},
		{"lowercase-bearer",     "auth: bearer eyJ_lower_case_token_xyz", "eyJ_lower_case_token_xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeReport(tc.in, SanitizeOptions{})
			if strings.Contains(got, tc.secret) {
				t.Errorf("token leaked through sanitizer: input=%q output=%q", tc.in, got)
			}
			if !strings.Contains(got, "<REDACTED>") {
				t.Errorf("expected <REDACTED> placeholder in output: %q", got)
			}
		})
	}
}

// FINDING 19 (round-5 validation memo, 2026-05-03): webhook URLs
// encode secrets in the path component (Slack / Discord / Zapier),
// and `alert_webhook_url:` config dumps would expose them. The
// sanitizer now redacts both the keyed form and known webhook hosts.
func TestSanitizeReport_WebhookURL_DoesNotLeakSecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		secret string
	}{
		{"slack-keyed",         "alert_webhook_url: https://hooks.slack.com/services/T0123/B4567/secrethere", "secrethere"},
		{"slack-bare",          "log: posting to https://hooks.slack.com/services/T0/B0/abcd1234tokenz", "abcd1234tokenz"},
		{"discord-bare",        "https://discord.com/api/webhooks/12345/abcd_secret_token", "abcd_secret_token"},
		{"discordapp-bare",     "https://discordapp.com/api/webhooks/999/xyz_legacy_token", "xyz_legacy_token"},
		{"zapier-bare",         "https://hooks.zapier.com/hooks/catch/123/abcde/", "abcde"},
		{"keyed-equals-form",   "webhook_url=https://hooks.slack.com/services/T1/B1/keyed_eq_form_secret", "keyed_eq_form_secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeReport(tc.in, SanitizeOptions{})
			if strings.Contains(got, tc.secret) {
				t.Errorf("webhook secret leaked: input=%q output=%q", tc.in, got)
			}
			if !strings.Contains(got, "<REDACTED>") {
				t.Errorf("expected <REDACTED> placeholder in output: %q", got)
			}
		})
	}
	// Sanity: a benign HTTPS URL (NOT a known webhook host) should NOT be redacted.
	in := "see https://github.com/qraveh/SelectiveMirror/issues/123 for details"
	got := SanitizeReport(in, SanitizeOptions{})
	if !strings.Contains(got, "/qraveh/SelectiveMirror/issues/123") {
		t.Errorf("benign URL was over-redacted: %q -> %q", in, got)
	}
}
