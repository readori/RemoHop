//go:build windows

package config

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateSIDs are the only accounts allowed into the private directory
// dir: the system, the Administrators group (which could take ownership
// anyway) and, usually, the user running this process.
//
// Not that user when this process runs elevated and dir is outside the
// user's profile: that is a machine-wide directory such as the Windows
// service's state under %ProgramData%, written by an administrator step.
// Letting the administrator's own account in there would let every
// program that account runs without elevation in too, and so change what
// the service, running as LocalSystem, reads and runs.
func privateSIDs(dir string) ([]*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	if token.IsElevated() && !inProfile(dir, os.Getenv("USERPROFILE")) {
		return []*windows.SID{system, admins}, nil
	}
	return []*windows.SID{user.User.Sid, system, admins}, nil
}

// MakePrivate restricts dir, and everything created in it from now on, to
// the current user (plus SYSTEM and Administrators; see privateSIDs for when
// the user is left out).
//
// Windows has no permission bits; who may read a file is decided by its
// access-control list, which a new file inherits from its directory. The
// default config directory under %AppData% inherits a user-only list from
// the profile, but a directory elsewhere does not: under %ProgramData%,
// where a service instance keeps its state, new directories are readable
// by every user of the machine. So the list is set explicitly, protected
// from inheritance from above and inheritable downwards, wherever key
// material is about to be stored.
func MakePrivate(dir string) error {
	sids, err := privateSIDs(dir)
	if err != nil {
		return err
	}
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// IsPrivate reports whether path (a file or directory) is accessible only
// to the accounts MakePrivate allows.
func IsPrivate(path string) (bool, error) {
	sids, err := privateSIDs(path)
	if err != nil {
		return false, err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return false, err
	}
	if dacl == nil {
		return false, nil // no list at all: everyone has full access
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue // a deny entry grants nobody anything
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		allowed := false
		for _, ok := range sids {
			allowed = allowed || sid.Equals(ok)
		}
		if !allowed {
			return false, nil
		}
	}
	return true, nil
}
