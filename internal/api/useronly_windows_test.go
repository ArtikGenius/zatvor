//go:build windows

package api_test

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// assertUserOnly checks that path has a protected DACL whose every entry
// grants access to the current user only.
func assertUserOnly(t *testing.T, path string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if ctrl&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("%s: DACL inherits from the parent", path)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("%s: no DACL (%v)", path, err)
	}
	if acl.AceCount == 0 {
		t.Errorf("%s: empty DACL", path)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("%s: ACE %d type %d, want allow", path, i, ace.Header.AceType)
		}
		if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !sid.Equals(user.User.Sid) {
			t.Errorf("%s: ACE %d is for %s, want only the current user %s", path, i, sid, user.User.Sid)
		}
	}
}
