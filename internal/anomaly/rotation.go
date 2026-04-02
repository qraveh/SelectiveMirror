package anomaly

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RotationConfig controls cleanup of old anomaly files.
type RotationConfig struct {
	MaxAgeDays int   // default 30
	MaxSizeMB  int64 // default 50 (total across all files)
}

// DefaultRotation returns the default rotation config.
func DefaultRotation() RotationConfig {
	return RotationConfig{MaxAgeDays: 30, MaxSizeMB: 50}
}

// Rotate removes anomaly files older than MaxAgeDays or when total exceeds MaxSizeMB.
// Returns the number of files removed.
func Rotate(dataDir string, cfg RotationConfig) (int, error) {
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = 30
	}
	if cfg.MaxSizeMB <= 0 {
		cfg.MaxSizeMB = 50
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	// Collect anomaly files sorted by name (date-stamped, so oldest first)
	type fileInfo struct {
		path string
		size int64
		date string // YYYY-MM-DD extracted from filename
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "anomalies-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		// Extract date: "anomalies-2026-04-02.jsonl" → "2026-04-02"
		date := strings.TrimPrefix(name, "anomalies-")
		date = strings.TrimSuffix(date, ".jsonl")

		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{
			path: filepath.Join(dataDir, name),
			size: info.Size(),
			date: date,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].date < files[j].date // oldest first
	})

	cutoff := time.Now().AddDate(0, 0, -cfg.MaxAgeDays).Format("2006-01-02")
	removed := 0

	// Phase 1: Remove files older than MaxAgeDays
	var remaining []fileInfo
	for _, f := range files {
		if f.date < cutoff {
			os.Remove(f.path)
			removed++
		} else {
			remaining = append(remaining, f)
		}
	}

	// Phase 2: Remove oldest files if total size exceeds MaxSizeMB
	maxBytes := cfg.MaxSizeMB * 1024 * 1024
	var totalSize int64
	for _, f := range remaining {
		totalSize += f.size
	}
	for len(remaining) > 0 && totalSize > maxBytes {
		oldest := remaining[0]
		os.Remove(oldest.path)
		totalSize -= oldest.size
		remaining = remaining[1:]
		removed++
	}

	return removed, nil
}
