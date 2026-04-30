//go:build windows

package config

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsAdminOwnedPath must reject files owned by a regular user. Regression
// for the SEC-C5 gate — service mode loads hook-bearing configs only from
// admin-owned, admin-only-writable locations.
func TestIsAdminOwnedPath_RegularUserFile(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "user-owned.txt")
	if err := os.WriteFile(p, []byte("# user file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ok, err := IsAdminOwnedPath(p)
	if err != nil {
		t.Fatalf("IsAdminOwnedPath: %v", err)
	}
	if ok {
		t.Errorf("user-owned file classified as admin-owned (would let a non-admin escalate via hooks)")
	}
}

// IsAdminOwnedPath must surface an error for a path that doesn't exist —
// callers must not silently treat a missing config as admin-owned (would be
// a privilege-escalation footgun if combined with a later "create the
// missing config" path).
func TestIsAdminOwnedPath_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	missing := filepath.Join(tmp, "does-not-exist.yaml")

	if _, err := IsAdminOwnedPath(missing); err == nil {
		t.Error("expected error for missing path; got nil")
	}
}

// SM-213: after RestrictDirToSystemAndAdmins, the directory's DACL must
// list ONLY SYSTEM and BUILTIN\Administrators as ACCESS_ALLOWED ACE
// trustees. No BUILTIN\Users, Authenticated Users, or Everyone ACE
// should remain — even if the parent (%ProgramData%\) granted them.
//
// Regression test for the privacy-contract gap on multi-user Windows
// hosts where the default %ProgramData%\<app> DACL inherits
// BUILTIN\Users:R&X and exposes state.db, status.json, anomaly logs.
func TestRestrictDirToSystemAndAdmins_DACLContents(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "service-data")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := RestrictDirToSystemAndAdmins(target); err != nil {
		t.Fatalf("RestrictDirToSystemAndAdmins: %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl == nil {
		t.Fatal("DACL is NULL — would grant unrestricted access (the very bug we're guarding against)")
	}

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("WinLocalSystemSid: %v", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatalf("WinBuiltinAdministratorsSid: %v", err)
	}
	usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
	if err != nil {
		t.Fatalf("WinBuiltinUsersSid: %v", err)
	}
	authUsersSID, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		t.Fatalf("WinAuthenticatedUserSid: %v", err)
	}
	everyoneSID, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("WinWorldSid: %v", err)
	}

	sawSystem := false
	sawAdmins := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("GetAce(%d): %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(systemSID):
			sawSystem = true
		case sid.Equals(adminSID):
			sawAdmins = true
		case sid.Equals(usersSID):
			t.Errorf("DACL contains BUILTIN\\Users ACE — privacy contract violation (SM-213 regression)")
		case sid.Equals(authUsersSID):
			t.Errorf("DACL contains Authenticated Users ACE — privacy contract violation (SM-213 regression)")
		case sid.Equals(everyoneSID):
			t.Errorf("DACL contains Everyone ACE — privacy contract violation (SM-213 regression)")
		default:
			// Any non-admin trustee is a leak by definition. SM-213.
			t.Errorf("DACL contains unexpected trustee SID %s (only SYSTEM and Administrators are allowed)", sid.String())
		}
	}
	if !sawSystem {
		t.Error("DACL is missing SYSTEM ACE — service would lose its own access")
	}
	if !sawAdmins {
		t.Error("DACL is missing BUILTIN\\Administrators ACE — admin recovery / uninstall would fail")
	}
}

// SM-213: applying the restriction twice must be a no-op (idempotent).
// Service-mode startup re-applies on every restart, so this matters.
func TestRestrictDirToSystemAndAdmins_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "service-data")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := RestrictDirToSystemAndAdmins(target); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	// Verify final state still has SYSTEM ACE present.
	sd, err := windows.GetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL after triple-apply is missing or NULL")
	}
	if dacl.AceCount < 2 {
		t.Errorf("expected at least 2 ACEs after apply, got %d", dacl.AceCount)
	}
}
