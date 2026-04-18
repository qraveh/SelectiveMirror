//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

// IsAdminOwnedPath reports whether the file at path is owned by root (uid 0).
// SEC-C5: Unix equivalent of the Windows admin-owner check.
func IsAdminOwnedPath(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("stat %q: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot determine owner uid for %q", path)
	}
	return stat.Uid == 0, nil
}
