package anomaly

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizePath_HomeDirRedacted(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	path := filepath.Join(home, "Documents", "secret.txt")
	got := SanitizePath(path)
	if got == path {
		t.Errorf("expected home dir redacted, got %q", got)
	}
	if got != "~/Documents/secret.txt" {
		t.Errorf("expected ~/Documents/secret.txt, got %q", got)
	}
}

func TestSanitizePath_Empty(t *testing.T) {
	if got := SanitizePath(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSanitizePath_NoHomePrefix(t *testing.T) {
	got := SanitizePath("/tmp/unrelated/file.txt")
	if got != "/tmp/unrelated/file.txt" {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestSanitizeAnomaly_RedactsAll(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	a := &Anomaly{
		Path:    filepath.Join(home, "file.txt"),
		Message: "error in " + home + " directory",
		Detail:  "stack at " + filepath.ToSlash(home) + "/main.go:42",
	}
	SanitizeAnomaly(a)

	if a.Path != "~/file.txt" {
		t.Errorf("path = %q", a.Path)
	}
	if a.Message == "error in "+home+" directory" {
		t.Error("message not sanitized")
	}
	if a.Detail == "stack at "+filepath.ToSlash(home)+"/main.go:42" {
		t.Error("detail not sanitized")
	}
}

func TestSanitizeAnomaly_Nil(t *testing.T) {
	SanitizeAnomaly(nil) // should not panic
}

func TestSanitizeAnomaly_ExtraPrefixesCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path casing behavior")
	}
	SetExtraSanitizePrefixes([]string{`C:\Work\ClientA`})
	t.Cleanup(func() { SetExtraSanitizePrefixes(nil) })

	a := &Anomaly{
		Path:    `c:\work\clienta\secret.txt`,
		Message: `failure under c:\work\clienta\secret.txt`,
		Detail:  `rclone copied c:/work/clienta/secret.txt`,
	}
	SanitizeAnomaly(a)

	combined := strings.ToLower(a.Path + "\n" + a.Message + "\n" + a.Detail)
	for _, forbidden := range []string{`c:\work\clienta`, "c:/work/clienta", "clienta"} {
		if strings.Contains(combined, strings.ToLower(forbidden)) {
			t.Fatalf("anomaly sanitizer leaked case-varied mirror prefix %q:\n%+v", forbidden, a)
		}
	}
}
