// Shared report sanitization for the report-bug and crash-report paths.
//
// The validation contract (docs/PRIVACY.md + the system-validation
// suite at C:\SelectiveMirror\system-validation) requires that bug
// reports and crash reports — both the on-disk save and the version
// posted to GitHub via --browser — strip:
//
//   - filesystem paths under the user's home, the config directory,
//     and any mirror's local_path
//   - bare filenames inside those paths (e.g. "QuarterlyPlan.txt"
//     should not survive after path-prefix redaction)
//   - rclone-style remote URIs (gdrive:foo/bar, s3:bucket/path)
//   - credential-bearing key=value pairs (token=, password=, secret=,
//     api_key=, bearer …, authorization: …, etc.)
//   - mirror names (user-chosen labels often reveal context)
//
// Two paths feed into one sanitizer to avoid drift between the bug
// report builder (cmd/smirror/main.go::cmdReportBug) and the crash
// report builder (cmd/smirror/crashreport.go::buildCrashReport). The
// older bug-report code only redacted paths; SM-164 / SM-171 ask for
// the stricter contract.
//
// This file is the single source of truth.

package telemetry

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SanitizeOptions configures SanitizeReport. All fields are optional —
// any zero value disables that substitution dimension.
type SanitizeOptions struct {
	// HomeDir is the user's home directory. Replaced with "~". Both
	// native-separator and forward-slash forms are matched (case-
	// insensitively, to tolerate Windows path-case variation).
	HomeDir string

	// ConfigDir is the directory containing the loaded config file.
	// Replaced with "<configdir>". Same case/slash handling as HomeDir.
	ConfigDir string

	// MirrorPaths is the list of per-mirror local_path values, in the
	// order the maintainer should see in the redacted output. Mirror
	// at index i is replaced with "<mirror_i_path>".
	MirrorPaths []string

	// MirrorNames is the list of per-mirror Name values, parallel to
	// MirrorPaths but allowed to be a different length / NIL. Each name
	// is replaced with "mirror_<i>". Substring substitution is
	// deliberate: a name can appear inside non-path contexts (log
	// messages like "queue full for CustomerAlpha").
	MirrorNames []string
}

// Pre-compiled regexes — compiled once at package init.
var (
	// Credential-style key=value or key: value pairs. Matches the key,
	// the separator, and the value up to the next whitespace, comma,
	// semicolon, quote, or angle bracket. Case-insensitive on the key.
	//
	// We deliberately keep the matched key name visible in the output
	// (so a maintainer can see "token=<REDACTED>" rather than just
	// "<REDACTED>") — that's diagnostic without leaking the value.
	reCredential = regexp.MustCompile(
		`(?i)\b(token|password|passwd|secret|api[_-]?key|bearer|authorization|client[_-]?secret|access[_-]?token|x[_-]?api[_-]?key|aws[_-]?secret[_-]?access[_-]?key|aws[_-]?access[_-]?key[_-]?id|cookie)\s*[=:]\s*[^\s,;'"<>]+`)

	// Bare bearer / basic auth tokens with whitespace separator instead
	// of '=' or ':'. Catches "Bearer eyJabc..." and "Basic xxxxx" lines
	// that get logged without the surrounding "Authorization:" header.
	reBearerSpace = regexp.MustCompile(
		`(?i)\b(bearer|basic)\s+[A-Za-z0-9._\-=+/]{6,}`)

	// Trailing-path-after-placeholder. After path substitutions have
	// produced strings like "<mirror_0_path>/CustomerAlpha/QuarterlyPlan.txt",
	// this regex chops the trailing path components down to "<files>".
	// The placeholder set is closed: we created these placeholders
	// ourselves, so we can match them precisely.
	rePlaceholderPath = regexp.MustCompile(
		`((?:<mirror_\d+_path>|<configdir>|~))[\\/][^\s<>"'()]+`)

	// rclone-style remote URIs: a short lowercase scheme followed by
	// ':' and a path. Excludes a small allowlist of widely-recognized
	// network protocols (http/https/file/git/ws/wss) so URLs in user-
	// agent strings or bug links survive intact.
	reRemoteURI = regexp.MustCompile(
		`\b([a-z][a-z0-9_-]{1,30}):([A-Za-z0-9_./\\\-]+)`)
)

var urlSchemeAllow = map[string]bool{
	"http":  true,
	"https": true,
	"file":  true,
	"git":   true,
	"ws":    true,
	"wss":   true,
	// Note: "ssh" / "ftp" / "sftp" intentionally NOT here — they're
	// also valid rclone backend names. Treat them as remotes (redact)
	// rather than as URLs; if anyone later embeds an ssh:// URL in a
	// bug report they're better off with redaction.
}

// SanitizeReport returns the redacted form of report under opts. The
// substitution order is documented inline; do not reorder without
// rerunning the system-validation suite. Sanitization is idempotent
// (running it twice produces the same output as running it once).
func SanitizeReport(report string, opts SanitizeOptions) string {
	// 1. Credentials FIRST. If we let path substitution run first and
	//    a token value happened to be a path-shaped string, we'd lose
	//    the chance to redact via name=value pairing.
	report = reCredential.ReplaceAllStringFunc(report, func(m string) string {
		// Preserve the leading key + separator. The match has the
		// form "key<ws><sep><ws>value"; find the separator and keep
		// everything up to and including it.
		sepIdx := strings.IndexAny(m, "=:")
		if sepIdx <= 0 {
			return "<REDACTED>"
		}
		return m[:sepIdx+1] + "<REDACTED>"
	})
	// 1b. Bearer/Basic with whitespace separator. We replace the value
	//     but keep the scheme word visible.
	report = reBearerSpace.ReplaceAllStringFunc(report, func(m string) string {
		// First whitespace separates the scheme from the token.
		for i, c := range m {
			if c == ' ' || c == '\t' {
				return m[:i] + " <REDACTED>"
			}
		}
		return "<REDACTED>"
	})

	// 2. Path-prefix substitutions. Longest-first ensures a parent
	//    directory doesn't shadow a more specific child. Forward-slash
	//    forms are added alongside native-separator forms to catch
	//    log lines that came from rclone / golang stdlib in flipped
	//    form.
	type sub struct{ from, to string }
	var subs []sub
	if opts.HomeDir != "" {
		subs = append(subs,
			sub{opts.HomeDir, "~"},
			sub{filepath.ToSlash(opts.HomeDir), "~"},
		)
	}
	if opts.ConfigDir != "" {
		subs = append(subs,
			sub{opts.ConfigDir, "<configdir>"},
			sub{filepath.ToSlash(opts.ConfigDir), "<configdir>"},
		)
	}
	for i, p := range opts.MirrorPaths {
		if p == "" {
			continue
		}
		repl := fmt.Sprintf("<mirror_%d_path>", i)
		subs = append(subs,
			sub{p, repl},
			sub{filepath.ToSlash(p), repl},
		)
	}
	sort.SliceStable(subs, func(i, j int) bool {
		return len(subs[i].from) > len(subs[j].from)
	})
	for _, s := range subs {
		report = caseInsensitiveReplaceAll(report, s.from, s.to)
	}

	// 3. Trailing-path redaction. Catches anything after a placeholder
	//    that still looks like a filesystem path component (e.g. the
	//    bare filename "QuarterlyPlan.txt" left over after step 2
	//    chopped its parent).
	report = rePlaceholderPath.ReplaceAllString(report, "$1/<files>")

	// 4. Remote URI redaction. Done after path subs because a path
	//    can incidentally contain a colon (rare on Windows; possible
	//    in bytestring contexts). We don't want to misclassify those
	//    as remotes.
	report = reRemoteURI.ReplaceAllStringFunc(report, func(m string) string {
		idx := strings.Index(m, ":")
		if idx <= 0 {
			return m
		}
		scheme := strings.ToLower(m[:idx])
		if urlSchemeAllow[scheme] {
			return m
		}
		return scheme + ":<REDACTED>"
	})

	// 5. Mirror name substitution. Done LAST so that a name appearing
	//    inside a path was already covered by the path substitution
	//    in step 2. Length-descending order avoids a name being a
	//    prefix of another name (e.g., "Acme" replaced inside
	//    "AcmeCorp").
	type nameSub struct{ name, repl string }
	var nameSubs []nameSub
	for i, n := range opts.MirrorNames {
		if n == "" {
			continue
		}
		nameSubs = append(nameSubs, nameSub{n, fmt.Sprintf("mirror_%d", i)})
	}
	sort.SliceStable(nameSubs, func(i, j int) bool {
		return len(nameSubs[i].name) > len(nameSubs[j].name)
	})
	for _, s := range nameSubs {
		report = strings.ReplaceAll(report, s.name, s.repl)
	}

	return report
}

// caseInsensitiveReplaceAll replaces every occurrence of from in s with
// to, matching case-insensitively but preserving to verbatim. Used for
// path substitution because Windows paths may appear in the report in
// either case (the user's choice in config vs. the OS-canonicalized
// form rclone or the watcher emit).
func caseInsensitiveReplaceAll(s, from, to string) string {
	if from == "" {
		return s
	}
	fromLower := strings.ToLower(from)
	var b strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(strings.ToLower(s[i:]), fromLower)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		b.WriteString(to)
		i += idx + len(from)
	}
	return b.String()
}
