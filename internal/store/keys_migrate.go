package store

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ErrKeyConflict: both a DPAPI key file and a Credential Manager key exist
// and they differ, so it is unknown which one the store was written with.
var ErrKeyConflict = errors.New("store: DPAPI key file and Credential Manager key differ")

// errCredNotFound is what a credStore's Read returns for a missing
// credential.
var errCredNotFound = errors.New("store: credential not found")

// credStore is the part of Credential Manager the migration uses.
type credStore interface {
	Read(name string) ([]byte, error) // errCredNotFound when missing
	Write(name string, key []byte) error
	Delete(name string) error
}

// migrateKey moves the key in the file at path (read by readFile) into the
// credential name, once:
//   - no file: nothing to do;
//   - no credential: write it with the same bytes and read it back; only
//     when it reads back equal is the file deleted. A read-back that fails
//     or differs deletes the credential just written again, so a broken
//     migration never blocks a later one, and the file stays;
//   - an equal credential (an earlier migration stopped before the
//     delete): the file is deleted;
//   - a different credential: ErrKeyConflict, and both stay.
//
// moved reports that the file is gone now.
func migrateKey(path string, readFile func() ([]byte, error), c credStore, name string) (moved bool, err error) {
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("%w: %w", ErrKey, err)
	}
	key, err := readFile()
	if err != nil {
		return false, err
	}
	defer wipeKey(key)
	have, err := c.Read(name)
	switch {
	case errors.Is(err, errCredNotFound):
		if err := c.Write(name, key); err != nil {
			return false, fmt.Errorf("%w: write credential: %w", ErrKey, err)
		}
		back, rerr := c.Read(name)
		if rerr != nil || subtle.ConstantTimeCompare(back, key) != 1 {
			wipeKey(back)
			if derr := c.Delete(name); derr != nil {
				return false, fmt.Errorf("%w: the credential did not read back and could not be removed: %w", ErrKey, derr)
			}
			if rerr != nil {
				return false, fmt.Errorf("%w: read credential back: %w", ErrKey, rerr)
			}
			return false, ErrKeyConflict
		}
		wipeKey(back)
	case err != nil:
		return false, fmt.Errorf("%w: read credential: %w", ErrKey, err)
	default:
		same := subtle.ConstantTimeCompare(have, key) == 1
		wipeKey(have)
		if !same {
			return false, ErrKeyConflict
		}
	}
	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("%w: remove DPAPI key file: %w", ErrKey, err)
	}
	return true, nil
}

func wipeKey(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
