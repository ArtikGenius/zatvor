// Package store keeps the subscriptions and profiles in one encrypted file.
// Subscription URLs and credentials never reach disk in clear (spec R3.5).
//
// File layout: "ZTVS", one version byte, a 12-byte random nonce, then the
// AES-256-GCM ciphertext of the JSON document. The header and nonce are
// authenticated as additional data. Save writes a temp file in the same
// directory, fsyncs it, renames the current file to ".bak" and renames the
// temp file into place. Load never writes anything.
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

const (
	fileName   = "profiles.zst"
	formatVer  = 1
	headerLen  = 5 // magic + version
	nonceLen   = 12
	docVersion = 1
)

var magic = []byte("ZTVS")

// Store is the encrypted profile store in one directory.
type Store struct {
	dir  string
	aead cipher.AEAD
	mu   sync.Mutex
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
		return nil, fmt.Errorf("store: key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("store: key is %d bytes, want %d", len(key), KeySize)
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
	return &Store{dir: dir, aead: aead}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// Load returns the saved subscriptions; a missing file is an empty store.
// A wrong key, corruption or an unknown version is an error. It does not
// fall back to ".bak" and never touches it.
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
	if len(data) < headerLen+nonceLen+s.aead.Overhead() || !bytes.Equal(data[:len(magic)], magic) {
		return nil, errors.New("store: not a Zatvor store file")
	}
	if data[len(magic)] != formatVer {
		return nil, fmt.Errorf("store: unsupported format version %d", data[len(magic)])
	}
	header := data[:headerLen]
	nonce := data[headerLen : headerLen+nonceLen]
	plain, err := s.aead.Open(nil, nonce, data[headerLen+nonceLen:], header)
	if err != nil {
		return nil, errors.New("store: cannot decrypt (wrong key or corrupted file)")
	}
	var doc document
	if err := json.Unmarshal(plain, &doc); err != nil {
		return nil, errors.New("store: decrypted content is not valid")
	}
	if doc.Version != docVersion {
		return nil, fmt.Errorf("store: unsupported document version %d", doc.Version)
	}
	return doc.Subscriptions, nil
}

// Save replaces the store atomically, keeping the previous file as ".bak".
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

	tmp, err := os.CreateTemp(s.dir, fileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		cleanup()
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if _, err := os.Stat(s.path()); err == nil {
		if err := os.Rename(s.path(), s.path()+".bak"); err != nil {
			os.Remove(tmpName)
			return err
		}
	}
	if err := os.Rename(tmpName, s.path()); err != nil {
		os.Remove(tmpName)
		return err
	}
	if d, err := os.Open(s.dir); err == nil { // best effort: persist the renames
		d.Sync()
		d.Close()
	}
	return nil
}
