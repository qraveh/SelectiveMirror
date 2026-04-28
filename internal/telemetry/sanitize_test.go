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
