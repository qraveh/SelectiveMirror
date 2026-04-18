//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// IsAdminOwnedPath reports whether the file/directory at path is owned by a
// built-in administrative principal (LocalSystem or Administrators).
// SEC-C5: used to refuse loading a hook-bearing config from user-writable
// locations in service mode — otherwise a non-admin user can escalate to
// LocalSystem by injecting a hook into their own config.yaml.
func IsAdminOwnedPath(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
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
	trustedInstallerSID, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464")
	if err == nil && owner.Equals(trustedInstallerSID) {
		return true, nil
	}

	return owner.Equals(systemSID) || owner.Equals(adminSID), nil
}
