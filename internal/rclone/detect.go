// Package rclone provides rclone binary detection, version parsing,
// and compatibility checking.
package rclone

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Version represents a parsed semantic version.
type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string // original string from `rclone version`
}

// String returns "Major.Minor.Patch".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// AtLeast returns true if v >= other.
func (v Version) AtLeast(major, minor, patch int) bool {
	if v.Major != major {
		return v.Major > major
	}
	if v.Minor != minor {
		return v.Minor > minor
	}
	return v.Patch >= patch
}

// Info holds the result of rclone binary detection.
type Info struct {
	Path    string  // resolved absolute path to the rclone binary
	Version Version // parsed version
	OS      string  // from rclone version output (e.g., "windows/amd64")
}

// Compatibility describes the compatibility level with SelectiveMirror.
type Compatibility int

const (
	CompatFull    Compatibility = iota // v1.73+: all features
	CompatPartial                      // v1.50-1.72: missing --skip-links
	CompatNone                         // <1.50: missing critical subcommands
)

// CompatCheck returns the compatibility level and a human-readable message.
func (info *Info) CompatCheck() (Compatibility, string) {
	v := info.Version
	if v.AtLeast(1, 73, 0) {
		return CompatFull, fmt.Sprintf("rclone %s — full compatibility", v)
	}
	if v.AtLeast(1, 50, 0) {
		return CompatPartial, fmt.Sprintf("rclone %s — partial: --skip-links unavailable (requires 1.73+), symlinks may leak to remote", v)
	}
	return CompatNone, fmt.Sprintf("rclone %s — incompatible: missing critical subcommands (requires 1.50+)", v)
}

// Detect finds and validates the rclone binary.
// configuredPath is the user-configured rclone_path (may be empty, relative, or absolute).
// Returns Info on success, or error if rclone cannot be found or executed.
func Detect(configuredPath string) (*Info, error) {
	path, err := resolve(configuredPath)
	if err != nil {
		return nil, fmt.Errorf("rclone not found: %w\n  searched: %s", err, searchDescription(configuredPath))
	}

	// Get version
	cmd := exec.Command(path, "version")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("rclone at %q failed to execute: %w", path, err)
	}

	ver, osArch := parseVersionOutput(string(out))
	return &Info{
		Path:    path,
		Version: ver,
		OS:      osArch,
	}, nil
}

// resolve finds the rclone binary path using the search order:
// 1. Configured path (if absolute and exists)
// 2. System PATH (exec.LookPath)
// 3. Same directory as smirror.exe (bundled rclone)
// 4. Common install locations (Windows-specific)
func resolve(configuredPath string) (string, error) {
	// 1. If configured path is absolute, check it directly
	if configuredPath != "" && filepath.IsAbs(configuredPath) {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath, nil
		}
		return "", fmt.Errorf("configured path %q does not exist", configuredPath)
	}

	// Use configured name or default
	name := configuredPath
	if name == "" {
		name = "rclone"
	}

	// 2. System PATH
	if path, err := exec.LookPath(name); err == nil {
		abs, _ := filepath.Abs(path)
		return abs, nil
	}

	// 3. Same directory as the running executable (bundled rclone)
	if exePath, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exePath), "rclone.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	// 4. Common install locations (Windows only)
	if runtime.GOOS == "windows" {
		candidates := windowsSearchPaths()
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}

	return "", fmt.Errorf("%q not found in PATH or common install locations", name)
}

// windowsSearchPaths returns common rclone install locations on Windows.
func windowsSearchPaths() []string {
	var paths []string

	// Program Files
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		paths = append(paths, filepath.Join(pf, "rclone", "rclone.exe"))
	}

	// WinGet links (current user)
	if lad := os.Getenv("LOCALAPPDATA"); lad != "" {
		paths = append(paths, filepath.Join(lad, "Microsoft", "WinGet", "Links", "rclone.exe"))
	}

	// Chocolatey
	if choco := os.Getenv("ChocolateyInstall"); choco != "" {
		paths = append(paths, filepath.Join(choco, "bin", "rclone.exe"))
	}

	// Scoop
	if up := os.Getenv("USERPROFILE"); up != "" {
		paths = append(paths, filepath.Join(up, "scoop", "shims", "rclone.exe"))
	}

	// WinGet packages — scan user profiles (needed when running as SYSTEM service)
	// WinGet installs rclone under each user's LocalAppData with a versioned directory.
	if sd := os.Getenv("SystemDrive"); sd != "" {
		usersDir := filepath.Join(sd, "Users")
		entries, _ := os.ReadDir(usersDir)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			wingetPkgs := filepath.Join(usersDir, entry.Name(), "AppData", "Local",
				"Microsoft", "WinGet", "Packages")
			pkgEntries, err := os.ReadDir(wingetPkgs)
			if err != nil {
				continue
			}
			for _, pkg := range pkgEntries {
				if pkg.IsDir() && strings.Contains(pkg.Name(), "Rclone.Rclone") {
					// Scan for rclone.exe inside versioned subdirectory
					subDir := filepath.Join(wingetPkgs, pkg.Name())
					subs, _ := os.ReadDir(subDir)
					for _, sub := range subs {
						if sub.IsDir() && strings.HasPrefix(sub.Name(), "rclone-") {
							candidate := filepath.Join(subDir, sub.Name(), "rclone.exe")
							paths = append(paths, candidate)
						}
					}
					// Also check directly in the package dir
					paths = append(paths, filepath.Join(subDir, "rclone.exe"))
				}
			}
		}
	}

	return paths
}

// parseVersionOutput extracts version and OS/arch from `rclone version` output.
// Example input:
//
//	rclone v1.68.2
//	- os/version: Microsoft Windows 11 Home 10.0.26200.5603 (64 bit)
//	- os/kernel: windows (amd64)
//	- os/type: windows
//	- os/arch: amd64 (Intel(R) Core(TM) Ultra 9 275HX)
//	- go/version: go1.23.4
//	- go/linking: static
//	- go/tags: cmount
var versionRegex = regexp.MustCompile(`rclone\s+v?(\d+)\.(\d+)\.(\d+)`)

func parseVersionOutput(output string) (Version, string) {
	var ver Version

	m := versionRegex.FindStringSubmatch(output)
	if m != nil {
		ver.Major, _ = strconv.Atoi(m[1])
		ver.Minor, _ = strconv.Atoi(m[2])
		ver.Patch, _ = strconv.Atoi(m[3])
		ver.Raw = m[0]
	}

	// Extract OS/arch
	osArch := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "os/kernel:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				osArch = strings.TrimSpace(parts[1])
			}
		}
	}

	return ver, osArch
}

// searchDescription returns a human-readable description of where rclone was searched.
func searchDescription(configuredPath string) string {
	parts := []string{"PATH", "exe directory"}
	if configuredPath != "" {
		parts = append([]string{configuredPath}, parts...)
	}
	if runtime.GOOS == "windows" {
		parts = append(parts, "%ProgramFiles%\\rclone", "WinGet links", "Chocolatey", "Scoop")
	}
	return strings.Join(parts, ", ")
}
