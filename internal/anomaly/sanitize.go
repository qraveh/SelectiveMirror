package anomaly

import (
	"os"
	"path/filepath"
	"strings"
)

// SanitizePath redacts the user's home directory from file paths.
// "C:\Users\raveh\Documents\foo.txt" → "~\Documents\foo.txt"
func SanitizePath(path string) string {
	if path == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	// Normalize both to forward slashes for comparison
	normPath := filepath.ToSlash(path)
	normHome := filepath.ToSlash(home)
	if strings.HasPrefix(normPath, normHome) {
		return "~" + normPath[len(normHome):]
	}
	return path
}

// SanitizeAnomaly redacts paths in an Anomaly's string fields.
func SanitizeAnomaly(a *Anomaly) {
	if a == nil {
		return
	}
	a.Path = SanitizePath(a.Path)
	a.Detail = sanitizeText(a.Detail)
	a.Message = sanitizeText(a.Message)
}

// sanitizeText replaces home directory occurrences in free-text fields.
func sanitizeText(text string) string {
	if text == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return text
	}
	// Replace both slash styles
	text = strings.ReplaceAll(text, home, "~")
	text = strings.ReplaceAll(text, filepath.ToSlash(home), "~")
	return text
}
