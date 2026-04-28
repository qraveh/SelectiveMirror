package anomaly

import (
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
)

// SEC-M5: extra path prefixes that the engine wants redacted alongside
// the user's home directory. Set via SetExtraSanitizePrefixes from
// startup once cfg.Projects is known. Each entry's content (a project's
// LocalPath) and its forward-slash form get substituted to a stable
// placeholder so anomaly logs and webhooks don't leak full project
// paths in service mode (where LocalSystem's home is C:\Windows\System32
// and the projects live anywhere on disk).
var (
	extraPrefixesMu       gosync.RWMutex
	extraPrefixesNative   []string // native-separator form
	extraPrefixesSlash    []string // forward-slash form
	extraPrefixPlaceholder = "<mirror>"
)

// SetExtraSanitizePrefixes registers extra path prefixes (typically
// per-mirror local_path values) that SanitizePath / SanitizeAnomaly
// should redact alongside the user home dir. Pass nil/empty to clear.
// Safe to call concurrently with SanitizePath; the slice is
// re-assigned under lock and reads take a read lock per call.
func SetExtraSanitizePrefixes(paths []string) {
	extraPrefixesMu.Lock()
	defer extraPrefixesMu.Unlock()
	extraPrefixesNative = extraPrefixesNative[:0]
	extraPrefixesSlash = extraPrefixesSlash[:0]
	for _, p := range paths {
		if p == "" {
			continue
		}
		extraPrefixesNative = append(extraPrefixesNative, p)
		extraPrefixesSlash = append(extraPrefixesSlash, filepath.ToSlash(p))
	}
}

// SanitizePath redacts the user's home directory and any registered
// extra prefixes (SEC-M5) from file paths.
//
// "C:\Users\raveh\Documents\foo.txt" → "~/Documents/foo.txt"
// "C:\Orch\some\file.txt" → "<mirror>/some/file.txt" (if C:\Orch is
//
//	registered via SetExtraSanitizePrefixes)
func SanitizePath(path string) string {
	if path == "" {
		return ""
	}
	normPath := filepath.ToSlash(path)

	home, _ := os.UserHomeDir()
	if home != "" {
		normHome := filepath.ToSlash(home)
		if strings.HasPrefix(normPath, normHome) {
			return "~" + normPath[len(normHome):]
		}
	}

	// SEC-M5: try registered extra prefixes (longest-match first
	// would matter for nested mirrors; we accept the first match
	// and suggest callers register most-specific paths first).
	extraPrefixesMu.RLock()
	defer extraPrefixesMu.RUnlock()
	for _, p := range extraPrefixesSlash {
		if strings.HasPrefix(normPath, p) {
			return extraPrefixPlaceholder + normPath[len(p):]
		}
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

// sanitizeText replaces home directory and registered extra prefixes
// in free-text fields. SEC-M5 extends prior home-only behavior.
func sanitizeText(text string) string {
	if text == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = strings.ReplaceAll(text, home, "~")
		text = strings.ReplaceAll(text, filepath.ToSlash(home), "~")
	}
	extraPrefixesMu.RLock()
	defer extraPrefixesMu.RUnlock()
	for i, p := range extraPrefixesNative {
		text = strings.ReplaceAll(text, p, extraPrefixPlaceholder)
		text = strings.ReplaceAll(text, extraPrefixesSlash[i], extraPrefixPlaceholder)
	}
	return text
}
