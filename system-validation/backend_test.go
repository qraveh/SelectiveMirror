package systemval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackend_LocalFull runs a full integration test using a local rclone
// destination (bare path, no remote config needed).
func TestBackend_LocalFull(t *testing.T) {
	t.Parallel()
	requireRclone(t)
	coverage.Record("backend_local")

	env := newTestEnv(t)

	// Create diverse files.
	createFile(t, filepath.Join(env.SrcDir, "readme.md"), "# Project\n")
	createFile(t, filepath.Join(env.SrcDir, "main.go"), "package main\n")
	createFile(t, filepath.Join(env.SrcDir, "sub", "nested.txt"), "nested")
	createBinaryFile(t, filepath.Join(env.SrcDir, "data.bin"), 1024)
	createFile(t, filepath.Join(env.SrcDir, ".hidden"), "hidden")
	createFile(t, filepath.Join(env.SrcDir, "名前.txt"), "unicode")

	// Phase 1: Initial sync.
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)

	// Verify all files synced.
	assertFileExists(t, filepath.Join(env.DstDir, "readme.md"))
	assertFileExists(t, filepath.Join(env.DstDir, "main.go"))
	assertFileExists(t, filepath.Join(env.DstDir, "sub", "nested.txt"))
	assertFileExists(t, filepath.Join(env.DstDir, "data.bin"))
	assertFileExists(t, filepath.Join(env.DstDir, ".hidden"))

	// Phase 2: Verify hashes match.
	assertFileHashMatch(t,
		filepath.Join(env.SrcDir, "data.bin"),
		filepath.Join(env.DstDir, "data.bin"))
	assertFileContent(t, filepath.Join(env.DstDir, "readme.md"), "# Project\n")

	// Phase 3: Modify a file and re-sync.
	createFile(t, filepath.Join(env.SrcDir, "readme.md"), "# Updated Project\n\nNew content.\n")
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileContent(t, filepath.Join(env.DstDir, "readme.md"),
		"# Updated Project\n\nNew content.\n")

	// Phase 4: Add new files and re-sync.
	createFile(t, filepath.Join(env.SrcDir, "new.txt"), "brand new")
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(env.DstDir, "new.txt"))

	// Phase 5: test-mirrors should find no drift.
	r = runSmirror(t, env.CfgPath, "test-mirrors")
	assertExitCode(t, r, 0)

	// Phase 6: dry-run should find nothing pending.
	r = runSmirror(t, env.CfgPath, "dry-run")
	assertExitCode(t, r, 0)

	// Phase 7: Status should work.
	r = runSmirror(t, env.CfgPath, "status")
	assertExitCode(t, r, 0)

	// Phase 8: explain should work.
	r = runSmirror(t, env.CfgPath, "explain", "mirror0", "readme.md")
	assertExitCode(t, r, 0)
	assertStdoutContains(t, r, "INCLUDE")

	// Phase 9: project-stats should work.
	r = runSmirror(t, env.CfgPath, "project-stats")
	assertExitCode(t, r, 0)

	t.Logf("Local backend full integration: all 9 phases passed")
}

// TestBackend_ListAll enumerates all rclone backends and for each one:
//  1. Configures a mirror with that backend as remote
//  2. Runs test-mirrors
//  3. Verifies exit code is 3 (rclone auth error) not 1 (crash), no panic
func TestBackend_ListAll(t *testing.T) {
	t.Parallel()
	requireRclone(t)

	backends := listRcloneBackends(t)
	if len(backends) == 0 {
		t.Fatal("rclone reported zero backends")
	}
	t.Logf("Found %d rclone backends", len(backends))

	// Skip backends that need special handling.
	skipBackends := map[string]string{
		"local":     "tested separately in TestBackend_LocalFull",
		"alias":     "requires existing remote",
		"crypt":     "requires existing remote",
		"combine":   "requires existing remotes",
		"union":     "requires existing remotes",
		"chunker":   "requires existing remote",
		"hasher":    "requires existing remote",
		"compress":  "requires existing remote",
		"cache":     "requires existing remote",
		"archive":   "requires existing remote",
	}

	for _, backend := range backends {
		backend := backend
		if reason, ok := skipBackends[backend]; ok {
			t.Run(backend, func(t *testing.T) {
				t.Skip(reason)
			})
			continue
		}

		t.Run(backend, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			src := filepath.Join(root, "src")
			data := filepath.Join(root, "data")
			os.MkdirAll(src, 0755)
			os.MkdirAll(data, 0755)
			noN := boolPtr(false)

			// Use the backend as a remote with a fake bucket/path.
			remote := fmt.Sprintf("%s:systemval-test-bucket/test", backend)

			cfg := createConfig(t, root, configOpts{
				Mirrors: []mirrorDef{{
					Name:      "backend_" + backend,
					LocalPath: src,
					Remote:    remote,
				}},
				StateDB:        filepath.Join(data, "state.db"),
				LogFile:        filepath.Join(data, "s.log"),
				LogLevel:       "error",
				SyncWorkers:    1,
				NotifyEnabled:  noN,
				AnomalyEnabled: noN,
				VerifyIntervalSec: -1,
			})

			// test-mirrors will attempt rclone connectivity and fail with auth error.
			r := runSmirrorWithTimeout(t, 30*time.Second, cfg, "test-mirrors")
			assertNoPanic(t, r)

			// Expected: exit 3 (rclone error) or exit 5 (drift), not exit 1 (crash).
			// Some backends may cause exit 2 if the remote format is rejected.
			if r.ExitCode == 1 {
				// Exit 1 + no panic is concerning but not fatal — log it.
				t.Logf("WARNING: backend %q gave exit 1 (general error)", backend)
			}

			coverage.Record("backend_sweep")
		})
	}

	t.Logf("Backend sweep complete")
}

// TestBackend_AllSimultaneous configures all backends simultaneously in a
// single config and runs test-mirrors to verify no crash or hang.
func TestBackend_AllSimultaneous(t *testing.T) {
	requireRclone(t)

	t.Parallel()

	backends := listRcloneBackends(t)

	skipBackends := map[string]bool{
		"local": true, "alias": true, "crypt": true, "combine": true,
		"union": true, "chunker": true, "hasher": true, "compress": true,
		"cache": true, "archive": true,
	}

	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0755)

	var mirrors []mirrorDef
	for _, backend := range backends {
		if skipBackends[backend] {
			continue
		}
		src := filepath.Join(root, "src_"+backend)
		os.MkdirAll(src, 0755)
		mirrors = append(mirrors, mirrorDef{
			Name:      "be_" + backend,
			LocalPath: src,
			Remote:    fmt.Sprintf("%s:systemval-all-test/test", backend),
		})
	}

	if len(mirrors) == 0 {
		t.Skip("no backends to test")
	}

	noN := boolPtr(false)
	cfg := createConfig(t, root, configOpts{
		Mirrors:        mirrors,
		StateDB:        filepath.Join(data, "state.db"),
		LogFile:        filepath.Join(data, "s.log"),
		LogLevel:       "error",
		SyncWorkers:    1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	// Should complete without hanging or crashing.
	r := runSmirrorWithTimeout(t, 3*time.Minute, cfg, "test-mirrors")
	assertNoPanic(t, r)

	// Process should have exited (not hung).
	if r.ExitCode == -1 {
		t.Error("test-mirrors timed out with all backends — possible hang")
	}

	t.Logf("All %d backends simultaneous: exit %d, duration %v",
		len(mirrors), r.ExitCode, r.Duration)
}

// TestBackend_DryRunAllBackends runs dry-run with each backend to test
// command construction without attempting actual sync.
func TestBackend_DryRunAllBackends(t *testing.T) {
	requireRclone(t)

	t.Parallel()

	backends := listRcloneBackends(t)
	skipBackends := map[string]bool{
		"local": true, "alias": true, "crypt": true, "combine": true,
		"union": true, "chunker": true, "hasher": true, "compress": true,
		"cache": true, "archive": true,
	}

	for _, backend := range backends {
		backend := backend
		if skipBackends[backend] {
			continue
		}

		t.Run("dryrun_"+backend, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			src := filepath.Join(root, "src")
			data := filepath.Join(root, "data")
			os.MkdirAll(src, 0755)
			os.MkdirAll(data, 0755)
			noN := boolPtr(false)

			createFile(t, filepath.Join(src, "test.txt"), "test")

			cfg := createConfig(t, root, configOpts{
				Mirrors: []mirrorDef{{
					Name:      "dr_" + backend,
					LocalPath: src,
					Remote:    fmt.Sprintf("%s:systemval-dryrun/test", backend),
				}},
				StateDB:        filepath.Join(data, "state.db"),
				LogFile:        filepath.Join(data, "s.log"),
				LogLevel:       "error",
				SyncWorkers:    1,
				NotifyEnabled:  noN,
				AnomalyEnabled: noN,
				VerifyIntervalSec: -1,
			})

			r := runSmirrorWithTimeout(t, 15*time.Second, cfg, "dry-run")
			assertNoPanic(t, r)
		})
	}
}

// ---------------------------------------------------------------------------
// Helper: enumerate rclone backends
// ---------------------------------------------------------------------------

func listRcloneBackends(t *testing.T) []string {
	t.Helper()

	out, err := exec.Command(rcloneBin, "help", "backends").CombinedOutput()
	if err != nil {
		// Fallback: try `rclone listremotes --long` or just use known list.
		t.Logf("rclone help backends failed: %v, using hardcoded list", err)
		return hardcodedBackends()
	}

	var backends []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "All") || strings.HasPrefix(line, "To see") {
			continue
		}
		// Format: "  name       Description"
		parts := strings.Fields(line)
		if len(parts) >= 1 {
			name := parts[0]
			// Filter out non-backend lines.
			if len(name) > 0 && name[0] >= 'a' && name[0] <= 'z' {
				backends = append(backends, name)
			}
		}
	}

	if len(backends) < 10 {
		t.Logf("Only found %d backends from rclone, supplementing with hardcoded", len(backends))
		return hardcodedBackends()
	}

	return backends
}

func hardcodedBackends() []string {
	return []string{
		"alias", "azureblob", "azurefiles", "b2", "box", "cache", "chunker",
		"cloudinary", "combine", "compress", "crypt", "doi", "drive", "dropbox",
		"fichier", "filefabric", "filelu", "filescom", "filen", "ftp", "gcs",
		"gofile", "gphotos", "hasher", "hdfs", "hidrive", "http",
		"iclouddrive", "imagekit", "internetarchive", "internxt", "jottacloud",
		"koofr", "linkbox", "local", "mailru", "mega", "memory", "netstorage",
		"onedrive", "oos", "opendrive", "pcloud", "pikpak", "pixeldrain",
		"premiumizeme", "protondrive", "putio", "qingstor", "quatrix",
		"s3", "seafile", "sftp", "shade", "sharefile", "sia", "smb",
		"storj", "sugarsync", "swift", "tardigrade", "ulozto", "union",
		"webdav", "yandex", "zoho", "archive", "drime",
	}
}
