package sync

import (
	"strings"

	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// redactRcloneArgs returns a sanitized space-joined form of the rclone
// argv suitable for slog Debug logging. SEC-H9.
//
// We reuse telemetry.SanitizeReport with empty SanitizeOptions, which
// runs the credential regex set and the rclone-style remote-URI redactor
// without applying any path-prefix substitutions (those need cfg state
// the runRclone caller doesn't always carry). The result keeps local
// paths visible (operators need them to debug) but redacts any
// `token=…`, `password=…`, `signature=…`, `key=…`, `Bearer …`,
// `Authorization: …`, or remote-URI form like `gdrive:foo/path` that
// might appear inside a `--rclone-extra-flags` value or a signed URL
// passed as a positional argument.
func redactRcloneArgs(args []string) string {
	return telemetry.SanitizeReport(strings.Join(args, " "), telemetry.SanitizeOptions{})
}

// redactRcloneStderr returns a sanitized form of an rclone stderr line
// (or buffer) for slog Warn logging. SEC-H10.
//
// rclone error output frequently contains:
//   - HTTP-API responses with `Authorization:` or `?signature=…` URLs
//   - OAuth refresh-token JSON with `access_token=…`
//   - "Failed to authenticate: token=ya29.…" diagnostic lines
//   - Long signed-URL forms in retry messages
//
// All of those are caught by telemetry.SanitizeReport's credential and
// bearer regexes. We don't apply path substitutions here either —
// operator needs to see local paths to triage real failures.
func redactRcloneStderr(line string) string {
	return telemetry.SanitizeReport(line, telemetry.SanitizeOptions{})
}
