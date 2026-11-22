package store

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// fakeCreds is a credStore over a map; mangle changes what a Read after a
// Write returns.
type fakeCreds struct {
	m       map[string][]byte
	mangle  bool
	readErr error
	deletes int
}

func (f *fakeCreds) Read(name string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	b, ok := f.m[name]
	if !ok {
		return nil, errCredNotFound
	}
	out := append([]byte(nil), b...)
	if f.mangle {
		out[0] ^= 0xff
	}
	return out, nil
}

func (f *fakeCreds) Write(name string, key []byte) error {
	f.m[name] = append([]byte(nil), key...)
	return nil
}

func (f *fakeCreds) Delete(name string) error { f.deletes++; delete(f.m, name); return nil }

func keyFile(t *testing.T) (string, []byte, func() ([]byte, error)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "live.key")
	key := bytes.Repeat([]byte{7}, KeySize)
	if err := os.WriteFile(path, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key, func() ([]byte, error) { return append([]byte(nil), key...), nil }
}

func exists(path string) bool { _, err := os.Stat(path); return !errors.Is(err, fs.ErrNotExist) }

func TestMigrateKeyMovesOnce(t *testing.T) {
	path, key, read := keyFile(t)
	c := &fakeCreds{m: map[string][]byte{}}
	moved, err := migrateKey(path, read, c, "Zatvor/x")
	if err != nil || !moved || exists(path) || !bytes.Equal(c.m["Zatvor/x"], key) {
		t.Fatalf("moved=%v err=%v file=%v cred=%x", moved, err, exists(path), c.m["Zatvor/x"])
	}
	// No file any more: nothing to do.
	if moved, err := migrateKey(path, read, c, "Zatvor/x"); moved || err != nil {
		t.Fatalf("second run: moved=%v err=%v", moved, err)
	}
}

func TestMigrateKeyReadBackMismatchRemovesCredential(t *testing.T) {
	path, _, read := keyFile(t)
	c := &fakeCreds{m: map[string][]byte{}, mangle: true}
	if _, err := migrateKey(path, read, c, "Zatvor/x"); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("err = %v, want ErrKeyConflict", err)
	}
	if _, ok := c.m["Zatvor/x"]; ok || c.deletes != 1 {
		t.Fatalf("the credential written by the failed migration must be removed (deletes %d)", c.deletes)
	}
	if !exists(path) {
		t.Fatal("the key file must stay when the move failed")
	}
	// The next start can migrate.
	c.mangle = false
	if moved, err := migrateKey(path, read, c, "Zatvor/x"); err != nil || !moved {
		t.Fatalf("retry: moved=%v err=%v", moved, err)
	}
}

func TestMigrateKeyFinishesInterruptedMove(t *testing.T) {
	path, key, read := keyFile(t)
	c := &fakeCreds{m: map[string][]byte{"Zatvor/x": key}}
	if moved, err := migrateKey(path, read, c, "Zatvor/x"); err != nil || !moved || exists(path) {
		t.Fatalf("moved=%v err=%v file=%v", moved, err, exists(path))
	}
}

func TestMigrateKeyConflictKeepsBoth(t *testing.T) {
	path, _, read := keyFile(t)
	other := bytes.Repeat([]byte{9}, KeySize)
	c := &fakeCreds{m: map[string][]byte{"Zatvor/x": other}}
	if _, err := migrateKey(path, read, c, "Zatvor/x"); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("err = %v, want ErrKeyConflict", err)
	}
	if !exists(path) || !bytes.Equal(c.m["Zatvor/x"], other) || c.deletes != 0 {
		t.Fatal("a foreign credential and the file must both stay")
	}
}
