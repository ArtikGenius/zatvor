package rotlog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotates(t *testing.T) {
	if Max != 5<<20 {
		t.Fatalf("Max = %d, want 5 MiB", Max)
	}
	path := filepath.Join(t.TempDir(), "zatvord.log")
	w := New(path, 100)
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes
	for i := 0; i < 2; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	if fi, _ := os.Stat(path); fi.Size() != 80 {
		t.Fatalf("size before the limit = %d, want 80", fi.Size())
	}
	// The third line would pass 100 bytes: the file moves to .1 first.
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() != 40 {
		t.Fatalf("size after rotation = %d, want 40", fi.Size())
	}
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != 80 {
		t.Fatalf(".1 = %v, %v; want the old 80 bytes", fi, err)
	}
	// Only one old file is kept.
	for i := 0; i < 3; i++ {
		w.Write(line)
	}
	if _, err := os.Stat(path + ".2"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a second old file was kept")
	}
	if fi, _ := os.Stat(path + ".1"); fi.Size() != 80 {
		t.Fatalf(".1 = %d bytes, want 80 (the newer full file)", fi.Size())
	}
	// A second writer (the other process) sees the same file and limit.
	other := New(path, 100)
	if _, err := other.Write(line); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Size() > 100 {
		t.Fatalf("the shared file grew past the limit: %d", fi.Size())
	}
}
