//go:build windows && systemtest

package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/systest"
)

func TestMain(m *testing.M) { systest.Main(m) }

func TestCredManRoundTrip(t *testing.T) {
	systest.Require(t)
	rnd := make([]byte, 8)
	if _, err := rand.Read(rnd); err != nil {
		t.Fatal(err)
	}
	target := "zatvor-test-" + hex.EncodeToString(rnd)
	t.Cleanup(func() {
		if err := DeleteCredManKey(target); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	k1, err := CredManKey(target).Key()
	if err != nil {
		t.Fatal(err)
	}
	if len(k1) != KeySize {
		t.Fatalf("key size %d", len(k1))
	}
	k2, err := CredManKey(target).Key()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Fatal("second Key() differs")
	}
	if err := DeleteCredManKey(target); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCredManKey(target); err != nil {
		t.Fatalf("deleting a missing credential: %v", err)
	}
}

func TestMigrateDPAPIToCredMan(t *testing.T) {
	systest.Require(t)
	rnd := make([]byte, 8)
	if _, err := rand.Read(rnd); err != nil {
		t.Fatal(err)
	}
	target := "zatvor-test-" + hex.EncodeToString(rnd)
	t.Cleanup(func() {
		if err := DeleteCredManKey(target); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	path := filepath.Join(t.TempDir(), "store.key")

	// No DPAPI file: nothing to migrate.
	if moved, err := MigrateDPAPIToCredMan(path, target); err != nil || moved {
		t.Fatalf("without a DPAPI file: moved=%v err=%v", moved, err)
	}

	old, err := DPAPIFileKey(path).Key()
	if err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateDPAPIToCredMan(path, target)
	if err != nil || !moved {
		t.Fatalf("migrate: moved=%v err=%v", moved, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the DPAPI file must be gone after the migration: %v", err)
	}
	got, err := CredManKey(target).Key()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Fatal("Credential Manager holds another key than the DPAPI file had")
	}

	// A DPAPI file next to an existing credential with other bytes is a
	// conflict: both stay.
	if _, err := DPAPIFileKey(path).Key(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateDPAPIToCredMan(path, target); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("conflict: err=%v, want ErrKeyConflict", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the DPAPI file must stay on a conflict: %v", err)
	}
}
