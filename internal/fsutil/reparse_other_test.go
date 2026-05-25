//go:build !windows

package fsutil

import "testing"

// R-15: trivial coverage for the non-Windows stub. The function always
// returns false on POSIX (reparse points are an NTFS concept). This
// test exists to clear the per-package coverage floor on non-Windows
// CI runners; the actual security-relevant behavior is tested in
// reparse_windows_test.go.

func TestIsReparsePoint_StubAlwaysFalse(t *testing.T) {
	// Any path works; the stub never inspects it.
	for _, p := range []string{"", "/tmp", "/", "/etc/passwd", "/nonexistent"} {
		if IsReparsePoint(p) {
			t.Errorf("non-Windows stub returned true for %q (should always be false)", p)
		}
	}
}
