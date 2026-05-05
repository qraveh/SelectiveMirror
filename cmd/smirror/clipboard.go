package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

// copyToClipboard writes the given text to the OS clipboard via the
// platform's standard clipboard utility:
//
//   - Windows: clip.exe (always present in System32; ships with the OS).
//   - macOS:   pbcopy (always present, /usr/bin).
//   - Linux:   xclip → wl-copy fallback. Either may be missing on
//              minimal installations; an informative error is returned
//              so the caller can fall back to printing the report.
//
// PF-E5: the alternative to --browser for
// `smirror report-bug`. Browser-based submit puts the report in a URL
// query string which then ends up in browser history; clipboard is the
// privacy-preserving alternative.
func copyToClipboard(text string) error {
	candidates := clipboardCandidates()
	if len(candidates) == 0 {
		return fmt.Errorf("no clipboard utility known for platform %s", runtime.GOOS)
	}
	var lastErr error
	for _, c := range candidates {
		if _, err := exec.LookPath(c.cmd); err != nil {
			lastErr = fmt.Errorf("%s not on PATH", c.cmd)
			continue
		}
		cmd := exec.Command(c.cmd, c.args...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			lastErr = fmt.Errorf("%s: stdin pipe: %w", c.cmd, err)
			continue
		}
		if err := cmd.Start(); err != nil {
			lastErr = fmt.Errorf("%s: start: %w", c.cmd, err)
			continue
		}
		if _, err := stdin.Write([]byte(text)); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			lastErr = fmt.Errorf("%s: write: %w", c.cmd, err)
			continue
		}
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			lastErr = fmt.Errorf("%s: wait: %w", c.cmd, err)
			continue
		}
		return nil
	}
	return lastErr
}

type clipboardCmd struct {
	cmd  string
	args []string
}

func clipboardCandidates() []clipboardCmd {
	switch runtime.GOOS {
	case "windows":
		return []clipboardCmd{{"clip.exe", nil}}
	case "darwin":
		return []clipboardCmd{{"pbcopy", nil}}
	case "linux":
		return []clipboardCmd{
			{"wl-copy", nil}, // Wayland (newer)
			{"xclip", []string{"-selection", "clipboard"}},
		}
	}
	return nil
}
