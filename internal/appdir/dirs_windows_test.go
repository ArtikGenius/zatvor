//go:build windows

package appdir

import (
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDirsUserOnly(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Zatvor")
	d, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	tok := windows.GetCurrentProcessToken()
	user, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d.Data, d.Cache, d.Logs, d.Journal, d.Geo, d.Run} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		ctrl, _, err := sd.Control()
		if err != nil {
			t.Fatal(err)
		}
		if ctrl&windows.SE_DACL_PROTECTED == 0 {
			t.Errorf("%s: DACL inherits from the parent", p)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			t.Fatalf("%s: no DACL (%v)", p, err)
		}
		if acl.AceCount != 1 {
			t.Errorf("%s: %d ACEs, want 1", p, acl.AceCount)
		}
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, 0, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("%s: ACE type %d, want allow", p, ace.Header.AceType)
		}
		if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !sid.Equals(user.User.Sid) {
			t.Errorf("%s: ACE is for %s, want the current user %s", p, sid, user.User.Sid)
		}
	}
}
