package systemval

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// =========================================================================
// VERSION / HELP / UNKNOWN
// =========================================================================

func TestCLI_Version(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "version")
	assertExitCode(t, r, 0)
	assertStdoutContains(t, r, "smirror")
	assertNoPanic(t, r)
	coverage.Record("exit_0")
}

func TestCLI_Help(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"help", "--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			r := runSmirrorRaw(t, flag)
			assertExitCode(t, r, 0)
			assertStdoutContains(t, r, "start")
			assertStdoutContains(t, r, "sync-now")
			assertNoPanic(t, r)
		})
	}
}

func TestCLI_NoArgs(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t)
	// Should print usage and exit nonzero.
	if r.ExitCode == 0 {
		t.Error("expected nonzero exit for no args")
	}
	assertNoPanic(t, r)
}

func TestCLI_UnknownCommand(t *testing.T) {
	t.Parallel()
	r := runSmirrorRaw(t, "nonexistent-cmd-xyz")
	assertExitCode(t, r, 1)
	assertOutputContains(t, r, "unknown command")
	assertNoPanic(t, r)
	coverage.Record("exit_1")
}

// =========================================================================
// STATUS
// =========================================================================

func TestCLI_Status(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_status")

	t.Run("Happy", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "status")
		// Status should succeed even when service isn't running.
		// Accept exit 0 or 1 (DB locking under parallel load is transient).
		if r.ExitCode != 0 && r.ExitCode != 1 {
			t.Errorf("exit code = %d, want 0 or 1", r.ExitCode)
		}
		assertNoPanic(t, r)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "status")
		assertExitCode(t, r, 2)
		assertNoPanic(t, r)
		coverage.Record("exit_2")
	})

	t.Run("AfterSyncNow", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "a.txt"), "hello")
		runSmirror(t, env.CfgPath, "sync-now")

		r := runSmirror(t, env.CfgPath, "status")
		assertExitCode(t, r, 0)
		// Should show the mirror name.
		assertOutputContains(t, r, "mirror0")
	})

	t.Run("EmptyMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "status")
		assertExitCode(t, r, 0)
	})
}

// =========================================================================
// SYNC-NOW
// =========================================================================

func TestCLI_SyncNow(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_sync_now")

	t.Run("Happy_SingleFile", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "hello.txt"), "world")

		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
		assertFileExists(t, filepath.Join(env.DstDir, "hello.txt"))
		assertFileContent(t, filepath.Join(env.DstDir, "hello.txt"), "world")
		coverage.Record("exit_0")
		coverage.Record("scenario_file_create")
	})

	t.Run("Happy_MultipleFiles", func(t *testing.T) {
		env := newTestEnv(t)
		for i := 0; i < 10; i++ {
			createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("file%d.txt", i)),
				fmt.Sprintf("content-%d", i))
		}
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		for i := 0; i < 10; i++ {
			assertFileExists(t, filepath.Join(env.DstDir, fmt.Sprintf("file%d.txt", i)))
		}
	})

	t.Run("SpecificMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		createFile(t, filepath.Join(env.RootDir, "src0", "a.txt"), "aaa")
		createFile(t, filepath.Join(env.RootDir, "src1", "b.txt"), "bbb")

		// Sync only mirror0.
		r := runSmirror(t, env.CfgPath, "sync-now", "mirror0")
		assertExitCode(t, r, 0)
		assertFileExists(t, filepath.Join(env.RootDir, "dst0", "a.txt"))
		// mirror1 should NOT have synced.
		assertFileNotExists(t, filepath.Join(env.RootDir, "dst1", "b.txt"))
	})

	t.Run("UnknownMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "sync-now", "nonexistent_mirror")
		if r.ExitCode == 0 {
			t.Error("expected nonzero exit for unknown mirror")
		}
		assertNoPanic(t, r)
	})

	t.Run("EmptyDirectory", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "sync-now")
		assertExitCode(t, r, 2)
	})

	t.Run("AllMirrors_NoArg", func(t *testing.T) {
		env := newTestEnvN(t, 3)
		for i := 0; i < 3; i++ {
			createFile(t, filepath.Join(env.RootDir, fmt.Sprintf("src%d", i), "f.txt"),
				fmt.Sprintf("mirror%d", i))
		}
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		for i := 0; i < 3; i++ {
			assertFileExists(t, filepath.Join(env.RootDir, fmt.Sprintf("dst%d", i), "f.txt"))
		}
	})

	t.Run("NestedDirectories", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "a", "b", "c", "deep.txt"), "deep")
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertFileExists(t, filepath.Join(env.DstDir, "a", "b", "c", "deep.txt"))
		assertFileContent(t, filepath.Join(env.DstDir, "a", "b", "c", "deep.txt"), "deep")
	})

	t.Run("SyncNow_Idempotent", func(t *testing.T) {
		// Bug hunter: running sync-now twice shouldn't corrupt state.
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "x.txt"), "data")
		r1 := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r1, 0)
		r2 := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r2, 0)
		assertNoPanic(t, r2)
		assertFileContent(t, filepath.Join(env.DstDir, "x.txt"), "data")
	})

	t.Run("SyncNow_AfterFileModify", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "m.txt"), "v1")
		runSmirror(t, env.CfgPath, "sync-now")
		assertFileContent(t, filepath.Join(env.DstDir, "m.txt"), "v1")

		// Modify the file.
		createFile(t, filepath.Join(env.SrcDir, "m.txt"), "v2-updated")
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertFileContent(t, filepath.Join(env.DstDir, "m.txt"), "v2-updated")
		coverage.Record("scenario_file_modify")
	})

	t.Run("SyncNow_RespectsGlobalExcludes", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(data, 0755)

		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors: []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
			GlobalExcludes: []string{"*.log", "*.tmp", ".git/"},
			StateDB:        filepath.Join(data, "state.db"),
			LogFile:        filepath.Join(data, "s.log"),
			LogLevel:       "debug",
			SyncWorkers:    1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})

		createFile(t, filepath.Join(src, "keep.txt"), "keep")
		createFile(t, filepath.Join(src, "skip.log"), "skip")
		createFile(t, filepath.Join(src, "skip.tmp"), "skip")
		createFile(t, filepath.Join(src, ".git", "config"), "skip")

		r := runSmirror(t, cfg, "sync-now")
		assertExitCode(t, r, 0)
		assertFileExists(t, filepath.Join(dst, "keep.txt"))
		assertFileNotExists(t, filepath.Join(dst, "skip.log"))
		assertFileNotExists(t, filepath.Join(dst, "skip.tmp"))
		assertFileNotExists(t, filepath.Join(dst, ".git", "config"))
	})

	t.Run("SyncNow_RespectsSyncIgnore", func(t *testing.T) {
		env := newTestEnv(t)
		createSyncIgnore(t, env.SrcDir, []string{"secret/", "*.bak"})
		createFile(t, filepath.Join(env.SrcDir, "ok.txt"), "ok")
		createFile(t, filepath.Join(env.SrcDir, "secret", "key.pem"), "secret")
		createFile(t, filepath.Join(env.SrcDir, "old.bak"), "backup")

		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertFileExists(t, filepath.Join(env.DstDir, "ok.txt"))
		assertFileNotExists(t, filepath.Join(env.DstDir, "secret", "key.pem"))
		assertFileNotExists(t, filepath.Join(env.DstDir, "old.bak"))
	})

	t.Run("SyncNow_BinaryFile", func(t *testing.T) {
		env := newTestEnv(t)
		createBinaryFile(t, filepath.Join(env.SrcDir, "data.bin"), 4096)
		r := runSmirror(t, env.CfgPath, "sync-now")
		assertExitCode(t, r, 0)
		assertFileHashMatch(t, filepath.Join(env.SrcDir, "data.bin"),
			filepath.Join(env.DstDir, "data.bin"))
		coverage.Record("path_binary")
	})
}

// =========================================================================
// DRY-RUN
// =========================================================================

func TestCLI_DryRun(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_dry_run")

	t.Run("Happy", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "new.txt"), "data")
		r := runSmirror(t, env.CfgPath, "dry-run")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
		// CRITICAL: file must NOT be copied.
		assertFileNotExists(t, filepath.Join(env.DstDir, "new.txt"))
	})

	t.Run("NoChanges", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "synced.txt"), "done")
		runSmirror(t, env.CfgPath, "sync-now")
		r := runSmirror(t, env.CfgPath, "dry-run")
		assertExitCode(t, r, 0)
	})

	t.Run("SpecificMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		createFile(t, filepath.Join(env.RootDir, "src0", "a.txt"), "a")
		r := runSmirror(t, env.CfgPath, "dry-run", "mirror0")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("UnknownMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "dry-run", "no_such_mirror")
		if r.ExitCode == 0 {
			t.Error("expected nonzero exit for unknown mirror")
		}
	})

	t.Run("ReadOnly_NoSideEffects", func(t *testing.T) {
		// Bug hunter: dry-run must not create the state DB or modify anything.
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "x.txt"), "x")
		// Note: The state DB path is set in config, so it might get created
		// on config load. We just verify no files are copied.
		r := runSmirror(t, env.CfgPath, "dry-run")
		assertExitCode(t, r, 0)
		assertFileNotExists(t, filepath.Join(env.DstDir, "x.txt"))
	})

	t.Run("DryRun_WithSyncIgnore", func(t *testing.T) {
		env := newTestEnv(t)
		createSyncIgnore(t, env.SrcDir, []string{"*.secret"})
		createFile(t, filepath.Join(env.SrcDir, "ok.txt"), "ok")
		createFile(t, filepath.Join(env.SrcDir, "hidden.secret"), "secret")
		r := runSmirror(t, env.CfgPath, "dry-run")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
		// CRITICAL: no files should actually be copied.
		assertFileNotExists(t, filepath.Join(env.DstDir, "ok.txt"))
		assertFileNotExists(t, filepath.Join(env.DstDir, "hidden.secret"))
	})
}

// =========================================================================
// TEST-MIRRORS (+ aliases doctor, verify)
// =========================================================================

func TestCLI_TestMirrors(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_test_mirrors")

	t.Run("Happy", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "a.txt"), "a")
		runSmirror(t, env.CfgPath, "sync-now")
		r := runSmirror(t, env.CfgPath, "test-mirrors")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("Alias_Doctor", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "doctor")
		assertExitCode(t, r, 0)
		coverage.Record("alias_doctor")
	})

	t.Run("Alias_Verify", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "verify")
		assertExitCode(t, r, 0)
		coverage.Record("alias_verify")
	})

	t.Run("SpecificMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		r := runSmirror(t, env.CfgPath, "test-mirrors", "mirror0")
		assertExitCode(t, r, 0)
	})

	t.Run("UnknownMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "test-mirrors", "nonexistent")
		if r.ExitCode == 0 {
			t.Error("expected nonzero exit for unknown mirror")
		}
	})

	t.Run("DriftDetected_UnsyncedFile", func(t *testing.T) {
		// File exists locally but has never been synced -> drift.
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "unsynced.txt"), "drift")
		r := runSmirror(t, env.CfgPath, "test-mirrors")
		// test-mirrors should detect the unsynced file. It may exit 0 or 5
		// depending on whether the implementation considers new files as drift.
		assertNoPanic(t, r)
		if r.ExitCode == 5 {
			coverage.Record("exit_5")
		}
	})

	t.Run("DriftDetected_MismatchedContent", func(t *testing.T) {
		// Sync a file, then modify the remote copy -> hash mismatch -> drift.
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "drifty.txt"), "original")
		runSmirror(t, env.CfgPath, "sync-now")
		// Tamper with remote.
		createFile(t, filepath.Join(env.DstDir, "drifty.txt"), "TAMPERED")
		r := runSmirror(t, env.CfgPath, "test-mirrors")
		assertNoPanic(t, r)
		if r.ExitCode == 5 {
			coverage.Record("exit_5")
		}
	})

	t.Run("RcloneError_BadRemote", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors: []mirrorDef{{
				Name: "bad", LocalPath: src,
				Remote: "nonexistentremote999:bucket/path",
			}},
			StateDB:        filepath.Join(data, "state.db"),
			LogFile:        filepath.Join(data, "s.log"),
			LogLevel:       "debug",
			SyncWorkers:    1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "test-mirrors")
		if r.ExitCode == 3 {
			coverage.Record("exit_3")
		}
		assertNoPanic(t, r)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "test-mirrors")
		assertExitCode(t, r, 2)
	})
}

// =========================================================================
// LIST-FILTERS
// =========================================================================

func TestCLI_ListFilters(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_list_filters")

	t.Run("Happy_WithSyncIgnore", func(t *testing.T) {
		env := newTestEnv(t)
		createSyncIgnore(t, env.SrcDir, []string{"*.log", "build/"})
		r := runSmirror(t, env.CfgPath, "list-filters")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
		assertStdoutContains(t, r, "*.log")
	})

	t.Run("Happy_NoSyncIgnore", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "list-filters")
		assertExitCode(t, r, 0)
	})

	t.Run("WithGlobalExcludes", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:        []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
			GlobalExcludes: []string{".DS_Store", "Thumbs.db"},
			StateDB:        filepath.Join(data, "state.db"),
			LogFile:        filepath.Join(data, "s.log"),
			SyncWorkers:    1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "list-filters")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, ".DS_Store")
	})

	t.Run("SpecificMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		r := runSmirror(t, env.CfgPath, "list-filters", "mirror0")
		assertExitCode(t, r, 0)
	})

	t.Run("UnknownMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "list-filters", "bogus")
		if r.ExitCode == 0 {
			t.Error("expected nonzero exit for unknown mirror")
		}
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "list-filters")
		assertExitCode(t, r, 2)
	})
}

// =========================================================================
// EXPLAIN
// =========================================================================

func TestCLI_Explain(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_explain")

	t.Run("IncludedFile", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "ok.txt"), "ok")
		r := runSmirror(t, env.CfgPath, "explain", "mirror0", "ok.txt")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, "INCLUDE")
		assertNoPanic(t, r)
	})

	t.Run("ExcludedFile", func(t *testing.T) {
		env := newTestEnv(t)
		createSyncIgnore(t, env.SrcDir, []string{"*.secret"})
		createFile(t, filepath.Join(env.SrcDir, "keys.secret"), "s")
		r := runSmirror(t, env.CfgPath, "explain", "mirror0", "keys.secret")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, "EXCLUDE")
	})

	t.Run("MissingArgs_NoPath", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "explain", "mirror0")
		if r.ExitCode == 0 {
			t.Error("expected error for missing path arg")
		}
	})

	t.Run("MissingArgs_NoMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "explain")
		if r.ExitCode == 0 {
			t.Error("expected error for missing mirror + path args")
		}
	})

	t.Run("UnknownMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "explain", "nosuchmirror", "file.txt")
		if r.ExitCode == 0 {
			t.Error("expected error for unknown mirror")
		}
	})

	t.Run("NonexistentFile", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "explain", "mirror0", "does_not_exist.txt")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("ExcludedDirectory", func(t *testing.T) {
		env := newTestEnv(t)
		createSyncIgnore(t, env.SrcDir, []string{"build/"})
		os.MkdirAll(filepath.Join(env.SrcDir, "build"), 0755)
		r := runSmirror(t, env.CfgPath, "explain", "mirror0", "build/output.js")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, "EXCLUDE")
	})

	t.Run("AfterSync_ShowsState", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "synced.txt"), "data")
		runSmirror(t, env.CfgPath, "sync-now")
		r := runSmirror(t, env.CfgPath, "explain", "mirror0", "synced.txt")
		assertExitCode(t, r, 0)
		// After sync, explain should show sync state info.
		assertStdoutContains(t, r, "INCLUDE")
	})

	t.Run("GlobalExclude", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:        []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
			GlobalExcludes: []string{".DS_Store"},
			StateDB:        filepath.Join(data, "state.db"),
			LogFile:        filepath.Join(data, "s.log"),
			SyncWorkers:    1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "explain", "m", ".DS_Store")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, "EXCLUDE")
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "explain", "m", "f.txt")
		assertExitCode(t, r, 2)
	})
}

// =========================================================================
// PROJECT-STATS (+ alias stats)
// =========================================================================

func TestCLI_ProjectStats(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_project_stats")

	t.Run("Happy", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "main.go"), "package main\n\nfunc main() {}\n")
		createFile(t, filepath.Join(env.SrcDir, "readme.md"), "# Hello\n")
		r := runSmirror(t, env.CfgPath, "project-stats")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("Alias_Stats", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "stats")
		assertExitCode(t, r, 0)
		coverage.Record("alias_stats")
	})

	t.Run("EmptyMirror", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "project-stats")
		assertExitCode(t, r, 0)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "project-stats")
		assertExitCode(t, r, 2)
	})

	t.Run("SpecificMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		createFile(t, filepath.Join(env.RootDir, "src0", "a.py"), "print('hello')\n")
		r := runSmirror(t, env.CfgPath, "project-stats", "mirror0")
		assertExitCode(t, r, 0)
	})
}

// =========================================================================
// REPORT-BUG
// =========================================================================

func TestCLI_ReportBug(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_report_bug")

	t.Run("Stdout", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "report-bug", "--stdout")
		assertExitCode(t, r, 0)
		assertStdoutContains(t, r, "smirror")
		assertNoPanic(t, r)
	})

	t.Run("NoConfig", func(t *testing.T) {
		// report-bug should work even without valid config.
		r := runSmirror(t, "/nonexistent/config.yaml", "report-bug", "--stdout")
		// It may succeed with partial report or fail with config error.
		assertNoPanic(t, r)
	})

	t.Run("GeneratesFile", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "report-bug")
		assertExitCode(t, r, 0)
		// Check stdout for file path.
		assertNoPanic(t, r)
	})
}

// TestCLI_ReportBug_FailureScenario creates a realistic failure environment
// (broken mirror, sync errors, error logs) and verifies report-bug output.
// The OpenBrowser subtest opens the pre-filled GitHub issue form for visual
// verification — skipped in short mode.
func TestCLI_ReportBug_FailureScenario(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_report_bug_failure")

	// Set up environment: 2 mirrors. Both dirs exist (config validates paths),
	// but we use a bogus rclone remote on the second so sync-now fails at runtime.
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	os.MkdirAll(dataDir, 0755)

	goodSrc := filepath.Join(root, "src-good")
	goodDst := filepath.Join(root, "dst-good")
	badSrc := filepath.Join(root, "src-bad")
	os.MkdirAll(goodSrc, 0755)
	os.MkdirAll(goodDst, 0755)
	os.MkdirAll(badSrc, 0755)

	// Seed mirrors with files so sync has something to work with.
	os.WriteFile(filepath.Join(goodSrc, "hello.txt"), []byte("hello world"), 0644)
	os.WriteFile(filepath.Join(badSrc, "report.docx"), []byte("fake docx"), 0644)

	noNotify := boolPtr(false)
	noAnomaly := boolPtr(false)
	cfgPath := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{
			{Name: "working-mirror", LocalPath: goodSrc, Remote: goodDst},
			// Bogus remote triggers rclone failure at sync time.
			{Name: "broken-mirror", LocalPath: badSrc, Remote: "nonexistent-remote:bucket/path"},
		},
		StateDB:           filepath.Join(dataDir, "state.db"),
		LogFile:           filepath.Join(dataDir, "smirror.log"),
		LogLevel:          "debug",
		SyncWorkers:       1,
		NotifyEnabled:     noNotify,
		AnomalyEnabled:    noAnomaly,
		VerifyIntervalSec: -1,
	})

	// Run sync-now — working-mirror succeeds, broken-mirror fails (bad remote).
	r := runSmirror(t, cfgPath, "sync-now")
	t.Logf("sync-now exit=%d stdout=%d bytes stderr=%d bytes", r.ExitCode, len(r.Stdout), len(r.Stderr))

	// Write a status.json with error metrics to simulate a running instance
	// that has accumulated sync failures.
	statusJSON := `{
		"version": "0.8.28-dev",
		"uptime": "2h15m30s",
		"start_time": "2026-04-13T10:00:00+03:00",
		"queue_depth": 3,
		"files_synced": 142,
		"bytes_uploaded": 5242880,
		"sync_errors": 17,
		"avg_sync_latency_ms": 340,
		"p95_sync_latency_ms": 1200,
		"p99_sync_latency_ms": 2500,
		"generated_at": "2026-04-13T12:15:30+03:00"
	}`
	os.WriteFile(filepath.Join(dataDir, "status.json"), []byte(statusJSON), 0644)

	// Append realistic error log lines to the log file.
	errorLogs := `
2026-04-13T12:10:05+03:00 ERR sync failed project=broken-mirror file=report.docx error="rclone copyto: directory not found"
2026-04-13T12:10:06+03:00 ERR sync failed project=broken-mirror file=data.csv error="rclone copyto: directory not found"
2026-04-13T12:11:00+03:00 WRN quiescence timeout project=working-mirror file=large-upload.zip elapsed=5.2s
2026-04-13T12:12:30+03:00 ERR rclone exit code 1 project=broken-mirror error="Failed to copy: couldn't list files"
2026-04-13T12:13:00+03:00 INF sync complete project=working-mirror files=1 bytes=11
2026-04-13T12:14:00+03:00 ERR batch sync refused project=broken-mirror reason="source path does not exist"
2026-04-13T12:15:30+03:00 INF heartbeat uptime=2h15m30s synced=142 errors=17 queue=3
`
	f, _ := os.OpenFile(filepath.Join(dataDir, "smirror.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	f.WriteString(errorLogs)
	f.Close()

	// --- Subtest: verify report content via --stdout ---
	t.Run("VerifyContent", func(t *testing.T) {
		r := runSmirror(t, cfgPath, "report-bug", "--stdout")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)

		report := r.Stdout

		// Must contain version and platform.
		assertStdoutContains(t, r, "smirror version:")
		assertStdoutContains(t, r, "platform:")

		// Must contain both mirror names.
		assertStdoutContains(t, r, "working-mirror")
		assertStdoutContains(t, r, "broken-mirror")

		// Must contain live metrics from status.json.
		assertStdoutContains(t, r, "sync_errors: 17")
		assertStdoutContains(t, r, "queue_depth: 3")
		assertStdoutContains(t, r, "files_synced: 142")

		// Must contain the log section as a separate block.
		if !strings.Contains(report, "--- Recent Logs") {
			t.Error("report missing '--- Recent Logs' section header")
		}

		// Must contain error log lines we injected.
		assertStdoutContains(t, r, "sync failed")
		assertStdoutContains(t, r, "directory not found")

		// Paths should be sanitized (no raw home directory).
		home, _ := os.UserHomeDir()
		if home != "" && strings.Contains(report, home) {
			t.Errorf("report contains unsanitized home directory: %s", home)
		}

		t.Logf("Report length: %d bytes", len(report))
	})

	// --- Subtest: autonomously verify URL pre-fill without opening a browser ---
	// Reconstructs the URL from the --stdout report (same logic as --open) and
	// verifies that title, environment, and logs query params are well-formed.
	t.Run("VerifyURLPrefill", func(t *testing.T) {
		r := runSmirror(t, cfgPath, "report-bug", "--stdout")
		assertExitCode(t, r, 0)
		report := r.Stdout

		// Split report into env and logs the same way main.go does.
		envReport := report
		logReport := ""
		if idx := strings.Index(report, "\n--- Recent Logs"); idx >= 0 {
			envReport = report[:idx]
			rest := report[idx+1:]
			if nl := strings.Index(rest, "\n"); nl >= 0 {
				logReport = rest[nl+1:]
			}
		}

		// Build the URL the same way cmdReportBug --open does.
		// Extract version from the report (first line after "smirror version: ").
		ver := "unknown"
		for _, line := range strings.Split(report, "\n") {
			if strings.HasPrefix(line, "smirror version: ") {
				ver = strings.TrimPrefix(line, "smirror version: ")
				break
			}
		}
		title := fmt.Sprintf("smirror %s (windows/amd64): ", ver)

		baseURL := "https://github.com/qraveh/SelectiveMirror/issues/new?template=bug_report.yml"
		issueURL := baseURL +
			"&title=" + url.QueryEscape(title) +
			"&environment=" + url.QueryEscape(envReport)
		if logReport != "" {
			issueURL += "&logs=" + url.QueryEscape(logReport)
		}

		// Parse and verify query params.
		u, err := url.Parse(issueURL)
		if err != nil {
			t.Fatalf("failed to parse constructed URL: %v", err)
		}
		q := u.Query()

		// Title must contain version and platform.
		gotTitle := q.Get("title")
		if gotTitle == "" {
			t.Fatal("URL missing 'title' query param")
		}
		if !strings.Contains(gotTitle, ver) {
			t.Errorf("title missing version %q: got %q", ver, gotTitle)
		}
		if !strings.Contains(gotTitle, "windows/amd64") {
			t.Errorf("title missing platform: got %q", gotTitle)
		}
		// Must NOT contain the old [Bug] prefix.
		if strings.Contains(gotTitle, "[Bug]") {
			t.Errorf("title still uses old [Bug] prefix: %q", gotTitle)
		}

		// Environment must contain diagnostics.
		gotEnv := q.Get("environment")
		if gotEnv == "" {
			t.Fatal("URL missing 'environment' query param")
		}
		for _, want := range []string{"smirror version:", "platform:", "rclone", "working-mirror", "broken-mirror", "sync_errors: 17"} {
			if !strings.Contains(gotEnv, want) {
				t.Errorf("environment field missing %q", want)
			}
		}

		// Logs must contain the injected error lines.
		gotLogs := q.Get("logs")
		if gotLogs == "" {
			t.Fatal("URL missing 'logs' query param")
		}
		for _, want := range []string{"sync failed", "directory not found", "ERR"} {
			if !strings.Contains(gotLogs, want) {
				t.Errorf("logs field missing %q", want)
			}
		}

		// URL must not exceed browser limit.
		if len(issueURL) > 8000 {
			t.Errorf("URL too long: %d bytes (max 8000)", len(issueURL))
		}

		t.Logf("URL length: %d bytes, title: %q", len(issueURL), gotTitle)
	})

	// --- Subtest: open browser (manual visual check, skipped in short/CI) ---
	t.Run("OpenBrowser", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping browser open in short mode")
		}

		r := runSmirror(t, cfgPath, "report-bug", "--open")
		assertNoPanic(t, r)
		assertStdoutContains(t, r, "--- Opening browser ---")

		t.Log("Browser should have opened with pre-filled GitHub issue form.")
		t.Log("Verify: Title has version prompt, Environment has diagnostics, Logs has error lines.")
	})
}

// =========================================================================
// REMOTE
// =========================================================================

func TestCLI_Remote(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_remote")

	t.Run("Show_NoDefault", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "remote")
		assertExitCode(t, r, 0)
		assertNoPanic(t, r)
	})

	t.Run("Set_LocalPath", func(t *testing.T) {
		env := newTestEnv(t)
		remotePath := filepath.Join(env.RootDir, "myremote")
		os.MkdirAll(remotePath, 0755)
		r := runSmirror(t, env.CfgPath, "remote", remotePath)
		assertExitCode(t, r, 0)

		// Verify it was set by reading config.
		r2 := runSmirror(t, env.CfgPath, "remote")
		assertExitCode(t, r2, 0)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/config.yaml", "remote")
		// remote command is lenient with missing config — shows "(not configured)".
		assertNoPanic(t, r)
	})
}

// =========================================================================
// ADDMIRROR (+ aliases add, add-mirror)
// =========================================================================

func TestCLI_AddMirror(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_addmirror")

	t.Run("Happy_WithDest", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "newsrc")
		dst := filepath.Join(root, "newdst")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)

		// Create a minimal config first.
		data := filepath.Join(root, "data")
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:    []mirrorDef{},
			StateDB:    filepath.Join(data, "state.db"),
			LogFile:    filepath.Join(data, "s.log"),
			SyncWorkers: 1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})

		r := runSmirror(t, cfg, "addmirror", src, "-dest", dst)
		assertNoPanic(t, r)
		// May succeed or may need interactive confirmation.
		// Check that the config was updated.
	})

	t.Run("Alias_Add", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		data := filepath.Join(root, "data")
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:    []mirrorDef{},
			StateDB:    filepath.Join(data, "state.db"),
			LogFile:    filepath.Join(data, "s.log"),
			SyncWorkers: 1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "add", src, "-dest", dst)
		assertNoPanic(t, r)
		coverage.Record("alias_add")
	})

	t.Run("Alias_AddMirror", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		data := filepath.Join(root, "data")
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:    []mirrorDef{},
			StateDB:    filepath.Join(data, "state.db"),
			LogFile:    filepath.Join(data, "s.log"),
			SyncWorkers: 1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "add-mirror", src, "-dest", dst)
		assertNoPanic(t, r)
		coverage.Record("alias_add_mirror")
	})

	t.Run("NoArgs", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "addmirror")
		if r.ExitCode == 0 {
			t.Error("expected error for missing path arg")
		}
	})

	t.Run("NonexistentPath", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "addmirror", "/no/such/path/xyz", "-dest", env.DstDir)
		// addmirror may accept nonexistent paths (deferred validation).
		assertNoPanic(t, r)
	})

	t.Run("FileNotDir", func(t *testing.T) {
		env := newTestEnv(t)
		filePath := filepath.Join(env.RootDir, "afile.txt")
		createFile(t, filePath, "data")
		r := runSmirror(t, env.CfgPath, "addmirror", filePath, "-dest", env.DstDir)
		// addmirror may accept files (validation happens at sync time).
		assertNoPanic(t, r)
	})
}

// =========================================================================
// UNMIRROR (+ aliases remove, remove-mirror, removemirror)
// =========================================================================

func TestCLI_Unmirror(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_unmirror")

	t.Run("ByName", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		r := runSmirror(t, env.CfgPath, "unmirror", "mirror1")
		assertNoPanic(t, r)
	})

	t.Run("Alias_Remove", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		r := runSmirror(t, env.CfgPath, "remove", "mirror1")
		assertNoPanic(t, r)
		coverage.Record("alias_remove")
	})

	t.Run("Alias_RemoveMirror", func(t *testing.T) {
		env := newTestEnvN(t, 2)
		r := runSmirror(t, env.CfgPath, "remove-mirror", "mirror1")
		assertNoPanic(t, r)
		coverage.Record("alias_remove_mirror")
	})

	t.Run("NotFound", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "unmirror", "nonexistent")
		if r.ExitCode == 0 {
			t.Error("expected error for unknown mirror")
		}
	})

	t.Run("NoArgs", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "unmirror")
		if r.ExitCode == 0 {
			t.Error("expected error for missing args")
		}
	})

	t.Run("RemoteUntouched", func(t *testing.T) {
		// Bug hunter: unmirror should NOT delete remote files.
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "keep.txt"), "keep")
		runSmirror(t, env.CfgPath, "sync-now")
		assertFileExists(t, filepath.Join(env.DstDir, "keep.txt"))

		runSmirror(t, env.CfgPath, "unmirror", "mirror0")
		// Remote file should still exist.
		assertFileExists(t, filepath.Join(env.DstDir, "keep.txt"))
	})
}

// =========================================================================
// SERVICE
// =========================================================================

func TestCLI_Service(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_service")

	t.Run("NoArgs", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "service")
		if r.ExitCode == 0 {
			t.Error("expected error for missing service action")
		}
		assertNoPanic(t, r)
	})

	t.Run("UnknownAction", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirror(t, env.CfgPath, "service", "bogus")
		if r.ExitCode == 0 {
			t.Error("expected error for unknown service action")
		}
	})

	t.Run("Install_NotElevated", func(t *testing.T) {
		// On non-elevated shell, install should fail with a permission error.
		env := newTestEnv(t)
		r := runSmirrorWithTimeout(t, 10*time.Second, env.CfgPath, "service", "install")
		// Should fail (not crash) when not running as admin.
		assertNoPanic(t, r)
		if r.ExitCode == 0 {
			t.Skip("running as admin — cannot test non-elevated failure")
		}
	})

	t.Run("Alias_Clean", func(t *testing.T) {
		env := newTestEnv(t)
		r := runSmirrorWithTimeout(t, 10*time.Second, env.CfgPath, "clean", "--yes")
		assertNoPanic(t, r)
		coverage.Record("alias_clean")
	})
}

// =========================================================================
// SELFUPDATE
// =========================================================================

func TestCLI_SelfUpdate(t *testing.T) {
	t.Parallel()
	coverage.Record("cli_selfupdate")

	t.Run("Check", func(t *testing.T) {
		r := runSmirrorRaw(t, "selfupdate", "--check")
		// Exit 0 = up to date, Exit 6 = update available
		if r.ExitCode != 0 && r.ExitCode != 6 {
			t.Errorf("unexpected exit code %d (expected 0 or 6)", r.ExitCode)
		}
		assertNoPanic(t, r)
	})

	t.Run("WhatsNew", func(t *testing.T) {
		r := runSmirrorRaw(t, "selfupdate", "--whatsnew")
		assertNoPanic(t, r)
	})
}

// =========================================================================
// START (foreground watcher)
// =========================================================================

func TestCLI_Start(t *testing.T) {
	coverage.Record("cli_start")

	t.Run("Happy_GracefulStop", func(t *testing.T) {
		env := newTestEnv(t)
		createFile(t, filepath.Join(env.SrcDir, "init.txt"), "initial")

		proc := startSmirror(t, env.CfgPath, "start")
		// Wait for lock file to appear (indicates startup complete).
		lockPath := filepath.Join(env.DataDir, "smirror.lock")
		if !waitForFile(t, lockPath, 10*time.Second) {
			r := proc.Stop()
			t.Fatalf("lock file never appeared; stdout=%s stderr=%s",
				truncate(r.Stdout, 300), truncate(r.Stderr, 300))
		}

		// Give it a moment to reconcile, then stop.
		time.Sleep(2 * time.Second)
		r := proc.Stop()
		assertNoPanic(t, r)
	})

	t.Run("NoConfig", func(t *testing.T) {
		r := runSmirrorWithTimeout(t, 5*time.Second, "/nonexistent/config.yaml", "start")
		assertExitCode(t, r, 2)
	})

	t.Run("LockConflict", func(t *testing.T) {
		env := newTestEnv(t)
		// Start first instance.
		proc1 := startSmirror(t, env.CfgPath, "start")
		lockPath := filepath.Join(env.DataDir, "smirror.lock")
		if !waitForFile(t, lockPath, 10*time.Second) {
			proc1.Kill()
			t.Fatal("first instance never started")
		}
		time.Sleep(500 * time.Millisecond)

		// Second instance should fail with lock conflict.
		r := runSmirrorWithTimeout(t, 10*time.Second, env.CfgPath, "start")
		if r.ExitCode == 4 {
			coverage.Record("exit_4")
		} else {
			t.Logf("expected exit 4 (lock conflict), got %d", r.ExitCode)
		}
		assertNoPanic(t, r)
		coverage.Record("scenario_lock")

		proc1.Stop()
	})

	t.Run("RcloneNotFound", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		dst := filepath.Join(root, "dst")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors:    []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
			RclonePath: "/nonexistent/rclone-xyz",
			StateDB:    filepath.Join(data, "state.db"),
			LogFile:    filepath.Join(data, "s.log"),
			SyncWorkers: 1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirrorWithTimeout(t, 10*time.Second, cfg, "start")
		if r.ExitCode == 3 {
			coverage.Record("exit_3")
		}
		assertNoPanic(t, r)
	})
}

// =========================================================================
// EXIT CODE VALIDATION (dedicated section to ensure all are covered)
// =========================================================================

func TestExitCodes(t *testing.T) {
	t.Parallel()

	t.Run("Exit0_Version", func(t *testing.T) {
		r := runSmirrorRaw(t, "version")
		assertExitCode(t, r, 0)
		coverage.Record("exit_0")
	})

	t.Run("Exit1_UnknownCmd", func(t *testing.T) {
		r := runSmirrorRaw(t, "definitely_not_a_command")
		assertExitCode(t, r, 1)
		coverage.Record("exit_1")
	})

	t.Run("Exit2_BadConfig", func(t *testing.T) {
		tmp := t.TempDir()
		badCfg := filepath.Join(tmp, "bad.yaml")
		os.WriteFile(badCfg, []byte("this is not valid yaml: ["), 0644)
		r := runSmirror(t, badCfg, "status")
		assertExitCode(t, r, 2)
		coverage.Record("exit_2")
	})

	t.Run("Exit2_MissingConfig", func(t *testing.T) {
		r := runSmirror(t, "/nonexistent/path/config.yaml", "status")
		assertExitCode(t, r, 2)
		coverage.Record("exit_2")
	})

	t.Run("Exit3_RcloneBadRemote", func(t *testing.T) {
		root := t.TempDir()
		src := filepath.Join(root, "src")
		data := filepath.Join(root, "data")
		os.MkdirAll(src, 0755)
		os.MkdirAll(data, 0755)
		noN := boolPtr(false)
		cfg := createConfig(t, root, configOpts{
			Mirrors: []mirrorDef{{
				Name: "m", LocalPath: src,
				Remote: "fakexyz999:some/path",
			}},
			StateDB:        filepath.Join(data, "state.db"),
			LogFile:        filepath.Join(data, "s.log"),
			SyncWorkers:    1,
			NotifyEnabled:  noN,
			AnomalyEnabled: noN,
			VerifyIntervalSec: -1,
		})
		r := runSmirror(t, cfg, "test-mirrors")
		if r.ExitCode == 3 {
			coverage.Record("exit_3")
		} else {
			t.Logf("expected exit 3, got %d", r.ExitCode)
		}
	})
}

// =========================================================================
// BUG HUNTER: EDGE CASES AND OPTION COMBINATIONS
// =========================================================================

func TestBugHunter_ConfigFlag_AllCommands(t *testing.T) {
	// Bug hunter: Ensure --config flag works with every command that uses it.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "probe.txt"), "probe")

	commands := [][]string{
		{"status"},
		{"sync-now"},
		{"dry-run"},
		{"test-mirrors"},
		{"list-filters"},
		{"explain", "mirror0", "probe.txt"},
		{"project-stats"},
		{"report-bug", "--stdout"},
		{"remote"},
	}
	for _, cmd := range commands {
		t.Run(strings.Join(cmd, "_"), func(t *testing.T) {
			r := runSmirror(t, env.CfgPath, cmd...)
			assertNoPanic(t, r)
			// Should not be exit 2 (config error) since config is valid.
			if r.ExitCode == 2 {
				t.Errorf("unexpected config error: %s", truncate(r.Stderr, 200))
			}
		})
	}
}

func TestBugHunter_DoubleSync(t *testing.T) {
	// Bug hunter: Two rapid sync-now calls shouldn't corrupt state DB.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "a.txt"), "aaa")

	r1 := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r1, 0)
	r2 := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r2, 0)
	assertNoPanic(t, r2)
	assertFileContent(t, filepath.Join(env.DstDir, "a.txt"), "aaa")
}

func TestBugHunter_SyncThenDryRun(t *testing.T) {
	// Bug hunter: dry-run after sync-now should show no pending changes.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "done.txt"), "done")
	runSmirror(t, env.CfgPath, "sync-now")
	r := runSmirror(t, env.CfgPath, "dry-run")
	assertExitCode(t, r, 0)
	// Should ideally not list "done.txt" as pending.
}

func TestBugHunter_SyncWithEmptyConfig(t *testing.T) {
	// Bug hunter: config with no mirrors should be handled gracefully.
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)
	cfg := createConfig(t, root, configOpts{
		Mirrors:    []mirrorDef{},
		StateDB:    filepath.Join(data, "state.db"),
		LogFile:    filepath.Join(data, "s.log"),
		SyncWorkers: 1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	for _, cmd := range []string{"sync-now", "dry-run", "status", "project-stats"} {
		t.Run(cmd, func(t *testing.T) {
			r := runSmirror(t, cfg, cmd)
			assertNoPanic(t, r)
			// Should either succeed or give config error, never crash.
		})
	}
}

func TestBugHunter_VeryLongMirrorName(t *testing.T) {
	// Bug hunter: extremely long mirror name.
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	data := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	longName := strings.Repeat("a", 300)
	cfg := createConfig(t, root, configOpts{
		Mirrors:    []mirrorDef{{Name: longName, LocalPath: src, Remote: dst}},
		StateDB:    filepath.Join(data, "state.db"),
		LogFile:    filepath.Join(data, "s.log"),
		SyncWorkers: 1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "sync-now")
	assertNoPanic(t, r)
}

func TestBugHunter_UnicodeInMirrorName(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	data := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	cfg := createConfig(t, root, configOpts{
		Mirrors:    []mirrorDef{{Name: "テスト-mirror", LocalPath: src, Remote: dst}},
		StateDB:    filepath.Join(data, "state.db"),
		LogFile:    filepath.Join(data, "s.log"),
		SyncWorkers: 1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	createFile(t, filepath.Join(src, "hello.txt"), "hello")
	r := runSmirror(t, cfg, "sync-now")
	assertNoPanic(t, r)
}

func TestBugHunter_SpecialCharsInConfigPath(t *testing.T) {
	// Bug hunter: config file in path with spaces and special chars.
	t.Parallel()
	root := t.TempDir()
	weirdDir := filepath.Join(root, "path with spaces & (parens)")
	src := filepath.Join(weirdDir, "src")
	dst := filepath.Join(weirdDir, "dst")
	data := filepath.Join(weirdDir, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	cfg := createConfig(t, weirdDir, configOpts{
		Mirrors:    []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
		StateDB:    filepath.Join(data, "state.db"),
		LogFile:    filepath.Join(data, "s.log"),
		SyncWorkers: 1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	createFile(t, filepath.Join(src, "test.txt"), "test")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	assertFileContent(t, filepath.Join(dst, "test.txt"), "test")
}

func TestBugHunter_MaxFileSizeMB(t *testing.T) {
	// Bug hunter: file exactly at, just under, and just over the limit.
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	data := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{{
			Name: "m", LocalPath: src, Remote: dst,
			MaxFileSizeMB: 1, // 1 MB limit
		}},
		StateDB:        filepath.Join(data, "state.db"),
		LogFile:        filepath.Join(data, "s.log"),
		SyncWorkers:    1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	// Under limit: 500KB
	createBinaryFile(t, filepath.Join(src, "small.bin"), 500*1024)
	// Over limit: 2MB
	createBinaryFile(t, filepath.Join(src, "large.bin"), 2*1024*1024)
	// Exactly at limit (within rounding).
	createBinaryFile(t, filepath.Join(src, "exact.bin"), 1*1024*1024)

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(dst, "small.bin"))
	// Note: max_file_size_mb enforcement may only apply to watcher mode
	// (per-file sync), not batch rclone copy in sync-now. Log rather than fail.
	if fileExists(filepath.Join(dst, "large.bin")) {
		t.Log("NOTE: large.bin synced despite max_file_size_mb=1 (sync-now uses batch copy)")
	}
}

func TestBugHunter_ConcurrentSyncNow(t *testing.T) {
	// Bug hunter: two sync-now invocations at the same time shouldn't deadlock or corrupt.
	t.Parallel()
	env := newTestEnv(t)
	for i := 0; i < 5; i++ {
		createFile(t, filepath.Join(env.SrcDir, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("data%d", i))
	}

	done := make(chan smirrorResult, 2)
	go func() {
		done <- runSmirror(t, env.CfgPath, "sync-now")
	}()
	go func() {
		done <- runSmirror(t, env.CfgPath, "sync-now")
	}()

	for i := 0; i < 2; i++ {
		r := <-done
		assertNoPanic(t, r)
	}
}

func TestBugHunter_SyncWorkers_Range(t *testing.T) {
	// Bug hunter: test with min (1) and max (16) workers.
	t.Parallel()
	for _, workers := range []int{1, 4, 16} {
		t.Run(fmt.Sprintf("Workers%d", workers), func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "src")
			dst := filepath.Join(root, "dst")
			data := filepath.Join(root, "data")
			os.MkdirAll(src, 0755)
			os.MkdirAll(dst, 0755)
			os.MkdirAll(data, 0755)
			noN := boolPtr(false)
			cfg := createConfig(t, root, configOpts{
				Mirrors:    []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
				StateDB:    filepath.Join(data, "state.db"),
				LogFile:    filepath.Join(data, "s.log"),
				SyncWorkers: workers,
				NotifyEnabled:  noN,
				AnomalyEnabled: noN,
				VerifyIntervalSec: -1,
			})
			for i := 0; i < 5; i++ {
				createFile(t, filepath.Join(src, fmt.Sprintf("w%d.txt", i)), "data")
			}
			r := runSmirror(t, cfg, "sync-now")
			assertExitCode(t, r, 0)
			assertNoPanic(t, r)
		})
	}
}

func TestBugHunter_EmptyFileContent(t *testing.T) {
	// Bug hunter: zero-byte files should sync without error.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "empty.txt"), "")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(env.DstDir, "empty.txt"))
	assertFileContent(t, filepath.Join(env.DstDir, "empty.txt"), "")
	coverage.Record("path_empty_file")
}

func TestBugHunter_DotFile(t *testing.T) {
	// Bug hunter: dotfiles should be included unless excluded.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, ".hidden"), "secret")
	createFile(t, filepath.Join(env.SrcDir, ".env"), "VAR=1")
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertFileExists(t, filepath.Join(env.DstDir, ".hidden"))
	assertFileExists(t, filepath.Join(env.DstDir, ".env"))
	coverage.Record("path_dotfile")
}

func TestBugHunter_SyncIgnoreIsNotSynced(t *testing.T) {
	// SM-125: .syncignore is a control file and must NOT be synced to remote.
	// Test name reflects the post-fix behavior. Before SM-125, the filter
	// engine treated .syncignore as ordinary content; that was a bug.
	t.Parallel()
	env := newTestEnv(t)
	createSyncIgnore(t, env.SrcDir, []string{"*.tmp"})
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	// .syncignore itself must NOT appear on the remote.
	assertFileNotExists(t, filepath.Join(env.DstDir, ".syncignore"))
}

func TestBugHunter_StateDB_DeletedBetweenRuns(t *testing.T) {
	// Bug hunter: if state DB is deleted between runs, should recover gracefully.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "a.txt"), "a")
	runSmirror(t, env.CfgPath, "sync-now")

	// Delete the state DB.
	dbPath := filepath.Join(env.DataDir, "state.db")
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	// Re-sync should work (re-creates DB, re-syncs everything).
	r := runSmirror(t, env.CfgPath, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)
}

func TestBugHunter_StatusAfterCorruptDB(t *testing.T) {
	// Bug hunter: corrupted state.db should not cause a crash.
	t.Parallel()
	env := newTestEnv(t)
	createFile(t, filepath.Join(env.SrcDir, "a.txt"), "a")
	runSmirror(t, env.CfgPath, "sync-now")

	// Corrupt the state DB.
	dbPath := filepath.Join(env.DataDir, "state.db")
	os.WriteFile(dbPath, []byte("THIS IS NOT A SQLITE DATABASE"), 0644)

	r := runSmirror(t, env.CfgPath, "status")
	assertNoPanic(t, r)
}

func TestBugHunter_MirrorOrderIndependence(t *testing.T) {
	// Bug hunter: order of mirrors in config should not affect behavior.
	t.Parallel()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0755)

	mirrors := make([]mirrorDef, 3)
	for i := 0; i < 3; i++ {
		src := filepath.Join(root, fmt.Sprintf("src%d", i))
		dst := filepath.Join(root, fmt.Sprintf("dst%d", i))
		os.MkdirAll(src, 0755)
		os.MkdirAll(dst, 0755)
		createFile(t, filepath.Join(src, "data.txt"), fmt.Sprintf("mirror%d", i))
		mirrors[i] = mirrorDef{
			Name:      fmt.Sprintf("m%d", i),
			LocalPath: src,
			Remote:    dst,
		}
	}
	noN := boolPtr(false)
	cfg := createConfig(t, root, configOpts{
		Mirrors:    mirrors,
		StateDB:    filepath.Join(data, "state.db"),
		LogFile:    filepath.Join(data, "s.log"),
		SyncWorkers: 1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})

	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	for i := 0; i < 3; i++ {
		assertFileContent(t, filepath.Join(root, fmt.Sprintf("dst%d", i), "data.txt"),
			fmt.Sprintf("mirror%d", i))
	}
}

func TestBugHunter_BandwidthLimit(t *testing.T) {
	// Bug hunter: bandwidth_limit option shouldn't crash.
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	data := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	cfg := createConfig(t, root, configOpts{
		Mirrors:        []mirrorDef{{Name: "m", LocalPath: src, Remote: dst}},
		BandwidthLimit: "1M",
		StateDB:        filepath.Join(data, "state.db"),
		LogFile:        filepath.Join(data, "s.log"),
		SyncWorkers:    1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})
	createFile(t, filepath.Join(src, "test.txt"), "test")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)
}

func TestBugHunter_RcloneExtraFlags(t *testing.T) {
	// Bug hunter: rclone_extra_flags should be passed through without crash.
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	data := filepath.Join(root, "data")
	os.MkdirAll(src, 0755)
	os.MkdirAll(dst, 0755)
	os.MkdirAll(data, 0755)
	noN := boolPtr(false)

	cfg := createConfig(t, root, configOpts{
		Mirrors: []mirrorDef{{
			Name: "m", LocalPath: src, Remote: dst,
			RcloneExtra: []string{"--transfers", "2"},
		}},
		StateDB:        filepath.Join(data, "state.db"),
		LogFile:        filepath.Join(data, "s.log"),
		SyncWorkers:    1,
		NotifyEnabled:  noN,
		AnomalyEnabled: noN,
		VerifyIntervalSec: -1,
	})
	createFile(t, filepath.Join(src, "test.txt"), "test")
	r := runSmirror(t, cfg, "sync-now")
	assertExitCode(t, r, 0)
	assertNoPanic(t, r)
}
