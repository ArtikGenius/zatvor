package appdir

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestValidateDataDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	full := filepath.Join(base, "full")
	ours := filepath.Join(base, "ours")
	withSettings := filepath.Join(base, "settings")
	for _, d := range []string{home, full, withSettings, filepath.Join(ours, "journal"), filepath.Join(ours, "run")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(full, "thesis.docx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withSettings, SettingsFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.VolumeName(base) + string(filepath.Separator)
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{"relative/dir", false},
		{root, false},
		{home, false},
		{full, false},
		{ours, true},
		{withSettings, true},
		{filepath.Join(base, "new"), true},
		{filepath.Join(home, "zatvor"), true},
	} {
		err := ValidateDataDir(c.path, home)
		if (err == nil) != c.ok {
			t.Errorf("ValidateDataDir(%q) = %v, want ok=%v", c.path, err, c.ok)
		}
	}
}

func TestLayoutCreatesNothing(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Zatvor")
	d := Layout(base)
	if d.Data != base || d.Run != filepath.Join(base, "run") || d.Logs != filepath.Join(base, "logs") || d.Journal != filepath.Join(base, "journal") {
		t.Errorf("Layout = %+v", d)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("Layout created %s (%v)", base, err)
	}
	r, err := Resolve(base)
	if err != nil || r != d {
		t.Errorf("Resolve = %+v, %v; want Layout's %+v", r, err, d)
	}
}

func TestHome(t *testing.T) {
	env := map[string]string{"HOME": "/home/u", "USERPROFILE": `C:\Users\u`}
	want := env["HOME"]
	if runtime.GOOS == "windows" {
		want = env["USERPROFILE"]
	}
	if got := Home(func(k string) string { return env[k] }); got != want {
		t.Errorf("Home = %q, want %q", got, want)
	}
}
