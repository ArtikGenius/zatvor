//go:build windows && systemtest

package store

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
