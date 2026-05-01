package systemval

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// smirror invocation
// ---------------------------------------------------------------------------

type smirrorResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// runSmirror invokes smirror.exe with --config prepended.
func runSmirror(t *testing.T, cfgPath string, args ...string) smirrorResult {
	t.Helper()
	full := append([]string{"--config", cfgPath}, args...)
	return runSmirrorRaw(t, full...)
}

// runSmirrorRaw invokes smirror.exe without any automatic flags.
func runSmirrorRaw(t *testing.T, args ...string) smirrorResult {
	t.Helper()
	return runSmirrorCtx(t, context.Background(), args...)
}

// runSmirrorWithTimeout invokes smirror.exe with a context deadline.
func runSmirrorWithTimeout(t *testing.T, timeout time.Duration, cfgPath string, args ...string) smirrorResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"--config", cfgPath}, args...)
	return runSmirrorCtx(t, ctx, full...)
}

func runSmirrorCtx(t *testing.T, ctx context.Context, args ...string) smirrorResult {
	t.Helper()
	start := time.Now()
	cmd := exec.CommandContext(ctx, smirrorBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Prevent inheriting console (Windows service detection).
	cmd.Stdin = strings.NewReader("")

	err := cmd.Run()
	dur := time.Since(start)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() != nil {
			exitCode = -1 // timeout
		} else {
			t.Fatalf("exec error (not ExitError): %v", err)
		}
	}
	return smirrorResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: dur,
	}
}

// ---------------------------------------------------------------------------
// Background smirror process (for "start" command)
// ---------------------------------------------------------------------------

type smirrorProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	done   chan struct{}
	err    error
}

// startSmirror launches smirror in the background. Caller must call Stop() or Kill().
func startSmirror(t *testing.T, cfgPath string, args ...string) *smirrorProcess {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	full := append([]string{"--config", cfgPath}, args...)
	cmd := exec.CommandContext(ctx, smirrorBin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader("")

	// On Windows, create a new process group so we can send Ctrl+C.
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	}

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("startSmirror: %v", err)
	}

	p := &smirrorProcess{
		cmd:    cmd,
		cancel: cancel,
		stdout: &stdout,
		stderr: &stderr,
		done:   make(chan struct{}),
	}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	t.Cleanup(func() {
		p.Kill()
	})
	return p
}

// Stop sends a graceful stop (kill the context) and waits up to 10s.
func (p *smirrorProcess) Stop() smirrorResult {
	p.cancel()
	return p.waitDone(10 * time.Second)
}

// Kill force-kills the process.
func (p *smirrorProcess) Kill() {
	if p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
	p.cancel()
	<-p.done
}

// WaitExit waits for the process to exit within timeout.
func (p *smirrorProcess) WaitExit(timeout time.Duration) smirrorResult {
	return p.waitDone(timeout)
}

func (p *smirrorProcess) waitDone(timeout time.Duration) smirrorResult {
	select {
	case <-p.done:
	case <-time.After(timeout):
		p.Kill()
	}
	exitCode := 0
	if p.err != nil {
		if exitErr, ok := p.err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return smirrorResult{
		ExitCode: exitCode,
		Stdout:   p.stdout.String(),
		Stderr:   p.stderr.String(),
	}
}

// ---------------------------------------------------------------------------
// Config generation
// ---------------------------------------------------------------------------

type mirrorDef struct {
	Name           string
	LocalPath      string
	Remote         string
	DebounceSec    int
	MaxFileSizeMB  int
	DeletePolicy   string
	QuarantineDays int
	SyncIgnorePath string
	PreSyncHook    string
	PostSyncHook   string
	RcloneExtra    []string
}

type configOpts struct {
	Mirrors              []mirrorDef
	GlobalExcludes       []string
	StateDB              string
	LogFile              string
	LogLevel             string
	RclonePath           string
	BandwidthLimit       string
	SyncWorkers          int
	ReconcileIntervalSec int
	DeletePolicy         string
	QuarantineDays       int
	DefaultRemote        string
	VerifyIntervalSec    int
	NotifyEnabled        *bool
	AnomalyEnabled       *bool
	ExtraYAML            string // appended verbatim
}

// createConfig writes config.yaml and returns its path.
func createConfig(t *testing.T, dir string, opts configOpts) string {
	t.Helper()

	var b strings.Builder
	// Mirrors
	if len(opts.Mirrors) > 0 {
		b.WriteString("mirrors:\n")
		for _, m := range opts.Mirrors {
			b.WriteString(fmt.Sprintf("  - name: %q\n", m.Name))
			b.WriteString(fmt.Sprintf("    local_path: %q\n", m.LocalPath))
			b.WriteString(fmt.Sprintf("    remote: %q\n", m.Remote))
			if m.DebounceSec > 0 {
				b.WriteString(fmt.Sprintf("    debounce_sec: %d\n", m.DebounceSec))
			}
			if m.MaxFileSizeMB > 0 {
				b.WriteString(fmt.Sprintf("    max_file_size_mb: %d\n", m.MaxFileSizeMB))
			}
			if m.DeletePolicy != "" {
				b.WriteString(fmt.Sprintf("    delete_policy: %q\n", m.DeletePolicy))
			}
			if m.QuarantineDays > 0 {
				b.WriteString(fmt.Sprintf("    quarantine_days: %d\n", m.QuarantineDays))
			}
			if m.SyncIgnorePath != "" {
				b.WriteString(fmt.Sprintf("    syncignore_path: %q\n", m.SyncIgnorePath))
			}
			if m.PreSyncHook != "" {
				b.WriteString(fmt.Sprintf("    pre_sync_hook: %q\n", m.PreSyncHook))
			}
			if m.PostSyncHook != "" {
				b.WriteString(fmt.Sprintf("    post_sync_hook: %q\n", m.PostSyncHook))
			}
			if len(m.RcloneExtra) > 0 {
				b.WriteString("    rclone_extra_flags:\n")
				for _, f := range m.RcloneExtra {
					b.WriteString(fmt.Sprintf("      - %q\n", f))
				}
			}
		}
	}
	// Global excludes
	if len(opts.GlobalExcludes) > 0 {
		b.WriteString("global_excludes:\n")
		for _, e := range opts.GlobalExcludes {
			b.WriteString(fmt.Sprintf("  - %q\n", e))
		}
	}
	// Scalars
	if opts.StateDB != "" {
		b.WriteString(fmt.Sprintf("state_db: %q\n", opts.StateDB))
	}
	if opts.LogFile != "" {
		b.WriteString(fmt.Sprintf("log_file: %q\n", opts.LogFile))
	}
	if opts.LogLevel != "" {
		b.WriteString(fmt.Sprintf("log_level: %q\n", opts.LogLevel))
	}
	if opts.RclonePath != "" {
		b.WriteString(fmt.Sprintf("rclone_path: %q\n", opts.RclonePath))
	}
	if opts.BandwidthLimit != "" {
		b.WriteString(fmt.Sprintf("bandwidth_limit: %q\n", opts.BandwidthLimit))
	}
	if opts.SyncWorkers > 0 {
		b.WriteString(fmt.Sprintf("sync_workers: %d\n", opts.SyncWorkers))
	}
	if opts.ReconcileIntervalSec > 0 {
		b.WriteString(fmt.Sprintf("reconcile_interval_sec: %d\n", opts.ReconcileIntervalSec))
	}
	if opts.DeletePolicy != "" {
		b.WriteString(fmt.Sprintf("delete_policy: %q\n", opts.DeletePolicy))
	}
	if opts.QuarantineDays > 0 {
		b.WriteString(fmt.Sprintf("quarantine_days: %d\n", opts.QuarantineDays))
	}
	if opts.DefaultRemote != "" {
		b.WriteString(fmt.Sprintf("default_remote: %q\n", opts.DefaultRemote))
	}
	if opts.VerifyIntervalSec != 0 {
		b.WriteString(fmt.Sprintf("verify_interval_sec: %d\n", opts.VerifyIntervalSec))
	}
	if opts.NotifyEnabled != nil {
		b.WriteString(fmt.Sprintf("notify_enabled: %v\n", *opts.NotifyEnabled))
	}
	if opts.AnomalyEnabled != nil {
		b.WriteString(fmt.Sprintf("anomaly_detection_enabled: %v\n", *opts.AnomalyEnabled))
	}
	if opts.ExtraYAML != "" {
		b.WriteString(opts.ExtraYAML)
		b.WriteString("\n")
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0644); err != nil {
		t.Fatalf("createConfig: %v", err)
	}
	return cfgPath
}

// testEnv bundles all temp paths for a single test.
type testEnv struct {
	RootDir string
	SrcDir  string
	DstDir  string
	DataDir string
	CfgPath string
}

// newTestEnv creates a fresh isolated test environment with a single mirror.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvN(t, 1)
}

// newTestEnvN creates an environment with n mirrors.  Mirror names are
// "mirror0", "mirror1", etc.  Source dirs are SrcDir for mirror0, or
// <RootDir>/src1, src2, ... for the rest.  Similarly for DstDir.
func newTestEnvN(t *testing.T, n int) *testEnv {
	t.Helper()
	root := t.TempDir()

	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	mirrors := make([]mirrorDef, n)
	srcDir := filepath.Join(root, "src0")
	dstDir := filepath.Join(root, "dst0")
	for i := 0; i < n; i++ {
		src := filepath.Join(root, fmt.Sprintf("src%d", i))
		dst := filepath.Join(root, fmt.Sprintf("dst%d", i))
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		if i == 0 {
			srcDir = src
			dstDir = dst
		}
		mirrors[i] = mirrorDef{
			Name:      fmt.Sprintf("mirror%d", i),
			LocalPath: src,
			Remote:    dst, // local-to-local (rclone treats bare paths as local)
		}
	}

	noNotify := boolPtr(false)
	noAnomaly := boolPtr(false)
	cfgPath := createConfig(t, root, configOpts{
		Mirrors:           mirrors,
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "debug",
		SyncWorkers:       1,
		NotifyEnabled:     noNotify,
		AnomalyEnabled:    noAnomaly,
		VerifyIntervalSec: -1, // disable periodic verify
	})

	return &testEnv{
		RootDir: root,
		SrcDir:  srcDir,
		DstDir:  dstDir,
		DataDir: dataDir,
		CfgPath: cfgPath,
	}
}

// newTestEnvWithPolicy creates a single-mirror env with a specific delete policy.
func newTestEnvWithPolicy(t *testing.T, policy string) *testEnv {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(srcDir, 0755)
	os.MkdirAll(dstDir, 0755)

	noNotify := boolPtr(false)
	noAnomaly := boolPtr(false)
	cfgPath := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{{
			Name:      "testmirror",
			LocalPath: srcDir,
			Remote:    dstDir,
		}},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "debug",
		SyncWorkers:       1,
		DeletePolicy:      policy,
		NotifyEnabled:     noNotify,
		AnomalyEnabled:    noAnomaly,
		VerifyIntervalSec: -1,
	})

	return &testEnv{
		RootDir: root,
		SrcDir:  srcDir,
		DstDir:  dstDir,
		DataDir: dataDir,
		CfgPath: cfgPath,
	}
}

func boolPtr(b bool) *bool { return &b }

// ---------------------------------------------------------------------------
// Filesystem helpers
// ---------------------------------------------------------------------------

// createFile writes content to path, creating parent dirs as needed.
func createFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("createFile mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("createFile write: %v", err)
	}
}

// createBinaryFile writes n random bytes to path.
func createBinaryFile(t *testing.T, path string, sizeBytes int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("createBinaryFile mkdir: %v", err)
	}
	data := make([]byte, sizeBytes)
	rand.Read(data)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("createBinaryFile write: %v", err)
	}
}

// createSyncIgnore writes a .syncignore file in dir.
func createSyncIgnore(t *testing.T, dir string, rules []string) {
	t.Helper()
	content := strings.Join(rules, "\n") + "\n"
	createFile(t, filepath.Join(dir, ".syncignore"), content)
}

// fileExists returns true if path is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// dirExists returns true if path is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// readFileContent returns file contents as string.
func readFileContent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readFileContent %q: %v", path, err)
	}
	return string(data)
}

// fileHash returns the MD5 hex hash of a file.
func fileHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("fileHash open %q: %v", path, err)
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatalf("fileHash copy: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// listFiles returns all relative file paths under dir (forward slashes, sorted).
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// fileCount returns the number of regular files under dir.
func fileCount(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// ---------------------------------------------------------------------------
// Waiting / polling
// ---------------------------------------------------------------------------

// waitForFile polls until path exists or timeout expires.
func waitForFile(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fileExists(path) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// waitForFileCount polls until dir has at least n files or timeout expires.
func waitForFileCount(t *testing.T, dir string, n int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fileCount(dir) >= n {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// waitForCondition polls fn until it returns true or timeout expires.
func waitForCondition(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// Status JSON reading
// ---------------------------------------------------------------------------

type statusJSON struct {
	Version       string `json:"version"`
	Uptime        string `json:"uptime"`
	FilesSynced   int64  `json:"files_synced"`
	BytesUploaded int64  `json:"bytes_uploaded"`
	SyncErrors    int64  `json:"sync_errors"`
	QueueDepth    int64  `json:"queue_depth"`
	P95LatencyMs  int64  `json:"p95_sync_latency_ms"`
	P99LatencyMs  int64  `json:"p99_sync_latency_ms"`
}

func readStatusJSON(t *testing.T, dataDir string) *statusJSON {
	t.Helper()
	path := filepath.Join(dataDir, "status.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("readStatusJSON: %v", err)
	}
	var s statusJSON
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("readStatusJSON unmarshal: %v", err)
	}
	return &s
}

// ---------------------------------------------------------------------------
// Assertions
// ---------------------------------------------------------------------------

// assertExitCode checks the result exit code and reports stdout/stderr on mismatch.
func assertExitCode(t *testing.T, r smirrorResult, want int) {
	t.Helper()
	if r.ExitCode != want {
		t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
			r.ExitCode, want, truncate(r.Stdout, 500), truncate(r.Stderr, 500))
	}
}

// assertStdoutContains checks that stdout contains the substring.
func assertStdoutContains(t *testing.T, r smirrorResult, sub string) {
	t.Helper()
	if !strings.Contains(r.Stdout, sub) {
		t.Errorf("stdout missing %q\nstdout: %s", sub, truncate(r.Stdout, 500))
	}
}

// assertStderrContains checks that stderr contains the substring.
func assertStderrContains(t *testing.T, r smirrorResult, sub string) {
	t.Helper()
	if !strings.Contains(r.Stderr, sub) {
		t.Errorf("stderr missing %q\nstderr: %s", sub, truncate(r.Stderr, 500))
	}
}

// assertOutputContains checks stdout+stderr combined.
func assertOutputContains(t *testing.T, r smirrorResult, sub string) {
	t.Helper()
	combined := r.Stdout + r.Stderr
	if !strings.Contains(combined, sub) {
		t.Errorf("output missing %q\nstdout: %s\nstderr: %s", sub,
			truncate(r.Stdout, 300), truncate(r.Stderr, 300))
	}
}

// assertNoPanic checks that stderr does not contain Go panic traces.
func assertNoPanic(t *testing.T, r smirrorResult) {
	t.Helper()
	if strings.Contains(r.Stderr, "goroutine ") && strings.Contains(r.Stderr, "panic") {
		t.Errorf("PANIC detected in stderr:\n%s", truncate(r.Stderr, 1000))
	}
}

// assertFileExists fails if path does not exist.
func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if !fileExists(path) {
		t.Errorf("expected file to exist: %s", path)
	}
}

// assertFileNotExists fails if path exists.
func assertFileNotExists(t *testing.T, path string) {
	t.Helper()
	if fileExists(path) {
		t.Errorf("expected file NOT to exist: %s", path)
	}
}

// assertFileContent checks that a file has the expected content.
func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got := readFileContent(t, path)
	if got != want {
		t.Errorf("file %s content = %q, want %q", path, truncate(got, 200), truncate(want, 200))
	}
}

// assertFileHashMatch checks that two files have the same MD5 hash.
func assertFileHashMatch(t *testing.T, path1, path2 string) {
	t.Helper()
	h1 := fileHash(t, path1)
	h2 := fileHash(t, path2)
	if h1 != h2 {
		t.Errorf("hash mismatch: %s (%s) != %s (%s)", path1, h1, path2, h2)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...[truncated]"
}

// ---------------------------------------------------------------------------
// Coverage tracker — tracks which goals the test suite has exercised
// ---------------------------------------------------------------------------

type coverageGoal struct {
	Description string
	Required    int
	actual      int
}

type coverageTracker struct {
	mu    sync.Mutex
	goals map[string]*coverageGoal
}

var coverage = &coverageTracker{
	goals: map[string]*coverageGoal{
		// CLI commands (14)
		"cli_start":         {Description: "CLI: start command tested", Required: 1},
		"cli_sync_now":      {Description: "CLI: sync-now command tested", Required: 1},
		"cli_dry_run":       {Description: "CLI: dry-run command tested", Required: 1},
		"cli_status":        {Description: "CLI: status command tested", Required: 1},
		"cli_test_mirrors":  {Description: "CLI: test-mirrors command tested", Required: 1},
		"cli_list_filters":  {Description: "CLI: list-filters command tested", Required: 1},
		"cli_explain":       {Description: "CLI: explain command tested", Required: 1},
		"cli_project_stats": {Description: "CLI: project-stats command tested", Required: 1},
		"cli_report_bug":    {Description: "CLI: report-bug command tested", Required: 1},
		"cli_remote":        {Description: "CLI: remote command tested", Required: 1},
		"cli_addmirror":     {Description: "CLI: addmirror command tested", Required: 1},
		"cli_unmirror":      {Description: "CLI: unmirror command tested", Required: 1},
		"cli_service":       {Description: "CLI: service command tested", Required: 1},
		"cli_selfupdate":    {Description: "CLI: selfupdate command tested", Required: 1},
		"cli_telemetry":     {Description: "CLI: telemetry command tested", Required: 1},

		// Aliases
		"alias_doctor":        {Description: "Alias: doctor -> test-mirrors", Required: 1},
		"alias_verify":        {Description: "Alias: verify -> test-mirrors", Required: 1},
		"alias_stats":         {Description: "Alias: stats -> project-stats", Required: 1},
		"alias_add":           {Description: "Alias: add -> addmirror", Required: 1},
		"alias_add_mirror":    {Description: "Alias: add-mirror -> addmirror", Required: 1},
		"alias_remove":        {Description: "Alias: remove -> unmirror", Required: 1},
		"alias_remove_mirror": {Description: "Alias: remove-mirror -> unmirror", Required: 1},
		"alias_clean":         {Description: "Alias: clean -> service stop uninstall --clean", Required: 1},

		// Exit codes
		"exit_0": {Description: "Exit code 0 (success)", Required: 1},
		"exit_1": {Description: "Exit code 1 (general error)", Required: 1},
		"exit_2": {Description: "Exit code 2 (config error)", Required: 1},
		"exit_3": {Description: "Exit code 3 (rclone error)", Required: 1},
		"exit_4": {Description: "Exit code 4 (lock conflict)", Required: 1},
		"exit_5": {Description: "Exit code 5 (drift detected)", Required: 1},

		// Delete policies
		"delete_ignore":     {Description: "Delete policy: ignore", Required: 1},
		"delete_delete":     {Description: "Delete policy: delete", Required: 1},
		"delete_quarantine": {Description: "Delete policy: quarantine", Required: 1},

		// Filter pattern classes
		"filter_wildcard":   {Description: "Filter: wildcard pattern (*.ext)", Required: 1},
		"filter_negation":   {Description: "Filter: negation pattern (!pattern)", Required: 1},
		"filter_directory":  {Description: "Filter: directory pattern (dir/)", Required: 1},
		"filter_doublestar": {Description: "Filter: double-star pattern (**/foo)", Required: 1},
		"filter_anchored":   {Description: "Filter: anchored pattern (/root)", Required: 1},
		"filter_charclass":  {Description: "Filter: character class ([a-z])", Required: 1},

		// Config validation errors
		"cfgerr_no_mirrors":    {Description: "Config error: no mirrors defined", Required: 1},
		"cfgerr_no_name":       {Description: "Config error: missing name", Required: 1},
		"cfgerr_no_local_path": {Description: "Config error: missing local_path", Required: 1},
		"cfgerr_no_remote":     {Description: "Config error: missing remote", Required: 1},
		"cfgerr_dup_name":      {Description: "Config error: duplicate mirror name", Required: 1},
		"cfgerr_bad_yaml":      {Description: "Config error: invalid YAML syntax", Required: 1},
		"cfgerr_bad_policy":    {Description: "Config error: invalid delete_policy", Required: 1},

		// Path edge cases
		"path_unicode":    {Description: "Path: Unicode filename", Required: 1},
		"path_spaces":     {Description: "Path: spaces in filename", Required: 1},
		"path_dotfile":    {Description: "Path: dotfile (.hidden)", Required: 1},
		"path_deep_nest":  {Description: "Path: deeply nested dirs", Required: 1},
		"path_binary":     {Description: "Path: binary file content", Required: 1},
		"path_empty_file": {Description: "Path: empty (0 byte) file", Required: 1},
		"path_special":    {Description: "Path: special chars (brackets, hash, etc.)", Required: 1},
		"path_long":       {Description: "Path: long filename", Required: 1},

		// Backend coverage
		"backend_local": {Description: "Backend: local full integration", Required: 1},
		"backend_sweep": {Description: "Backend: 50+ backends enumerated", Required: 50},

		// Scenarios
		"scenario_file_create":  {Description: "Scenario: file create -> sync -> verify", Required: 1},
		"scenario_file_modify":  {Description: "Scenario: file modify -> resync", Required: 1},
		"scenario_file_delete":  {Description: "Scenario: file delete + policy", Required: 1},
		"scenario_multi_mirror": {Description: "Scenario: multi-mirror isolation", Required: 1},
		"scenario_lock":         {Description: "Scenario: lock contention", Required: 1},
		"scenario_burst":        {Description: "Scenario: burst file creation", Required: 1},
		"scenario_reconcile":    {Description: "Scenario: startup reconciliation", Required: 1},

		// Telemetry contract coverage
		"telemetry_default_none":              {Description: "Telemetry: default tier is none and status is visible", Required: 1},
		"telemetry_tier_transition":           {Description: "Telemetry: tier transitions persist", Required: 1},
		"telemetry_report_bug_submit":         {Description: "Telemetry: report-bug --submit consent flow", Required: 1},
		"telemetry_report_bug_browser":        {Description: "Telemetry: report-bug browser/one-shot help contract", Required: 1},
		"telemetry_report_bug_sanitization":   {Description: "Telemetry: report-bug sanitizes paths, filenames, remotes, secrets", Required: 1},
		"telemetry_build_key_diag":            {Description: "Telemetry: version reports build-key fingerprint", Required: 1},
		"telemetry_release_build_key":         {Description: "Telemetry: MSI release build embeds signing key", Required: 1},
		"telemetry_privacy_contract":          {Description: "Telemetry: client payload respects privacy field limits", Required: 1},
		"telemetry_no_startup_update_ping":    {Description: "Telemetry: default None has no startup update ping", Required: 1},
		"telemetry_worker_paths":              {Description: "Telemetry: worker exposes documented ingest/forget paths", Required: 1},
		"telemetry_worker_edge_privacy":       {Description: "Telemetry: worker enforces edge privacy and resource limits", Required: 1},
		"telemetry_schema_ingest_processing":  {Description: "Telemetry: server normalizes accepted ingest envelopes", Required: 1},
		"telemetry_rls_envelope_binding":      {Description: "Telemetry: RLS authenticates envelope fields", Required: 1},
		"telemetry_rls_server_owned_columns":  {Description: "Telemetry: RLS protects server-owned ingest state", Required: 1},
		"telemetry_digest_privacy":            {Description: "Telemetry: weekly digest respects privacy and markdown safety", Required: 1},
		"telemetry_canonical_html_escape":     {Description: "Telemetry: canonical JSON covers HTML-sensitive strings", Required: 1},
		"telemetry_crash_report_sanitization": {Description: "Telemetry: crash reports use explicit consent and safe sanitization", Required: 1},
		"telemetry_retention_raw_purge":       {Description: "Telemetry: retention purges normalized raw report fields", Required: 1},
		"telemetry_tier_fail_closed":          {Description: "Telemetry: tier gate fails closed on unreadable runtime state", Required: 1},
		"telemetry_github_token_timeout":      {Description: "Telemetry: GitHub token lookup cannot hang network paths", Required: 1},
		"telemetry_ops_docs_views":            {Description: "Telemetry: operations docs reference defined SQL views", Required: 1},
		"telemetry_rollup_taxonomy_join":      {Description: "Telemetry: rollup taxonomy joins avoid cross-products", Required: 1},
		"telemetry_validation_harness":        {Description: "Telemetry: validation coverage does not mask failed tests", Required: 1},
		"telemetry_validation_rclone_gate":    {Description: "Telemetry: static validation checks do not require rclone", Required: 1},

		// SM-157 / round-3 panel — schema-conformance for v2 architecture
		"telemetry_v2_schema_no_personal_data":     {Description: "Telemetry v2: only rollup tables exist (no personal data on disk)", Required: 1},
		"telemetry_v2_schema_no_narrative":         {Description: "Telemetry v2: no narrative-shaped columns in schema", Required: 1},
		"telemetry_v2_schema_no_heartbeat":         {Description: "Telemetry v2: event_kind ENUM has no heartbeat variant", Required: 1},
		"telemetry_v2_schema_no_accumulated_counts": {Description: "Telemetry v2: no accumulated-metric columns", Required: 1},
		"telemetry_v2_schema_no_geo":               {Description: "Telemetry v2: no geography fields", Required: 1},
		"telemetry_v2_schema_no_hw_fingerprint":    {Description: "Telemetry v2: no hardware fingerprint fields", Required: 1},
		"telemetry_v2_schema_bucketized_numerics":  {Description: "Telemetry v2: every numeric column is a bucket ENUM", Required: 1},
		"telemetry_v2_schema_no_install_id":        {Description: "Telemetry v2: install_id never appears as a column", Required: 1},
		"telemetry_v2_schema_replay_overcount_only": {Description: "Telemetry v2: INSERT only into rollup tables (replay can only over-count)", Required: 1},
		"telemetry_v2_schema_counters_monotonic":   {Description: "Telemetry v2: counters are monotonic (no decrement path)", Required: 1},
		"telemetry_v2_artifacts_no_narrative_quotes": {Description: "Telemetry v2: no quoted bug-report text in published artifacts", Required: 1},
		"telemetry_v2_cli_forget_rejected":         {Description: "Telemetry v2: smirror telemetry forget exits with v2 migration message", Required: 1},
		"telemetry_v2_cli_inspect_works":           {Description: "Telemetry v2: smirror telemetry inspect prints structured payload", Required: 1},
	},
}

// Record marks a coverage goal as exercised. SM-176 caveat: this
// records the goal at the moment the test starts, BEFORE any
// assertions run. A test that calls Record and then fails the rest of
// its assertions will still see its goal as "met" in the coverage
// report. This was the right semantics for the original suite (each
// test was expected to pass; coverage just confirmed it ran), but
// failed-but-counted is the wrong story for a privacy/security suite
// where the maintainer needs to see the failure footprint.
//
// New code should prefer RecordPass(t, goalID) below, which only
// counts the goal if the test passes its assertions.
func (c *coverageTracker) Record(goalID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, ok := c.goals[goalID]; ok {
		g.actual++
	}
}

func (c *coverageTracker) RecordN(goalID string, n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if g, ok := c.goals[goalID]; ok {
		g.actual += n
	}
}

// RecordPass registers goalID with a t.Cleanup hook so the goal counter
// is only incremented if the test passes. Failed tests do NOT mark the
// goal as met. SM-176.
//
// Migration target: every Record(goalID) call in this suite should
// eventually become RecordPass(t, goalID). The transition is mechanical
// (same goalID strings) and intentionally left as a follow-up so the
// switchover lands in one focused review.
func (c *coverageTracker) RecordPass(t *testing.T, goalID string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if g, ok := c.goals[goalID]; ok {
			g.actual++
		}
	})
}

// Report returns a formatted table and whether the included goals are met.
func (c *coverageTracker) Report(onlyRecorded bool) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	var b strings.Builder
	b.WriteString("\n=== System Validation Coverage Report ===\n\n")
	if onlyRecorded {
		b.WriteString("Focused run: showing only coverage goals recorded by selected tests.\n\n")
	}

	// Sort keys for stable output.
	keys := make([]string, 0, len(c.goals))
	for k := range c.goals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	allMet := true
	met, total := 0, 0
	for _, k := range keys {
		g := c.goals[k]
		if onlyRecorded && g.actual == 0 {
			continue
		}
		total++
		status := "PASS"
		if g.actual < g.Required {
			status = "FAIL"
			allMet = false
		} else {
			met++
		}
		b.WriteString(fmt.Sprintf("  %-25s %4d / %-4d  %s  %s\n",
			k, g.actual, g.Required, status, g.Description))
	}

	b.WriteString(fmt.Sprintf("\n  %d / %d goals met\n", met, total))
	return b.String(), allMet
}
