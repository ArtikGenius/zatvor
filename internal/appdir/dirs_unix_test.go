//go:build !windows

package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirsUserOnly(t *testing.T) {
	base := filepath.Join(t.TempDir(), "Zatvor")
	d, err := Resolve(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d.Data, d.Cache, d.Logs, d.Journal, d.Geo, d.Run} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != 0o700 {
			t.Errorf("%s: mode %v, want 0700", p, got)
		}
	}
}
