// Package store keeps the subscriptions and profiles in one encrypted file.
// Subscription URLs and credentials never reach disk in clear (spec R3.5).
//
// File layout: "ZTVS", one version byte, a 12-byte random nonce, then the
// AES-256-GCM ciphertext of the JSON document. The header and nonce are
// authenticated as additional data. Save writes a temp file in the same
// directory, fsyncs it and renames it over the main file; the main file is
// never moved. Before that, the current main file is copied to ".bak" only if
// it decrypts with the store's key, so a bad main file never replaces a good
// backup. Load never writes anything; callers should Load before Save.
package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ArtikGenius/zatvor/internal/profile"
)

const tmpPrefix = "profiles.zst.tmp-"

const (
	fileName   = "profiles.zst"
	formatVer  = 1
	headerLen  = 5 // magic + version
	nonceLen   = 12
	docVersion = 1
)

var magic = []byte("ZTVS")

// Errors Load, Open and FileKey return, for errors.Is. Messages never hold
// store contents.
var (
	// ErrKey: the key provider failed or gave a key that is not 32 bytes
	// (a key file with the wrong size or wide permissions).
	ErrKey = errors.New("store: key unavailable")
	// ErrWrongKey: the file does not authenticate with this key. Either the
	// key changed or the ciphertext was damaged; GCM cannot tell which.
	ErrWrongKey = errors.New("store: cannot decrypt (wrong key or damaged file)")
	// ErrCorrupt: the file is a store file but truncated, or it decrypted
	// to content that is not a store document.
	ErrCorrupt = errors.New("store: file is damaged")
	// ErrFormat: the file is not a Zatvor store file.
	ErrFormat = errors.New("store: not a Zatvor store file")
	// ErrVersion: a store file or document version this build does not know
	// (written by a newer Zatvor).
	ErrVersion = errors.New("store: unsupported store version")
)

// Store is the encrypted profile store in one directory.
type Store struct {
	dir  string
	aead cipher.AEAD
	mu   sync.Mutex
	// afterBak is a test hook run after the .bak copy, before the replace.
	afterBak func() error
}

type document struct {
	Version       int                    `json:"version"`
	Subscriptions []profile.Subscription `json:"subscriptions"`
}

// Open prepares dir (mode 0700) and the cipher. The key must be 32 bytes.
// Errors never include store contents.
func Open(dir string, kp KeyProvider) (*Store, error) {
	key, err := kp.Key()
	if err != nil {
		if errors.Is(err, ErrKey) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %w", ErrKey, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: key is %d bytes, want %d", ErrKey, len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// Remove temp files left by a crashed Save (our own prefix only).
	if stray, _ := filepath.Glob(filepath.Join(dir, tmpPrefix+"*")); len(stray) > 0 {
		for _, f := range stray {
			os.Remove(f)
		}
	}
	return &Store{dir: dir, aead: aead}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// Load returns the saved subscriptions; a missing file is an empty store.
// A wrong key or damaged ciphertext is ErrWrongKey, a truncated file or bad
// document ErrCorrupt, a foreign file ErrFormat and an unknown version
// ErrVersion; other errors are I/O errors. It does not fall back to ".bak"
// and never touches it.
func (s *Store) Load() ([]profile.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	switch {
	case !bytes.HasPrefix(data, magic) && !bytes.HasPrefix(magic, data):
		return nil, ErrFormat // a prefix of the magic is a truncated store file
	case len(data) > len(magic) && data[len(magic)] != formatVer:
		return nil, fmt.Errorf("%w: format %d", ErrVersion, data[len(magic)])
	case len(data) < headerLen+nonceLen+s.aead.Overhead():
		return nil, fmt.Errorf("%w: truncated", ErrCorrupt)
	}
	header := data[:headerLen]
	nonce := data[headerLen : headerLen+nonceLen]
	plain, err := s.aead.Open(nil, nonce, data[headerLen+nonceLen:], header)
	if err != nil {
		return nil, ErrWrongKey
	}
	var doc document
	if err := json.Unmarshal(plain, &doc); err != nil {
		return nil, fmt.Errorf("%w: decrypted content is not a store document", ErrCorrupt)
	}
	if doc.Version != docVersion {
		return nil, fmt.Errorf("%w: document %d", ErrVersion, doc.Version)
	}
	return doc.Subscriptions, nil
}

// Save replaces the store atomically. The previous main file becomes ".bak"
// only if it opens with this store's key; otherwise ".bak" is left as it is
// and the new data is still written. Callers should Load first, so a wrong key
// or damaged file is noticed before it is overwritten.
func (s *Store) Save(subs []profile.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if subs == nil {
		subs = []profile.Subscription{}
	}
	plain, err := json.Marshal(document{Version: docVersion, Subscriptions: subs})
	if err != nil {
		return fmt.Errorf("store: encode: %w", err)
	}
	out := make([]byte, 0, headerLen+nonceLen+len(plain)+s.aead.Overhead())
	out = append(out, magic...)
	out = append(out, formatVer)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, plain, out[:headerLen])

	// 1. Copy the current file to ".bak" (temp + fsync + rename); the main
	// file is never moved, so it is present at every instant.
	if old, err := os.ReadFile(s.path()); err == nil {
		if !s.opens(old) {
			// keep the last good .bak
		} else if err := s.writeAtomic(s.path()+".bak", old); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if s.afterBak != nil {
		if err := s.afterBak(); err != nil {
			return err
		}
	}
	// 2. Atomically replace the main file.
	if err := s.writeAtomic(s.path(), out); err != nil {
		return err
	}
	// 3. Persist the renames where the platform supports it.
	if d, err := os.Open(s.dir); err == nil { // best effort: persist the renames
		d.Sync()
		d.Close()
	}
	return nil
}

// opens reports whether data is a store file that decrypts and authenticates
// with this store's key.
func (s *Store) opens(data []byte) bool {
	if len(data) < headerLen+nonceLen+s.aead.Overhead() || !bytes.Equal(data[:len(magic)], magic) || data[len(magic)] != formatVer {
		return false
	}
	_, err := s.aead.Open(nil, data[headerLen:headerLen+nonceLen], data[headerLen+nonceLen:], data[:headerLen])
	return err == nil
}

// writeAtomic writes data to a temp file in dir, fsyncs it and renames it over
// dest.
func (s *Store) writeAtomic(dest string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, tmpPrefix+"*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error { tmp.Close(); os.Remove(name); return err }
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dest); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
