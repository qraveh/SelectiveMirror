package main

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qraveh/SelectiveMirror/internal/config"
	"github.com/qraveh/SelectiveMirror/internal/rclone"
	"github.com/qraveh/SelectiveMirror/internal/service"
	"github.com/qraveh/SelectiveMirror/internal/state"
	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// selfUpdateResult captures the preflight analysis before asking the user.
type selfUpdateResult struct {
	current       string
	latest        *telemetry.ReleaseInfo
	needsAdmin    bool
	adminReasons  []string
	svcInstalled  bool
	svcRunning    bool
	binaryPath    string
	binaryDir     string
	canWriteDir   bool
	zipAsset      *telemetry.Asset
	checksumAsset *telemetry.Asset
}

// selfUpdateFlags holds parsed flags for the selfupdate command.
type selfUpdateFlags struct {
	checkOnly     bool
	whatsNew      bool
	autoYes       bool
	includeRclone bool
}

// parseSelfUpdateFlags parses selfupdate command flags from args.
func parseSelfUpdateFlags(args []string) selfUpdateFlags {
	var f selfUpdateFlags
	for _, a := range args {
		switch a {
		case "--check":
			f.checkOnly = true
		case "--whatsnew":
			f.whatsNew = true
		case "--yes", "-y":
			f.autoYes = true
		case "--include-rclone":
			f.includeRclone = true
		}
	}
	return f
}

func cmdSelfUpdate(configPath string, args []string) {
	flags := parseSelfUpdateFlags(args)
	checkOnly := flags.checkOnly
	whatsNew := flags.whatsNew
	autoYes := flags.autoYes
	includeRclone := flags.includeRclone

	fmt.Printf("smirror %s\n", version)
	fmt.Println()

	// --- Phase 1: Check for updates ---
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := telemetry.NewClient("", version)
	release, err := client.CheckForUpdate(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking for updates: %v\n", err)
		os.Exit(ExitError)
	}
	if release == nil {
		fmt.Println("No releases found on GitHub.")
		return
	}

	latestVer := strings.TrimPrefix(release.TagName, "v")
	cmp := telemetry.CompareVersions(version, latestVer)
	if cmp >= 0 {
		fmt.Printf("Already up to date (v%s).\n", latestVer)
		return
	}

	fmt.Printf("New version available: v%s (current: %s)\n", latestVer, version)
	fmt.Printf("  Released: %s\n", release.PublishedAt.Format("2006-01-02"))
	if release.Name != "" && release.Name != release.TagName {
		fmt.Printf("  Name:     %s\n", release.Name)
	}
	fmt.Printf("  Details:  %s\n", release.HTMLURL)

	// --- --whatsnew: show release notes summary ---
	if whatsNew || checkOnly {
		if release.Body != "" {
			fmt.Println()
			fmt.Println("What's new:")
			fmt.Println(strings.Repeat("-", 60))
			// Print release body (markdown), trimmed to reasonable length
			body := strings.TrimSpace(release.Body)
			lines := strings.Split(body, "\n")
			maxLines := 40
			if len(lines) > maxLines {
				for _, line := range lines[:maxLines] {
					fmt.Println(line)
				}
				fmt.Printf("  ... (%d more lines, see release page)\n", len(lines)-maxLines)
			} else {
				fmt.Println(body)
			}
			fmt.Println(strings.Repeat("-", 60))
		} else {
			fmt.Println("\n(No release notes available.)")
		}
	}

	if checkOnly {
		return
	}

	// --- Phase 2: Preflight (before asking anything) ---
	result := selfUpdatePreflight(release)

	// Print preflight findings
	fmt.Println()
	if result.svcInstalled {
		if result.svcRunning {
			fmt.Println("  Service:  installed and RUNNING (will be stopped and restarted)")
		} else {
			fmt.Println("  Service:  installed but stopped (will be restarted after update)")
		}
	}
	fmt.Printf("  Binary:   %s\n", result.binaryPath)

	if !result.canWriteDir {
		fmt.Printf("  Write:    NO (cannot write to %s)\n", result.binaryDir)
	}

	if result.zipAsset == nil {
		fmt.Fprintf(os.Stderr, "\nError: no Windows amd64 zip asset found in release v%s.\n", latestVer)
		fmt.Fprintf(os.Stderr, "Available assets:\n")
		for _, a := range release.Assets {
			fmt.Fprintf(os.Stderr, "  - %s (%d bytes)\n", a.Name, a.Size)
		}
		os.Exit(ExitError)
	}

	fmt.Printf("  Download: %s (%.1f MB)\n", result.zipAsset.Name, float64(result.zipAsset.Size)/(1024*1024))

	if result.checksumAsset != nil {
		fmt.Println("  Verify:   SHA256 checksum available")
	} else {
		fmt.Println("  Verify:   no checksum file (integrity check skipped)")
	}

	if result.needsAdmin && !isAdmin() {
		fmt.Println()
		fmt.Println("Administrator privileges required:")
		for _, reason := range result.adminReasons {
			fmt.Printf("  - %s\n", reason)
		}
		fmt.Println()
		fmt.Println("Re-run from an elevated (Administrator) prompt:")
		fmt.Println("  smirror selfupdate")
		os.Exit(ExitUpgrade)
	}

	// --- Phase 3: Confirmation ---
	if !autoYes {
		fmt.Printf("\nUpgrade smirror %s -> v%s? [y/N] ", version, latestVer)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		if line != "y" && line != "yes" {
			fmt.Println("Upgrade cancelled.")
			os.Exit(ExitUpgrade)
		}
	}

	// --- Phase 4: Download and verify ---
	fmt.Println()
	stageDir, err := downloadAndVerify(ctx, result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(ExitError)
	}
	defer os.RemoveAll(stageDir)

	stagedBinary := filepath.Join(stageDir, "smirror.exe")

	// Verify staged binary runs
	fmt.Print("Verifying staged binary... ")
	out, err := exec.Command(stagedBinary, "version").CombinedOutput()
	if err != nil {
		fmt.Printf("FAILED\n")
		fmt.Fprintf(os.Stderr, "Staged binary failed to run: %v\n%s\n", err, out)
		os.Exit(ExitError)
	}
	fmt.Printf("OK (%s)\n", strings.TrimSpace(string(out)))

	// --- Phase 5: Stop service if running ---
	wasRunning := false
	if result.svcRunning {
		fmt.Print("Stopping service... ")
		if err := service.Stop(); err != nil {
			fmt.Printf("FAILED: %v\n", err)
			fmt.Fprintln(os.Stderr, "Cannot stop service. Stop it manually and retry.")
			os.Exit(ExitError)
		}
		wasRunning = true
		fmt.Println("OK")
		// Brief wait for file handles to release
		time.Sleep(1 * time.Second)
	}

	// --- Phase 6: Atomic swap ---
	fmt.Print("Replacing binary... ")
	backupPath := result.binaryPath + ".bak"
	if err := swapBinary(result.binaryPath, stagedBinary, backupPath); err != nil {
		fmt.Printf("FAILED: %v\n", err)
		// Attempt rollback
		if _, statErr := os.Stat(backupPath); statErr == nil {
			if rbErr := os.Rename(backupPath, result.binaryPath); rbErr == nil {
				fmt.Fprintln(os.Stderr, "Rolled back to previous version.")
			}
		}
		if wasRunning {
			_ = service.Start()
		}
		os.Exit(ExitError)
	}
	fmt.Println("OK")

	// Post-swap verification
	fmt.Print("Post-upgrade verification... ")
	out, err = exec.Command(result.binaryPath, "version").CombinedOutput()
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		fmt.Fprintln(os.Stderr, "New binary failed verification. Rolling back...")
		os.Remove(result.binaryPath)
		if rbErr := os.Rename(backupPath, result.binaryPath); rbErr != nil {
			fmt.Fprintf(os.Stderr, "CRITICAL: rollback failed: %v\n", rbErr)
			fmt.Fprintf(os.Stderr, "Manual recovery: rename %s to %s\n", backupPath, result.binaryPath)
		} else {
			fmt.Fprintln(os.Stderr, "Rolled back to previous version.")
		}
		if wasRunning {
			_ = service.Start()
		}
		os.Exit(ExitError)
	}
	fmt.Printf("OK (%s)\n", strings.TrimSpace(string(out)))

	// Clean up backup
	os.Remove(backupPath)

	// --- Phase 7: Restart service ---
	if wasRunning || (result.svcInstalled && !result.svcRunning) {
		if wasRunning {
			fmt.Print("Restarting service... ")
			if err := service.Start(); err != nil {
				fmt.Printf("FAILED: %v\n", err)
				fmt.Fprintln(os.Stderr, "Service failed to start. Start manually: smirror service start")
			} else {
				fmt.Println("OK")
			}
		}
	}

	// --- Phase 8: rclone update (optional) ---
	if includeRclone {
		fmt.Println()
		updateRclone()
	}

	// --- Done ---
	fmt.Printf("\nsmirror upgraded to v%s successfully.\n", latestVer)

	// Record update time in state DB (best-effort)
	recordUpdateTime(configPath, latestVer)
}

// selfUpdatePreflight gathers all information needed to decide whether the
// update can proceed. Runs BEFORE any user confirmation or destructive action.
func selfUpdatePreflight(release *telemetry.ReleaseInfo) selfUpdateResult {
	result := selfUpdateResult{
		current: version,
		latest:  release,
	}

	// Locate current binary
	exePath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot determine executable path: %v\n", err)
		exePath = os.Args[0]
	}
	exePath, _ = filepath.EvalSymlinks(exePath)
	result.binaryPath = exePath
	result.binaryDir = filepath.Dir(exePath)

	// Test write access to binary directory
	testFile := filepath.Join(result.binaryDir, ".smirror-write-test")
	if f, err := os.Create(testFile); err == nil {
		f.Close()
		os.Remove(testFile)
		result.canWriteDir = true
	} else {
		result.canWriteDir = false
		result.needsAdmin = true
		result.adminReasons = append(result.adminReasons,
			fmt.Sprintf("Cannot write to binary directory: %s", result.binaryDir))
	}

	// Check service state
	result.svcInstalled, result.svcRunning = service.IsRunning()
	if result.svcRunning {
		result.needsAdmin = true
		result.adminReasons = append(result.adminReasons,
			"Service 'smirror' is running (needs stop/restart)")
	} else if result.svcInstalled {
		result.needsAdmin = true
		result.adminReasons = append(result.adminReasons,
			"Service 'smirror' is installed (needs restart after update)")
	}

	// Find assets
	result.zipAsset = telemetry.FindAsset(release.Assets, "windows_amd64.zip")
	result.checksumAsset = telemetry.FindAsset(release.Assets, "checksums.txt")

	return result
}

// downloadAndVerify downloads the release zip, verifies its checksum, and
// extracts smirror.exe to a staging directory. Returns the staging dir path.
func downloadAndVerify(ctx context.Context, result selfUpdateResult) (string, error) {
	stageDir, err := os.MkdirTemp("", "smirror-selfupdate-*")
	if err != nil {
		return "", fmt.Errorf("creating staging directory: %w", err)
	}

	// Download zip
	zipPath := filepath.Join(stageDir, result.zipAsset.Name)
	fmt.Printf("Downloading %s... ", result.zipAsset.Name)
	if err := downloadFile(ctx, result.zipAsset.BrowserDownloadURL, zipPath); err != nil {
		return stageDir, fmt.Errorf("downloading release: %w", err)
	}

	zipInfo, _ := os.Stat(zipPath)
	fmt.Printf("OK (%.1f MB)\n", float64(zipInfo.Size())/(1024*1024))

	// Verify checksum if available
	if result.checksumAsset != nil {
		fmt.Print("Verifying checksum... ")
		checksumPath := filepath.Join(stageDir, "checksums.txt")
		if err := downloadFile(ctx, result.checksumAsset.BrowserDownloadURL, checksumPath); err != nil {
			fmt.Printf("SKIP (download failed: %v)\n", err)
		} else {
			if err := verifyChecksum(zipPath, checksumPath, result.zipAsset.Name); err != nil {
				return stageDir, fmt.Errorf("checksum verification failed: %w", err)
			}
			fmt.Println("OK (SHA256 match)")
		}
	}

	// Extract smirror.exe from zip
	fmt.Print("Extracting smirror.exe... ")
	if err := extractFromZip(zipPath, "smirror.exe", filepath.Join(stageDir, "smirror.exe")); err != nil {
		return stageDir, fmt.Errorf("extracting binary: %w", err)
	}
	fmt.Println("OK")

	return stageDir, nil
}

// downloadFile downloads a URL to a local file path.
func downloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("smirror/%s", version))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

// verifyChecksum checks the SHA256 of zipPath against the expected hash in
// checksumPath (GoReleaser format: "<hash>  <filename>").
func verifyChecksum(zipPath, checksumPath, expectedName string) error {
	// Compute SHA256 of zip
	f, err := os.Open(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing: %w", err)
	}
	actual := hex.EncodeToString(h.Sum(nil))

	// Parse checksums.txt
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<sha256>  <filename>" (two spaces)
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == expectedName {
			expected := strings.ToLower(parts[0])
			if actual != expected {
				return fmt.Errorf("SHA256 mismatch: expected %s, got %s", expected, actual)
			}
			return nil
		}
	}

	return fmt.Errorf("checksum for %s not found in checksums.txt", expectedName)
}

// extractFromZip extracts a single file from a zip archive.
func extractFromZip(zipPath, fileName, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	// SM-094: Cap extraction size to prevent decompression bombs.
	// 200 MB is ~5× the largest legitimate smirror.exe and rclone.exe combined.
	const maxExtractSize = 200 * 1024 * 1024

	for _, f := range r.File {
		// Match by base name (GoReleaser puts files at root or in a subdirectory)
		if filepath.Base(f.Name) == fileName && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			out, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer out.Close()

			written, err := io.Copy(out, io.LimitReader(rc, maxExtractSize+1))
			if err != nil {
				return err
			}
			if written > maxExtractSize {
				return fmt.Errorf("archive entry %q exceeds %d bytes (potential decompression bomb)", fileName, maxExtractSize)
			}
			return nil
		}
	}

	return fmt.Errorf("%s not found in zip archive", fileName)
}

// swapBinary replaces the current binary with the staged one.
// 1. Rename current -> backup
// 2. Copy staged -> current (copy, not rename, because they may be on different volumes)
func swapBinary(currentPath, stagedPath, backupPath string) error {
	// Remove any leftover backup from a previous failed attempt
	os.Remove(backupPath)

	// Rename current binary to backup
	if err := os.Rename(currentPath, backupPath); err != nil {
		return fmt.Errorf("backing up current binary: %w", err)
	}

	// Copy staged binary to install location
	src, err := os.Open(stagedPath)
	if err != nil {
		return fmt.Errorf("opening staged binary: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(currentPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("creating new binary: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("writing new binary: %w", err)
	}

	return nil
}

// updateRclone attempts to update rclone using rclone's own selfupdate,
// with a direct download fallback.
func updateRclone() {
	fmt.Println("Checking rclone update...")
	info, err := rclone.Detect("")
	if err != nil {
		fmt.Printf("  rclone not found: %v\n", err)
		fmt.Println("  Skipping rclone update.")
		return
	}
	fmt.Printf("  Current rclone: %s (%s)\n", info.Version.String(), info.Path)

	// Try rclone selfupdate (available since rclone v1.55)
	fmt.Print("  Running rclone selfupdate... ")
	cmd := exec.Command(info.Path, "selfupdate")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		fmt.Printf("FAILED: %v\n", err)
		if output != "" {
			fmt.Printf("  %s\n", output)
		}
		fmt.Println("  You can update rclone manually: winget upgrade Rclone.Rclone")
		fmt.Println("  Or: rclone selfupdate")
		return
	}
	fmt.Println("OK")
	if output != "" {
		// Print rclone's output indented
		for _, line := range strings.Split(output, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
}

// recordUpdateTime writes the update timestamp to the state DB for
// rate-limiting startup update checks.
func recordUpdateTime(configPath, newVersion string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		return
	}
	defer st.Close()
	_ = st.SetMeta("last_update_check", time.Now().UTC().Format(time.RFC3339))
	_ = st.SetMeta("last_selfupdate", newVersion)
}

// checkForUpdateOnStartup performs a non-blocking update check and prints
// a one-line notice if a newer version is available. Rate-limited to once
// per 24 hours via state DB.
func checkForUpdateOnStartup(configPath string) {
	// Load config to access state DB
	cfg, err := config.Load(configPath)
	if err != nil {
		return
	}
	st, err := state.Open(cfg.StateDB)
	if err != nil {
		return
	}
	defer st.Close()

	// Rate limit: once per 24 hours
	if last, err := st.GetMeta("last_update_check"); err == nil && last != "" {
		if t, err := time.Parse(time.RFC3339, last); err == nil {
			if time.Since(t) < 24*time.Hour {
				return
			}
		}
	}

	// Background check with tight timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := telemetry.NewClient("", version)
	release, err := client.CheckForUpdate(ctx)
	if err != nil || release == nil {
		return
	}

	// Record check time regardless of result
	_ = st.SetMeta("last_update_check", time.Now().UTC().Format(time.RFC3339))

	latestVer := strings.TrimPrefix(release.TagName, "v")
	if telemetry.CompareVersions(version, latestVer) < 0 {
		fmt.Fprintf(os.Stderr, "Note: smirror v%s is available (current: %s). Run: smirror selfupdate\n\n", latestVer, version)
	}
}
