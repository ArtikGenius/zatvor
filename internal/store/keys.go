package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
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
// mode 0600 (complete before it appears and never overwritten, so concurrent
// first users agree), and refused on later use if group or others can read
// it (non-Windows). For tests and the Linux fallback.
func FileKey(path string) KeyProvider { return fileKey{path} }

func (f fileKey) Key() ([]byte, error) {
	key, err := f.read()
	if errors.Is(err, fs.ErrNotExist) {
		if err := f.create(); err != nil {
			return nil, err
		}
		return f.read()
	}
	if errors.Is(err, errShort) {
		// A creator from an older build writes in place; give it a moment.
		time.Sleep(50 * time.Millisecond)
		key, err = f.read()
	}
	return key, err
}

var errShort = fmt.Errorf("%w: key file has the wrong size", ErrKey)

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
			return nil, fmt.Errorf("%w: key file %s has mode %v, want 0600", ErrKey, f.path, fi.Mode().Perm())
		}
	}
	buf, err := io.ReadAll(io.LimitReader(file, KeySize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read key file %s: %w", ErrKey, f.path, err)
	}
	if len(buf) != KeySize {
		return nil, fmt.Errorf("%w: %s holds %d bytes, want exactly %d", errShort, f.path, len(buf), KeySize)
	}
	return buf, nil
}

// create writes a new key to a temp file, fsyncs it and links it to the
// key path. The link fails if a key already exists (a concurrent creator
// won), so a key is never overwritten and the key path only ever names a
// complete file.
func (f fileKey) create() error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(name, f.path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if d, err := os.Open(dir); err == nil { // best effort: persist the link
		d.Sync()
		d.Close()
	}
	return nil
}
