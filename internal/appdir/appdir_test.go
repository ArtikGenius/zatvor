package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCreatesAllDirs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Zatvor")
	d, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	if d.Data != base {
		t.Errorf("Data = %q, want %q", d.Data, base)
	}
	for name, p := range map[string]string{"Data": d.Data, "Cache": d.Cache, "Logs": d.Logs, "Journal": d.Journal, "Geo": d.Geo, "Run": d.Run} {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			t.Errorf("%s %q: not a directory (%v)", name, p, err)
		}
	}
	if _, err := Resolve(base); err != nil {
		t.Errorf("second Resolve: %v", err)
	}
}

func TestResolveRejectsEmptyBase(t *testing.T) {
	if _, err := Resolve(""); err == nil {
		t.Fatal("empty base accepted")
	}
}

func TestDefaultFor(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name, goos string
		env        map[string]string
		want       string
		wantErr    bool
	}{
		{"windows", "windows", map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}, filepath.Join(`C:\Users\u\AppData\Local`, "Zatvor"), false},
		{"windows unset", "windows", nil, "", true},
		{"darwin", "darwin", map[string]string{"HOME": "/Users/u"}, filepath.Join("/Users/u", "Library", "Application Support", "Zatvor"), false},
		{"linux xdg", "linux", map[string]string{"XDG_DATA_HOME": "/x/data", "HOME": "/home/u"}, filepath.Join("/x/data", "zatvor"), false},
		{"linux home", "linux", map[string]string{"HOME": "/home/u"}, filepath.Join("/home/u", ".local", "share", "zatvor"), false},
		{"linux unset", "linux", nil, "", true},
	}
	for _, tc := range tests {
		got, err := defaultFor(tc.goos, env(tc.env))
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q (err %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}
