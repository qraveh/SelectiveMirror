// Package metrics provides persistent operational counters and status tracking.
package metrics

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const latencyRingSize = 1000 // last N sync latencies for percentile calculation

// Collector tracks operational metrics with thread-safe counters.
type Collector struct {
	// Atomic counters (since process start)
	filesSynced      atomic.Int64
	bytesUploaded    atomic.Int64
	syncErrors       atomic.Int64
	totalLatencyMs   atomic.Int64 // sum of all sync durations for average calculation
	metadataSynced   atomic.Int64 // mtime-only updates (no content re-upload)

	// Per-project state (protected by mu)
	mu              sync.RWMutex
	lastSync        map[string]time.Time // project -> last successful sync
	lastError       map[string]string    // project -> last error message
	lastErrorTime   map[string]time.Time // project -> last error timestamp
	lastScanTime    time.Time            // last startup reconciliation
	startTime       time.Time
	queueDepth      atomic.Int64

	// Latency ring buffer for percentile calculation (protected by mu)
	latencyRing [latencyRingSize]int64
	latencyPos  int
	latencyLen  int

	// AnomalySummaryFunc is set by the caller to provide anomaly counts for status output.
	AnomalySummaryFunc func() map[string]int64
}

// Status is the JSON-serializable metrics snapshot.
type Status struct {
	Version         string                   `json:"version"`
	Uptime          string                   `json:"uptime"`
	StartTime       string                   `json:"start_time"`
	LastScanTime    string                   `json:"last_scan_time,omitempty"`
	QueueDepth      int64                    `json:"queue_depth"`
	FilesSynced     int64                    `json:"files_synced"`
	MetadataSynced  int64                    `json:"metadata_synced"`
	BytesUploaded   int64                    `json:"bytes_uploaded"`
	SyncErrors      int64                    `json:"sync_errors"`
	AvgLatencyMs    int64                    `json:"avg_sync_latency_ms"`
	P95LatencyMs    int64                    `json:"p95_sync_latency_ms"`
	P99LatencyMs    int64                    `json:"p99_sync_latency_ms"`
	Projects        map[string]ProjectStatus `json:"projects"`
	AnomalyCounts   map[string]int64         `json:"anomaly_counts,omitempty"`
	GeneratedAt     string                   `json:"generated_at"`
}

// ProjectStatus holds per-project metrics.
type ProjectStatus struct {
	LastSync      string `json:"last_sync,omitempty"`
	LastError     string `json:"last_error,omitempty"`
	LastErrorTime string `json:"last_error_time,omitempty"`
}

// New creates a new metrics collector.
func New() *Collector {
	return &Collector{
		lastSync:      make(map[string]time.Time),
		lastError:     make(map[string]string),
		lastErrorTime: make(map[string]time.Time),
		startTime:     time.Now(),
	}
}

// RecordSync records a successful file sync.
func (c *Collector) RecordSync(project string, bytes int64, latencyMs int64) {
	c.filesSynced.Add(1)
	c.bytesUploaded.Add(bytes)
	c.totalLatencyMs.Add(latencyMs)

	c.mu.Lock()
	c.lastSync[project] = time.Now()
	c.latencyRing[c.latencyPos] = latencyMs
	c.latencyPos = (c.latencyPos + 1) % latencyRingSize
	if c.latencyLen < latencyRingSize {
		c.latencyLen++
	}
	c.mu.Unlock()
}

// latencyPercentile returns the p-th percentile (0-100) of recent sync latencies.
// Returns 0 if no data. Caller must hold mu (at least RLock).
func (c *Collector) latencyPercentile(p int) int64 {
	if c.latencyLen == 0 {
		return 0
	}
	sorted := make([]int64, c.latencyLen)
	copy(sorted, c.latencyRing[:c.latencyLen])
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := (p * (c.latencyLen - 1)) / 100
	return sorted[idx]
}

// LatencyP95 returns the 95th percentile sync latency in ms.
func (c *Collector) LatencyP95() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latencyPercentile(95)
}

// LatencyP99 returns the 99th percentile sync latency in ms.
func (c *Collector) LatencyP99() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latencyPercentile(99)
}

// RecordError records a sync error.
func (c *Collector) RecordError(project string, errMsg string) {
	c.syncErrors.Add(1)

	c.mu.Lock()
	c.lastError[project] = errMsg
	c.lastErrorTime[project] = time.Now()
	c.mu.Unlock()
}

// RecordMetadataSync records a successful mtime-only metadata sync.
func (c *Collector) RecordMetadataSync(project string) {
	c.metadataSynced.Add(1)

	c.mu.Lock()
	c.lastSync[project] = time.Now()
	c.mu.Unlock()
}

// RecordScanComplete records that startup reconciliation finished.
func (c *Collector) RecordScanComplete() {
	c.mu.Lock()
	c.lastScanTime = time.Now()
	c.mu.Unlock()
}

// SetQueueDepth updates the current queue depth.
func (c *Collector) SetQueueDepth(n int64) {
	c.queueDepth.Store(n)
}

// Snapshot returns a JSON-serializable metrics snapshot.
func (c *Collector) Snapshot(version string) Status {
	c.mu.RLock()
	defer c.mu.RUnlock()

	synced := c.filesSynced.Load()
	var avgLatency int64
	if synced > 0 {
		avgLatency = c.totalLatencyMs.Load() / synced
	}

	projects := make(map[string]ProjectStatus)
	for proj, t := range c.lastSync {
		ps := ProjectStatus{
			LastSync: t.UTC().Format(time.RFC3339),
		}
		if errMsg, ok := c.lastError[proj]; ok {
			ps.LastError = errMsg
			if et, ok := c.lastErrorTime[proj]; ok {
				ps.LastErrorTime = et.UTC().Format(time.RFC3339)
			}
		}
		projects[proj] = ps
	}
	// Include projects that have errors but no successful sync
	for proj, errMsg := range c.lastError {
		if _, exists := projects[proj]; !exists {
			ps := ProjectStatus{LastError: errMsg}
			if et, ok := c.lastErrorTime[proj]; ok {
				ps.LastErrorTime = et.UTC().Format(time.RFC3339)
			}
			projects[proj] = ps
		}
	}

	s := Status{
		Version:        version,
		Uptime:         time.Since(c.startTime).Round(time.Second).String(),
		StartTime:      c.startTime.UTC().Format(time.RFC3339),
		QueueDepth:     c.queueDepth.Load(),
		FilesSynced:    synced,
		MetadataSynced: c.metadataSynced.Load(),
		BytesUploaded:  c.bytesUploaded.Load(),
		SyncErrors:     c.syncErrors.Load(),
		AvgLatencyMs:   avgLatency,
		P95LatencyMs:   c.latencyPercentile(95),
		P99LatencyMs:   c.latencyPercentile(99),
		Projects:       projects,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if !c.lastScanTime.IsZero() {
		s.LastScanTime = c.lastScanTime.UTC().Format(time.RFC3339)
	}
	if c.AnomalySummaryFunc != nil {
		s.AnomalyCounts = c.AnomalySummaryFunc()
	}
	return s
}

// WriteStatusFile writes the current metrics snapshot to a JSON file.
func (c *Collector) WriteStatusFile(dataDir, version string) error {
	s := c.Snapshot(version)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling status: %w", err)
	}

	path := filepath.Join(dataDir, "status.json")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating status dir: %w", err)
	}

	// Write atomically via temp file
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing status: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming status: %w", err)
	}
	return nil
}

// FormatHuman returns a human-readable summary of the current metrics.
func (c *Collector) FormatHuman() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	synced := c.filesSynced.Load()
	var avgLatency int64
	if synced > 0 {
		avgLatency = c.totalLatencyMs.Load() / synced
	}

	bytes := c.bytesUploaded.Load()
	var bytesStr string
	switch {
	case bytes >= 1<<30:
		bytesStr = fmt.Sprintf("%.1f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		bytesStr = fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		bytesStr = fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		bytesStr = fmt.Sprintf("%d B", bytes)
	}

	p95 := c.latencyPercentile(95)
	p99 := c.latencyPercentile(99)

	return fmt.Sprintf("Uptime: %s | Files synced: %d | Metadata synced: %d | Uploaded: %s | Errors: %d | Avg latency: %dms | p95: %dms | p99: %dms | Queue: %d",
		time.Since(c.startTime).Round(time.Second),
		synced,
		c.metadataSynced.Load(),
		bytesStr,
		c.syncErrors.Load(),
		avgLatency,
		p95,
		p99,
		c.queueDepth.Load(),
	)
}
