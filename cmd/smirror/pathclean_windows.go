//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

const (
	systemEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	userEnvKey   = `Environment`
)

// findInstallations locates the running binary and scans every PATH directory
// for additional smirror.exe copies.
func findInstallations() installationInfo {
	var info installationInfo

	// Current binary
	exePath, err := os.Executable()
	if err != nil {
		exePath = os.Args[0]
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	exePath = filepath.Clean(exePath)
	info.CurrentExe = exePath

	// Scan all PATH entries
	pathEnv := os.Getenv("PATH")
	seen := make(map[string]bool) // lowercased paths for dedup
	for _, dir := range strings.Split(pathEnv, string(os.PathListSeparator)) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "smirror.exe")
		fi, err := os.Stat(candidate)
		if err != nil || fi.IsDir() {
			continue
		}
		resolved, _ := filepath.EvalSymlinks(candidate)
		if resolved == "" {
			resolved = candidate
		}
		resolved = filepath.Clean(resolved)
		key := strings.ToLower(resolved)
		if seen[key] {
			continue
		}
		seen[key] = true
		info.AllFound = append(info.AllFound, resolved)
	}

	// Ensure current exe is in the list even if not on PATH
	curKey := strings.ToLower(info.CurrentExe)
	if !seen[curKey] {
		info.AllFound = append([]string{info.CurrentExe}, info.AllFound...)
	}

	info.HasDuplicates = len(info.AllFound) > 1
	return info
}

// readRegistryPATH reads the "Path" value from the given registry key.
// Returns the raw (unexpanded) value, its registry type, and any error.
func readRegistryPATH(root registry.Key, subkey string) (string, uint32, error) {
	k, err := registry.OpenKey(root, subkey, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, err
	}
	defer k.Close()

	val, valtype, err := k.GetStringValue("Path")
	if err != nil {
		return "", 0, err
	}
	return val, valtype, nil
}

// writeRegistryPATH writes the "Path" value back to the registry, preserving
// the original value type (REG_SZ vs REG_EXPAND_SZ).
func writeRegistryPATH(root registry.Key, subkey string, value string, valtype uint32) error {
	k, err := registry.OpenKey(root, subkey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if valtype == registry.EXPAND_SZ {
		return k.SetExpandStringValue("Path", value)
	}
	return k.SetStringValue("Path", value)
}

// broadcastSettingChange notifies running applications that environment
// variables have changed, so they can reload PATH without a reboot.
func broadcastSettingChange() error {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("SendMessageTimeoutW")

	const (
		hwndBroadcast  = 0xFFFF
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)

	env, _ := syscall.UTF16PtrFromString("Environment")
	ret, _, err := proc.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(env)),
		uintptr(smtoAbortIfHung),
		uintptr(5000),
		0,
	)
	if ret == 0 {
		return fmt.Errorf("SendMessageTimeout failed: %w", err)
	}
	return nil
}

// cleanPATHEntries scans System and User PATH for smirror directories and
// offers to remove them. Returns true if any PATH was modified.
func cleanPATHEntries(info installationInfo, autoYes bool) bool {
	smirrorDirs := smirrorDirsFromInfo(info)
	if len(smirrorDirs) == 0 {
		return false
	}

	// Read both PATHs from registry (unexpanded)
	systemPath, systemType, systemErr := readRegistryPATH(registry.LOCAL_MACHINE, systemEnvKey)
	userPath, userType, userErr := readRegistryPATH(registry.CURRENT_USER, userEnvKey)

	var sysRemove, sysRemaining []string
	if systemErr == nil {
		sysRemove, sysRemaining = findPATHEntriesToRemove(systemPath, smirrorDirs)
	}

	var usrRemove, usrRemaining []string
	if userErr == nil {
		usrRemove, usrRemaining = findPATHEntriesToRemove(userPath, smirrorDirs)
	}

	if len(sysRemove) == 0 && len(usrRemove) == 0 {
		fmt.Println("No smirror-related PATH entries found.")
		return false
	}

	// --- Preview ---
	fmt.Println()
	fmt.Println("PATH entries that reference smirror directories:")
	if len(sysRemove) > 0 {
		fmt.Println()
		fmt.Println("  System PATH (HKLM):")
		for _, e := range sysRemove {
			fmt.Printf("    - %s\n", e)
		}
	}
	if len(usrRemove) > 0 {
		fmt.Println()
		fmt.Println("  User PATH (HKCU):")
		for _, e := range usrRemove {
			fmt.Printf("    - %s\n", e)
		}
	}

	// --- Elevation check ---
	needsElevation := len(sysRemove) > 0 && !isAdmin()
	if needsElevation {
		fmt.Println()
		fmt.Println("  Note: System PATH requires Administrator privileges to modify.")
	}

	// --- Prompt ---
	if !autoYes {
		fmt.Println()
		what := "these PATH entries"
		if needsElevation && len(usrRemove) > 0 {
			what = "User PATH entries (System PATH requires elevation)"
		} else if needsElevation {
			what = "nothing (run elevated to modify System PATH)"
		}
		fmt.Printf("Remove %s? [y/N] ", what)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("PATH unchanged.")
			if needsElevation {
				printManualPATHInstructions(sysRemove)
			}
			return false
		}
	}

	// Nothing to do if only system entries and not elevated
	if needsElevation && len(usrRemove) == 0 {
		printManualPATHInstructions(sysRemove)
		return false
	}

	modified := false

	// --- Write User PATH ---
	if len(usrRemove) > 0 {
		newPath := strings.Join(usrRemaining, ";")
		if err := writeRegistryPATH(registry.CURRENT_USER, userEnvKey, newPath, userType); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating User PATH: %v\n", err)
		} else {
			for _, e := range usrRemove {
				fmt.Printf("  Removed from User PATH: %s\n", e)
			}
			modified = true
		}
	}

	// --- Write System PATH ---
	if len(sysRemove) > 0 && isAdmin() {
		// Safety: never leave System PATH empty
		if len(sysRemaining) == 0 {
			fmt.Fprintln(os.Stderr, "Warning: refusing to empty System PATH. Skipping.")
		} else {
			newPath := strings.Join(sysRemaining, ";")
			if err := writeRegistryPATH(registry.LOCAL_MACHINE, systemEnvKey, newPath, systemType); err != nil {
				fmt.Fprintf(os.Stderr, "Error updating System PATH: %v\n", err)
			} else {
				for _, e := range sysRemove {
					fmt.Printf("  Removed from System PATH: %s\n", e)
				}
				modified = true
			}
		}
	}

	// --- Broadcast ---
	if modified {
		if err := broadcastSettingChange(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not notify running applications: %v\n", err)
			fmt.Fprintln(os.Stderr, "Existing terminal windows may need to be restarted to see the PATH change.")
		} else {
			fmt.Println("  Environment change broadcast sent.")
		}
	}

	// Print manual instructions for system PATH if not elevated
	if needsElevation && len(sysRemove) > 0 {
		printManualPATHInstructions(sysRemove)
	}

	return modified
}

// printManualPATHInstructions prints instructions for manually removing
// entries from System PATH when elevation is not available.
func printManualPATHInstructions(entries []string) {
	fmt.Println()
	fmt.Println("System PATH requires Administrator privileges to modify.")
	fmt.Println("To remove manually:")
	fmt.Println("  1. Open System Properties > Environment Variables")
	fmt.Println("  2. Under \"System variables\", select \"Path\" and click Edit")
	for _, e := range entries {
		fmt.Printf("  3. Remove: %s\n", e)
	}
	fmt.Println()
	fmt.Println("Or run from an elevated prompt:")
	fmt.Println("  smirror service uninstall --clean")
}
