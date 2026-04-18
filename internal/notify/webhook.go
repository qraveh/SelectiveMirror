package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// WebhookPayload is the JSON body sent to the webhook URL.
type WebhookPayload struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`              // "incident_opened", "incident_escalated", "incident_resolved"
	Kind      string `json:"kind"`               // anomaly kind (e.g. "CircuitBreaker")
	Severity  string `json:"severity"`           // critical, error, warning, info
	Project   string `json:"project,omitempty"`
	Path      string `json:"path,omitempty"`     // relative path (sanitized via SanitizePath)
	Message   string `json:"message"`
	Detail    string `json:"detail,omitempty"`
	Count     int    `json:"count,omitempty"`     // accumulated event count (for escalation)
	Duration  string `json:"duration,omitempty"`  // how long the incident has been open
}

// incident tracks the state of a (kind, project) anomaly stream.
type incident struct {
	openedAt   time.Time
	lastEvent  time.Time
	count      int
	escalated  int // number of escalation alerts sent
	firstMsg   string
	firstDetail string
}

const (
	eventOpened    = "incident_opened"
	eventEscalated = "incident_escalated"
	eventResolved  = "incident_resolved"
)

// WebhookSender delivers incident-based anomaly alerts to an HTTP endpoint.
//
// Alerting model — state transitions, not individual events:
//   - OPENED: first occurrence of a (kind, project) pair → alert immediately
//   - ACCUMULATING: subsequent events for same incident → count silently
//   - ESCALATED: if incident persists for 30 min, send digest with accumulated count
//   - RESOLVED: no events for 10 min after last → alert with summary
//   - Panics always alert (bypass incident grouping)
//   - Async delivery (never blocks the sync path)
type WebhookSender struct {
	url    string
	client *http.Client
	log    *slog.Logger

	mu        sync.Mutex
	incidents map[string]*incident // key: "kind:project"

	// Tunable for testing
	EscalateAfter time.Duration // send escalation digest after this duration (default 30 min)
	ResolveAfter  time.Duration // resolve incident after this silence window (default 10 min)

	// SanitizePath redacts sensitive path prefixes before sending payloads.
	// If nil, paths are sent as-is. Set to anomaly.SanitizePath at init time.
	SanitizePath func(string) string
}

// AllowLoopbackWebhooks controls whether webhooks may target loopback/private
// addresses. Set to true only in tests that use httptest.NewServer. In
// production, leave false (the default) to enforce the SSRF defense.
// SEC-C4.
var AllowLoopbackWebhooks = false

// NewWebhookSender creates a webhook notifier for the given URL.
// SEC-C4: client is hardened against SSRF:
//   - CheckRedirect: redirects are not followed (would bypass URL validation)
//   - DialContext: rejects connections to loopback/private/link-local IPs
//     at each resolution step (defends against DNS rebinding)
func NewWebhookSender(url string) *WebhookSender {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ipAddr := range ips {
				if isBlockedWebhookIP(ipAddr.IP) {
					return nil, fmt.Errorf("webhook: refusing to connect to blocked IP %s (SSRF defense)", ipAddr.IP)
				}
			}
			// Dial using the first safe resolved IP to ensure we connect to
			// what we checked (defense against TOCTOU between lookup and dial).
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	w := &WebhookSender{
		url: url,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // do not follow redirects
			},
		},
		log:           slog.Default().With("component", "webhook"),
		incidents:     make(map[string]*incident),
		EscalateAfter: 30 * time.Minute,
		ResolveAfter:  10 * time.Minute,
	}
	return w
}

// isBlockedWebhookIP rejects loopback, private, link-local, and multicast
// addresses to prevent SSRF against internal metadata/services. SEC-C4.
// Tests that use httptest.NewServer must set AllowLoopbackWebhooks = true.
func isBlockedWebhookIP(ip net.IP) bool {
	if AllowLoopbackWebhooks && (ip.IsLoopback() || ip.IsPrivate()) {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() ||
		ip.IsInterfaceLocalMulticast()
}

// Record processes an anomaly event through the incident state machine.
// Safe for concurrent use. Never blocks.
func (w *WebhookSender) Record(kind, severity, project, path, message, detail string) {
	if w == nil || w.url == "" {
		return
	}

	// SM-090: Sanitize paths before including in webhook payloads.
	if w.SanitizePath != nil {
		path = w.SanitizePath(path)
		detail = w.SanitizePath(detail)
	}

	// Panics always alert immediately — they're rare and critical.
	if kind == "Panic" {
		go w.deliver(WebhookPayload{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Event:     eventOpened,
			Kind:      kind,
			Severity:  severity,
			Project:   project,
			Path:      path,
			Message:   message,
			Detail:    detail,
			Count:     1,
		})
		return
	}

	key := kind + ":" + project
	now := time.Now()

	w.mu.Lock()
	inc, exists := w.incidents[key]

	if !exists {
		// New incident — open it
		w.incidents[key] = &incident{
			openedAt:    now,
			lastEvent:   now,
			count:       1,
			firstMsg:    message,
			firstDetail: detail,
		}
		w.mu.Unlock()

		w.log.Info("incident opened", "kind", kind, "project", project)
		go w.deliver(WebhookPayload{
			Timestamp: now.UTC().Format(time.RFC3339),
			Event:     eventOpened,
			Kind:      kind,
			Severity:  severity,
			Project:   project,
			Path:      path,
			Message:   message,
			Detail:    detail,
			Count:     1,
		})
		return
	}

	// Existing incident — accumulate
	inc.count++
	inc.lastEvent = now

	// Check if escalation is due
	shouldEscalate := false
	sinceOpen := now.Sub(inc.openedAt)
	nextEscalation := w.EscalateAfter * time.Duration(inc.escalated+1)
	if sinceOpen >= nextEscalation {
		inc.escalated++
		shouldEscalate = true
	}
	count := inc.count
	duration := sinceOpen.Round(time.Second).String()
	w.mu.Unlock()

	if shouldEscalate {
		w.log.Info("incident escalated", "kind", kind, "project", project, "count", count, "duration", duration)
		go w.deliver(WebhookPayload{
			Timestamp: now.UTC().Format(time.RFC3339),
			Event:     eventEscalated,
			Kind:      kind,
			Severity:  severity,
			Project:   project,
			Path:      path,
			Message:   fmt.Sprintf("%s — %d events over %s", message, count, duration),
			Detail:    detail,
			Count:     count,
			Duration:  duration,
		})
	}
}

// CheckResolved scans open incidents and resolves any that have been silent
// for ResolveAfter. Call this periodically (e.g., from the heartbeat loop).
func (w *WebhookSender) CheckResolved() {
	if w == nil || w.url == "" {
		return
	}

	now := time.Now()
	var resolved []struct {
		key string
		inc incident
	}

	w.mu.Lock()
	for key, inc := range w.incidents {
		if now.Sub(inc.lastEvent) >= w.ResolveAfter {
			resolved = append(resolved, struct {
				key string
				inc incident
			}{key, *inc})
			delete(w.incidents, key)
		}
	}
	w.mu.Unlock()

	for _, r := range resolved {
		// Parse kind:project from key
		kind, project := parseIncidentKey(r.key)
		duration := r.inc.lastEvent.Sub(r.inc.openedAt).Round(time.Second).String()

		w.log.Info("incident resolved", "kind", kind, "project", project, "count", r.inc.count, "duration", duration)
		go w.deliver(WebhookPayload{
			Timestamp: now.UTC().Format(time.RFC3339),
			Event:     eventResolved,
			Kind:      kind,
			Severity:  "info",
			Project:   project,
			Message:   fmt.Sprintf("Resolved after %d events over %s", r.inc.count, duration),
			Detail:    r.inc.firstDetail,
			Count:     r.inc.count,
			Duration:  duration,
		})
	}
}

// OpenIncidents returns the number of currently open incidents.
func (w *WebhookSender) OpenIncidents() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.incidents)
}

func parseIncidentKey(key string) (kind, project string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// deliver performs the HTTP POST. Runs in a goroutine.
func (w *WebhookSender) deliver(payload WebhookPayload) {
	body, err := json.Marshal(payload)
	if err != nil {
		w.log.Warn("webhook marshal failed", "error", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		w.log.Warn("webhook request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "smirror-webhook/1.0")

	resp, err := w.client.Do(req)
	if err != nil {
		w.log.Warn("webhook delivery failed", "url", w.url, "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		w.log.Warn("webhook endpoint error", "url", w.url, "status", resp.StatusCode)
	} else {
		w.log.Debug("webhook delivered", "event", payload.Event, "kind", payload.Kind, "status", resp.StatusCode)
	}
}

// FormatSlackMessage returns a simple Slack-compatible message string.
func FormatSlackMessage(event, kind, severity, project, message string) string {
	emoji := map[string]string{
		eventOpened:    ":rotating_light:",
		eventEscalated: ":fire:",
		eventResolved:  ":white_check_mark:",
	}
	e := emoji[event]
	if e == "" {
		e = ":grey_question:"
	}
	return fmt.Sprintf("%s *[%s]* %s — %s: %s", e, event, kind, project, message)
}
