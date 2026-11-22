//go:build windows

package appdir

import "golang.org/x/sys/windows"

// fileAllAccess is FILE_ALL_ACCESS. A generic right such as GENERIC_ALL on an
// inheritable ACE makes Windows split it into an effective ACE and an
// inherit-only one; concrete rights keep a single ACE.
const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

// restrict replaces the DACL of path with a protected one (no inheritance from
// the parent) that grants full control to the current user only, and that
// children inherit.
func restrict(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: fileAllAccess,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}

// RestrictToUser makes path (a file or directory) accessible to the current
// user only: a protected DACL with a single full-control ACE.
func RestrictToUser(path string) error { return restrict(path) }
