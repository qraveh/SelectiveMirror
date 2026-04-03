//go:build windows

package main

import (
	"golang.org/x/sys/windows"
)

// isAdmin reports whether the current process is running with elevated
// (Administrator) privileges on Windows.
func isAdmin() bool {
	var sid *windows.SID
	// Well-known SID for the built-in Administrators group (S-1-5-32-544).
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	member, err := windows.Token(0).IsMember(sid)
	if err != nil {
		return false
	}
	return member
}
