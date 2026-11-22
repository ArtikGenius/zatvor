package store

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ArtikGenius/zatvor/internal/happ"
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

// writeSealed writes a store file with the given format byte and plaintext,
// sealed with s's key.
func writeSealed(t *testing.T, s *Store, ver byte, plain []byte) {
	t.Helper()
	out := append(append([]byte(nil), magic...), ver)
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	out = append(out, nonce...)
	out = s.aead.Seal(out, nonce, plain, out[:headerLen])
	if err := os.WriteFile(s.path(), out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 1c tells a wrong key, a damaged file, a foreign file and a newer format
// apart (only the first two offer the .bak restore).
func TestLoadErrorsTyped(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	if err := s.Save(sample(t)); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(s.path())
	wrong := open(t, dir, filepath.Join(t.TempDir(), "otherkey"))
	if _, err := wrong.Load(); !errors.Is(err, ErrWrongKey) {
		t.Errorf("wrong key: %v, want ErrWrongKey", err)
	}
	cases := []struct {
		name string
		data func() []byte
		want error
	}{
		{"truncated", func() []byte { return []byte("ZTVS") }, ErrCorrupt},
		{"empty", func() []byte { return nil }, ErrCorrupt},
		{"short foreign", func() []byte { return []byte("{}") }, ErrFormat},
		{"flipped byte", func() []byte {
			b := append([]byte(nil), good...)
			b[len(b)-1] ^= 1
			return b
		}, ErrWrongKey},
		{"foreign", func() []byte { return []byte(`{"subscriptions":[]}` + strings.Repeat(" ", 64)) }, ErrFormat},
		{"newer format", func() []byte {
			b := append([]byte(nil), good...)
			b[len(magic)] = 9
			return b
		}, ErrVersion},
	}
	for _, c := range cases {
		if err := os.WriteFile(s.path(), c.data(), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	writeSealed(t, s, formatVer, []byte("not json"))
	if _, err := s.Load(); !errors.Is(err, ErrCorrupt) {
		t.Errorf("bad JSON: %v, want ErrCorrupt", err)
	}
	writeSealed(t, s, formatVer, []byte(`{"version":99,"subscriptions":[]}`))
	if _, err := s.Load(); !errors.Is(err, ErrVersion) {
		t.Errorf("newer document: %v, want ErrVersion", err)
	}
}

func TestKeyErrorsTyped(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FileKey(short).Key(); !errors.Is(err, ErrKey) {
		t.Errorf("short key file: %v, want ErrKey", err)
	}
	if _, err := Open(dir, FileKey(short)); !errors.Is(err, ErrKey) {
		t.Errorf("Open with short key file: %v, want ErrKey", err)
	}
	if _, err := Open(dir, badKey{}); !errors.Is(err, ErrKey) {
		t.Errorf("Open with 16-byte key: %v, want ErrKey", err)
	}
	if runtime.GOOS != "windows" {
		wide := filepath.Join(dir, "wide")
		if err := os.WriteFile(wide, make([]byte, KeySize), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := FileKey(wide).Key(); !errors.Is(err, ErrKey) {
			t.Errorf("wide key file: %v, want ErrKey", err)
		}
	}
}

// Concurrent first use: every caller gets the same complete key; no one sees
// a missing, empty or half-written file.
func TestFileKeyConcurrentCreate(t *testing.T) {
	for round := 0; round < 50; round++ {
		path := filepath.Join(t.TempDir(), "key")
		const n = 8
		keys := make([][]byte, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				keys[i], errs[i] = FileKey(path).Key()
			}()
		}
		close(start)
		wg.Wait()
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				t.Fatalf("round %d caller %d: %v", round, i, errs[i])
			}
			if !bytes.Equal(keys[i], keys[0]) || len(keys[i]) != KeySize {
				t.Fatalf("round %d: callers got different keys", round)
			}
		}
		ents, _ := os.ReadDir(filepath.Dir(path))
		if len(ents) != 1 {
			t.Fatalf("round %d: leftover files %v", round, ents)
		}
	}
}

// decrypt returns the stored JSON document.
func decrypt(t *testing.T, s *Store) map[string]any {
	t.Helper()
	data, err := os.ReadFile(s.path())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.aead.Open(nil, data[headerLen:headerLen+nonceLen], data[headerLen+nonceLen:], data[:headerLen])
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(plain, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func happSample() []profile.Subscription {
	pr := &happ.Profile{Name: "provider", ProxySites: []string{"geosite:google"}}
	own := &happ.Profile{Name: "imported", DirectSites: []string{"geosite:category-ru"}}
	return []profile.Subscription{{
		ID: "s1", URL: "https://" + testHost + "/sub/REPLACE_ME", ProviderRouting: pr,
		Profiles: []profile.Profile{
			{ID: "a", SubscriptionID: "s1", RoutingSource: profile.Source{Kind: profile.SourceProvider, Happ: pr}},
			{ID: "b", SubscriptionID: "s1", RoutingSource: profile.Source{Happ: pr}}, // zero Kind is provider
			{ID: "c", SubscriptionID: "s1", RoutingSource: profile.Source{Kind: profile.SourceHapp, Happ: own}},
			{ID: "d", SubscriptionID: "s1", RoutingSource: profile.Source{Kind: profile.SourcePreset, Preset: profile.PresetAllProxy}},
		},
	}}
}

// The provider's Happ profile is stored once, on the subscription; provider
// profiles are re-linked to it on Load. Only SourceHapp keeps its own copy.
func TestProviderRoutingStoredOnce(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	in := happSample()
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	if in[0].Profiles[0].RoutingSource.Happ == nil {
		t.Fatal("Save changed the caller's profiles")
	}
	doc := decrypt(t, s)
	if doc["version"] != float64(docVersion) || docVersion < 2 {
		t.Fatalf("document version = %v", doc["version"])
	}
	profs := doc["subscriptions"].([]any)[0].(map[string]any)["Profiles"].([]any)
	for i, want := range []bool{false, false, true, false} {
		h := profs[i].(map[string]any)["RoutingSource"].(map[string]any)["Happ"]
		if (h != nil) != want {
			t.Errorf("profile %d: stored Happ = %v, want present=%v", i, h, want)
		}
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	sub := got[0]
	for _, i := range []int{0, 1} {
		if sub.Profiles[i].RoutingSource.Happ != sub.ProviderRouting || sub.ProviderRouting == nil {
			t.Errorf("profile %d not re-linked to ProviderRouting", i)
		}
	}
	if h := sub.Profiles[2].RoutingSource.Happ; h == nil || h == sub.ProviderRouting || h.Name != "imported" {
		t.Errorf("happ profile = %+v", h)
	}
	if sub.Profiles[3].RoutingSource.Happ != nil {
		t.Errorf("preset profile got a Happ profile")
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip differs:\n%+v\n%+v", got, in)
	}
}

// A version 1 document (Happ copied into every provider profile) still loads.
func TestLoadsDocumentVersion1(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, filepath.Join(dir, "key"))
	in := happSample()
	plain, err := json.Marshal(map[string]any{"version": 1, "subscriptions": in})
	if err != nil {
		t.Fatal(err)
	}
	writeSealed(t, s, formatVer, plain)
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Profiles[0].RoutingSource.Happ != got[0].ProviderRouting || !reflect.DeepEqual(got, in) {
		t.Fatalf("v1 load = %+v", got)
	}
}
