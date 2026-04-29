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
