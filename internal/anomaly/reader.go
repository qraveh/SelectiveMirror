package anomaly

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ReadRecent reads the most recent N anomalies from the anomaly directory.
// Files are read newest-first (by date in filename). Returns up to limit entries.
func ReadRecent(dataDir string, limit int) ([]*Anomaly, error) {
	anomalyDir := filepath.Join(dataDir, "anomalies")
	entries, err := os.ReadDir(anomalyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Collect anomaly files sorted by name descending (newest first)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "anomalies-") && strings.HasSuffix(e.Name(), ".jsonl") {
			files = append(files, filepath.Join(anomalyDir, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	var result []*Anomaly
	for _, path := range files {
		if len(result) >= limit {
			break
		}
		anomalies, err := readJSONLFile(path)
		if err != nil {
			continue
		}
		result = append(result, anomalies...)
	}

	// Sort by time descending (most recent first) and trim to limit
	sort.Slice(result, func(i, j int) bool {
		return result[i].Time > result[j].Time
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func readJSONLFile(path string) ([]*Anomaly, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var anomalies []*Anomaly
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var a Anomaly
		if err := json.Unmarshal(scanner.Bytes(), &a); err != nil {
			continue
		}
		anomalies = append(anomalies, &a)
	}
	return anomalies, nil
}
