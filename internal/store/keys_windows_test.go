//go:build windows

package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDPAPIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "store.key")
	k1, err := DPAPIFileKey(path).Key()
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != KeySize {
		t.Fatalf("key size %d", len(k1))
	}
	k2, err := DPAPIFileKey(path).Key()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("second provider got a different key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, k1) {
		t.Fatal("key stored in the clear")
	}
	if left, _ := filepath.Glob(path + ".tmp-*"); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestDPAPIFileUnreadableByOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.key")
	if _, err := DPAPIFileKey(path).Key(); err != nil {
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
		t.Error("DACL inherits from the parent")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("no DACL: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("DACL has %d ACEs, want 1", dacl.AceCount)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		t.Errorf("ACE type %d", ace.Header.AceType)
	}
	if !windows.EqualSid((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user.User.Sid) {
		t.Error("the only ACE is not the current user")
	}
}

func TestDPAPITamperedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.key")
	if _, err := DPAPIFileKey(path).Key(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DPAPIFileKey(path).Key(); !errors.Is(err, ErrKey) {
		t.Fatalf("got %v, want ErrKey", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DPAPIFileKey(path).Key(); !errors.Is(err, ErrKey) {
		t.Fatalf("empty file: got %v, want ErrKey", err)
	}
}
