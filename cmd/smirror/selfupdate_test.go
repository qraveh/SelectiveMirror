package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/qraveh/SelectiveMirror/internal/telemetry"
)

// --- selfupdate flag parsing ---

func TestParseSelfUpdateFlags_CheckOnly(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--check"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.checkOnly {
		t.Error("--check should set checkOnly")
	}
	if f.whatsNew || f.autoYes || f.includeRclone {
		t.Error("only checkOnly should be set")
	}
}

func TestParseSelfUpdateFlags_WhatsNew(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--whatsnew"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.whatsNew {
		t.Error("--whatsnew should set whatsNew")
	}
	if f.checkOnly || f.autoYes || f.includeRclone {
		t.Error("only whatsNew should be set")
	}
}

func TestParseSelfUpdateFlags_YesLong(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--yes"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.autoYes {
		t.Error("--yes should set autoYes")
	}
}

func TestParseSelfUpdateFlags_YesShort(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"-y"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.autoYes {
		t.Error("-y should set autoYes")
	}
}

func TestParseSelfUpdateFlags_IncludeRclone(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--include-rclone"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.includeRclone {
		t.Error("--include-rclone should set includeRclone")
	}
	if f.checkOnly || f.whatsNew || f.autoYes {
		t.Error("only includeRclone should be set")
	}
}

func TestParseSelfUpdateFlags_Multiple(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--check", "--whatsnew", "--yes", "--include-rclone"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.checkOnly || !f.whatsNew || !f.autoYes || !f.includeRclone {
		t.Error("all flags should be set when all are passed")
	}
}

func TestParseSelfUpdateFlags_Empty(t *testing.T) {
	f, err := parseSelfUpdateFlags(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.checkOnly || f.whatsNew || f.autoYes || f.includeRclone {
		t.Error("no flags should be set for empty args")
	}
}

func TestParseSelfUpdateFlags_UnknownFlagErrors(t *testing.T) {
	_, err := parseSelfUpdateFlags([]string{"--unknown", "--check"})
	if err == nil {
		t.Error("unknown flag should return an error")
	}
}

func TestParseSelfUpdateFlags_NonFlagArgsIgnored(t *testing.T) {
	f, err := parseSelfUpdateFlags([]string{"--check", "bogus"})
	if err != nil {
		t.Fatalf("non-flag args should not error: %v", err)
	}
	if !f.checkOnly {
		t.Error("--check should be set even with positional args")
	}
}

// --- service uninstall flag parsing ---

func TestParseServiceUninstallFlags_Clean(t *testing.T) {
	f := parseServiceUninstallFlags([]string{"--clean"})
	if !f.clean {
		t.Error("--clean should set clean")
	}
	if f.autoYes {
		t.Error("autoYes should not be set")
	}
}

func TestParseServiceUninstallFlags_YesLong(t *testing.T) {
	f := parseServiceUninstallFlags([]string{"--yes"})
	if !f.autoYes {
		t.Error("--yes should set autoYes")
	}
	if f.clean {
		t.Error("clean should not be set")
	}
}

func TestParseServiceUninstallFlags_YesShort(t *testing.T) {
	f := parseServiceUninstallFlags([]string{"-y"})
	if !f.autoYes {
		t.Error("-y should set autoYes")
	}
}

func TestParseServiceUninstallFlags_CleanAndYes(t *testing.T) {
	f := parseServiceUninstallFlags([]string{"--clean", "--yes"})
	if !f.clean || !f.autoYes {
		t.Error("both clean and autoYes should be set")
	}
}

func TestParseServiceUninstallFlags_Empty(t *testing.T) {
	f := parseServiceUninstallFlags(nil)
	if f.clean || f.autoYes {
		t.Error("no flags should be set for empty args")
	}
}

func TestParseServiceUninstallFlags_UnknownIgnored(t *testing.T) {
	f := parseServiceUninstallFlags([]string{"--force", "--clean", "extra"})
	if !f.clean {
		t.Error("--clean should be set")
	}
	if f.autoYes {
		t.Error("autoYes should not be set by unknown args")
	}
}

// --- verifyChecksum ---

func TestVerifyChecksum_ValidMatch(t *testing.T) {
	dir := t.TempDir()

	// Create a file with known content
	content := []byte("hello smirror selfupdate")
	zipPath := filepath.Join(dir, "test.zip")
	if err := os.WriteFile(zipPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Compute its SHA256
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])

	// Write checksums.txt in GoReleaser format
	checksumPath := filepath.Join(dir, "checksums.txt")
	checksumContent := fmt.Sprintf("%s  test.zip\n%s  other.zip\n", hash, "aaaa")
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := verifyChecksum(zipPath, checksumPath, "test.zip"); err != nil {
		t.Errorf("valid checksum should pass: %v", err)
	}
}

func TestVerifyChecksum_Mismatch(t *testing.T) {
	dir := t.TempDir()

	zipPath := filepath.Join(dir, "test.zip")
	if err := os.WriteFile(zipPath, []byte("real content"), 0644); err != nil {
		t.Fatal(err)
	}

	checksumPath := filepath.Join(dir, "checksums.txt")
	checksumContent := "0000000000000000000000000000000000000000000000000000000000000000  test.zip\n"
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0644); err != nil {
		t.Fatal(err)
	}

	err := verifyChecksum(zipPath, checksumPath, "test.zip")
	if err == nil {
		t.Error("mismatched checksum should fail")
	}
	if err != nil && !contains(err.Error(), "mismatch") {
		t.Errorf("expected mismatch error, got: %v", err)
	}
}

func TestVerifyChecksum_FileNotInChecksums(t *testing.T) {
	dir := t.TempDir()

	zipPath := filepath.Join(dir, "test.zip")
	if err := os.WriteFile(zipPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	checksumPath := filepath.Join(dir, "checksums.txt")
	checksumContent := "abcd1234  other_file.zip\n"
	if err := os.WriteFile(checksumPath, []byte(checksumContent), 0644); err != nil {
		t.Fatal(err)
	}

	err := verifyChecksum(zipPath, checksumPath, "test.zip")
	if err == nil {
		t.Error("missing file in checksums should fail")
	}
	if err != nil && !contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestDownloadAndVerify_ChecksumDownloadFailureFailsClosed(t *testing.T) {
	zipBytes := selfUpdateTestZipBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release.zip":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		case "/checksums.txt":
			http.Error(w, "checksum temporarily unavailable", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	stageDir, err := downloadAndVerify(context.Background(), selfUpdateResult{
		zipAsset: &telemetry.Asset{
			Name:               "SelectiveMirror_1.0.0_windows_amd64.zip",
			BrowserDownloadURL: srv.URL + "/release.zip",
		},
		checksumAsset: &telemetry.Asset{
			Name:               "checksums.txt",
			BrowserDownloadURL: srv.URL + "/checksums.txt",
		},
	})
	if stageDir != "" {
		defer os.RemoveAll(stageDir)
	}
	if err == nil {
		t.Fatal("checksum asset was advertised but could not be downloaded; selfupdate should fail closed")
	}
}

func TestDownloadAndVerify_MissingChecksumAssetFailsClosed(t *testing.T) {
	zipBytes := selfUpdateTestZipBytes(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/release.zip" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBytes)
	}))
	defer srv.Close()

	stageDir, err := downloadAndVerify(context.Background(), selfUpdateResult{
		zipAsset: &telemetry.Asset{
			Name:               "SelectiveMirror_1.0.0_windows_amd64.zip",
			BrowserDownloadURL: srv.URL + "/release.zip",
		},
	})
	if stageDir != "" {
		defer os.RemoveAll(stageDir)
	}
	if err == nil {
		t.Fatal("release has no checksum asset; selfupdate should fail closed or require an explicit override")
	}
}

func TestDownloadAndVerify_AdvertisedOversizeZipFailsClosed(t *testing.T) {
	zipBytes := selfUpdateTestZipBytes(t)
	hash := sha256.Sum256(zipBytes)
	checksum := fmt.Sprintf("%x  SelectiveMirror_1.0.0_windows_amd64.zip\n", hash)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release.zip":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		case "/checksums.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(checksum))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	stageDir, err := downloadAndVerify(context.Background(), selfUpdateResult{
		zipAsset: &telemetry.Asset{
			Name:               "SelectiveMirror_1.0.0_windows_amd64.zip",
			Size:               500 * 1024 * 1024,
			BrowserDownloadURL: srv.URL + "/release.zip",
		},
		checksumAsset: &telemetry.Asset{
			Name:               "checksums.txt",
			BrowserDownloadURL: srv.URL + "/checksums.txt",
		},
	})
	if stageDir != "" {
		defer os.RemoveAll(stageDir)
	}
	if err == nil {
		t.Fatal("release asset advertises an oversized ZIP; selfupdate should fail before download/extract")
	}
}

// --- extractFromZip ---

func TestExtractFromZip_RootLevel(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	destPath := filepath.Join(dir, "extracted.exe")

	// Create a zip with smirror.exe at root
	createTestZip(t, zipPath, map[string]string{
		"smirror.exe": "binary content here",
		"README.md":   "readme",
	})

	if err := extractFromZip(zipPath, "smirror.exe", destPath); err != nil {
		t.Fatalf("extract should succeed: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "binary content here" {
		t.Errorf("extracted content mismatch: %q", data)
	}
}

func TestExtractFromZip_InSubdirectory(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")
	destPath := filepath.Join(dir, "extracted.exe")

	// GoReleaser sometimes nests files in a subdirectory
	createTestZip(t, zipPath, map[string]string{
		"SelectiveMirror_v1.0.0_windows_amd64/smirror.exe": "nested binary",
		"SelectiveMirror_v1.0.0_windows_amd64/README.md":   "readme",
	})

	if err := extractFromZip(zipPath, "smirror.exe", destPath); err != nil {
		t.Fatalf("extract from subdirectory should succeed: %v", err)
	}

	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "nested binary" {
		t.Errorf("extracted content mismatch: %q", data)
	}
}

func TestExtractFromZip_FileNotFound(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "test.zip")

	createTestZip(t, zipPath, map[string]string{
		"other.exe": "not what we want",
	})

	err := extractFromZip(zipPath, "smirror.exe", filepath.Join(dir, "out.exe"))
	if err == nil {
		t.Error("should fail when target file is not in zip")
	}
	if err != nil && !contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// --- swapBinary ---

func TestSwapBinary_Success(t *testing.T) {
	dir := t.TempDir()

	current := filepath.Join(dir, "smirror.exe")
	staged := filepath.Join(dir, "staged.exe")
	backup := filepath.Join(dir, "smirror.exe.bak")

	os.WriteFile(current, []byte("old version"), 0755)
	os.WriteFile(staged, []byte("new version"), 0755)

	if err := swapBinary(current, staged, backup); err != nil {
		t.Fatalf("swap should succeed: %v", err)
	}

	// Current should have new content
	data, _ := os.ReadFile(current)
	if string(data) != "new version" {
		t.Errorf("current binary should have new content, got: %q", data)
	}

	// Backup should have old content
	data, _ = os.ReadFile(backup)
	if string(data) != "old version" {
		t.Errorf("backup should have old content, got: %q", data)
	}
}

func TestSwapBinary_CleansUpOldBackup(t *testing.T) {
	dir := t.TempDir()

	current := filepath.Join(dir, "smirror.exe")
	staged := filepath.Join(dir, "staged.exe")
	backup := filepath.Join(dir, "smirror.exe.bak")

	os.WriteFile(current, []byte("old"), 0755)
	os.WriteFile(staged, []byte("new"), 0755)
	os.WriteFile(backup, []byte("stale backup"), 0755)

	if err := swapBinary(current, staged, backup); err != nil {
		t.Fatalf("swap should succeed: %v", err)
	}

	// Backup should be the OLD current, not the stale one
	data, _ := os.ReadFile(backup)
	if string(data) != "old" {
		t.Errorf("backup should be the old binary, got: %q", data)
	}
}

func TestSwapBinary_CurrentMissing(t *testing.T) {
	dir := t.TempDir()

	current := filepath.Join(dir, "nonexistent.exe")
	staged := filepath.Join(dir, "staged.exe")
	backup := filepath.Join(dir, "backup.exe")

	os.WriteFile(staged, []byte("new"), 0755)

	err := swapBinary(current, staged, backup)
	if err == nil {
		t.Error("should fail if current binary doesn't exist")
	}
}

// --- dry-test lock detection ---

func TestCleanDryTest_UnlockedFiles(t *testing.T) {
	dir := t.TempDir()

	// Create some files
	files := []string{"config.yaml", "state.db", "selectivemirror.log"}
	for _, name := range files {
		os.WriteFile(filepath.Join(dir, name), []byte("data"), 0644)
	}

	// All should be openable for RDWR (no locks)
	blocked := dryTestRemovability(dir, files)
	if len(blocked) > 0 {
		t.Errorf("expected no blocked files, got: %v", blocked)
	}
}

func TestCleanDryTest_LockedFile(t *testing.T) {
	dir := t.TempDir()

	lockName := "state.db"
	lockPath := filepath.Join(dir, lockName)
	os.WriteFile(lockPath, []byte("data"), 0644)

	// Hold the file open with exclusive access (no sharing) via platform helper.
	// This simulates another process holding the file open.
	fh, err := openExclusiveForTest(lockPath)
	if err != nil {
		t.Skipf("cannot open file exclusively on this platform: %v", err)
	}
	defer fh.Close()

	blocked := dryTestRemovability(dir, []string{lockName})
	if len(blocked) == 0 {
		t.Error("expected locked file to be reported as blocked")
	}
}

func TestCleanDryTest_NonexistentFileSkipped(t *testing.T) {
	dir := t.TempDir()

	// File doesn't exist — should not appear in blocked list
	blocked := dryTestRemovability(dir, []string{"nonexistent.yaml"})
	if len(blocked) > 0 {
		t.Errorf("nonexistent files should be skipped, got: %v", blocked)
	}
}

// --- FindAsset (telemetry helper) ---

func TestFindAsset_Match(t *testing.T) {
	assets := []telemetry.Asset{
		{Name: "SelectiveMirror_1.0.0_linux_amd64.tar.gz"},
		{Name: "SelectiveMirror_1.0.0_windows_amd64.zip"},
		{Name: "checksums.txt"},
	}

	a := telemetry.FindAsset(assets, "windows_amd64.zip")
	if a == nil {
		t.Fatal("should find windows asset")
	}
	if a.Name != "SelectiveMirror_1.0.0_windows_amd64.zip" {
		t.Errorf("wrong asset: %s", a.Name)
	}
}

func TestFindAsset_CaseInsensitive(t *testing.T) {
	assets := []telemetry.Asset{
		{Name: "CHECKSUMS.TXT"},
	}
	a := telemetry.FindAsset(assets, "checksums.txt")
	if a == nil {
		t.Error("FindAsset should be case-insensitive")
	}
}

func TestFindAsset_NoMatch(t *testing.T) {
	assets := []telemetry.Asset{
		{Name: "something_else.tar.gz"},
	}
	a := telemetry.FindAsset(assets, "windows_amd64.zip")
	if a != nil {
		t.Error("should return nil when no asset matches")
	}
}

func TestFindAsset_Empty(t *testing.T) {
	a := telemetry.FindAsset(nil, "anything")
	if a != nil {
		t.Error("should return nil for empty assets")
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func selfUpdateTestZipBytes(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "SelectiveMirror_1.0.0_windows_amd64.zip")
	createTestZip(t, zipPath, map[string]string{
		"smirror.exe": "binary content",
	})
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	return zipBytes
}

func createTestZip(t *testing.T, zipPath string, files map[string]string) {
	t.Helper()
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
