package anomaly

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	gosync "sync"
)

// caseInsensitiveOnWindows: NTFS is case-insensitive, so a configured
// mirror at "C:\Work\ClientA" and a runtime path "c:\work\clienta"
// name the SAME directory and both must be redacted. POSIX is case-
// sensitive, so we keep the prior strict matching there. SM-195.
func ciHasPrefix(s, prefix string) bool {
	if runtime.GOOS != "windows" {
		return strings.HasPrefix(s, prefix)
	}
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// ciReplaceAll is strings.ReplaceAll on POSIX and a case-insensitive
// search-and-replace on Windows. Replacement preserves the original
// non-matched bytes verbatim. SM-195.
func ciReplaceAll(text, old, new string) string {
	if runtime.GOOS != "windows" {
		return strings.ReplaceAll(text, old, new)
	}
	if old == "" {
		return text
	}
	lowText := strings.ToLower(text)
	lowOld := strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lowText[i:], lowOld)
		if j == -1 {
			b.WriteString(text[i:])
			return b.String()
		}
		b.WriteString(text[i : i+j])
		b.WriteString(new)
		i += j + len(old)
	}
}

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
// "C:\Users\alice\Documents\foo.txt" → "~/Documents/foo.txt"
// "C:\Projects\MyApp\some\file.txt" → "<mirror>/some/file.txt" (if
//
//	C:\Projects\MyApp is registered via SetExtraSanitizePrefixes)
func SanitizePath(path string) string {
	if path == "" {
		return ""
	}
	normPath := filepath.ToSlash(path)

	home, _ := os.UserHomeDir()
	if home != "" {
		normHome := filepath.ToSlash(home)
		if ciHasPrefix(normPath, normHome) {
			return "~" + normPath[len(normHome):]
		}
	}

	// SEC-M5: try registered extra prefixes (longest-match first
	// would matter for nested mirrors; we accept the first match
	// and suggest callers register most-specific paths first).
	// SM-195: case-insensitive prefix match on Windows so a runtime
	// path with different casing than the registered prefix is still
	// redacted.
	extraPrefixesMu.RLock()
	defer extraPrefixesMu.RUnlock()
	for _, p := range extraPrefixesSlash {
		if ciHasPrefix(normPath, p) {
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
// in free-text fields. SEC-M5 extends prior home-only behavior. SM-195
// makes the replacements case-insensitive on Windows so paths embedded
// in error messages with different casing than the registered prefix
// are still redacted.
func sanitizeText(text string) string {
	if text == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		text = ciReplaceAll(text, home, "~")
		text = ciReplaceAll(text, filepath.ToSlash(home), "~")
	}
	extraPrefixesMu.RLock()
	defer extraPrefixesMu.RUnlock()
	for i, p := range extraPrefixesNative {
		text = ciReplaceAll(text, p, extraPrefixPlaceholder)
		text = ciReplaceAll(text, extraPrefixesSlash[i], extraPrefixPlaceholder)
	}
	return text
}
