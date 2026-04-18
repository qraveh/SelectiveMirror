//go:build windows

package task

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// runner is the function used to shell out to schtasks.exe. It is indirected
// through a package variable so tests can replace it with a fake that records
// calls and returns canned output. The production implementation is
// defaultRunner (below).
var runner = defaultRunner

// defaultRunner executes schtasks.exe with the given args and returns its
// combined output.
func defaultRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// currentUser returns the DOMAIN\user principal for the logged-on user.
// Indirected for test injection.
var currentUser = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("current user: %w", err)
	}
	return u.Username, nil
}

// Install registers the SelectiveMirror scheduled task for the current user.
// The task runs smirror at user logon with "start --config <configPath>".
// Returns ErrAlreadyInstalled if the task already exists.
func Install(configPath string) error {
	installed, err := IsInstalled()
	if err != nil {
		return fmt.Errorf("check installed: %w", err)
	}
	if installed {
		return ErrAlreadyInstalled
	}

	principal, err := currentUser()
	if err != nil {
		return err
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve smirror path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("absolute smirror path: %w", err)
	}

	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("absolute config path: %w", err)
	}

	def := XMLDefinition{
		UserPrincipal: principal,
		Command:       exePath,
		Arguments:     fmt.Sprintf(`start --config "%s"`, absConfig),
	}
	xmlUTF8, err := def.RenderXML()
	if err != nil {
		return fmt.Errorf("render task XML: %w", err)
	}
	xmlUTF16 := encodeUTF16LEWithBOM(xmlUTF8)

	// schtasks /Create /XML requires a file on disk.
	f, err := os.CreateTemp("", "smirror-task-*.xml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := f.Name()
	if _, err := f.Write(xmlUTF16); err != nil {
		f.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp XML: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp XML: %w", err)
	}
	defer os.Remove(tmpName)

	out, err := runner("schtasks.exe", "/Create", "/TN", TaskName, "/XML", tmpName, "/F")
	if err != nil {
		return fmt.Errorf("schtasks /Create: %w\n%s", err, string(out))
	}
	return nil
}

// Uninstall removes the SelectiveMirror scheduled task for the current user.
// Returns ErrNotInstalled if no task is registered.
func Uninstall() error {
	installed, err := IsInstalled()
	if err != nil {
		return fmt.Errorf("check installed: %w", err)
	}
	if !installed {
		return ErrNotInstalled
	}
	out, err := runner("schtasks.exe", "/Delete", "/TN", TaskName, "/F")
	if err != nil {
		return fmt.Errorf("schtasks /Delete: %w\n%s", err, string(out))
	}
	return nil
}

// Start runs the task immediately (without waiting for logon). Returns
// ErrNotInstalled if no task is registered.
func Start() error {
	installed, err := IsInstalled()
	if err != nil {
		return fmt.Errorf("check installed: %w", err)
	}
	if !installed {
		return ErrNotInstalled
	}
	out, err := runner("schtasks.exe", "/Run", "/TN", TaskName)
	if err != nil {
		return fmt.Errorf("schtasks /Run: %w\n%s", err, string(out))
	}
	return nil
}

// Stop terminates any running instance of the task. Returns nil when the
// task is installed but not running; returns ErrNotInstalled if the task
// isn't registered.
func Stop() error {
	installed, err := IsInstalled()
	if err != nil {
		return fmt.Errorf("check installed: %w", err)
	}
	if !installed {
		return ErrNotInstalled
	}
	// schtasks /End is idempotent — returns a non-zero but non-fatal code
	// when the task isn't running. We swallow that case.
	out, err := runner("schtasks.exe", "/End", "/TN", TaskName)
	if err != nil {
		msg := string(out)
		// "The system cannot find the file specified." / "is not currently running"
		if strings.Contains(msg, "not currently running") ||
			strings.Contains(msg, "cannot find the file specified") {
			return nil
		}
		return fmt.Errorf("schtasks /End: %w\n%s", err, msg)
	}
	return nil
}

// IsInstalled reports whether the SelectiveMirror task is registered for
// the current user.
func IsInstalled() (bool, error) {
	out, err := runner("schtasks.exe", "/Query", "/TN", TaskName, "/FO", "LIST")
	if err != nil {
		msg := string(out)
		// schtasks returns a non-zero exit with a distinctive message when
		// the task doesn't exist. Treat that as "not installed" rather than
		// a query error.
		if strings.Contains(msg, "cannot find the file specified") ||
			strings.Contains(msg, "does not exist") {
			return false, nil
		}
		return false, fmt.Errorf("schtasks /Query: %w\n%s", err, msg)
	}
	return true, nil
}

// Query returns the current Status of the SelectiveMirror task. Always
// safe to call; returns Status{Installed: false} when the task isn't
// registered rather than an error.
func Query() (Status, error) {
	var s Status

	// Prefer XML output — it carries structured fields and ignores
	// locale-dependent LIST format.
	out, err := runner("schtasks.exe", "/Query", "/TN", TaskName, "/XML")
	if err != nil {
		msg := string(out)
		if strings.Contains(msg, "cannot find the file specified") ||
			strings.Contains(msg, "does not exist") {
			return s, nil
		}
		return s, fmt.Errorf("schtasks /Query /XML: %w\n%s", err, msg)
	}
	s.Installed = true

	// Then query LIST /V for LastRunTime / LastResult / NextRunTime fields,
	// which /XML does not expose.
	listOut, err := runner("schtasks.exe", "/Query", "/TN", TaskName, "/FO", "LIST", "/V")
	if err == nil {
		parseQueryList(string(listOut), &s)
	}
	return s, nil
}

// parseQueryList extracts Status fields from the locale-English output of
// `schtasks /Query /FO LIST /V`. Unknown locales fall through gracefully
// (fields remain empty).
func parseQueryList(listOutput string, s *Status) {
	for _, line := range strings.Split(listOutput, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		switch key {
		case "Status":
			s.Running = strings.EqualFold(val, "Running") || strings.EqualFold(val, "Ready")
			// "Running" → actually running now; "Ready" → scheduled but idle.
			// Only "Running" should count as running.
			s.Running = strings.EqualFold(val, "Running")
		case "Last Run Time":
			// Windows reports "11/30/1999 12:00:00 AM" for tasks that have
			// never run (sentinel "no previous run" date). Treat as empty.
			if val != "N/A" && val != "" && !strings.Contains(val, "11/30/1999") {
				s.LastRunTime = val
			}
		case "Last Result":
			// Result 267011 = SCHED_S_TASK_HAS_NOT_RUN (no previous run).
			if val != "N/A" && val != "" && val != "267011" {
				s.LastRunResult = val
			}
		case "Next Run Time":
			if val != "N/A" && val != "" {
				s.NextRunTime = val
			}
		}
	}
}

// encodeUTF16LEWithBOM converts UTF-8 input to UTF-16LE with a leading
// byte-order mark, which is what the Windows Task Scheduler expects for
// XML task definitions on disk.
func encodeUTF16LEWithBOM(utf8 []byte) []byte {
	runes := utf16.Encode([]rune(string(utf8)))
	buf := make([]byte, 2+len(runes)*2)
	// BOM: 0xFF 0xFE (little-endian)
	buf[0] = 0xFF
	buf[1] = 0xFE
	for i, r := range runes {
		buf[2+i*2] = byte(r)
		buf[2+i*2+1] = byte(r >> 8)
	}
	return buf
}

// ExpectedXML returns the XML body (UTF-8) that Install would have written
// for the given inputs. Exported for use by `smirror task` to show the user
// what's about to be installed (preview mode).
func ExpectedXML(exePath, configPath string) ([]byte, error) {
	principal, err := currentUser()
	if err != nil {
		return nil, err
	}
	absExe, err := filepath.Abs(exePath)
	if err != nil {
		return nil, err
	}
	absConfig, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	def := XMLDefinition{
		UserPrincipal:    principal,
		Command:          absExe,
		Arguments:        fmt.Sprintf(`start --config "%s"`, absConfig),
		RegistrationTime: time.Now().UTC(),
	}
	return def.RenderXML()
}

// validateXMLAgainstInstalled is a debug helper: compare the task currently
// installed against what we would install now. Returns any differences.
// Unused at present — kept for future `smirror task verify`.
func validateXMLAgainstInstalled() error {
	installedOut, err := runner("schtasks.exe", "/Query", "/TN", TaskName, "/XML")
	if err != nil {
		return fmt.Errorf("query installed: %w", err)
	}
	var x struct {
		XMLName xml.Name
	}
	return xml.Unmarshal(installedOut, &x)
}
