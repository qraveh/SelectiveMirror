//go:build windows

package config

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// trustedInstallerSIDStr is the well-known SID for NT SERVICE\TrustedInstaller.
// Used by Windows Update / WinSxS to own protected system files.
const trustedInstallerSIDStr = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// Write-class access mask bits. A DACL ACE that grants any of these to a
// non-admin trustee defeats the SEC-C5 admin-owned-config gate. Values from
// winnt.h; not all are exposed by golang.org/x/sys/windows.
const (
	maskFileWriteData       = 0x00000002
	maskFileAppendData      = 0x00000004
	maskFileWriteEA         = 0x00000010
	maskFileWriteAttributes = 0x00000100
	maskDelete              = 0x00010000
	maskWriteDAC            = 0x00040000
	maskWriteOwner          = 0x00080000
	maskGenericAll          = 0x10000000
	maskGenericWrite        = 0x40000000

	writeAccessMask uint32 = maskFileWriteData |
		maskFileAppendData |
		maskFileWriteEA |
		maskFileWriteAttributes |
		maskDelete |
		maskWriteDAC |
		maskWriteOwner |
		maskGenericAll |
		maskGenericWrite
)

// IsAdminOwnedPath reports whether the file/directory at path is both:
//
//  1. owned by a built-in administrative principal (LocalSystem, BUILTIN\
//     Administrators, or TrustedInstaller), AND
//  2. ACL-protected such that no non-admin principal has write access.
//
// SEC-C5: used to refuse loading a hook-bearing config from user-writable
// locations in service mode — otherwise a non-admin user can escalate to
// LocalSystem by injecting a hook into a config they can edit.
//
// SEC-H6 hardening: the previous implementation checked the owner SID only.
// An admin-owned file with a DACL granting `Authenticated Users:Modify`
// would pass the owner check while still being writable by any user — same
// LPE outcome. This implementation also walks the DACL and rejects the
// path if any ACCESS_ALLOWED ACE grants write-class permissions to a
// non-admin trustee.
func IsAdminOwnedPath(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return false, fmt.Errorf("GetNamedSecurityInfo(%q): %w", path, err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		return false, fmt.Errorf("Owner(%q): %w", path, err)
	}

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, fmt.Errorf("WinLocalSystemSid: %w", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, fmt.Errorf("WinBuiltinAdministratorsSid: %w", err)
	}
	trustedInstallerSID, _ := windows.StringToSid(trustedInstallerSIDStr)

	ownerOK := owner.Equals(systemSID) ||
		owner.Equals(adminSID) ||
		(trustedInstallerSID != nil && owner.Equals(trustedInstallerSID))
	if !ownerOK {
		return false, nil
	}

	// Walk the DACL. Any ACCESS_ALLOWED ACE that grants any write-class
	// permission to a non-admin trustee fails the gate. A NULL DACL grants
	// everyone full access; that is also a fail.
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, fmt.Errorf("DACL(%q): %w", path, err)
	}
	if dacl == nil {
		// NULL DACL = unrestricted. The audit treats this as user-writable.
		return false, nil
	}

	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, fmt.Errorf("GetAce(%d): %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		// Trustee SID is laid out starting at the SidStart field.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))

		if uint32(ace.Mask)&writeAccessMask == 0 {
			continue // ACE doesn't grant write — irrelevant
		}
		if sid.Equals(systemSID) || sid.Equals(adminSID) {
			continue
		}
		if trustedInstallerSID != nil && sid.Equals(trustedInstallerSID) {
			continue
		}
		// Non-admin trustee with write access. Reject.
		return false, nil
	}

	return true, nil
}
