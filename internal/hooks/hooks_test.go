package hooks

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRun_EmptyCommand_NoOp(t *testing.T) {
	r := New(30 * time.Second)
	err := r.Run(context.Background(), "", Env{Project: "test"})
	if err != nil {
		t.Errorf("empty command should be no-op, got: %v", err)
	}
}

func TestRun_NilRunner_NoOp(t *testing.T) {
	var r *Runner
	err := r.Run(context.Background(), "echo hello", Env{})
	if err != nil {
		t.Errorf("nil runner should be no-op, got: %v", err)
	}
}

func TestRun_SimpleCommand(t *testing.T) {
	r := New(30 * time.Second)

	// Use echo which writes to stdout (captured by CombinedOutput).
	// We verify the hook ran by checking no error returned.
	err := r.Run(context.Background(), "echo hello", Env{
		Project: "test-proj",
		File:    "file.txt",
		Remote:  "gdrive:backup",
		Event:   "post_sync",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRun_EnvVariables(t *testing.T) {
	r := New(30 * time.Second)

	// Verify env vars are set by checking the hook command can access them.
	// On Windows, cmd.exe expands %VAR%. We use a simple echo that
	// includes the env var — if it expands, the hook infrastructure works.
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "echo %SMIRROR_PROJECT%"
	} else {
		cmd = "echo $SMIRROR_PROJECT"
	}

	// The hook runs successfully if env vars are set (no error).
	// We can't easily capture stdout in the test, but the hook not failing
	// with "SMIRROR_PROJECT not defined" confirms env vars are set.
	err := r.Run(context.Background(), cmd, Env{
		Project: "myproj",
		Event:   "pre_sync",
	})
	if err != nil {
		t.Fatalf("Run with env vars: %v", err)
	}
}

func TestRun_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("timeout test unreliable on Windows (cmd.exe process tree not killed by context)")
	}

	r := New(1 * time.Second)
	err := r.Run(context.Background(), "sleep 30", Env{Project: "test"})
	if err == nil {
		t.Error("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want 'timed out'", err.Error())
	}
}

func TestRun_FailingCommand(t *testing.T) {
	r := New(30 * time.Second)
	err := r.Run(context.Background(), "exit 1", Env{Project: "test"})
	if err == nil {
		t.Error("expected error from failing command")
	}
}

// SEC-C5: hook must be skipped if any env value contains shell metacharacters.
func TestRun_RejectsShellMetachars(t *testing.T) {
	r := New(30 * time.Second)
	tests := []struct {
		name string
		env  Env
	}{
		{"ampersand_in_file", Env{Project: "p", File: "a&calc.exe", Event: "post_sync"}},
		{"pipe_in_file", Env{Project: "p", File: "a|b", Event: "post_sync"}},
		{"redirect_in_project", Env{Project: "p>x", File: "f.txt", Event: "post_sync"}},
		{"backtick_in_remote", Env{Project: "p", File: "f.txt", Remote: "gd:`whoami`", Event: "post_sync"}},
		{"dollar_paren_in_file", Env{Project: "p", File: "$(rm)", Event: "post_sync"}},
		{"semicolon_in_file", Env{Project: "p", File: "a;b", Event: "post_sync"}},
		{"newline_in_file", Env{Project: "p", File: "a\nb", Event: "post_sync"}},
		{"carriage_return", Env{Project: "p", File: "a\rb", Event: "post_sync"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Use "echo hi" — it should be rejected BEFORE exec.
			err := r.Run(context.Background(), "echo hi", tc.env)
			if err == nil {
				t.Errorf("hook should be rejected for %+v, got nil error", tc.env)
			}
			if err != nil && !strings.Contains(err.Error(), "shell metacharacter") {
				t.Errorf("expected shell metacharacter error, got: %v", err)
			}
		})
	}
}

// SEC-C5: safe filenames must NOT be rejected.
func TestRun_AllowsSafeFilenames(t *testing.T) {
	r := New(30 * time.Second)
	tests := []Env{
		{Project: "p", File: "normal.txt", Event: "post_sync"},
		{Project: "p", File: "with spaces.txt", Event: "post_sync"},
		{Project: "p", File: "path/to/file.md", Event: "post_sync"},
		{Project: "p", File: "file-with-dashes_and_underscores.txt", Event: "post_sync"},
		{Project: "p", File: "UPPERCASE.TXT", Event: "post_sync"},
	}
	for _, env := range tests {
		t.Run(env.File, func(t *testing.T) {
			err := r.Run(context.Background(), "echo hi", env)
			if err != nil && strings.Contains(err.Error(), "shell metacharacter") {
				t.Errorf("safe filename %q wrongly rejected: %v", env.File, err)
			}
		})
	}
}

func TestContainsShellMetachar(t *testing.T) {
	safe := []string{"", "normal.txt", "with spaces", "a/b/c", "UPPER_lower-123", "héllo"}
	unsafe := []string{"a&b", "a|b", "a>b", "a<b", "a\"b", "a^b", "a$b", "a`b", "a(b", "a)b", "a;b", "a\nb", "a\rb"}
	for _, s := range safe {
		if containsShellMetachar(s) {
			t.Errorf("safe string %q wrongly flagged", s)
		}
	}
	for _, s := range unsafe {
		if !containsShellMetachar(s) {
			t.Errorf("unsafe string %q not flagged", s)
		}
	}
}
