package systemval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Round 14: destructive delete-policy contract checks from the round-14 multi-role panel review.
// These are black-box tests: the suite drives smirror.exe and observes local
// rclone-backend side effects, without importing production code.

func TestPanelR14_Config_GlobalInvalidDeletePolicyRejected(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	cfgText := readFileContent(t, env.CfgPath)
	cfgText = strings.Replace(cfgText, "notify_enabled: false\n", "notify_enabled: false\ndelete_policy: ignor\n", 1)
	if err := os.WriteFile(env.CfgPath, []byte(cfgText), 0644); err != nil {
		t.Fatalf("write invalid-policy config: %v", err)
	}

	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	if r.ExitCode == 0 {
		t.Fatalf("global delete_policy typo was accepted; this silently falls back to destructive delete\nstdout:\n%s\nstderr:\n%s", r.Stdout, r.Stderr)
	}
	assertOutputContains(t, r, "delete_policy")
}

func TestPanelR14_DeletePolicyIgnore_SyncNowRetainsRemoteFile(t *testing.T) {
	t.Parallel()
	requireRclone(t)

	env := newTestEnvWithPolicy(t, "ignore")
	localPath := filepath.Join(env.SrcDir, "archive.txt")
	remotePath := filepath.Join(env.DstDir, "archive.txt")

	createFile(t, localPath, "keep me remotely")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)
	assertFileExists(t, remotePath)

	if err := os.Remove(localPath); err != nil {
		t.Fatalf("remove local file: %v", err)
	}
	r = runSmirror(t, env.CfgPath, "sync-now")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)

	if !fileExists(remotePath) {
		t.Fatalf("delete_policy=ignore did not retain previously synced remote file after sync-now; archive mode is destructive")
	}
}

func TestPanelR14_DeletePolicyQuarantine_GhostCleanupMovesOrphan(t *testing.T) {
	t.Parallel()
	requireRclone(t)

	env := newTestEnvWithPolicy(t, "quarantine")
	orphanPath := filepath.Join(env.DstDir, "manual-orphan.txt")
	createFile(t, orphanPath, "remote-only file")

	r := runSmirror(t, env.CfgPath, "sync-now")
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)

	if fileExists(orphanPath) {
		t.Fatalf("orphan still exists at original remote path; expected quarantine move")
	}
	quarantineRoot := filepath.Join(env.DstDir, ".quarantine")
	quarantined := false
	_ = filepath.WalkDir(quarantineRoot, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(filepath.Base(path), "manual-orphan.txt") {
			quarantined = true
		}
		return nil
	})
	if !quarantined {
		t.Fatalf("delete_policy=quarantine deleted an orphan instead of moving it under %s", quarantineRoot)
	}
}

func TestPanelR14_RemoteCommand_ApostrophePathKeepsConfigLoadable(t *testing.T) {
	t.Parallel()

	env := newTestEnv(t)
	dst := filepath.Join(env.RootDir, "O'Brien")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatalf("create apostrophe destination: %v", err)
	}

	r := runSmirror(t, env.CfgPath, "remote", dst)
	assertNoPanic(t, r)
	assertExitCode(t, r, 0)

	r = runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
	if r.ExitCode != 0 {
		t.Fatalf("smirror remote wrote YAML that status cannot reload for apostrophe path\nstdout:\n%s\nstderr:\n%s", r.Stdout, r.Stderr)
	}
}
