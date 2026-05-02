// Issue-URL builder for report-bug — prefills the GitHub issue form
// with the sanitized environment + logs, with smart truncation for the
// URL-length cap (~8KB on real-world OS / browser combinations).
//
// Extracted from cmdReportBug's --browser branch so the SM-158 --submit
// pipeline can reuse the SAME URL builder. The "always print the
// GitHub-issue URL on --submit" rule from docs/SM-158-report-bug-submit-
// plan.md (2026-05-02 update) requires this: a successful telemetry
// contribution doesn't replace the GitHub Issue — it complements it.
// The narrative lives there.

package main

import (
	"fmt"
	"net/url"
	"runtime"
	"strings"
)

// maxIssueURL is the per-call cap. Empirically: Chrome and Firefox both
// handle URLs up to ~32KB internally, but Windows + many shells choke
// at smaller sizes. 8KB is the conservative ceiling that works
// everywhere we've tested. SM-158: this cap matters for browser- AND
// non-browser paths since the URL we PRINT must also work when the
// user pastes it into a terminal.
const maxIssueURL = 8000

// prefilledIssueURL returns a GitHub issue-creation URL with the bundle
// pre-loaded into the bug_report.yml template's `environment` and
// `logs` form fields. Title is composed from version + GOOS + GOARCH.
//
// Truncates intelligently if the encoded URL exceeds maxIssueURL: first
// truncates `environment` to 3000 chars and `logs` to 1500 chars; if
// still too long, drops `logs` entirely. The bundle is sanitized
// upstream, so even truncated bytes are safe to put on the wire.
//
// Caller passes the already-sanitized bundle. Caller is responsible
// for ensuring the bundle fits the GitHub-issue-template fields.
func prefilledIssueURL(sanitizedBundle string) string {
	envReport := sanitizedBundle
	logReport := ""
	if idx := strings.Index(sanitizedBundle, "\n--- Recent Logs"); idx >= 0 {
		envReport = sanitizedBundle[:idx]
		rest := sanitizedBundle[idx+1:]
		if nl := strings.Index(rest, "\n"); nl >= 0 {
			logReport = rest[nl+1:]
		}
	}

	title := fmt.Sprintf("smirror %s (%s/%s): ", version, runtime.GOOS, runtime.GOARCH)

	build := func(envBody, logBody string) string {
		u := issueBugURL +
			"&title=" + url.QueryEscape(title) +
			"&environment=" + url.QueryEscape(envBody)
		if logBody != "" {
			u += "&logs=" + url.QueryEscape(logBody)
		}
		return u
	}

	out := build(envReport, logReport)
	if len(out) <= maxIssueURL {
		return out
	}

	// Truncate per-field, narrowest first.
	truncEnv := envReport
	if len(truncEnv) > 3000 {
		truncEnv = truncEnv[:3000] + "\n... (truncated, run: smirror report-bug --stdout)"
	}
	truncLog := logReport
	if len(truncLog) > 1500 {
		truncLog = truncLog[:1500] + "\n... (truncated)"
	}
	out = build(truncEnv, truncLog)
	if len(out) <= maxIssueURL {
		return out
	}

	// Last-resort: drop logs entirely.
	return build(truncEnv, "")
}
