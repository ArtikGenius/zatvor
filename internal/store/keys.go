package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// KeySize is the AES-256 key length the store requires.
const KeySize = 32

// KeyProvider supplies the store's 32-byte key. OS-backed providers (DPAPI,
// Keychain, Credential Manager) live in stage 1b.
type KeyProvider interface {
	Key() ([]byte, error)
}

type fileKey struct{ path string }

// FileKey keeps the key in a file: created with crypto/rand on first use with
// mode 0600, and refused on later use if group or others can read it
// (non-Windows). For tests and the Linux fallback.
func FileKey(path string) KeyProvider { return fileKey{path} }

func (f fileKey) Key() ([]byte, error) {
	key, err := f.read()
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.create(); err != nil {
			return nil, err
		}
		return f.read()
	}
	return key, err
}

func (f fileKey) read() ([]byte, error) {
	file, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if runtime.GOOS != "windows" {
		fi, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("store: key file %s has mode %v, want 0600", f.path, fi.Mode().Perm())
		}
	}
	buf := make([]byte, KeySize+1)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return nil, fmt.Errorf("store: key file %s is empty", f.path)
	}
	if n != KeySize {
		return nil, fmt.Errorf("store: key file %s must hold exactly %d bytes", f.path, KeySize)
	}
	return buf[:n], nil
}

func (f fileKey) create() error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	file, err := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(key); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
