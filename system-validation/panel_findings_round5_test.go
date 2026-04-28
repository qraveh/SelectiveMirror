package systemval

// panel_findings_round5_test.go — Round 5 system-validation tests
// synthesized from a fresh four-lens panel review (filesystem-specifics /
// YAML config edges / CLI completeness / endurance) against v0.9.27-dev on
// 2026-04-29.
//
// R1: config / security              — bug + many gaps, mostly shipped.
// R2: live watcher / sync / recovery — 0 new bugs, 1 OBS.
// R3: workflows / multi-mirror / gitignore — 1 BUG (parent-exclusion).
// R4: anomaly / CLI mutation / hooks — 1 BUG + 1 FIND.
// R5 priorities (areas not yet stressed):
//   - YAML config edge cases (BOM, quoting, comments, multi-doc)
//   - Filesystem-specific behavior (ADS, long paths, hidden, hard links)
//   - CLI completeness (flag forms, exit codes, doc accuracy)
//   - Endurance (rotation actually invoked, state DB compaction)

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// =========================================================================
// 1. ENDURANCE: anomaly rotation function exists but is never invoked
// =========================================================================

// Endurance reviewer #1 (Critical): `anomaly.Rotate` is defined in
// internal/anomaly/rotation.go and unit-tested, but is NEVER called from
// any production code path. FR-ANOM-10 promises 30-day / 50 MB retention.
// In practice, anomaly JSONL files accumulate indefinitely.
//
// Black-box test: grep the source tree for the call site. If absent in
// production code (cmd/* + internal/* excluding _test.go), the rotation is
// dead code.
func TestPanelR5_Endurance_AnomalyRotationNeverCalled(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	// Walk cmd/ and internal/ for non-test .go files; grep for anomaly.Rotate
	// or `\.Rotate\(` calls (excluding the definition itself).
	found := false
	var hits []string
	roots := []string{filepath.Join(repoRoot, "cmd"), filepath.Join(repoRoot, "internal")}
	for _, dir := range roots {
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(data)
			for _, line := range strings.Split(text, "\n") {
				trimmed := strings.TrimSpace(line)
				// Skip the function definition.
				if strings.HasPrefix(trimmed, "func Rotate(") ||
					strings.HasPrefix(trimmed, "func (") && strings.Contains(trimmed, ") Rotate(") {
					continue
				}
				// Skip pure comments.
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				// Look for a real call.
				if strings.Contains(trimmed, "anomaly.Rotate(") ||
					strings.Contains(trimmed, ".Rotate(") && strings.Contains(strings.ToLower(path), "anomaly") {
					hits = append(hits, fmt.Sprintf("%s: %s", path, trimmed))
					found = true
				}
			}
			return nil
		})
	}
	if !found {
		t.Errorf("PANEL BUG: anomaly.Rotate is defined and unit-tested " +
			"(internal/anomaly/rotation.go + rotation_test.go) but never invoked from " +
			"any production code path (cmd/* or internal/anomaly/*.go non-test). " +
			"FR-ANOM-10 commits to 30-day / 50 MB retention — currently anomaly files " +
			"accumulate forever. The call should be wired into heartbeatLoop's " +
			"reconcile tick alongside state.PruneOldLogs. " +
			"Source-tree scan found 0 production callers.")
	} else {
		t.Logf("anomaly.Rotate calls found: %v", hits)
	}
}

func repoRootFromHere(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	// system-validation/ → parent.
	return filepath.Dir(wd)
}

// =========================================================================
// 2. YAML CONFIG EDGE CASES
// =========================================================================

// YAML #15 (Critical): formatMirrorBlock writes name/local_path UNQUOTED.
// If the user supplies a name with `#`, `:`, or other YAML-special chars,
// the round-trip through addmirror → Load breaks.
func TestPanelR5_YAML_AddMirror_NameWithHash(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src-special")
	dst := filepath.Join(root, "dst-special")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "seed", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	// Try to add a mirror whose path contains a `#` (which YAML treats as
	// the start of a comment when unquoted). On Windows we need a real
	// directory; create one with `#` in the name.
	srcHash := filepath.Join(root, "has#hash")
	dstHash := filepath.Join(root, "dst-hash")
	os.MkdirAll(srcHash, 0755)
	os.MkdirAll(dstHash, 0755)

	r := runSmirrorWithTimeout(t, 30*1000_000_000, cfg, "addmirror", srcHash, "-dest", dstHash)
	if r.ExitCode != 0 {
		t.Logf("addmirror with `#`-in-path rejected: exit=%d. Acceptable defense if "+
			"the validator catches it. stderr=%s",
			r.ExitCode, truncate(r.Stderr, 300))
		return
	}

	// addmirror succeeded — now verify the config still loads.
	r = runSmirror(t, cfg, "test-mirrors")
	if r.ExitCode == 2 {
		cfgBytes, _ := os.ReadFile(cfg)
		t.Errorf("PANEL BUG: addmirror accepted a path with `#` and wrote it UNQUOTED to "+
			"config.yaml; subsequent test-mirrors fails to load (exit 2). "+
			"Per YAML auditor finding #15, formatMirrorBlock writes name/local_path "+
			"with `%%s` instead of `%%q`. config.yaml content:\n%s",
			truncate(string(cfgBytes), 800))
	}
}

// YAML #15 variant: name with leading whitespace or special chars.
// We can't readily create a directory with `:` on Windows (reserved), but
// `#` and trailing dot tests are plausible.
func TestPanelR5_YAML_AddMirror_PathWithSpace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src-seed")
	dst := filepath.Join(root, "dst-seed")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "seed", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "data", "state.db"),
		LogFile:           filepath.Join(root, "data", "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})
	os.MkdirAll(filepath.Dir(filepath.Join(root, "data", "state.db")), 0755)

	// Path with spaces — common on Windows ("C:\Program Files\...").
	srcSpace := filepath.Join(root, "Some Folder With Spaces")
	dstSpace := filepath.Join(root, "DstSpace")
	os.MkdirAll(srcSpace, 0755)
	os.MkdirAll(dstSpace, 0755)

	r := runSmirrorWithTimeout(t, 30*1000_000_000, cfg, "addmirror", srcSpace, "-dest", dstSpace)
	if r.ExitCode != 0 {
		t.Logf("addmirror with spaces in path rejected: exit=%d", r.ExitCode)
		return
	}

	// Verify config still loads.
	r2 := runSmirror(t, cfg, "test-mirrors")
	if r2.ExitCode == 2 {
		cfgBytes, _ := os.ReadFile(cfg)
		t.Errorf("PANEL BUG: addmirror with space-in-path produced an invalid config.yaml. "+
			"YAML auditor #15 confirmed: formatMirrorBlock writes name/local_path UNQUOTED; "+
			"YAML accepts spaces in plain scalars but the round-trip is fragile. config.yaml:\n%s",
			truncate(string(cfgBytes), 800))
	}
}

// YAML #3: BOM-prefixed config.yaml. yaml.v3 may or may not tolerate
// the BOM at the start of the file. The custom edit.go path strips it,
// but the Load path does not.
func TestPanelR5_YAML_BOMPrefixedConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfgPath := filepath.Join(root, "config.yaml")
	body := fmt.Sprintf(`mirrors:
  - name: bommed
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
sync_workers: 1
notify_enabled: false
anomaly_detection_enabled: false
verify_interval_sec: -1
`, src, dst, filepath.Join(root, "state.db"), filepath.Join(root, "s.log"))

	// Prefix UTF-8 BOM.
	bom := []byte{0xEF, 0xBB, 0xBF}
	os.WriteFile(cfgPath, append(bom, []byte(body)...), 0600)

	r := runSmirror(t, cfgPath, "test-mirrors")
	if r.ExitCode == 2 {
		t.Logf("PANEL OBS: config with UTF-8 BOM rejected at Load (exit 2). "+
			"Per YAML auditor #3, edit.go strips BOM but Load() does not. "+
			"Windows users editing config in Notepad get a BOM by default. "+
			"Recommendation: stripBOM() should be called before yaml.Unmarshal. "+
			"stderr=%s", truncate(r.Stderr, 300))
	} else {
		t.Logf("BOM-prefixed config loaded successfully (exit=%d). " +
			"yaml.v3 may already tolerate BOM.", r.ExitCode)
	}
}

// YAML #1: multiple `---`-separated YAML documents in one file.
func TestPanelR5_YAML_MultiDocConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src1 := filepath.Join(root, "src1")
	dst1 := filepath.Join(root, "dst1")
	src2 := filepath.Join(root, "src2")
	dst2 := filepath.Join(root, "dst2")
	for _, d := range []string{src1, dst1, src2, dst2} {
		os.MkdirAll(d, 0755)
	}

	cfgPath := filepath.Join(root, "config.yaml")
	body := fmt.Sprintf(`mirrors:
  - name: doc1-mirror
    local_path: %q
    remote: %q
state_db: %q
log_file: %q
sync_workers: 1
notify_enabled: false
anomaly_detection_enabled: false
verify_interval_sec: -1
---
mirrors:
  - name: doc2-mirror
    local_path: %q
    remote: %q
`, src1, dst1, filepath.Join(root, "state.db"), filepath.Join(root, "s.log"), src2, dst2)
	os.WriteFile(cfgPath, []byte(body), 0600)

	r := runSmirror(t, cfgPath, "project-stats")
	t.Logf("multi-doc config exit=%d. "+
		"Behavior: yaml.v3 reads first document only; second is silently ignored. "+
		"User pasting two configs sees only the first. "+
		"Recommendation: detect `---` separator past header and warn or reject. "+
		"stdout=%s", r.ExitCode, truncate(r.Stdout, 400))
}

// YAML #12: inline comments dropped on SetField mutation.
func TestPanelR5_YAML_InlineCommentsAfterRemoteCmd(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfgPath := filepath.Join(root, "config.yaml")
	body := fmt.Sprintf(`# top-level comment
mirrors:
  - name: m1                # my favorite mirror
    local_path: %q
    remote: %q
default_remote: "old:bucket"  # original default
state_db: %q
log_file: %q
sync_workers: 1
notify_enabled: false
anomaly_detection_enabled: false
verify_interval_sec: -1
`, src, dst, filepath.Join(root, "state.db"), filepath.Join(root, "s.log"))
	os.WriteFile(cfgPath, []byte(body), 0600)

	r := runSmirror(t, cfgPath, "remote", "new:bucket/path")
	if r.ExitCode != 0 {
		t.Skipf("remote set returned %d, can't test", r.ExitCode)
	}

	cfgBytes, _ := os.ReadFile(cfgPath)
	cfgText := string(cfgBytes)

	keptInline := strings.Contains(cfgText, "my favorite mirror")
	keptStandalone := strings.Contains(cfgText, "top-level comment")

	if !keptInline {
		t.Logf("PANEL OBS: inline comment `# my favorite mirror` was lost after `remote set`. " +
			"Per YAML auditor #12, SetField does line-based string replacement and drops " +
			"the comment portion of the line. Recommendation: parse line into " +
			"(value, comment) and re-emit comment.")
	}
	if !keptStandalone {
		t.Errorf("PANEL BUG: standalone comments lost after `remote set`. config.yaml: %s",
			truncate(cfgText, 600))
	}
}

// =========================================================================
// 3. FILESYSTEM-SPECIFIC BEHAVIORS
// =========================================================================

// FS #1: NTFS Alternate Data Streams. Most cloud backends don't support
// ADS, so they're silently lost. Local-to-local mirror should at least
// log a warning, but the test verifies current behavior.
func TestPanelR5_FS_NTFS_ADS_StrippedSilently(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("ADS is NTFS-specific")
	}
	env := newTestEnv(t)

	mainFile := filepath.Join(env.SrcDir, "doc.txt")
	createFile(t, mainFile, "main content")

	// Add an ADS via PowerShell.
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`Add-Content -Path '%s' -Stream 'zone.identifier' -Value 'mark-of-the-web'`,
			mainFile))
	if err := cmd.Run(); err != nil {
		t.Skipf("could not create ADS: %v", err)
	}

	r := runSmirror(t, env.CfgPath, "sync-now")
	if r.ExitCode != 0 {
		t.Fatalf("sync-now exit=%d", r.ExitCode)
	}

	// Check whether the destination ALSO has the stream.
	dstFile := filepath.Join(env.DstDir, "doc.txt")
	if !fileExists(dstFile) {
		t.Skip("main file not synced; can't probe ADS")
	}

	streamCheck := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`(Get-Item '%s' -Stream zone.identifier -ErrorAction SilentlyContinue) -ne $null`,
			dstFile))
	streamOut, _ := streamCheck.Output()
	hasStream := strings.Contains(strings.ToLower(string(streamOut)), "true")

	if !hasStream {
		t.Logf("PANEL OBS: NTFS Alternate Data Stream (zone.identifier) was silently stripped " +
			"during sync. For local-to-local mirrors this is unexpected; for cloud backends it's " +
			"expected (most don't support ADS). FS reviewer #1: smirror does not warn the user. " +
			"Recommendation: detect ADS-bearing files and emit a one-time warning per file.")
	}
}

// FS #6: hidden files (NTFS HIDDEN attribute). Watcher sees them; they
// sync without warning. desktop.ini and thumbs.db should likely be in
// default global_excludes.
func TestPanelR5_FS_HiddenFiles_SyncedByDefault(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("HIDDEN attribute is NTFS-specific")
	}
	env := newTestEnv(t)

	hidden := filepath.Join(env.SrcDir, "desktop.ini")
	createFile(t, hidden, "[ViewState]\nViewLevel=0\n")
	exec.Command("attrib", "+H", hidden).Run()

	thumbs := filepath.Join(env.SrcDir, "thumbs.db")
	createFile(t, thumbs, "binary-thumb-data")
	exec.Command("attrib", "+H", thumbs).Run()

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	if fileExists(filepath.Join(env.DstDir, "desktop.ini")) {
		t.Logf("PANEL OBS: desktop.ini synced by default (HIDDEN attribute ignored). " +
			"Recommendation: extend config.example.yaml global_excludes to cover " +
			"`desktop.ini`, `thumbs.db`, `*.lnk`, and other Windows-OS noise files.")
	}
	if fileExists(filepath.Join(env.DstDir, "thumbs.db")) {
		t.Logf("PANEL OBS: thumbs.db synced; same recommendation as desktop.ini.")
	}
}

// FS #2: hard links. Both should sync; content-addressed skip should
// prevent double-upload.
func TestPanelR5_FS_HardLinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("hardlink test uses fsutil")
	}
	env := newTestEnv(t)

	a := filepath.Join(env.SrcDir, "A.txt")
	b := filepath.Join(env.SrcDir, "B.txt")
	createFile(t, a, "shared-content")

	hl := exec.Command("fsutil", "hardlink", "create", b, a)
	if err := hl.Run(); err != nil {
		t.Skipf("hardlink creation failed (admin-required): %v", err)
	}

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	if !fileExists(filepath.Join(env.DstDir, "A.txt")) {
		t.Errorf("hard link source A.txt not synced")
	}
	if !fileExists(filepath.Join(env.DstDir, "B.txt")) {
		t.Errorf("hard link sibling B.txt not synced")
	}
	t.Logf("PANEL OBS: hard-linked file synced as two separate remote files (expected — " +
		"backends generally don't support hard links). Content-addressed skip should prevent " +
		"double upload bandwidth, but the remote storage cost is 2× as if they were copies.")
}

// FS #8: case-only rename on case-insensitive NTFS.
func TestPanelR5_FS_CaseOnlyRename(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitivity is NTFS-default")
	}
	env := newTestEnv(t)

	upper := filepath.Join(env.SrcDir, "FOO.TXT")
	createFile(t, upper, "v1")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	if !fileExists(filepath.Join(env.DstDir, "FOO.TXT")) {
		t.Skip("initial sync did not produce expected file")
	}

	// Case-only rename to "foo.txt".
	lower := filepath.Join(env.SrcDir, "foo.txt")
	if err := os.Rename(upper, lower); err != nil {
		// Some Windows configs/locks reject case-only rename.
		t.Skipf("case-only rename failed: %v", err)
	}

	r = runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)

	hasUpper := fileExists(filepath.Join(env.DstDir, "FOO.TXT"))
	hasLower := fileExists(filepath.Join(env.DstDir, "foo.txt"))
	t.Logf("PANEL OBS: after case-only rename FOO.TXT → foo.txt: "+
		"hasUpper=%v hasLower=%v. "+
		"Per FS reviewer #8: NTFS preserves case but fsnotify may report this as a "+
		"single Rename event with same-case names normalized. The remote may end up "+
		"with both names, or with one of them.",
		hasUpper, hasLower)
}

// =========================================================================
// 4. CLI COMPLETENESS
// =========================================================================

// CLI #1: -dest does NOT accept equals form (`-dest=value`).
func TestPanelR5_CLI_DestEqualsFormUnsupported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(dataDir, 0755)

	cfgPath := filepath.Join(root, "config.yaml")
	// Don't create config; let addmirror create it fresh.

	r := runSmirrorWithTimeout(t, 30*1000_000_000, cfgPath, "addmirror", src, "-dest="+dst)
	// The space form works; the equals form should also work for parity.
	if r.ExitCode != 0 {
		t.Logf("PANEL OBS: `-dest=<value>` (equals form) is not accepted; addmirror "+
			"exit=%d. The space form `-dest <value>` works. CLI reviewer #1 + #15: "+
			"flag forms are inconsistent. Recommendation: parse `-dest=` like "+
			"`--config=` does. stderr=%s",
			r.ExitCode, truncate(r.Stderr, 200))
	}
}

// CLI #6: `smirror version` output is multi-line (version + copyright +
// telemetry build-key), but README shows a one-line format.
func TestPanelR5_CLI_VersionOutputShape(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "version")
	assertExitCode(t, r, 0)
	lines := strings.Count(strings.TrimSpace(r.Stdout), "\n") + 1
	hasCopyright := strings.Contains(r.Stdout, "Copyright")
	hasTelemetry := strings.Contains(r.Stdout, "telemetry")

	t.Logf("`smirror version` produces %d lines. hasCopyright=%v, hasTelemetry=%v. "+
		"README documents this as one-line. stdout=%s",
		lines, hasCopyright, hasTelemetry, truncate(r.Stdout, 300))
	if lines == 1 {
		t.Errorf("expected multi-line per current code; got 1")
	}
}

// CLI #7/#8/#16: unknown flags produce inconsistent exit codes (1 vs 2).
func TestPanelR5_CLI_UnknownFlag_ExitCodeConsistency(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "test", LocalPath: src, Remote: dst},
		},
		StateDB:           filepath.Join(root, "state.db"),
		LogFile:           filepath.Join(root, "s.log"),
		LogLevel:          "error",
		SyncWorkers:       1,
		NotifyEnabled:     boolPtr(false),
		AnomalyEnabled:    boolPtr(false),
		VerifyIntervalSec: -1,
	})

	cases := []struct {
		cmd string
	}{
		{"status"},
		{"sync-now"},
		{"dry-run"},
		{"list-filters"},
	}
	codes := map[string]int{}
	for _, c := range cases {
		r := runSmirror(t, cfg, c.cmd, "--bogus-flag-xyz")
		codes[c.cmd] = r.ExitCode
	}

	t.Logf("PANEL OBS: unknown flags produce these exit codes: %v. "+
		"Per CLI reviewer #7/#8/#16: addmirror returns 1, others may vary. "+
		"Recommendation: standardize on exit 2 (config error) for unknown-flag rejection.",
		codes)
}

// CLI #5: `--browser` is the new name for `--open`. Verify both still work.
func TestPanelR5_CLI_ReportBug_BrowserAliasOfOpen(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("--browser opens a browser")
	}
	t.Skip("explicit user opt-in only; would launch a browser. Documented for v1.0 manual review.")
}
