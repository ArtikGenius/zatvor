package settings

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ArtikGenius/zatvor/internal/gen"
)

func full() Settings {
	return Settings{
		Version:       1,
		Ports:         gen.Ports{Mixed: 10801, Probe: 10802, DevTools: 10803, API: 10804},
		ActiveProfile: "profile-7",
		DevTools:      true,
		LogLevel:      "debug",
		UpdateOnStart: true,
	}
}

func TestRoundTripEveryField(t *testing.T) {
	want := full()
	v := reflect.ValueOf(want)
	for i := 0; i < v.NumField(); i++ { // guards against a new field the test forgets
		if f := v.Field(i); f.Kind() != reflect.Struct && f.IsZero() {
			t.Fatalf("test fixture leaves %s zero", v.Type().Field(i).Name)
		}
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLoadAbsentGivesDefaults(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "none.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != Defaults() || got.Version != 1 || got.LogLevel == "" {
		t.Fatalf("got %+v", got)
	}
}

func TestNeverEmptyOverNonEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(p, full()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	if err := Save(p, Settings{}); !errors.Is(err, ErrRefuseEmpty) {
		t.Fatalf("err = %v, want ErrRefuseEmpty", err)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed")
	}
}

func TestEmptyOverNothingIsAllowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := Save(p, Settings{}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveKeepsBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	a := full()
	if err := Save(p, a); err != nil {
		t.Fatal(err)
	}
	b := full()
	b.ActiveProfile = "profile-8"
	if err := Save(p, b); err != nil {
		t.Fatal(err)
	}
	bak, err := Load(p + ".bak")
	if err != nil || bak != a {
		t.Fatalf("bak = %+v, %v; want %+v", bak, err, a)
	}
}

func TestCorruptMainKeepsGoodBackup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	a := full()
	if err := Save(p, a); err != nil {
		t.Fatal(err)
	}
	b := full()
	b.LogLevel = "info"
	if err := Save(p, b); err != nil { // .bak = a
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if err := Save(p, b); err != nil {
		t.Fatal(err)
	}
	if bak, err := Load(p + ".bak"); err != nil || bak != a {
		t.Fatalf("good .bak was replaced: %+v, %v", bak, err)
	}
}

func TestCrashBetweenSteps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	old := full()
	if err := Save(p, old); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("crash")
	afterTemp = func() error { return boom }
	t.Cleanup(func() { afterTemp = nil })
	next := full()
	next.ActiveProfile = "other"
	if err := Save(p, next); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	got, err := Load(p)
	if err != nil || got != old {
		t.Fatalf("old file: %+v, %v", got, err)
	}
}

func TestNewerVersionRefused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("err = %v, want ErrNewerVersion", err)
	}
}

func TestEmptyFileIsCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}
