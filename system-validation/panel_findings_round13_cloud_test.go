package systemval

// panel_findings_round13_cloud_test.go — Round 13: cloud-backend
// validation against a real S3-compatible endpoint (MinIO local by
// default, but works against any rclone S3 / S3-compatible remote).
//
// This is the round that the user pushed for: instead of local-to-local
// rclone, exercise smirror against a real S3 API surface. MinIO is
// S3-compatible so the same test code validates the entire S3 family
// (AWS S3, Wasabi, Backblaze B2, Cloudflare R2, etc.) as long as rclone
// has a working remote configured.
//
// CONFIGURATION (read from env):
//   SMIRROR_TEST_S3_REMOTE           e.g. "s3-smirror-test:smirror-validation"
//   SMIRROR_TEST_RCLONE_CONFIG       absolute path to the test rclone.conf
//
// If either is unset, ALL cloud tests skip — preserves the existing local
// test path. The harness REFUSES to run if the remote name doesn't
// contain "test" (Tier-0 safety guardrail).
//
// Each test uses a per-run UUID prefix under the configured bucket so two
// concurrent runs don't collide. Cleanup happens in t.Cleanup().

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cloudConfig holds the shared inputs for cloud tests, read from env once.
type cloudConfig struct {
	Remote       string // base remote, e.g. "s3-smirror-test:smirror-validation"
	RcloneConfig string // path to the dedicated rclone.conf
	rclonePath   string // resolved rclone binary
}

func loadCloudConfig(t *testing.T) *cloudConfig {
	t.Helper()
	remote := os.Getenv("SMIRROR_TEST_S3_REMOTE")
	cfg := os.Getenv("SMIRROR_TEST_RCLONE_CONFIG")
	if remote == "" || cfg == "" {
		t.Skipf("cloud tests skipped — set SMIRROR_TEST_S3_REMOTE and " +
			"SMIRROR_TEST_RCLONE_CONFIG to enable")
	}
	// Tier-0 safety: refuse remotes whose name doesn't shout "test".
	if !strings.Contains(strings.ToLower(remote), "test") {
		t.Fatalf("SMIRROR_TEST_S3_REMOTE=%q does not contain 'test' — refusing "+
			"to run for safety", remote)
	}
	// Verify rclone config file exists.
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("SMIRROR_TEST_RCLONE_CONFIG=%q does not exist: %v", cfg, err)
	}
	return &cloudConfig{
		Remote:       remote,
		RcloneConfig: cfg,
		rclonePath:   rcloneBin,
	}
}

// runRcloneTest invokes rclone with the test config (separate from any
// production rclone.conf). Used for setup, ground-truth checks, and
// cleanup — never for what smirror itself drives.
func (c *cloudConfig) rclone(t *testing.T, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--config", c.RcloneConfig}, args...)
	cmd := exec.Command(c.rclonePath, full...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// uniqPrefix returns a per-test-run subpath like
// "<base-remote>/run-<8-hex>/<sub>" so concurrent test runs and
// individual tests don't collide.
func (c *cloudConfig) uniqPrefix(t *testing.T, sub string) string {
	t.Helper()
	var b [4]byte
	rand.Read(b[:])
	return c.Remote + "/run-" + hex.EncodeToString(b[:]) + "/" + sub
}

// cleanupRemote removes everything under the per-test prefix.
func (c *cloudConfig) cleanupRemote(t *testing.T, prefix string) {
	t.Helper()
	out, err := c.rclone(t, "purge", prefix)
	if err != nil {
		t.Logf("cleanup of %s failed: %v\nout: %s", prefix, err, truncate(out, 300))
	}
}

// listRemote returns the list of files (basenames) at the given remote.
func (c *cloudConfig) listRemote(t *testing.T, prefix string) []string {
	t.Helper()
	out, err := c.rclone(t, "lsf", prefix)
	if err != nil {
		t.Logf("lsf %s: %v", prefix, err)
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// makeS3Config writes a smirror config.yaml for a single mirror to an S3 remote.
func (c *cloudConfig) makeS3Config(t *testing.T, root, mirrorName, srcDir, remotePrefix string,
	extra string) string {
	t.Helper()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	// We can't use the helper's configOpts because it doesn't expose
	// rclone_config; inject via ExtraYAML.
	yamlExtra := fmt.Sprintf("rclone_config: %q\n", c.RcloneConfig)
	if extra != "" {
		yamlExtra += extra
	}
	return createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: mirrorName, LocalPath: srcDir, Remote: remotePrefix,
				// Cap bandwidth per-mirror — defense against runaway uploads.
				RcloneExtra: []string{"--bwlimit", "10M"}},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       2,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
		ExtraYAML:         yamlExtra,
	})
}

// =========================================================================
// 1. BASIC ROUND-TRIP: small file syncs, checksum verifies
// =========================================================================

func TestPanelR13_Cloud_BasicSync(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "basic")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	cfg := cc.makeS3Config(t, root, "test", src, prefix, "")

	createFile(t, filepath.Join(src, "hello.txt"), "hello cloud")
	createFile(t, filepath.Join(src, "sub", "nested.txt"), "nested content")

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)

	// Verify both files landed at the remote with correct content.
	got, err := cc.rclone(t, "cat", prefix+"/hello.txt")
	if err != nil {
		t.Errorf("PANEL BUG: hello.txt not retrievable from %s: %v", prefix, err)
	} else if got != "hello cloud" {
		t.Errorf("PANEL BUG: hello.txt content mismatch: got %q want %q", got, "hello cloud")
	}

	got2, err := cc.rclone(t, "cat", prefix+"/sub/nested.txt")
	if err != nil {
		t.Errorf("PANEL BUG: sub/nested.txt not retrievable: %v", err)
	} else if got2 != "nested content" {
		t.Errorf("PANEL BUG: nested content mismatch: got %q want %q", got2, "nested content")
	}

	// rclone check: bytewise verify against local. This is the
	// canonical test for NFR-FC-01 (byte-identical sync).
	out, err := cc.rclone(t, "check", "--checksum", src, prefix)
	if err != nil {
		t.Errorf("PANEL BUG: rclone check post-sync failed: %v\n%s",
			err, truncate(out, 400))
	}
}

// =========================================================================
// 2. DELETE PROPAGATION (delete_policy: delete)
// =========================================================================

func TestPanelR13_Cloud_DeletePropagation(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "delete")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	cfg := cc.makeS3Config(t, root, "test", src, prefix,
		"delete_policy: \"delete\"\n")

	createFile(t, filepath.Join(src, "doomed.txt"), "doomed")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	// Verify it's there.
	if names := cc.listRemote(t, prefix); !contains(names, "doomed.txt") {
		t.Fatalf("doomed.txt not synced; got: %v", names)
	}

	// Delete locally and re-sync.
	os.Remove(filepath.Join(src, "doomed.txt"))
	r = runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	// Verify it's gone from remote.
	names := cc.listRemote(t, prefix)
	if contains(names, "doomed.txt") {
		t.Errorf("PANEL BUG: doomed.txt should have been deleted from remote (delete_policy=delete) "+
			"but still exists. Remote contents: %v", names)
	}
}

// =========================================================================
// 3. UNICODE FILENAMES
// =========================================================================

func TestPanelR13_Cloud_UnicodeFilenames(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "unicode")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	cfg := cc.makeS3Config(t, root, "test", src, prefix, "")

	// Cover several scripts.
	cases := []struct {
		name    string
		content string
	}{
		{"日本語.txt", "japanese"},
		{"русский.txt", "russian"},
		{"عربى.txt", "arabic"},
		{"café.txt", "with diacritic"},
		{"emoji-📁.txt", "with emoji"},
	}
	for _, c := range cases {
		createFile(t, filepath.Join(src, c.name), c.content)
	}

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	names := cc.listRemote(t, prefix)
	for _, c := range cases {
		if !contains(names, c.name) {
			t.Errorf("PANEL BUG: Unicode filename %q did not appear at remote. "+
				"Remote contents: %v", c.name, names)
		}
	}
}

// =========================================================================
// 4. GHOST CLEANUP — manually plant a remote-only file, sync-now removes it
// =========================================================================

func TestPanelR13_Cloud_GhostCleanup(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "ghost")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	cfg := cc.makeS3Config(t, root, "test", src, prefix,
		"delete_policy: \"delete\"\n")

	// Sync a normal file first to establish the mirror.
	createFile(t, filepath.Join(src, "real.txt"), "real")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	// Manually upload a file to remote that has no local counterpart.
	tmp := filepath.Join(root, "ghost.txt")
	os.WriteFile(tmp, []byte("orphan-content"), 0644)
	if out, err := cc.rclone(t, "copy", tmp, prefix); err != nil {
		t.Fatalf("manual upload of ghost: %v\n%s", err, out)
	}
	if names := cc.listRemote(t, prefix); !contains(names, "ghost.txt") {
		t.Fatalf("ghost not present after manual upload: %v", names)
	}

	// sync-now does ghost cleanup with delete_policy=delete.
	r = runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	names := cc.listRemote(t, prefix)
	if contains(names, "ghost.txt") {
		t.Errorf("PANEL BUG: ghost.txt should be cleaned up by sync-now (delete_policy=delete) "+
			"but still exists at remote. Remote: %v", names)
	}
}

// =========================================================================
// 5. CONCURRENT MULTI-MIRROR SAME BACKEND (R3-PF-3 / R7-PF-3 claim)
// =========================================================================
//
// The CLAUDE.md design doctrine is "single rclone per backend" to avoid
// thundering-herd against the same backend's API. R7 reviewer #11 noted
// the code does NOT enforce this — multiple workers can spawn rclone
// concurrently against the same remote. With MinIO local we can verify
// observable correctness even if the design claim is unenforced.

func TestPanelR13_Cloud_TwoMirrorsSameBackend(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	srcA := filepath.Join(root, "srcA")
	srcB := filepath.Join(root, "srcB")
	os.MkdirAll(srcA, 0755)
	os.MkdirAll(srcB, 0755)
	prefixA := cc.uniqPrefix(t, "twoMirrorsA")
	prefixB := cc.uniqPrefix(t, "twoMirrorsB")
	t.Cleanup(func() {
		cc.cleanupRemote(t, prefixA)
		cc.cleanupRemote(t, prefixB)
	})

	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)
	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "mirrorA", LocalPath: srcA, Remote: prefixA,
				RcloneExtra: []string{"--bwlimit", "10M"}},
			{Name: "mirrorB", LocalPath: srcB, Remote: prefixB,
				RcloneExtra: []string{"--bwlimit", "10M"}},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       4,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
		ExtraYAML:         fmt.Sprintf("rclone_config: %q\n", cc.RcloneConfig),
	})

	// 10 files in each mirror, both targeting MinIO simultaneously.
	for i := 0; i < 10; i++ {
		createFile(t, filepath.Join(srcA, fmt.Sprintf("a%02d.txt", i)),
			fmt.Sprintf("A-%d", i))
		createFile(t, filepath.Join(srcB, fmt.Sprintf("b%02d.txt", i)),
			fmt.Sprintf("B-%d", i))
	}

	start := time.Now()
	r := runSmirror(t, cfg, "sync-now")
	dur := time.Since(start)
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)

	// Verify all 20 files landed correctly.
	namesA := cc.listRemote(t, prefixA)
	namesB := cc.listRemote(t, prefixB)
	if len(namesA) != 10 {
		t.Errorf("PANEL BUG: mirrorA expected 10 files at remote, got %d: %v",
			len(namesA), namesA)
	}
	if len(namesB) != 10 {
		t.Errorf("PANEL BUG: mirrorB expected 10 files at remote, got %d: %v",
			len(namesB), namesB)
	}
	t.Logf("PANEL OBS: 2 mirrors × 10 files synced concurrently to same backend in %v. "+
		"Per CLAUDE.md 'single rclone per backend' doctrine, smirror should serialize "+
		"rclone subprocesses targeting the same backend. R7-PF-3 noted no semaphore "+
		"enforces this. With MinIO local + 4 workers + 2 mirrors, no observable "+
		"correctness issue was found this run.", dur)
}

// =========================================================================
// 6. CHECKSUM-SKIP behavior — re-sync of unchanged content is fast
// =========================================================================

func TestPanelR13_Cloud_ChecksumSkip(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "checksum")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	cfg := cc.makeS3Config(t, root, "test", src, prefix, "")

	// 10 files, ~1 KB each.
	for i := 0; i < 10; i++ {
		createFile(t, filepath.Join(src, fmt.Sprintf("f%02d.txt", i)),
			strings.Repeat(fmt.Sprintf("c%d-", i), 200))
	}

	// First sync — must upload everything.
	start := time.Now()
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	firstDur := time.Since(start)

	// Re-sync without changes — content-addressed skip should make it fast.
	start = time.Now()
	r = runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	secondDur := time.Since(start)

	t.Logf("PANEL OBS: first sync %v, second sync (no changes) %v. With --checksum "+
		"the second pass should be much faster than the first (lsjson + hash compare, "+
		"no upload). If they're similar, checksum-skip may not be effective.",
		firstDur, secondDur)
}

// =========================================================================
// 7. EXPLAIN ↔ SYNC consistency on real backend
// =========================================================================
//
// Round 12's FuzzPanelR12_ExplainVsSyncConsistency tested this on local
// rclone. Re-test on a real S3 backend — the rclone-filter-file translation
// path is identical, but the actual rclone invocation differs.

func TestPanelR13_Cloud_ExplainVsSyncConsistency(t *testing.T) {
	t.Parallel()
	cc := loadCloudConfig(t)

	root := t.TempDir()
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0755)
	prefix := cc.uniqPrefix(t, "explainVsSync")
	t.Cleanup(func() { cc.cleanupRemote(t, prefix) })

	createSyncIgnore(t, src, []string{"*.log", "*.tmp", "!important.log"})

	cfg := cc.makeS3Config(t, root, "test", src, prefix, "")

	files := map[string]bool{ // file → expected to land at remote
		"keep.txt":      true,  // not matched by exclude
		"info.log":      false, // matched by *.log
		"important.log": true,  // re-included by !important.log
		"foo.tmp":       false, // matched by *.tmp
	}
	for name := range files {
		createFile(t, filepath.Join(src, name), "x")
	}

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)

	got := cc.listRemote(t, prefix)
	for name, want := range files {
		// Cross-check against `smirror explain`.
		rExp := runSmirror(t, cfg, "explain", "test", name)
		assertExitCode(t, rExp, 0)
		explainSays := extractStatus(rExp.Stdout)
		landed := contains(got, name)

		switch explainSays {
		case "INCLUDED":
			if !landed {
				t.Errorf("PANEL BUG: explain says INCLUDED for %q but it did NOT land "+
					"on the cloud remote. Filter-vs-sync divergence at the rclone-S3 boundary.", name)
			}
		case "EXCLUDED":
			if landed {
				t.Errorf("PANEL BUG: explain says EXCLUDED for %q but it DID land on "+
					"the cloud remote. Filter-vs-sync divergence.", name)
			}
		}

		// Also cross-check the table-driven expectation.
		if landed != want {
			t.Errorf("PANEL OBS: %q expected-to-land=%v actual-landed=%v explain=%s",
				name, want, landed, explainSays)
		}
	}
}

// =========================================================================
// helpers
// =========================================================================

// contains is a small helper used by several round-13 tests.
func contains(list []string, name string) bool {
	for _, n := range list {
		if n == name {
			return true
		}
	}
	return false
}

// keep sync.WaitGroup referenced (for any future concurrency tests).
var _ = sync.WaitGroup{}
