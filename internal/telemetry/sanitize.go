// Shared report sanitization for the report-bug and crash-report paths.
//
// The validation contract (docs/PRIVACY.md + the system-validation
// suite at C:\mine\SelectiveMirror\system-validation) requires that bug
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
	//
	// SM-180: env-var-style ALL_CAPS_WITH_UNDERSCORES names where the
	// sensitive word is a SUFFIX (`GITHUB_TOKEN=...`, `OPENAI_API_KEY=...`,
	// `AWS_SESSION_TOKEN=...`) didn't match because `\b` is not a word
	// boundary between two word characters (underscore is `\w`). Fix:
	// allow an optional env-var-shaped prefix `(\w*_)?` before the
	// sensitive keyword. The prefix is captured but not used in the
	// replace callback — `m[:sepIdx+1]` already preserves the full key
	// up to the separator, so `GITHUB_TOKEN=secret` becomes
	// `GITHUB_TOKEN=<REDACTED>`.
	reCredential = regexp.MustCompile(
		`(?i)\b(\w*_)?(token|password|passwd|secret|api[_-]?key|bearer|authorization|client[_-]?secret|access[_-]?token|x[_-]?api[_-]?key|aws[_-]?secret[_-]?access[_-]?key|aws[_-]?access[_-]?key[_-]?id|cookie)\s*[=:]\s*[^\s,;'"<>]+`)

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
	//
	// SM-188: paths with spaces previously leaked. The prior class
	// `[^\s<>"'()]+` terminated at the first space, so
	// `<mirror_0_path>/Project Alpha/Secret File.txt` reduced to
	// `<mirror_0_path>/<files> Alpha/Secret File.txt` — the trailing
	// filename survived. The new pattern allows spaces inside
	// components by terminating only at clear non-path delimiters
	// (tab, newline, angle-bracket, quote, comma, semicolon). Greedy
	// over multiple `[\\/]`-separated components so the entire path
	// collapses to a single `<files>` placeholder. Trade-off: prose
	// after a sanitized path with no clear delimiter may be
	// over-redacted; acceptable in privacy-first.
	rePlaceholderPath = regexp.MustCompile(
		`((?:<mirror_\d+_path>|<configdir>|~))(?:[\\/][^\t\n\r<>"',;]+)+`)

	// SM-189: absolute Windows paths (drive-letter form) that aren't
	// under any configured HomeDir / ConfigDir / MirrorPath placeholder.
	// Bug reports and crash dumps include log lines, panic stacks,
	// service-error messages, hook output, and rclone subprocess
	// output where a path like `C:\Windows\System32\config\SAM` or
	// `D:\Backup\PrivateProject\quarterly.xlsx` can appear. Without a
	// fallback redactor those leak verbatim. Same delimiter rules as
	// rePlaceholderPath above (allows spaces in components, stops at
	// clear non-path delimiters). Replaced with `<path>/<files>`.
	reAbsoluteWinPath = regexp.MustCompile(
		`\b[A-Za-z]:(?:[\\/][^\t\n\r<>"',;]+)+`)

	// rclone-style remote URIs: a short scheme followed by ':' and a
	// path. Excludes a small allowlist of widely-recognized network
	// protocols (http/https/file/git/ws/wss) — checked case-insensitively
	// in the replace callback — so URLs in user-agent strings or bug
	// links survive intact. SM-179 update: scheme allows mixed case and
	// 1-character names (rclone permits both, e.g., "GDrive:..." or
	// "x:..." for a single-letter remote name).
	reRemoteURI = regexp.MustCompile(
		`\b([A-Za-z][A-Za-z0-9_-]{0,30}):([A-Za-z0-9_./\\\-]+)`)

	// FINDING 19 (validation review, 2026-05-03): `https://`
	// is on urlSchemeAllow for safety (so URLs in error messages and
	// bug-reference links survive intact), but webhook URLs encode
	// secrets in the path component. Slack
	// (`https://hooks.slack.com/services/T_/B_/<token>`), Discord
	// (`https://discord.com/api/webhooks/<id>/<token>`), and the
	// alt-spelling `discordapp.com` all match this pattern. The
	// `alert_webhook_url` config key (SelectiveMirror's anomaly-alert
	// destination) is exactly the field most likely to contain such a
	// URL, and it can leak via:
	//   - the user pasting their config into the GitHub-issue body
	//   - log lines that mention the URL (slog Warn lines, retry
	//     diagnostics, etc.)
	//
	// Two layered regexes:
	//
	//   reAlertWebhookKeyed — `alert_webhook_url[=:]<URL>`. Catches
	//     the explicit config-line case. Redacts the entire URL,
	//     keeping the key for diagnostic value.
	//
	//   reKnownWebhookHost — known webhook hostnames followed by a
	//     path. Catches the URL in any context (log, prose, raw
	//     paste). Redacts the path component, keeping the host
	//     ("hooks.slack.com" tells the maintainer "this is a Slack
	//     webhook" without revealing the secret).
	reAlertWebhookKeyed = regexp.MustCompile(
		`(?i)\b(alert_webhook_url|webhook_url|alert_url)\s*[=:]\s*\S+`)

	reKnownWebhookHost = regexp.MustCompile(
		`(?i)\bhttps?://(hooks\.slack\.com|discord(?:app)?\.com/api/webhooks|hooks\.zapier\.com)/\S+`)
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
	// 0. Webhook URLs FIRST. FINDING 19 (validation review,
	//    2026-05-03): `https://` is in urlSchemeAllow so generic URLs
	//    pass through (preserving error-message links etc.), but
	//    webhook URLs encode secrets in the path component. Run these
	//    before steps 1+ so the secret is gone before the rest of the
	//    pipeline looks at the URL.
	//
	//    0a. Keyed form: `alert_webhook_url=URL` / `alert_webhook_url:
	//        URL` / `webhook_url=URL`. Replaces the whole URL with
	//        `<REDACTED>`, preserving the key for diagnostic value.
	report = reAlertWebhookKeyed.ReplaceAllStringFunc(report, func(m string) string {
		sepIdx := strings.IndexAny(m, "=:")
		if sepIdx <= 0 {
			return "<REDACTED>"
		}
		return m[:sepIdx+1] + "<REDACTED>"
	})
	//    0b. Known-webhook-host form: any URL whose host is a recognized
	//        webhook provider (Slack / Discord / Zapier). Redacts the
	//        path component, preserving the host so the maintainer can
	//        see "this is a Slack webhook" without seeing the token.
	report = reKnownWebhookHost.ReplaceAllStringFunc(report, func(m string) string {
		// Find the protocol+host prefix (https://hooks.slack.com),
		// then return prefix + "/<REDACTED>".
		// e.g. https://hooks.slack.com/services/T0/B0/x → https://hooks.slack.com/<REDACTED>
		schemeEnd := strings.Index(m, "://")
		if schemeEnd < 0 {
			return "<REDACTED>"
		}
		hostStart := schemeEnd + 3
		pathStart := strings.Index(m[hostStart:], "/")
		if pathStart < 0 {
			return m // no path; nothing secret to strip
		}
		return m[:hostStart+pathStart] + "/<REDACTED>"
	})

	// 1. Bearer / Basic FIRST — before reCredential. FINDING 18 (round-5
	//    validation memo, 2026-05-03): the prior ordering ran reCredential
	//    first, which matched "Authorization: Bearer eyJ.foo.bar.baz" as
	//    key="Authorization" + sep=":" + value="Bearer" (the value-shape
	//    regex stops at the space after "Bearer"), replacing with
	//    "Authorization:<REDACTED>" — and the actual token survived
	//    intact. Running reBearerSpace first replaces "Bearer
	//    eyJ.foo.bar.baz" → "Bearer <REDACTED>" before reCredential
	//    sees it, so the token is gone regardless of whether an
	//    "Authorization:" prefix is present.
	//
	//    Step 2 below (reCredential on the now-redacted string) will
	//    additionally collapse "Authorization: Bearer" → "Authorization:
	//    <REDACTED>" so the leading scheme word also disappears in the
	//    keyed case. The double replacement is idempotent and produces
	//    "Authorization:<REDACTED> <REDACTED>" — slightly noisy but
	//    correct; the actual secret is gone.
	report = reBearerSpace.ReplaceAllStringFunc(report, func(m string) string {
		// First whitespace separates the scheme from the token.
		for i, c := range m {
			if c == ' ' || c == '\t' {
				return m[:i] + " <REDACTED>"
			}
		}
		return "<REDACTED>"
	})
	// 2. Credential key=value / key: value pairs. Runs after step 1 so
	//    a `Bearer <token>` or `Basic <token>` value has already been
	//    redacted by the time the credential regex looks for `bearer`
	//    or `authorization` as a key.
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

	// 4. SM-189: fallback redaction for absolute Windows paths (drive-
	//    letter form) NOT under any configured prefix. The earlier
	//    prefix-substitution step (#2) handles paths under HomeDir /
	//    ConfigDir / MirrorPath; this step catches everything else —
	//    log lines like "error opening C:\Windows\System32\config\SAM",
	//    hook stderr referencing absolute paths, panic stacks, etc.
	//    Replaced with `<path>/<files>` so the maintainer sees the
	//    fact-of-an-absolute-path without the actual path.
	//
	//    Order matters: this MUST run BEFORE step 5 (remote URI),
	//    because the SM-179-relaxed remote URI regex now accepts
	//    1-character schemes, which would otherwise misclassify
	//    `C:\foo` as the 1-char remote `c:` followed by `\foo`.
	//    Drive-path is the more-likely interpretation when a
	//    single-letter scheme is followed by `[\\/]` plus content.
	report = reAbsoluteWinPath.ReplaceAllString(report, "<path>/<files>")

	// 5. Remote URI redaction. Done after path subs because a path
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

	// 6. Mirror name substitution. Done LAST so that a name appearing
	//    inside a path was already covered by the path substitution
	//    in step 2. Length-descending order avoids a name being a
	//    prefix of another name (e.g., "Acme" replaced inside
	//    "AcmeCorp").
	//
	//    SM-210/SM-211: matched case-INSENSITIVELY (Windows log lines
	//    can emit a mirror name in any casing), with ASCII word
	//    boundaries so a short / common-substring name (e.g. "log",
	//    "test", or even "m") doesn't garble English text by matching
	//    inside other words. Names shorter than 3 chars are skipped
	//    entirely — too likely to spuriously match; the path-prefix
	//    step (#2) already covers them when they appear in paths.
	type nameSub struct{ name, repl string }
	var nameSubs []nameSub
	for i, n := range opts.MirrorNames {
		if n == "" || len(n) < 3 {
			continue
		}
		nameSubs = append(nameSubs, nameSub{n, fmt.Sprintf("mirror_%d", i)})
	}
	sort.SliceStable(nameSubs, func(i, j int) bool {
		return len(nameSubs[i].name) > len(nameSubs[j].name)
	})
	for _, s := range nameSubs {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(s.name) + `\b`)
		report = re.ReplaceAllString(report, s.repl)
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
