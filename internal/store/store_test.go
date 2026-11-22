package store

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/link"
	"github.com/ArtikGenius/zatvor/internal/ojson"
	"github.com/ArtikGenius/zatvor/internal/profile"
)

const (
	testUUID = "11111111-2222-3333-4444-555555555555"
	testHost = "panel.example.com"
)

func sample(t *testing.T) []profile.Subscription {
	t.Helper()
	cfg, err := ojson.Parse([]byte(`{"remarks":"J","outbounds":[{"protocol":"vless","settings":{"address":"a.example.com","port":443,"id":"` + testUUID + `"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	n := link.Node{Scheme: "trojan", Remark: "N", Address: "b.example.com", Port: 443,
		Params: map[string]string{"password": "REPLACE_ME"}, Raw: "trojan://REPLACE_ME@b.example.com:443#N"}
	return []profile.Subscription{{
		ID: "s1", URL: "https://" + testHost + "/sub/REPLACE_ME", Title: "T", UA: "Zatvor/test (linux)", SendDevice: true,
		LastGood: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
		Profiles: []profile.Profile{
			{ID: "p1", Name: "N", SubscriptionID: "s1", Node: &n, RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}},
			{ID: "p2", Name: "J", SubscriptionID: "s1", Config: cfg.(*ojson.Object), Disabled: true},
		},
	}}
}

func open(t *testing.T, dir, keyFile string) *Store {
	t.Helper()
	s, err := Open(dir, FileKey(keyFile))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	if got, err := s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("empty Load = %v, %v", got, err)
	}
	want := sample(t)
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := open(t, dir, filepath.Join(dir, "key")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip differs:\n%+v\n%+v", got, want)
	}
}

func TestStoreEncrypted(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	s := open(t, dir, keyFile)
	if err := s.Save(sample(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample(t)); err != nil { // creates .bak
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.Name() == "key" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		for _, bad := range []string{"REPLACE_ME", testHost, testUUID, "example.com", "vless"} {
			if bytes.Contains(b, []byte(bad)) {
				t.Errorf("%s contains %q in clear", e.Name(), bad)
			}
		}
	}

	// Wrong key: Load fails, .bak untouched, current file untouched.
	bakPath := filepath.Join(dir, fileName+".bak")
	curPath := filepath.Join(dir, fileName)
	bakBefore, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf(".bak missing: %v", err)
	}
	curBefore, _ := os.ReadFile(curPath)
	other := t.TempDir()
	wrong := open(t, dir, filepath.Join(other, "otherkey"))
	if _, err := wrong.Load(); err == nil {
		t.Fatal("Load with wrong key must fail")
	} else if strings.Contains(err.Error(), testHost) || strings.Contains(err.Error(), "REPLACE_ME") {
		t.Fatalf("error leaks secrets: %v", err)
	}
	bakAfter, _ := os.ReadFile(bakPath)
	curAfter, _ := os.ReadFile(curPath)
	if !bytes.Equal(bakBefore, bakAfter) || !bytes.Equal(curBefore, curAfter) {
		t.Fatal("failed Load touched files")
	}

	// Corrupted file: error, no panic.
	if err := os.WriteFile(curPath, []byte("ZTVS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("truncated file must fail")
	}
	if b, _ := os.ReadFile(bakPath); !bytes.Equal(b, bakBefore) {
		t.Fatal("Load touched .bak")
	}
}

func TestStoreFlatSize(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	data := sample(t)
	size := func(name string) int64 {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	if err := s.Save(data); err != nil {
		t.Fatal(err)
	}
	first := size(fileName)
	for i := 0; i < 1000; i++ {
		if err := s.Save(data); err != nil {
			t.Fatal(err)
		}
	}
	if got := size(fileName); got != first {
		t.Fatalf("size grew from %d to %d", first, got)
	}
	if got := size(fileName + ".bak"); got != first {
		t.Fatalf(".bak size %d, want %d", got, first)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 { // store, .bak, key
		t.Fatalf("stray files: %v", ents)
	}
}

func TestSaveKeepsPreviousAsBak(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	a := sample(t)
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	b := sample(t)
	b[0].Title = "second"
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	// Load the .bak by renaming it into place in a copy.
	dir2 := t.TempDir()
	bak, _ := os.ReadFile(filepath.Join(dir, fileName+".bak"))
	_ = os.WriteFile(filepath.Join(dir2, fileName), bak, 0o600)
	k, _ := os.ReadFile(filepath.Join(dir, "key"))
	_ = os.WriteFile(filepath.Join(dir2, "key"), k, 0o600)
	got, err := open(t, dir2, filepath.Join(dir2, "key")).Load()
	if err != nil || got[0].Title != "T" {
		t.Fatalf("bak = %v, %v", got, err)
	}
}

func TestFileKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "key")
	k1, err := FileKey(path).Key()
	if err != nil || len(k1) != 32 {
		t.Fatalf("key = %d bytes, %v", len(k1), err)
	}
	k2, err := FileKey(path).Key()
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatal("key not stable")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", fi.Mode().Perm())
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := FileKey(path).Key(); err == nil {
			t.Fatal("wide permissions must be refused")
		}
		os.Chmod(path, 0o600)
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FileKey(short).Key(); err == nil {
		t.Fatal("short key must be refused")
	}
	if _, err := Open(dir, badKey{}); err == nil {
		t.Fatal("Open must reject a non-32-byte key")
	}
}

type badKey struct{}

func (badKey) Key() ([]byte, error) { return make([]byte, 16), nil }

func TestSaveKeepsMainOnBakFailure(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	s := open(t, dir, keyFile)
	prev := sample(t)
	if err := s.Save(prev); err != nil {
		t.Fatal(err)
	}
	s.afterBak = func() error { return os.ErrClosed }
	next := sample(t)
	next[0].Title = "next"
	if err := s.Save(next); err == nil {
		t.Fatal("want injected failure")
	}
	got, err := open(t, dir, keyFile).Load()
	if err != nil || !reflect.DeepEqual(got, prev) {
		t.Fatalf("main file lost previous data: %v", err)
	}
}

func TestOpenRemovesStrayTemps(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, tmpPrefix+"123")
	other := filepath.Join(dir, "unrelated.tmp")
	os.WriteFile(stray, []byte("x"), 0o600)
	os.WriteFile(other, []byte("x"), 0o600)
	open(t, dir, filepath.Join(dir, "key"))
	if _, err := os.Stat(stray); err == nil {
		t.Fatal("stray temp kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated file removed")
	}
}

func TestSaveKeepsGoodBakWhenMainCorrupt(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	s := open(t, dir, keyFile)
	first := sample(t)
	if err := s.Save(first); err != nil {
		t.Fatal(err)
	}
	second := sample(t)
	second[0].Title = "second"
	if err := s.Save(second); err != nil { // .bak now holds first
		t.Fatal(err)
	}
	main := filepath.Join(dir, fileName)
	b, _ := os.ReadFile(main)
	b[len(b)-1] ^= 0xff
	if err := os.WriteFile(main, b, 0o600); err != nil {
		t.Fatal(err)
	}
	third := sample(t)
	third[0].Title = "third"
	if err := s.Save(third); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || got[0].Title != "third" {
		t.Fatalf("main = %v, %v", got, err)
	}
	dir2 := t.TempDir()
	bak, _ := os.ReadFile(filepath.Join(dir, fileName+".bak"))
	k, _ := os.ReadFile(keyFile)
	os.WriteFile(filepath.Join(dir2, fileName), bak, 0o600)
	os.WriteFile(filepath.Join(dir2, "key"), k, 0o600)
	got, err := open(t, dir2, filepath.Join(dir2, "key")).Load()
	if err != nil || got[0].Title != "T" {
		t.Fatalf(".bak lost the good backup: %v %v", got, err)
	}
}
