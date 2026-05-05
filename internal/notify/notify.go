// Package notify provides Windows toast notifications for smirror events.
// On non-Windows platforms, notifications are no-ops (logged only).
package notify

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Level indicates the severity of a notification.
type Level int

const (
	Info    Level = iota
	Warning
	Error
)

// Notifier sends Windows toast notifications.
// Thread-safe; rate-limited to avoid notification storms.
//
// Two layered rate limits (panel B-tier 2026-05-04):
//   - Per-key dedup (`last` map): the same key within `minGap` is
//     suppressed. Default 5 minutes.
//   - Global token bucket (`globalTokens`/`globalRefilledAt`): caps the
//     total toast volume across ALL keys. Without this, 1000 distinct
//     paths failing in a burst would spawn 1000 PowerShell goroutines
//     simultaneously — desktop notification spam plus subprocess
//     storm. Bucket holds globalCapacity tokens, refills at
//     globalRefillRate per second; Send drops the toast (and logs)
//     once the bucket is empty until tokens are refilled.
type Notifier struct {
	enabled bool
	mu      sync.Mutex
	last    map[string]time.Time // key -> last notification time (dedup)
	minGap  time.Duration        // minimum gap between same-key notifications
	log     *slog.Logger

	// Global rate-limit (token bucket).
	globalTokens     int       // tokens currently available
	globalCapacity   int       // bucket capacity (default 10)
	globalRefillRate float64   // tokens added per second (default 1.0/30s ≈ 0.033)
	globalRefilledAt time.Time // last time tokens were added
}

// New creates a notifier. If enabled is false, all calls are no-ops.
func New(enabled bool) *Notifier {
	return &Notifier{
		enabled:          enabled,
		last:             make(map[string]time.Time),
		minGap:           5 * time.Minute,
		log:              slog.Default().With("component", "notify"),
		globalTokens:     10, // start full
		globalCapacity:   10,
		globalRefillRate: 1.0 / 30.0, // 1 token every 30s; sustainable rate ~120/hour
		globalRefilledAt: time.Now(),
	}
}

// refillTokens adds tokens based on time elapsed since the last refill,
// up to capacity. Caller must hold n.mu. Returns the new token count.
func (n *Notifier) refillTokens() int {
	now := time.Now()
	elapsed := now.Sub(n.globalRefilledAt).Seconds()
	added := int(elapsed * n.globalRefillRate)
	if added > 0 {
		n.globalTokens += added
		if n.globalTokens > n.globalCapacity {
			n.globalTokens = n.globalCapacity
		}
		n.globalRefilledAt = now
	}
	return n.globalTokens
}

// Send displays a Windows toast notification.
// The key parameter is used for rate-limiting: duplicate keys within minGap are suppressed.
// Bursts beyond the global token-bucket capacity are also dropped (panel B-tier).
func (n *Notifier) Send(level Level, title, message, key string) {
	if !n.enabled || runtime.GOOS != "windows" {
		return
	}

	n.mu.Lock()
	// Per-key dedup
	if last, ok := n.last[key]; ok && time.Since(last) < n.minGap {
		n.mu.Unlock()
		n.log.Debug("notification suppressed (per-key rate limit)", "key", key)
		return
	}
	// Global token bucket — refill first, then check
	if n.refillTokens() <= 0 {
		n.mu.Unlock()
		n.log.Warn("notification suppressed (global rate limit)", "key", key, "title", title)
		return
	}
	n.globalTokens--
	n.last[key] = time.Now()
	n.mu.Unlock()

	n.log.Info("notification", "level", level, "title", title, "message", message)
	go n.sendToast(title, message)
}

// Convenience methods

// SyncFailure notifies about repeated sync failures for a file.
func (n *Notifier) SyncFailure(project, path string, exitCode int) {
	n.Send(Error,
		fmt.Sprintf("smirror: sync failed (%s)", project),
		fmt.Sprintf("File: %s\nrclone exit code: %d", path, exitCode),
		fmt.Sprintf("sync_fail:%s:%s", project, path),
	)
}

// VerifyDrift notifies about drift detected during periodic verify.
func (n *Notifier) VerifyDrift(project string, driftCount int) {
	n.Send(Warning,
		fmt.Sprintf("smirror: drift detected (%s)", project),
		fmt.Sprintf("%d files out of sync with remote", driftCount),
		fmt.Sprintf("verify_drift:%s", project),
	)
}

// PathGone notifies when a project's local path disappears.
func (n *Notifier) PathGone(project, path string) {
	n.Send(Error,
		fmt.Sprintf("smirror: project path missing (%s)", project),
		fmt.Sprintf("Directory no longer exists: %s", path),
		fmt.Sprintf("path_gone:%s", project),
	)
}

// xmlEscape escapes a string for safe inclusion as XML element content.
// Pre-public-flip review (panel B-tier 2026-05-04, "Toast notification
// XML injection"): the prior implementation only escaped PowerShell `'`
// in title/message, which is correct for the BurntToast `-Text` path
// but the fallback `<text>%s</text>` XML path remained vulnerable to
// filenames containing `<`, `>`, `&`. A filename like `<x>` corrupts
// the XML; the toast silently fails (LoadXml throws). Not RCE — both
// paths run inside a child PowerShell with no privilege escalation —
// but a defensible toast that just fails-to-render is embarrassing
// when the maintainer is debugging "why didn't I see that anomaly?"
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;") // first — must precede the other &-named entities
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\n", "&#10;")
	s = strings.ReplaceAll(s, "\r", "&#13;")
	return s
}

// sendToast invokes PowerShell to display a Windows toast notification.
// Runs in a goroutine; errors are logged but never propagated.
//
// Two escaping passes:
//   - PowerShell single-quote doubling for the BurntToast `-Text` path
//     (which uses single-quoted PowerShell string literals).
//   - Full XML escaping for the fallback ToastNotificationManager path
//     where the title/message are interpolated into `<text>%s</text>`.
//
// The two paths receive DIFFERENT escaped values because PowerShell
// '' inside an XML attribute would be visible as the literal '' in
// the rendered toast.
func (n *Notifier) sendToast(title, message string) {
	// PowerShell single-quote doubling for the BurntToast path
	psTitle := strings.ReplaceAll(title, "'", "''")
	psMessage := strings.ReplaceAll(message, "'", "''")
	psMessage = strings.ReplaceAll(psMessage, "\n", "`n") // PS backtick-n for newline

	// Full XML escape for the fallback path
	xmlTitle := xmlEscape(title)
	xmlMessage := xmlEscape(message)

	// Use BurntToast if available, fall back to basic .NET toast
	script := fmt.Sprintf(`
$ErrorActionPreference = 'SilentlyContinue'
if (Get-Command New-BurntToastNotification -ErrorAction SilentlyContinue) {
    New-BurntToastNotification -Text '%s', '%s' -AppLogo $null
} else {
    [Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
    [Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom, ContentType = WindowsRuntime] | Out-Null
    $xml = @"
<toast>
  <visual>
    <binding template="ToastGeneric">
      <text>%s</text>
      <text>%s</text>
    </binding>
  </visual>
</toast>
"@
    $doc = [Windows.Data.Xml.Dom.XmlDocument]::new()
    $doc.LoadXml($xml)
    $toast = [Windows.UI.Notifications.ToastNotification]::new($doc)
    [Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('smirror').Show($toast)
}
`, psTitle, psMessage, xmlTitle, xmlMessage)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err := cmd.Run(); err != nil {
		n.log.Debug("toast notification failed", "error", err)
	}
}
