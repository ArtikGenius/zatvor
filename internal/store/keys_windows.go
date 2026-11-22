//go:build windows

package store

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ArtikGenius/zatvor/internal/appdir"
)

// dpapiEntropy is the optional entropy mixed into every DPAPI seal.
var dpapiEntropy = []byte("zatvor-store-v1")

// maxSealed bounds how much of a DPAPI file is read; a sealed 32-byte key is
// a few hundred bytes.
const maxSealed = 64 << 10

type dpapiKey struct{ path string }

// DPAPIFileKey keeps a random 32-byte key sealed with CryptProtectData
// (current-user scope, entropy "zatvor-store-v1") in a file that only the
// current user can access. It has no system side effects beyond that file.
// Like FileKey it is create-once: the file is complete before it appears and
// is never overwritten, so concurrent first users agree.
func DPAPIFileKey(path string) KeyProvider { return dpapiKey{path} }

func (d dpapiKey) Key() ([]byte, error) {
	key, err := d.read()
	if errors.Is(err, fs.ErrNotExist) {
		if err := d.create(); err != nil {
			return nil, err
		}
		return d.read()
	}
	return key, err
}

func (d dpapiKey) read() ([]byte, error) {
	f, err := os.Open(d.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: open key file %s: %w", ErrKey, d.path, err)
	}
	defer f.Close()
	sealed, err := io.ReadAll(io.LimitReader(f, maxSealed+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read key file %s: %w", ErrKey, d.path, err)
	}
	if len(sealed) == 0 || len(sealed) > maxSealed {
		return nil, fmt.Errorf("%w: key file %s has an invalid size", ErrKey, d.path)
	}
	key, err := dpapiUnprotect(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: unseal key file %s: %w", ErrKey, d.path, err)
	}
	if len(key) != KeySize {
		wipe(key)
		return nil, fmt.Errorf("%w: key file %s holds a key of the wrong size", ErrKey, d.path)
	}
	return key, nil
}

func (d dpapiKey) create() error {
	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("%w: random key: %w", ErrKey, err)
	}
	defer wipe(key)
	sealed, err := dpapiProtect(key)
	if err != nil {
		return fmt.Errorf("%w: seal key: %w", ErrKey, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(d.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := appdir.RestrictToUser(name); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: restrict %s: %w", ErrKey, name, err)
	}
	if _, err := tmp.Write(sealed); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	// The link fails if a key already exists (a concurrent creator won).
	if err := os.Link(name, d.path); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: publish key file %s: %w", ErrKey, d.path, err)
	}
	return nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func dpapiProtect(plain []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	ent := windows.DataBlob{Size: uint32(len(dpapiEntropy)), Data: &dpapiEntropy[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func dpapiUnprotect(sealed []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(sealed)), Data: &sealed[0]}
	ent := windows.DataBlob{Size: uint32(len(dpapiEntropy)), Data: &dpapiEntropy[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plain := append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
	wipe(unsafe.Slice(out.Data, out.Size))
	return plain, nil
}

// Credential Manager (advapi32). x/sys/windows does not wrap these.
var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW  = advapi32.NewProc("CredReadW")
	procCredWriteW = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

const (
	credTypeGeneric         = 1 // CRED_TYPE_GENERIC
	credPersistLocalMachine = 2 // CRED_PERSIST_LOCAL_MACHINE
)

// credential mirrors CREDENTIALW.
type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type credManKey struct{ target string }

// CredManKey keeps the key as a generic Windows credential "Zatvor/<target>"
// (CRED_PERSIST_LOCAL_MACHINE). Credential Manager is a user-visible system
// setting, so only live mode uses it. Create-once: an existing credential is
// used as is; otherwise a fresh random key is written and read back, so two
// first users end up with whatever Credential Manager holds.
func CredManKey(target string) KeyProvider { return credManKey{target} }

func credTarget(target string) string { return "Zatvor/" + target }

func (c credManKey) Key() ([]byte, error) {
	key, err := credRead(credTarget(c.target))
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		fresh := make([]byte, KeySize)
		if _, err := rand.Read(fresh); err != nil {
			return nil, fmt.Errorf("%w: random key: %w", ErrKey, err)
		}
		err = credWrite(credTarget(c.target), fresh)
		wipe(fresh)
		if err != nil {
			return nil, fmt.Errorf("%w: write credential: %w", ErrKey, err)
		}
		key, err = credRead(credTarget(c.target))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read credential: %w", ErrKey, err)
	}
	if len(key) != KeySize {
		wipe(key)
		return nil, fmt.Errorf("%w: credential %s holds a key of the wrong size", ErrKey, credTarget(c.target))
	}
	return key, nil
}

func credRead(target string) ([]byte, error) {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var p *credential
	r, _, e := procCredReadW.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&p)))
	if r == 0 {
		return nil, e
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(p)))
	if p.CredentialBlobSize == 0 || p.CredentialBlob == nil {
		return []byte{}, nil
	}
	return append([]byte(nil), unsafe.Slice(p.CredentialBlob, p.CredentialBlobSize)...), nil
}

func credWrite(target string, blob []byte) error {
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString("zatvor")
	if err != nil {
		return err
	}
	cred := credential{
		Type:               credTypeGeneric,
		TargetName:         name,
		CredentialBlobSize: uint32(len(blob)),
		CredentialBlob:     &blob[0],
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	r, _, e := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if r == 0 {
		return e
	}
	return nil
}

// DeleteCredManKey removes the credential CredManKey(target) uses. A missing
// credential is not an error.
func DeleteCredManKey(target string) error {
	name, err := windows.UTF16PtrFromString(credTarget(target))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKey, err)
	}
	r, _, e := procCredDelete.Call(uintptr(unsafe.Pointer(name)), credTypeGeneric, 0)
	if r == 0 {
		if errors.Is(e, windows.ERROR_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("%w: delete credential: %w", ErrKey, e)
	}
	return nil
}

// ErrKeyConflict: both a DPAPI key file and a Credential Manager key exist
// and they differ, so it is unknown which one the store was written with.
var ErrKeyConflict = errors.New("store: DPAPI key file and Credential Manager key differ")

// MigrateDPAPIToCredMan moves the key of DPAPIFileKey(path) to
// CredManKey(target), once (R17): when the DPAPI file exists and no
// credential does, the credential is written with the same key bytes and
// read back, and only when it reads back equal is the DPAPI file deleted.
// With no DPAPI file it does nothing. When the credential already exists
// with the same bytes (an earlier migration stopped before the delete) the
// file is deleted; with other bytes nothing is touched and the error is
// ErrKeyConflict. moved reports that the DPAPI file is gone now.
func MigrateDPAPIToCredMan(path, target string) (moved bool, err error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("%w: %w", ErrKey, err)
	}
	key, err := dpapiKey{path}.read()
	if err != nil {
		return false, err
	}
	defer wipe(key)
	name := credTarget(target)
	have, err := credRead(name)
	switch {
	case errors.Is(err, windows.ERROR_NOT_FOUND):
		if err := credWrite(name, key); err != nil {
			return false, fmt.Errorf("%w: write credential: %w", ErrKey, err)
		}
		if have, err = credRead(name); err != nil {
			return false, fmt.Errorf("%w: read credential back: %w", ErrKey, err)
		}
	case err != nil:
		return false, fmt.Errorf("%w: read credential: %w", ErrKey, err)
	}
	same := subtle.ConstantTimeCompare(have, key) == 1
	wipe(have)
	if !same {
		return false, ErrKeyConflict
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("%w: remove DPAPI key file: %w", ErrKey, err)
	}
	return true, nil
}
