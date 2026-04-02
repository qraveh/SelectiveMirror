package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzConfigLoad tests that config.Load never panics on arbitrary YAML input.
func FuzzConfigLoad(f *testing.F) {
	// Seed corpus with valid and edge-case configs
	f.Add([]byte("mirrors:\n  - name: test\n    local_path: /tmp/test\n    remote: local:/tmp/dst\n"))
	f.Add([]byte(""))
	f.Add([]byte("invalid yaml: ["))
	f.Add([]byte("mirrors: null\n"))
	f.Add([]byte("mirrors:\n  - name: \"\"\n"))
	f.Add([]byte("delete_policy: quarantine\nquarantine_days: -1\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		projDir := filepath.Join(dir, "proj")
		os.MkdirAll(projDir, 0755)

		cfgPath := filepath.Join(dir, "config.yaml")
		os.WriteFile(cfgPath, data, 0644)

		// Must not panic — errors are expected and OK
		_, _ = Load(cfgPath)
	})
}
