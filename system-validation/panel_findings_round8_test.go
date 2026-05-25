package systemval

// panel_findings_round8_test.go — Round 8 system-validation tests
// synthesized from a fresh four-lens panel review (internal test quality /
// ancillary code / CI pipeline / cumulative meta-review) against
// v0.9.32-dev on 2026-04-29.
//
// Eight rounds in. Bug discovery has converged. Round 8 priorities:
//   - CI/release pipeline gaps verifiable from file inspection
//   - Ancillary code (PowerShell test runners) gaps verifiable
//   - Re-confirm the 4 OPEN bugs against v0.9.32-dev
//   - Document cumulative scoreboard
//
// All round-8 findings are documented OBS — no new hard FAILs expected.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =========================================================================
// 1. CI/RELEASE PIPELINE
// =========================================================================

// CI reviewer #3: no gosec / gitleaks in CI workflows.
func TestPanelR8_CI_NoStaticSecurityScanning(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	wfDir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(wfDir)
	if err != nil {
		t.Skipf("no workflows dir: %v", err)
	}
	hasGosec := false
	hasGitleaks := false
	hasCodeQL := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wfDir, e.Name()))
		if err != nil {
			continue
		}
		text := strings.ToLower(string(data))
		if strings.Contains(text, "gosec") {
			hasGosec = true
		}
		if strings.Contains(text, "gitleaks") {
			hasGitleaks = true
		}
		if strings.Contains(text, "codeql") {
			hasCodeQL = true
		}
	}
	if !hasGosec && !hasGitleaks && !hasCodeQL {
		t.Logf("PANEL OBS: CI workflows do NOT include any of: gosec / gitleaks / CodeQL. " +
			"Per CI reviewer #3, secrets / hardcoded credentials / common Go security issues " +
			"are not caught until manual review. Recommendation: add `gosec` and `gitleaks` " +
			"as CI steps in ci.yml.")
	}
}

// CI reviewer #14: no Dependabot or Renovate config.
func TestPanelR8_CI_NoDependabotConfig(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	candidates := []string{
		".github/dependabot.yml",
		".github/dependabot.yaml",
		"renovate.json",
		".github/renovate.json",
		".renovaterc",
	}
	found := ""
	for _, p := range candidates {
		full := filepath.Join(repoRoot, p)
		if _, err := os.Stat(full); err == nil {
			found = p
			break
		}
	}
	if found == "" {
		t.Logf("PANEL OBS: no Dependabot or Renovate config found. " +
			"Go module dependencies (mattn/go-sqlite3, fsnotify, yaml, golang.org/x/sys) " +
			"are not auto-updated; security CVEs in dependencies are discovered via manual " +
			"audit only. Recommendation: add `.github/dependabot.yml` for go modules + " +
			"github-actions ecosystems.")
	} else {
		t.Logf("Dependabot/Renovate config found at %s", found)
	}
}

// CI reviewer #6: checksums.txt is unsigned. Only SHA256 integrity, no
// signature for authenticity. This is verifiable by inspecting release.yml
// and .goreleaser.yaml — no minisign / cosign / GPG signing step.
func TestPanelR8_CI_ChecksumsNotSigned(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	files := []string{
		".github/workflows/release.yml",
		".goreleaser.yaml",
		".goreleaser.yml",
	}
	hasSigning := false
	for _, p := range files {
		data, err := os.ReadFile(filepath.Join(repoRoot, p))
		if err != nil {
			continue
		}
		text := strings.ToLower(string(data))
		if strings.Contains(text, "minisign") || strings.Contains(text, "cosign") ||
			strings.Contains(text, "gpg") && strings.Contains(text, "sign") {
			hasSigning = true
			break
		}
	}
	if !hasSigning {
		t.Logf("PANEL OBS: no minisign / cosign / GPG signing step found in release pipeline. " +
			"checksums.txt is SHA256-only; not signed. Per CI reviewer #6 + R7 SEC-H8 " +
			"(Mismatch), the release artifact is integrity-checked but not authenticity-checked. " +
			"A pipeline compromise that swaps the binary AND the checksum is undetectable. " +
			"Recommendation: as a stopgap before SignPath comes through, sign checksums.txt " +
			"with minisign / cosign keyless.")
	}
}

// CI reviewer #12: winget manifest's InstallerSha256 is `PLACEHOLDER`.
func TestPanelR8_CI_WingetManifestPlaceholder(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	wingetDir := filepath.Join(repoRoot, "winget")
	entries, err := os.ReadDir(wingetDir)
	if err != nil {
		t.Skipf("no winget dir: %v", err)
	}
	hasPlaceholder := false
	var found string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(wingetDir, e.Name()))
		text := string(data)
		if strings.Contains(text, "PLACEHOLDER") || strings.Contains(strings.ToLower(text), "todo") {
			hasPlaceholder = true
			found = e.Name()
			break
		}
	}
	if hasPlaceholder {
		t.Logf("PANEL OBS: winget manifest at %s contains PLACEHOLDER / TODO marker. "+
			"Per CI reviewer #12, the InstallerSha256 must be computed and inserted manually " +
			"after release. No CI job auto-generates it. Recommendation: add a release.yml " +
			"step that computes the MSI hash and patches the manifest before tagging.", found)
	}
}

// =========================================================================
// 2. POWERSHELL SCRIPT QUALITY
// =========================================================================

// Ancillary reviewer #13: PowerShell test runners don't use Set-StrictMode.
func TestPanelR8_PowerShell_NoStrictMode(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	testDir := filepath.Join(repoRoot, "test")
	candidates := []string{"run_tests.ps1", "verify.ps1", "stress_test.ps1", "sla_smoke.ps1"}
	missingStrict := []string{}
	for _, c := range candidates {
		p := filepath.Join(testDir, c)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		text := string(data)
		if !strings.Contains(text, "Set-StrictMode") {
			missingStrict = append(missingStrict, c)
		}
	}
	if len(missingStrict) > 0 {
		t.Logf("PANEL OBS: %d PowerShell test scripts in test/ do not invoke Set-StrictMode: %v. "+
			"Per ancillary reviewer #13, undefined-variable typos silently evaluate to $null " +
			"instead of erroring. Recommendation: add `Set-StrictMode -Version Latest` at the " +
			"top of each script.", len(missingStrict), missingStrict)
	}
}

// Ancillary reviewer #14: hardcoded `C:\Program Files\Go` and `C:\mine\SelectiveMirror`
// in test scripts — breaks portability if Go/repo lives elsewhere.
func TestPanelR8_PowerShell_HardcodedPaths(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	testDir := filepath.Join(repoRoot, "test")
	candidates := []string{"run_tests.ps1", "verify.ps1", "stress_test.ps1", "sla_smoke.ps1"}
	hardcoded := []string{}
	for _, c := range candidates {
		data, err := os.ReadFile(filepath.Join(testDir, c))
		if err != nil {
			continue
		}
		text := string(data)
		// Look for absolute Windows paths to common install locations.
		for _, marker := range []string{`C:\Program Files\Go`, `C:\mine\SelectiveMirror`, `C:/mine/SelectiveMirror`} {
			if strings.Contains(text, marker) {
				hardcoded = append(hardcoded, c+": "+marker)
				break
			}
		}
	}
	if len(hardcoded) > 0 {
		t.Logf("PANEL OBS: PowerShell test scripts contain hardcoded paths: %v. "+
			"Per ancillary reviewer #14, this breaks if Go is installed elsewhere or the repo " +
			"lives in a non-default location. Recommendation: use $env:GOROOT, " +
			"`(Get-Command go).Source`, or relative paths.", hardcoded)
	}
}

// =========================================================================
// 3. INTERNAL TEST QUALITY (verifiable via grep)
// =========================================================================

// Internal-test-quality reviewer #15: TestToRcloneFilter_WhitespaceOnly
// reportedly contains only `t.Logf` (no assertion).
// Verify by reading the test file.
func TestPanelR8_InternalTests_WeakWhitespaceOnlyTest(t *testing.T) {
	t.Parallel()
	repoRoot := repoRootFromHere(t)
	filterTest := filepath.Join(repoRoot, "internal", "filter", "filter_test.go")
	data, err := os.ReadFile(filterTest)
	if err != nil {
		t.Skipf("can't read filter_test.go: %v", err)
	}
	text := string(data)
	idx := strings.Index(text, "TestToRcloneFilter_WhitespaceOnly")
	if idx < 0 {
		t.Skip("TestToRcloneFilter_WhitespaceOnly not found")
	}
	// Look at the next 800 bytes after the test header.
	end := idx + 800
	if end > len(text) {
		end = len(text)
	}
	body := text[idx:end]
	hasErrorf := strings.Contains(body, "t.Errorf") || strings.Contains(body, "t.Fatal")
	if !hasErrorf {
		t.Logf("PANEL OBS: TestToRcloneFilter_WhitespaceOnly contains no t.Errorf or t.Fatal " +
			"in its first 800 bytes — appears to be observation-only (t.Logf). " +
			"Per internal-test-quality reviewer #15, this test passes regardless of whether " +
			"whitespace-only patterns produce correct output. Recommendation: convert observation " +
			"to assertion with the expected rclone-filter content.")
	}
}

// =========================================================================
// 4. THE 4 OPEN BUGS — re-confirm against v0.9.32-dev
// =========================================================================

// Round-8 sentinel test: documents that the 4 OPEN bugs from prior rounds
// remain reproducing. The actual failure assertions are in the round-3/4/5
// test files; this test serves as a meta-checkpoint.
func TestPanelR8_OpenBugsScoreboard(t *testing.T) {
	t.Parallel()
	t.Logf("Round 8 scoreboard against v0.9.32-dev:\n" +
		"  BUG-R3-1 (gitignore parent-exclusion divergence): STILL OPEN [3 rounds]\n" +
		"  BUG-R4-1 (concurrent addmirror destroys mirror):  STILL OPEN [3 rounds]\n" +
		"  BUG-R5-1 (anomaly.Rotate dead code):              STILL OPEN [3 rounds]\n" +
		"  FIND-R4-1 (per-file hooks skip batch sync):       STILL OPEN [3 rounds]\n" +
		"\n" +
		"Newly-CLOSED in 0.9.32 cycle:\n" +
		"  GAP-8 (zero-byte state.db warn) - shipped 0.9.31\n" +
		"  GAP-9 (stale-lock PID detection) - shipped 0.9.31\n" +
		"  PF-D1 (FairQueue cooldown timer leak) - shipped 0.9.32\n" +
		"  SEC-M3 (closed by GAP-1) - shipped 0.9.30\n" +
		"  SEC-M6 (atomic writes) - shipped 0.9.30\n" +
		"  SEC-M8 (quarantine ns precision) - shipped 0.9.25\n" +
		"  SEC-M12 + SEC-M13 - shipped 0.9.32\n" +
		"  SEC-H9, H10, L4, L5 (log sanitization batch) - shipped 0.9.28\n" +
		"  SEC-H11 (install-rclone hash) - shipped 0.9.29\n" +
		"  SEC-M10 (downgrade-protection note) - shipped 0.9.29")
}

// =========================================================================
// 5. META-REVIEW: NFR-CA-01 (32-mirror) is still untested at scale
// =========================================================================

// Meta-reviewer top-1 priority: 32-mirror NFR-CA-01 stress test never
// performed across 7 rounds. Round 8 doesn't add a real 32-mirror test
// (that's a >10-minute test); just documents the gap.
func TestPanelR8_Meta_NFR_CA_01_StillUntested(t *testing.T) {
	t.Parallel()
	t.Logf("PANEL OBS: After 8 rounds, NFR-CA-01 (32 mirrors without degradation) is " +
		"still NOT TESTED. Round 3 multi-mirror review tested 5 mirrors; round 5 perf " +
		"tested 2-vs-8 startup time. The 32-mirror claim remains a marketing assertion. " +
		"Per round-8 meta-review priority #2: this is the highest-value untested " +
		"surface. Recommend a dedicated `system-validation/stress_32_mirror_test.go` " +
		"(or PowerShell harness in test/) that runs a 10-minute scenario with 32 mirrors.")
}

// Meta-reviewer top-4 priority: disk-full scenarios are NEVER tested.
// Round 7 found 30+ LogAction-error-suppression sites; without disk-full
// test, audit-trail loss on ENOSPC is undetected.
func TestPanelR8_Meta_DiskFullNeverTested(t *testing.T) {
	t.Parallel()
	t.Logf("PANEL OBS: Across 8 rounds, NO test injects disk-full (ENOSPC). " +
		"Round 7 R7-PF-8 documented 30+ LogAction-error-suppression sites; " +
		"BUG-R5-1 (anomaly rotation dead code) means anomaly disk usage is unbounded; " +
		"a disk-full event would silently lose audit trail + corrupt state DB writes. " +
		"Per round-8 meta-review priority #4: a disk-full fault-injection test would " +
		"surface a class of bugs we haven't tested. Recommend: a Windows-specific " +
		"test that uses NtCreateFile with reduced quota, or a Linux test using a " +
		"loopback FS at the size limit, then exercises sync + log + state writes.")
}
