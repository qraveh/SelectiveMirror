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
type Notifier struct {
	enabled bool
	mu      sync.Mutex
	last    map[string]time.Time // key -> last notification time (dedup)
	minGap  time.Duration        // minimum gap between same-key notifications
	log     *slog.Logger
}

// New creates a notifier. If enabled is false, all calls are no-ops.
func New(enabled bool) *Notifier {
	return &Notifier{
		enabled: enabled,
		last:    make(map[string]time.Time),
		minGap:  5 * time.Minute,
		log:     slog.Default().With("component", "notify"),
	}
}

// Send displays a Windows toast notification.
// The key parameter is used for rate-limiting: duplicate keys within minGap are suppressed.
func (n *Notifier) Send(level Level, title, message, key string) {
	if !n.enabled || runtime.GOOS != "windows" {
		return
	}

	// Rate-limit by key
	n.mu.Lock()
	if last, ok := n.last[key]; ok && time.Since(last) < n.minGap {
		n.mu.Unlock()
		n.log.Debug("notification suppressed (rate limit)", "key", key)
		return
	}
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

// sendToast invokes PowerShell to display a Windows toast notification.
// Runs in a goroutine; errors are logged but never propagated.
func (n *Notifier) sendToast(title, message string) {
	// Escape single quotes for PowerShell
	title = strings.ReplaceAll(title, "'", "''")
	message = strings.ReplaceAll(message, "'", "''")
	// Replace newlines with \n literal for XML
	message = strings.ReplaceAll(message, "\n", "&#10;")

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
`, title, message, title, message)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	if err := cmd.Run(); err != nil {
		n.log.Debug("toast notification failed", "error", err)
	}
}
