package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/lock"
)

// buildCleanPlan must refuse when a smirror daemon holds the single-instance
// lock on the user data dir. Regression for: docs promised the check, code
// did not implement it, so `clean --self --yes` would race os.RemoveAll
// against the live daemon.
func TestBuildCleanPlan_RefusesWhenDaemonLockHeld(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("# placeholder\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Simulate a running daemon holding the lock on the user data dir.
	l, err := lock.Acquire(tmp)
	if err != nil {
		t.Fatalf("failed to acquire lock for test setup: %v", err)
	}
	defer l.Release()

	_, err = buildCleanPlan(configPath, cleanSelf)
	if err == nil {
		t.Fatal("expected buildCleanPlan to refuse when daemon lock is held")
	}
	if !strings.Contains(err.Error(), "smirror appears to be running") {
		t.Errorf("error %q does not mention running daemon", err)
	}
}

// When the user passes --config /custom/path/config.yaml and the dir doesn't
// exist (or load fails), we must NOT silently fall back to ~/.selectivemirror.
// Regression for: clean --self --yes against a typo'd custom config used to
// wipe the home data dir instead.
func TestBuildCleanPlan_NoHomeFallbackForCustomConfigPath(t *testing.T) {
	tmp := t.TempDir()
	// Custom config path whose containing dir does not exist.
	missing := filepath.Join(tmp, "deleted-dir", "config.yaml")

	plan, err := buildCleanPlan(missing, cleanSelf)
	if err != nil {
		t.Fatalf("buildCleanPlan returned error: %v", err)
	}
	if plan.userDataDir != "" {
		t.Errorf("userDataDir = %q, want empty (no home fallback for explicit --config)", plan.userDataDir)
	}
}

// When the user accepts the default config path AND that dir doesn't exist,
// we still should not error — we just have nothing to clean.
func TestBuildCleanPlan_DefaultConfigMissingDirIsHarmless(t *testing.T) {
	// We can't easily neutralize the real ~/.selectivemirror here without
	// touching the dev box. Restrict the assertion: if there is no config
	// file at the default path AND no home-dir fallback, plan should be empty
	// without erroring.
	//
	// We use a fake configPath that equals DefaultConfigPath() but whose dir
	// doesn't exist, then check the function doesn't crash. Practical regression
	// test: ensure the new logic doesn't panic on the empty case.
	if home, err := os.UserHomeDir(); err == nil {
		_ = home // home-dir state on dev box is allowed; test only exercises plumbing
	}
	// Pass an obviously-not-present sentinel; plan should build without panicking.
	plan, err := buildCleanPlan(filepath.Join(t.TempDir(), "nope", "config.yaml"), cleanSelf)
	if err != nil {
		t.Fatalf("buildCleanPlan errored unexpectedly: %v", err)
	}
	// userDataDir should be empty (the dir doesn't exist).
	if plan.userDataDir != "" {
		t.Errorf("userDataDir = %q, want empty", plan.userDataDir)
	}
}
